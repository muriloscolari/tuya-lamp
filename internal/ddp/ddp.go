package ddp

import (
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
)

const (
	DefaultPort = 4048
	DefaultAddr = "127.0.0.1:4048"

	flagVerMask  byte = 0xC0
	flagVer1     byte = 0x40
	flagTimecode byte = 0x10
	flagReply    byte = 0x04
	flagQuery    byte = 0x02
	flagPush     byte = 0x01

	dataTypeRGBW8 byte = 0x1B
)

var (
	// ErrInvalidPacket indicates a malformed or non-DDPv1 UDP payload.
	ErrInvalidPacket = errors.New("ddp: invalid packet")
	// ErrIgnored indicates a valid DDP packet that carries no frame color (query/reply/JSON).
	ErrIgnored = errors.New("ddp: ignored control packet")
)

// Packet represents a decoded DDP v1 UDP packet.
type Packet struct {
	Seq      uint8
	DataType uint8
	DestID   uint8
	Offset   uint32
	Length   uint16
	Push     bool
	Data     []byte
}

// ParsePacket decodes a DDP v1 write packet.
func ParsePacket(buf []byte) (Packet, error) {
	if len(buf) < 10 {
		return Packet{}, ErrInvalidPacket
	}
	flags := buf[0]
	if (flags & flagVerMask) != flagVer1 {
		return Packet{}, ErrInvalidPacket
	}

	destID := buf[3]
	if (flags&flagQuery) != 0 || (flags&flagReply) != 0 || (destID >= 246 && destID <= 251) {
		return Packet{}, ErrIgnored
	}

	headerLen := 10
	if (flags & flagTimecode) != 0 {
		headerLen = 14
		if len(buf) < headerLen {
			return Packet{}, ErrInvalidPacket
		}
	}

	length := binary.BigEndian.Uint16(buf[8:10])
	if headerLen+int(length) > len(buf) {
		return Packet{}, ErrInvalidPacket
	}

	return Packet{
		Seq:      buf[1] & 0x0F,
		DataType: buf[2],
		DestID:   destID,
		Offset:   binary.BigEndian.Uint32(buf[4:8]),
		Length:   length,
		Push:     (flags & flagPush) != 0,
		Data:     buf[headerLen : headerLen+int(length)],
	}, nil
}

// ExtractRGB extracts a single RGB color from a DDP packet (averaging multi-LED frames).
func ExtractRGB(pkt Packet) (r, g, b uint8, ok bool) {
	if pkt.Offset != 0 {
		return 0, 0, 0, false
	}

	stride := 3
	if pkt.DataType == dataTypeRGBW8 {
		stride = 4
	}
	if len(pkt.Data) < stride {
		return 0, 0, 0, false
	}

	count := len(pkt.Data) / stride
	if count == 1 {
		return pkt.Data[0], pkt.Data[1], pkt.Data[2], true
	}

	var sumR, sumG, sumB uint32
	for i := 0; i < count; i++ {
		off := i * stride
		sumR += uint32(pkt.Data[off])
		sumG += uint32(pkt.Data[off+1])
		sumB += uint32(pkt.Data[off+2])
	}
	n := uint32(count)
	return uint8((sumR + n/2) / n), uint8((sumG + n/2) / n), uint8((sumB + n/2) / n), true
}

// RGBToHSV converts 8-bit sRGB channels to hue (0..359), saturation (0..100), and value (0..100).
func RGBToHSV(r, g, b uint8) (h, s, v int) {
	rf := float64(r) / 255.0
	gf := float64(g) / 255.0
	bf := float64(b) / 255.0

	maxC := math.Max(rf, math.Max(gf, bf))
	minC := math.Min(rf, math.Min(gf, bf))
	delta := maxC - minC

	v = int(math.Round(maxC * 100.0))
	if maxC <= 0 || delta <= 0 {
		return 0, 0, v
	}

	s = int(math.Round((delta / maxC) * 100.0))

	var hueDeg float64
	switch maxC {
	case rf:
		hueDeg = 60.0 * math.Mod((gf-bf)/delta, 6.0)
	case gf:
		hueDeg = 60.0 * (((bf - rf) / delta) + 2.0)
	default:
		hueDeg = 60.0 * (((rf - gf) / delta) + 4.0)
	}
	if hueDeg < 0 {
		hueDeg += 360.0
	}
	h = int(math.Round(hueDeg)) % 360
	if h < 0 {
		h += 360
	}
	return h, s, v
}

// Server listens for UDP DDP frames and forwards extracted RGB values.
type Server struct {
	addr    string
	onFrame func(r, g, b uint8)

	mu           sync.Mutex
	pc           net.PacketConn
	resolvedAddr string
	done         chan struct{}
}

// NewServer creates a UDP DDP server configured to listen on addr.
func NewServer(addr string, onFrame func(r, g, b uint8)) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	return &Server{
		addr:    addr,
		onFrame: onFrame,
	}
}

// Start binds the UDP socket and begins receiving DDP packets.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.pc != nil {
		s.mu.Unlock()
		return nil
	}
	pc, err := net.ListenPacket("udp4", s.addr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	done := make(chan struct{})
	s.pc = pc
	s.resolvedAddr = pc.LocalAddr().String()
	s.done = done
	s.mu.Unlock()

	go s.serveLoop(pc, done)
	return nil
}

func (s *Server) serveLoop(pc net.PacketConn, done chan struct{}) {
	defer close(done)
	buf := make([]byte, 2048)
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		pkt, err := ParsePacket(buf[:n])
		if err != nil {
			continue
		}
		if r, g, b, ok := ExtractRGB(pkt); ok && s.onFrame != nil {
			s.onFrame(r, g, b)
		}
	}
}

// Addr returns the bound UDP listen address (or configured address before Start).
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolvedAddr != "" {
		return s.resolvedAddr
	}
	return s.addr
}

// Close shuts down the UDP socket and waits for the receive loop to exit.
func (s *Server) Close() error {
	s.mu.Lock()
	pc := s.pc
	done := s.done
	s.pc = nil
	s.done = nil
	s.mu.Unlock()

	if pc == nil {
		return nil
	}
	err := pc.Close()
	if done != nil {
		<-done
	}
	return err
}
