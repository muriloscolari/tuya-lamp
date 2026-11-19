//go:build linux

package radio

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	afBluetooth = 31
	btProtoHCI  = 1
	solHCI      = 0
	hciFilterOp = 2
)

type sockaddrHCI struct {
	Family  uint16
	Dev     uint16
	Channel uint16
}

type hciFilter struct {
	TypeMask  uint32
	EventMask [2]uint32
	Opcode    uint16
	_         uint16
}

var adapterPattern = regexp.MustCompile(`^hci(\d+)$`)

// Radio provides raw HCI Bluetooth LE advertising and scanning on Linux.
// Requires CAP_NET_RAW or root.
type Radio struct {
	Adapter       string
	Index         uint16
	mu            sync.Mutex
	extended      *bool
	revSender     [6]byte
	filterCleared bool
	advActive     bool
	eventFd       int
	idleTimer     *time.Timer
}

// New creates a Radio bound to adapter (defaults to TUYA_LAMP_ADAPTER or "hci0").
func New(adapter string) (*Radio, error) {
	if adapter == "" {
		adapter = os.Getenv("TUYA_LAMP_ADAPTER")
	}
	if adapter == "" {
		adapter = "hci0"
	}
	m := adapterPattern.FindStringSubmatch(adapter)
	if m == nil {
		return nil, fmt.Errorf("radio: invalid adapter name %q", adapter)
	}
	idx, err := strconv.ParseUint(m[1], 10, 16)
	if err != nil {
		return nil, fmt.Errorf("radio: invalid adapter index: %w", err)
	}
	var sender [6]byte
	if _, err := rand.Read(sender[1:]); err != nil {
		return nil, fmt.Errorf("radio: rand address: %w", err)
	}
	sender[0] = 0xC2
	revSender := [6]byte{sender[5], sender[4], sender[3], sender[2], sender[1], sender[0]}
	return &Radio{Adapter: adapter, Index: uint16(idx), revSender: revSender, eventFd: -1}, nil
}

func openHCI(index uint16, filter hciFilter) (int, error) {
	fd, err := syscall.Socket(afBluetooth, syscall.SOCK_RAW, btProtoHCI)
	if err != nil {
		return -1, fmt.Errorf("radio: socket(AF_BLUETOOTH): %w", err)
	}

	addr := sockaddrHCI{Family: afBluetooth, Dev: index, Channel: 0}
	_, _, errno := syscall.Syscall(
		syscall.SYS_BIND,
		uintptr(fd),
		uintptr(unsafe.Pointer(&addr)),
		unsafe.Sizeof(addr),
	)
	if errno != 0 {
		syscall.Close(fd)
		return -1, fmt.Errorf("radio: bind(hci%d): %w", index, errno)
	}

	_, _, errno = syscall.Syscall6(
		syscall.SYS_SETSOCKOPT,
		uintptr(fd),
		uintptr(solHCI),
		uintptr(hciFilterOp),
		uintptr(unsafe.Pointer(&filter)),
		unsafe.Sizeof(filter),
		0,
	)
	if errno != 0 {
		syscall.Close(fd)
		return -1, fmt.Errorf("radio: setsockopt(HCI_FILTER): %w", errno)
	}

	return fd, nil
}

func setRecvTimeout(fd int, d time.Duration) error {
	tv := syscall.NsecToTimeval(d.Nanoseconds())
	if tv.Sec == 0 && tv.Usec == 0 {
		tv.Usec = 10000
	}
	return syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
}

func (r *Radio) command(ocf uint16, payload []byte, allowDisabled bool) ([]byte, error) {
	opcode := uint16(0x2000) | ocf
	filter := hciFilter{
		TypeMask:  0x10,
		EventMask: [2]uint32{(1 << 14) | (1 << 15), 0},
		Opcode:    opcode,
	}
	fd, err := openHCI(r.Index, filter)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)

	pkt := make([]byte, 4+len(payload))
	pkt[0] = 0x01
	binary.LittleEndian.PutUint16(pkt[1:3], opcode)
	pkt[3] = byte(len(payload))
	copy(pkt[4:], payload)

	if _, err := syscall.Write(fd, pkt); err != nil {
		return nil, fmt.Errorf("radio: write hci cmd %#x: %w", ocf, err)
	}

	buf := make([]byte, 512)
	deadline := time.Now().Add(5 * time.Second)
	var status byte
	var reply []byte

	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return nil, fmt.Errorf("radio: hci command %#x timed out", ocf)
		}
		_ = setRecvTimeout(fd, rem)
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return nil, fmt.Errorf("radio: read hci reply %#x: %w", ocf, err)
		}
		p := buf[:n]
		if len(p) >= 7 && p[0] == 0x04 && p[1] == 0x0E && binary.LittleEndian.Uint16(p[4:6]) == opcode {
			status = p[6]
			reply = append([]byte(nil), p[7:]...)
			break
		}
		if len(p) >= 7 && p[0] == 0x04 && p[1] == 0x0F && binary.LittleEndian.Uint16(p[5:7]) == opcode && p[3] != 0 {
			status = p[3]
			reply = nil
			break
		}
	}

	if status != 0 && !(allowDisabled && status == 0x0C) {
		return nil, fmt.Errorf("radio: hci command %#x failed with status %#x", ocf, status)
	}
	return reply, nil
}

func (r *Radio) detectMode() (bool, error) {
	if r.extended != nil {
		return *r.extended, nil
	}
	if _, err := r.command(0x03, nil, false); err != nil {
		return false, err
	}
	ext := false
	if sup, err := r.command(0x3B, nil, false); err == nil && len(sup) > 0 && sup[0] != 0 {
		ext = true
	}
	r.extended = &ext
	return ext, nil
}

func (r *Radio) closeEventFdLocked() {
	if r.eventFd >= 0 {
		syscall.Close(r.eventFd)
		r.eventFd = -1
	}
}

func (r *Radio) ensureEventFdLocked() int {
	if r.eventFd >= 0 {
		return r.eventFd
	}
	filter := hciFilter{
		TypeMask:  0x10,
		EventMask: [2]uint32{0, 1 << 30}, // HCI_EV_LE_META (0x3E)
		Opcode:    0,
	}
	fd, err := openHCI(r.Index, filter)
	if err != nil {
		return -1
	}
	r.eventFd = fd
	return fd
}

func (r *Radio) stopAdvertising(ext bool) error {
	r.advActive = false
	r.closeEventFdLocked()
	if ext {
		if _, err := r.command(0x39, []byte{0x00, 0x01, 0x01, 0x00, 0x00, 0x00}, true); err == nil {
			return nil
		}
		_, err := r.command(0x39, []byte{0x00, 0x00}, true)
		return err
	}
	_, err := r.command(0x0A, []byte{0x00}, true)
	return err
}

func (r *Radio) scheduleIdleStopLocked(ext bool) {
	if r.idleTimer != nil {
		r.idleTimer.Stop()
	}
	r.idleTimer = time.AfterFunc(500*time.Millisecond, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.advActive {
			_ = r.stopAdvertising(ext)
		}
	})
}

func (r *Radio) configureExtAdv(propsLo, filterPolicy byte) error {
	params := []byte{
		0x01, propsLo, 0x00, 0x20, 0x00, 0x00, 0x20, 0x00, 0x00, 0x07,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, filterPolicy, 0x7F,
		0x01, 0x00, 0x01, 0x00, 0x00,
	}
	if _, err := r.command(0x36, params, false); err != nil {
		return err
	}
	_, err := r.command(0x35, append([]byte{0x01}, r.revSender[:]...), false)
	return err
}

func (r *Radio) disconnectHandle(connHandle uint16) {
	opcode := uint16(0x0406) // OGF 0x01 (Link Control), OCF 0x0006 (HCI_Disconnect)
	filter := hciFilter{
		TypeMask:  0x10,
		EventMask: [2]uint32{(1 << 14) | (1 << 15), 0},
		Opcode:    opcode,
	}
	fd, err := openHCI(r.Index, filter)
	if err != nil {
		return
	}
	defer syscall.Close(fd)

	pkt := []byte{
		0x01,
		byte(opcode), byte(opcode >> 8),
		0x03,
		byte(connHandle), byte(connHandle >> 8),
		0x13, // Remote User Terminated Connection
	}
	_, _ = syscall.Write(fd, pkt)
}

// drainTermEventsLocked checks without blocking if the controller terminated advertising set 1.
func (r *Radio) drainTermEventsLocked() bool {
	fd := r.ensureEventFdLocked()
	if fd < 0 {
		return false
	}
	terminated := false
	buf := make([]byte, 256)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, syscall.MSG_DONTWAIT)
		if err != nil || n <= 0 {
			break
		}
		p := buf[:n]
		if len(p) >= 9 && p[0] == 0x04 && p[1] == 0x3E && p[3] == 0x12 && p[5] == 0x01 {
			terminated = true
			if p[4] == 0x00 {
				connHandle := binary.LittleEndian.Uint16(p[6:8]) & 0x0FFF
				r.disconnectHandle(connHandle)
			}
		}
	}
	return terminated
}

func (r *Radio) holdExtAdv(hold time.Duration) {
	fd := r.ensureEventFdLocked()
	if fd < 0 {
		time.Sleep(hold)
		return
	}

	deadline := time.Now().Add(hold)
	buf := make([]byte, 256)
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return
		}
		_ = setRecvTimeout(fd, rem)
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
				if time.Until(deadline) <= 0 {
					return
				}
				continue
			}
			time.Sleep(time.Until(deadline))
			return
		}
		p := buf[:n]
		// HCI_EV_LE_META (0x3E) -> subevent 0x12 (HCI_EV_LE_ADV_SET_TERM)
		if len(p) >= 9 && p[0] == 0x04 && p[1] == 0x3E && p[3] == 0x12 && p[5] == 0x01 {
			if p[4] == 0x00 {
				connHandle := binary.LittleEndian.Uint16(p[6:8]) & 0x0FFF
				r.disconnectHandle(connHandle)
			}
			if _, err := r.command(0x39, []byte{0x01, 0x01, 0x01, 0x00, 0x00, 0x00}, true); err != nil {
				r.advActive = false
			}
		}
	}
}

func (r *Radio) setupAdvSetLocked(ext bool) error {
	if err := r.stopAdvertising(ext); err != nil {
		return err
	}
	if !r.filterCleared {
		_, _ = r.command(0x10, nil, true) // LE Clear Filter Accept List
		r.filterCleared = true
	}
	if ext {
		_ = r.ensureEventFdLocked()
		if err := r.configureExtAdv(0x13, 0x03); err != nil {
			_, _ = r.command(0x3C, []byte{0x01}, true)
			if err2 := r.configureExtAdv(0x12, 0x00); err2 != nil {
				return err
			}
		}
	} else {
		if _, err := r.command(0x05, r.revSender[:], false); err != nil {
			return err
		}
		params := []byte{0x20, 0x00, 0x20, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x07, 0x03}
		if _, err := r.command(0x06, params, false); err != nil {
			return err
		}
		if _, err := r.command(0x09, make([]byte, 32), false); err != nil {
			return err
		}
	}
	return nil
}

// Advertise broadcasts each 31-byte BLE advertising payload for hold duration.
// During rapid streaming, it keeps the advertising set active and updates the payload in-place.
func (r *Radio) Advertise(ads [][]byte, hold time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.idleTimer != nil {
		r.idleTimer.Stop()
	}

	if hold <= 0 {
		hold = 450 * time.Millisecond
	}
	ext, err := r.detectMode()
	if err != nil {
		return err
	}

	if !r.advActive {
		if err := r.setupAdvSetLocked(ext); err != nil {
			return err
		}
	} else if ext && r.drainTermEventsLocked() {
		r.advActive = false
	}

	defer r.scheduleIdleStopLocked(ext)

	for _, ad := range ads {
		if len(ad) > 31 {
			return fmt.Errorf("radio: ad payload too long (%d bytes)", len(ad))
		}
		if ext {
			dataCmd := make([]byte, 4+len(ad))
			dataCmd[0] = 0x01
			dataCmd[1] = 0x03
			dataCmd[2] = 0x01
			dataCmd[3] = byte(len(ad))
			copy(dataCmd[4:], ad)
			if _, err := r.command(0x37, dataCmd, false); err != nil {
				// Re-setup set 1 if the controller lost state.
				if errSetup := r.setupAdvSetLocked(true); errSetup != nil {
					return err
				}
				if _, err2 := r.command(0x37, dataCmd, false); err2 != nil {
					return err2
				}
				r.advActive = false
			}
			if !r.advActive {
				if _, err := r.command(0x39, []byte{0x01, 0x01, 0x01, 0x00, 0x00, 0x00}, false); err != nil {
					_ = r.stopAdvertising(true)
					_, _ = r.command(0x3C, []byte{0x01}, true)
					if errCfg := r.configureExtAdv(0x12, 0x00); errCfg != nil {
						if errCfg2 := r.configureExtAdv(0x10, 0x00); errCfg2 != nil {
							return err
						}
					}
					if _, errData := r.command(0x37, dataCmd, false); errData != nil {
						return errData
					}
					if _, errEn := r.command(0x39, []byte{0x01, 0x01, 0x01, 0x00, 0x00, 0x00}, false); errEn != nil {
						return errEn
					}
				}
				r.advActive = true
			}
			r.holdExtAdv(hold)
		} else {
			dataCmd := make([]byte, 32)
			dataCmd[0] = byte(len(ad))
			copy(dataCmd[1:], ad)
			if _, err := r.command(0x08, dataCmd, false); err != nil {
				return err
			}
			if !r.advActive {
				if _, err := r.command(0x0A, []byte{0x01}, false); err != nil {
					return err
				}
				r.advActive = true
			}
			time.Sleep(hold)
		}
	}
	return nil
}

// Scan listens for BLE advertisements until ctx is cancelled and calls onReport for each advertising payload.
func (r *Radio) Scan(ctx context.Context, onReport func(advData []byte) bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.idleTimer != nil {
		r.idleTimer.Stop()
	}

	ext, err := r.detectMode()
	if err != nil {
		return err
	}

	filter := hciFilter{
		TypeMask:  0x10,
		EventMask: [2]uint32{0, 1 << 30},
		Opcode:    0,
	}
	fd, err := openHCI(r.Index, filter)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	_ = r.stopAdvertising(ext)

	if ext {
		_, _ = r.command(0x42, make([]byte, 6), true)
		if _, err := r.command(0x41, []byte{0x00, 0x00, 0x01, 0x01, 0x10, 0x00, 0x10, 0x00}, false); err != nil {
			return err
		}
		if _, err := r.command(0x42, []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00}, false); err != nil {
			return err
		}
		defer func() {
			_, _ = r.command(0x42, make([]byte, 6), true)
		}()
	} else {
		_, _ = r.command(0x0C, []byte{0x00, 0x00}, true)
		if _, err := r.command(0x0B, []byte{0x01, 0x10, 0x00, 0x10, 0x00, 0x00, 0x00}, false); err != nil {
			return err
		}
		if _, err := r.command(0x0C, []byte{0x01, 0x00}, false); err != nil {
			return err
		}
		defer func() {
			_, _ = r.command(0x0C, []byte{0x00, 0x00}, true)
		}()
	}

	buf := make([]byte, 4096)
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = setRecvTimeout(fd, 250*time.Millisecond)
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
		pkt := buf[:n]
		if len(pkt) < 6 || pkt[0] != 0x04 || pkt[1] != 0x3E {
			continue
		}
		var adv []byte
		if pkt[3] == 0x02 && len(pkt) > 13 {
			size := int(pkt[13])
			if 14+size <= len(pkt) {
				adv = pkt[14 : 14+size]
			}
		} else if pkt[3] == 0x0D && len(pkt) > 28 {
			size := int(pkt[28])
			if 29+size <= len(pkt) {
				adv = pkt[29 : 29+size]
			}
		}
		if len(adv) > 0 {
			if stop := onReport(adv); stop {
				return nil
			}
		}
	}
}
