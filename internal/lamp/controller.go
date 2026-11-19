package lamp

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muriloscolari/tuya-lamp/internal/ddp"
	"github.com/muriloscolari/tuya-lamp/internal/p2"
)

const (
	DPPower  byte = 1
	DPMode   byte = 2
	DPBright byte = 3
	DPTemp   byte = 4
	DPColour byte = 11

	seqLeaseStep       = 500
	seqRotateThreshold = 65000
	ddpConnectedWindow = 3 * time.Second
	ddpStreamingWindow = 3 * time.Second
)

var NamedColors = map[string]int{
	"red":    0,
	"orange": 30,
	"yellow": 60,
	"green":  120,
	"cyan":   180,
	"blue":   240,
	"purple": 270,
	"pink":   330,
}

// Advertiser transmits 31-byte BLE advertising payloads.
type Advertiser interface {
	Advertise(ads [][]byte, hold time.Duration) error
}

// Command describes a requested change to the lamp.
type Command struct {
	Action string `json:"action,omitempty"` // on, off, toggle, bright, temp, white, color, set
	On     *bool  `json:"on,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Bright *int   `json:"bright,omitempty"`
	Temp   *int   `json:"temp,omitempty"`
	Hue    *int   `json:"hue,omitempty"`
	Sat    *int   `json:"sat,omitempty"`
	Color  string `json:"color,omitempty"`
}

// OpenRGBStatus reports live DDP sync state and configuration.
type OpenRGBStatus struct {
	Config     OpenRGBConfig `json:"config"`
	ListenAddr string        `json:"listen_addr,omitempty"`
	Connected  bool          `json:"connected"`
	Streaming  bool          `json:"streaming"`
	LastRGB    [3]uint8      `json:"last_rgb"`
	PacketsRx  uint64        `json:"packets_rx"`
	FramesTx   uint64        `json:"frames_tx"`
	LastError  string        `json:"last_error,omitempty"`
}

// Status is the full public status returned by the API.
type Status struct {
	Configured bool          `json:"configured"`
	HasCapture bool          `json:"has_capture"`
	Name       string        `json:"name"`
	Node       string        `json:"node"`
	State      State         `json:"state"`
	LastError  string        `json:"last_error,omitempty"`
	OpenRGB    OpenRGBStatus `json:"openrgb"`
}

type streamTarget struct {
	hue  int
	sat  int
	val  int
	dark bool
}

// Controller manages lamp state, keys, debounced BLE radio transmission, and SSE subscribers.
type Controller struct {
	dir   string
	radio Advertiser

	mu                sync.Mutex
	cfg               *Config
	cap               *Capture
	state             State
	lastError         string
	persistedSeq      uint16
	openrgbCfg        OpenRGBConfig
	openrgbListenAddr string
	openrgbLastError  string
	openrgbPacketsRx  uint64
	openrgbFramesTx   uint64
	hasLastDDP        bool
	lastDDPRGB        [3]uint8
	lastDDPAt         time.Time
	lastDDPChangeAt   time.Time
	pending           [][]byte
	streamTarget      *streamTarget
	hasStreamTx       bool
	lastStreamHue     int
	lastStreamSat     int
	lastStreamVal     int
	streamDark        bool
	lastStreamTx      time.Time
	manualHoldUntil   time.Time
	lastStatePersist  time.Time
	lastSSEBroadcast  time.Time
	idleTimer         *time.Timer
	closed            bool
	notifyCh          chan struct{}
	stopCh            chan struct{}
	txDone            chan struct{}
	closeOnce         sync.Once
	subs              map[chan Status]struct{}
}

// NewController loads config/state from dir and starts the background BLE transmitter.
func NewController(dir string, radio Advertiser) (*Controller, error) {
	MaybeImportLegacy(dir)

	c := &Controller{
		dir:        dir,
		radio:      radio,
		state:      DefaultState(),
		openrgbCfg: DefaultOpenRGBConfig(),
		notifyCh:   make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
		txDone:     make(chan struct{}),
		subs:       make(map[chan Status]struct{}),
	}

	var cfg Config
	if err := readJSON(filepath.Join(dir, "config.json"), &cfg); err == nil && cfg.LocalKey != "" && cfg.BeaconKey != "" {
		if cfg.Src == "" {
			cfg.Src = "8001"
		}
		if cfg.Seq == 0 {
			cfg.Seq = 1000
		}
		if cfg.Name == "" {
			cfg.Name = "Zinnia Crux CR90"
		}
		c.cfg = &cfg
		c.persistedSeq = cfg.Seq
	}

	var capData Capture
	if err := readJSON(filepath.Join(dir, "capture.json"), &capData); err == nil && capData.ON != "" && capData.OFF != "" {
		c.cap = &capData
	}

	var st State
	if err := readJSON(filepath.Join(dir, "state.json"), &st); err == nil {
		if st.Bright < 1 || st.Bright > 100 {
			st.Bright = 100
		}
		if st.Sat <= 0 || st.Sat > 100 {
			st.Sat = 100
		}
		if st.Mode != "white" && st.Mode != "color" {
			st.Mode = "white"
		}
		c.state = st
	}

	var ocfg OpenRGBConfig
	if err := readJSON(filepath.Join(dir, "openrgb.json"), &ocfg); err == nil {
		ocfg.Normalize()
		c.openrgbCfg = ocfg
	}

	go c.txLoop()
	return c, nil
}

// Status returns a snapshot of the controller status.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Controller) isConnectedLocked(now time.Time) bool {
	return !c.lastDDPAt.IsZero() && now.Sub(c.lastDDPAt) < ddpConnectedWindow
}

func (c *Controller) isStreamingLocked(now time.Time) bool {
	return c.openrgbCfg.Enabled &&
		c.isConnectedLocked(now) &&
		!c.lastDDPChangeAt.IsZero() &&
		now.Sub(c.lastDDPChangeAt) < ddpStreamingWindow
}

func (c *Controller) statusLocked() Status {
	now := time.Now()
	s := Status{
		Configured: c.cfg != nil,
		HasCapture: c.cap != nil,
		Name:       "Zinnia Crux CR90",
		State:      c.state,
		LastError:  c.lastError,
		OpenRGB: OpenRGBStatus{
			Config:     c.openrgbCfg,
			ListenAddr: c.openrgbListenAddr,
			Connected:  c.isConnectedLocked(now),
			Streaming:  c.isStreamingLocked(now),
			LastRGB:    c.lastDDPRGB,
			PacketsRx:  c.openrgbPacketsRx,
			FramesTx:   c.openrgbFramesTx,
			LastError:  c.openrgbLastError,
		},
	}
	if c.cfg != nil {
		if c.cfg.Name != "" {
			s.Name = c.cfg.Name
		}
		s.Node = c.cfg.Dst
	}
	return s
}

// Subscribe registers an SSE listener for status updates.
func (c *Controller) Subscribe() chan Status {
	ch := make(chan Status, 8)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch
}

// Unsubscribe removes an SSE listener.
func (c *Controller) Unsubscribe(ch chan Status) {
	c.mu.Lock()
	delete(c.subs, ch)
	close(ch)
	c.mu.Unlock()
}

func (c *Controller) broadcastLocked() {
	st := c.statusLocked()
	for ch := range c.subs {
		select {
		case ch <- st:
		default:
		}
	}
}

// SaveCapture persists a captured ON/OFF frame pair.
func (c *Controller) SaveCapture(capData Capture) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cap = &capData
	if err := writeJSONAtomic(filepath.Join(c.dir, "capture.json"), &capData, 0o644); err != nil {
		return err
	}
	c.broadcastLocked()
	return nil
}

// SaveConfig validates keys and persists config.
func (c *Controller) SaveConfig(cfg Config) error {
	if _, err := p2.KeyFromString(cfg.LocalKey); err != nil {
		return err
	}
	bk, err := hex.DecodeString(cfg.BeaconKey)
	if err != nil || len(bk) != 16 {
		return errors.New("beacon_key must be 32 hex characters")
	}
	dst, err := hex.DecodeString(cfg.Dst)
	if err != nil || len(dst) != 2 {
		return errors.New("dst must be 4 hex characters")
	}
	if cfg.Src == "" {
		cfg.Src = "8001"
	}
	if cfg.Seq == 0 {
		cfg.Seq = 1000
	}
	if cfg.Name == "" {
		cfg.Name = "Zinnia Crux CR90"
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = &cfg
	c.persistedSeq = cfg.Seq
	if err := writeJSONAtomic(filepath.Join(c.dir, "config.json"), &cfg, 0o600); err != nil {
		return err
	}
	c.broadcastLocked()
	return nil
}

// DeriveAndSaveFromLocalKey recovers beaconKey from the saved capture and stores config.
func (c *Controller) DeriveAndSaveFromLocalKey(name, localKeyStr string) error {
	c.mu.Lock()
	capData := c.cap
	seq := uint16(1000)
	if c.cfg != nil && c.cfg.Seq > seq {
		seq = c.cfg.Seq + 10
	}
	c.mu.Unlock()

	if capData == nil {
		return errors.New("no captured ON/OFF frames yet")
	}
	localKey, err := p2.KeyFromString(localKeyStr)
	if err != nil {
		return err
	}
	onBytes, err := hex.DecodeString(capData.ON)
	if err != nil {
		return err
	}
	offBytes, err := hex.DecodeString(capData.OFF)
	if err != nil {
		return err
	}
	bkOn, err := p2.RecoverBeaconKey(onBytes, p2.PayloadON, localKey)
	if err != nil {
		return err
	}
	bkOff, err := p2.RecoverBeaconKey(offBytes, p2.PayloadOFF, localKey)
	if err != nil {
		return err
	}
	if string(bkOn) != string(bkOff) {
		return errors.New("local_key does not match captured ON/OFF frames")
	}
	if _, err := p2.ParseFrame(onBytes, localKey, bkOn); err != nil {
		return fmt.Errorf("frame verification failed: %w", err)
	}

	return c.SaveConfig(Config{
		Name:      name,
		LocalKey:  localKeyStr,
		BeaconKey: hex.EncodeToString(bkOn),
		Dst:       hex.EncodeToString(onBytes[3:5]),
		Src:       "8001",
		Seq:       seq,
	})
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func hueDiff(a, b int) int {
	d := absInt(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}

func encodeBright(pct int) []byte {
	v := 10 + int(float64(clamp(pct, 1, 100)-1)/99.0*990.0+0.5)
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return b[:]
}

func encodeTemp(pct int) []byte {
	v := clamp(pct, 0, 100) * 10
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return b[:]
}

func encodeColour(hue, sat, val int) []byte {
	var b [4]byte
	h := ((hue % 360) + 360) % 360
	binary.BigEndian.PutUint16(b[0:2], uint16(h))
	b[2] = byte(clamp(sat, 0, 100))
	b[3] = byte(clamp(val, 1, 100))
	return b[:]
}

func (c *Controller) broadcastThrottledLocked(now time.Time, force bool) {
	if !force && now.Sub(c.lastSSEBroadcast) < 100*time.Millisecond {
		return
	}
	c.lastSSEBroadcast = now
	c.broadcastLocked()
}

func (c *Controller) resetIdleTimerLocked() {
	if c.closed {
		return
	}
	if c.idleTimer != nil {
		c.idleTimer.Stop()
	}
	c.idleTimer = time.AfterFunc(3200*time.Millisecond, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return
		}
		c.broadcastLocked()
	})
}

func (c *Controller) allocSeqLocked(count int, persistNow bool) ([2]byte, uint16, error) {
	var src [2]byte
	if c.cfg == nil {
		return src, 0, errors.New("lamp not configured yet (run login/setup first)")
	}
	srcVal := uint16(0x8001)
	if c.cfg.Src != "" {
		if b, err := hex.DecodeString(c.cfg.Src); err == nil && len(b) == 2 {
			srcVal = binary.BigEndian.Uint16(b)
		}
	}

	if int(c.cfg.Seq)+count >= seqRotateThreshold {
		if srcVal < 0x8001 || srcVal >= 0xFFFE {
			srcVal = 0x8001
		} else {
			srcVal++
		}
		c.cfg.Src = fmt.Sprintf("%04x", srcVal)
		c.cfg.Seq = 10
		c.persistedSeq = 10
		persistNow = true
	}

	binary.BigEndian.PutUint16(src[:], srcVal)
	startSeq := c.cfg.Seq + 1
	c.cfg.Seq += uint16(count)

	if persistNow {
		if c.cfg.Seq > c.persistedSeq {
			c.persistedSeq = c.cfg.Seq
		}
		cfgCopy := *c.cfg
		cfgCopy.Seq = c.persistedSeq
		_ = writeJSONAtomic(filepath.Join(c.dir, "config.json"), &cfgCopy, 0o600)
	} else if c.cfg.Seq > c.persistedSeq {
		nextLease := int(c.cfg.Seq) + seqLeaseStep
		if nextLease >= seqRotateThreshold {
			nextLease = seqRotateThreshold - 1
		}
		c.persistedSeq = uint16(nextLease)
		cfgCopy := *c.cfg
		cfgCopy.Seq = c.persistedSeq
		_ = writeJSONAtomic(filepath.Join(c.dir, "config.json"), &cfgCopy, 0o600)
	}

	return src, startSeq, nil
}

func (c *Controller) buildAdsLocked(payloads [][]byte, persistNow bool) ([][]byte, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	if c.cfg == nil {
		return nil, errors.New("lamp not configured yet (run login/setup first)")
	}
	localKey, err := p2.KeyFromString(c.cfg.LocalKey)
	if err != nil {
		return nil, err
	}
	beaconKey, err := hex.DecodeString(c.cfg.BeaconKey)
	if err != nil || len(beaconKey) != 16 {
		return nil, errors.New("invalid beacon_key")
	}
	dstSlice, err := hex.DecodeString(c.cfg.Dst)
	if err != nil || len(dstSlice) != 2 {
		return nil, errors.New("invalid dst address")
	}
	var dst [2]byte
	copy(dst[:], dstSlice)

	src, startSeq, err := c.allocSeqLocked(len(payloads), persistNow)
	if err != nil {
		return nil, err
	}

	ads := make([][]byte, 0, len(payloads))
	for i, pl := range payloads {
		frame, err := p2.BuildFrame(src, dst, startSeq+uint16(i), pl, localKey, beaconKey)
		if err != nil {
			return nil, err
		}
		ads = append(ads, p2.Advertisement(frame))
	}
	return ads, nil
}

// SetOpenRGBListen records the UDP listen address and any startup bind error for DDP.
func (c *Controller) SetOpenRGBListen(addr, errMsg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openrgbListenAddr = addr
	c.openrgbLastError = errMsg
	c.broadcastLocked()
}

// SetOpenRGBConfig applies partial updates to the OpenRGB sync configuration.
func (c *Controller) SetOpenRGBConfig(patch OpenRGBConfigPatch) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if patch.Enabled != nil {
		c.openrgbCfg.Enabled = *patch.Enabled
		if *patch.Enabled {
			c.hasStreamTx = false
			c.hasLastDDP = false
			c.manualHoldUntil = time.Time{}
		} else {
			c.streamTarget = nil
			c.streamDark = false
		}
	}
	if patch.MaxBright != nil {
		c.openrgbCfg.MaxBright = *patch.MaxBright
	}
	if patch.MinIntervalMs != nil {
		c.openrgbCfg.MinIntervalMs = *patch.MinIntervalMs
	}
	if patch.HoldMs != nil {
		c.openrgbCfg.HoldMs = *patch.HoldMs
	}
	if patch.BlackMode != nil {
		c.openrgbCfg.BlackMode = *patch.BlackMode
	}
	c.openrgbCfg.Normalize()

	if err := writeJSONAtomic(filepath.Join(c.dir, "openrgb.json"), &c.openrgbCfg, 0o644); err != nil {
		return Status{}, err
	}
	st := c.statusLocked()
	c.broadcastLocked()
	return st, nil
}

// HandleDDP processes a single incoming RGB color frame from OpenRGB over DDP.
func (c *Controller) HandleDDP(r, g, b uint8) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}

	now := time.Now()
	c.openrgbPacketsRx++
	wasConnected := c.isConnectedLocked(now)
	wasStreaming := c.isStreamingLocked(now)
	c.lastDDPAt = now

	rgb := [3]uint8{r, g, b}
	same := c.hasLastDDP && c.lastDDPRGB == rgb
	c.lastDDPRGB = rgb
	c.hasLastDDP = true

	c.resetIdleTimerLocked()

	if same {
		if !wasConnected {
			c.broadcastThrottledLocked(now, true)
		}
		return
	}

	c.lastDDPChangeAt = now
	if !c.openrgbCfg.Enabled || c.cfg == nil || !c.state.On || now.Before(c.manualHoldUntil) {
		if !wasConnected || !wasStreaming {
			c.broadcastThrottledLocked(now, false)
		}
		return
	}

	if r == 0 && g == 0 && b == 0 {
		if c.openrgbCfg.BlackMode == "off" {
			c.streamTarget = &streamTarget{dark: true}
		} else {
			c.streamTarget = &streamTarget{
				hue:  c.state.Hue,
				sat:  c.state.Sat,
				val:  1,
				dark: false,
			}
		}
	} else {
		h, s, v := ddp.RGBToHSV(r, g, b)
		v = clamp(int(math.Round(float64(v)*float64(c.openrgbCfg.MaxBright)/100.0)), 1, 100)
		if s < 8 {
			h = c.state.Hue
		}
		c.streamTarget = &streamTarget{
			hue:  h,
			sat:  s,
			val:  v,
			dark: false,
		}
	}

	select {
	case c.notifyCh <- struct{}{}:
	default:
	}
}

// Apply updates lamp state and queues the corresponding P2 BLE frame(s).
// If sync is true, it waits for the radio transmission to finish (used by CLI).
func (c *Controller) Apply(cmd Command, syncTx bool) (Status, error) {
	c.mu.Lock()
	if c.cfg == nil {
		c.mu.Unlock()
		return Status{}, errors.New("lamp not configured yet (run login/setup first)")
	}

	var payloads [][]byte
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	turningOff := false
	modifiesColorOrMode := false

	switch action {
	case "on":
		c.state.On = true
		payloads = append(payloads, p2.PayloadON)
	case "off":
		c.state.On = false
		turningOff = true
		payloads = append(payloads, p2.PayloadOFF)
	case "toggle":
		c.state.On = !c.state.On
		if c.state.On {
			payloads = append(payloads, p2.PayloadON)
		} else {
			turningOff = true
			payloads = append(payloads, p2.PayloadOFF)
		}
	case "bright":
		if cmd.Bright == nil {
			c.mu.Unlock()
			return Status{}, errors.New("bright requires value 1-100")
		}
		modifiesColorOrMode = true
		c.state.Bright = clamp(*cmd.Bright, 1, 100)
		if c.state.Mode == "color" {
			payloads = append(payloads, p2.MustDPPayload([]p2.DP{
				{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{1}},
				{ID: DPColour, Type: p2.DPTypeRaw, Value: encodeColour(c.state.Hue, c.state.Sat, c.state.Bright)},
			}))
		} else {
			payloads = append(payloads, p2.MustDPPayload([]p2.DP{
				{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
				{ID: DPBright, Type: p2.DPTypeValue, Value: encodeBright(c.state.Bright)},
			}))
		}
	case "temp":
		if cmd.Temp == nil {
			c.mu.Unlock()
			return Status{}, errors.New("temp requires value 0-100")
		}
		modifiesColorOrMode = true
		c.state.Mode = "white"
		c.state.Temp = clamp(*cmd.Temp, 0, 100)
		payloads = append(payloads, p2.MustDPPayload([]p2.DP{
			{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
			{ID: DPTemp, Type: p2.DPTypeValue, Value: encodeTemp(c.state.Temp)},
		}))
	case "white":
		modifiesColorOrMode = true
		c.state.Mode = "white"
		if cmd.Bright != nil {
			c.state.Bright = clamp(*cmd.Bright, 1, 100)
		}
		payloads = append(payloads, p2.MustDPPayload([]p2.DP{
			{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
			{ID: DPBright, Type: p2.DPTypeValue, Value: encodeBright(c.state.Bright)},
		}))
	case "color":
		modifiesColorOrMode = true
		hue := c.state.Hue
		if cmd.Color != "" {
			if h, ok := NamedColors[strings.ToLower(cmd.Color)]; ok {
				hue = h
			} else {
				var parsed int
				if _, err := fmt.Sscanf(cmd.Color, "%d", &parsed); err != nil {
					c.mu.Unlock()
					return Status{}, fmt.Errorf("unknown color %q", cmd.Color)
				}
				hue = ((parsed % 360) + 360) % 360
			}
		}
		if cmd.Hue != nil {
			hue = ((*cmd.Hue % 360) + 360) % 360
		}
		if cmd.Sat != nil {
			c.state.Sat = clamp(*cmd.Sat, 0, 100)
		}
		if cmd.Bright != nil {
			c.state.Bright = clamp(*cmd.Bright, 1, 100)
		}
		c.state.Mode = "color"
		c.state.Hue = hue
		payloads = append(payloads, p2.MustDPPayload([]p2.DP{
			{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{1}},
			{ID: DPColour, Type: p2.DPTypeRaw, Value: encodeColour(c.state.Hue, c.state.Sat, c.state.Bright)},
		}))
	case "set", "":
		if cmd.On != nil && *cmd.On != c.state.On {
			c.state.On = *cmd.On
			if c.state.On {
				payloads = append(payloads, p2.PayloadON)
			} else {
				turningOff = true
				payloads = append(payloads, p2.PayloadOFF)
			}
		}
		if cmd.Mode == "white" || cmd.Mode == "color" || cmd.Bright != nil || cmd.Temp != nil || cmd.Hue != nil || cmd.Sat != nil {
			modifiesColorOrMode = true
		}
		if cmd.Mode == "white" || cmd.Mode == "color" {
			c.state.Mode = cmd.Mode
		}
		if cmd.Bright != nil {
			c.state.Bright = clamp(*cmd.Bright, 1, 100)
		}
		if cmd.Temp != nil {
			c.state.Temp = clamp(*cmd.Temp, 0, 100)
		}
		if cmd.Hue != nil {
			c.state.Hue = ((*cmd.Hue % 360) + 360) % 360
		}
		if cmd.Sat != nil {
			c.state.Sat = clamp(*cmd.Sat, 0, 100)
		}
		if c.state.Mode == "color" && (cmd.Mode == "color" || cmd.Hue != nil || cmd.Sat != nil || cmd.Bright != nil) {
			payloads = append(payloads, p2.MustDPPayload([]p2.DP{
				{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{1}},
				{ID: DPColour, Type: p2.DPTypeRaw, Value: encodeColour(c.state.Hue, c.state.Sat, c.state.Bright)},
			}))
		} else if c.state.Mode == "white" && (cmd.Mode == "white" || cmd.Bright != nil || cmd.Temp != nil) {
			var dps []p2.DP
			dps = append(dps, p2.DP{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}})
			if cmd.Bright != nil || cmd.Mode == "white" {
				dps = append(dps, p2.DP{ID: DPBright, Type: p2.DPTypeValue, Value: encodeBright(c.state.Bright)})
			}
			if cmd.Temp != nil {
				dps = append(dps, p2.DP{ID: DPTemp, Type: p2.DPTypeValue, Value: encodeTemp(c.state.Temp)})
			}
			payloads = append(payloads, p2.MustDPPayload(dps))
		}
	default:
		c.mu.Unlock()
		return Status{}, fmt.Errorf("unknown action %q", cmd.Action)
	}

	ads, err := c.buildAdsLocked(payloads, true)
	if err != nil {
		c.mu.Unlock()
		return Status{}, err
	}

	c.streamDark = false
	c.hasStreamTx = false
	c.hasLastDDP = false
	if turningOff || modifiesColorOrMode {
		c.streamTarget = nil
	}
	if modifiesColorOrMode {
		c.manualHoldUntil = time.Now().Add(3 * time.Second)
	}

	_ = writeJSONAtomic(filepath.Join(c.dir, "state.json"), &c.state, 0o644)
	c.lastStatePersist = time.Now()
	c.lastError = ""
	st := c.statusLocked()
	c.broadcastLocked()

	if syncTx {
		c.mu.Unlock()
		if len(ads) > 0 && c.radio != nil {
			if err := c.radio.Advertise(ads, 450*time.Millisecond); err != nil {
				c.mu.Lock()
				c.lastError = err.Error()
				c.broadcastLocked()
				c.mu.Unlock()
				return st, err
			}
		}
		return st, nil
	}

	// Coalesce pending slider frames; latest state always wins.
	if len(ads) > 0 {
		c.pending = ads
		select {
		case c.notifyCh <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
	return st, nil
}

func (c *Controller) buildStreamAdsLocked(target streamTarget) ([][]byte, time.Duration, error) {
	var payloads [][]byte
	if target.dark {
		payloads = append(payloads, p2.PayloadOFF)
	} else {
		if c.streamDark {
			payloads = append(payloads, p2.PayloadON)
		}
		if target.sat < 10 {
			payloads = append(payloads, p2.MustDPPayload([]p2.DP{
				{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
				{ID: DPBright, Type: p2.DPTypeValue, Value: encodeBright(target.val)},
			}))
		} else {
			payloads = append(payloads, p2.MustDPPayload([]p2.DP{
				{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{1}},
				{ID: DPColour, Type: p2.DPTypeRaw, Value: encodeColour(target.hue, target.sat, target.val)},
			}))
		}
	}

	ads, err := c.buildAdsLocked(payloads, false)
	if err != nil {
		return nil, 0, err
	}

	c.lastStreamTx = time.Now()
	hold := time.Duration(clamp(c.openrgbCfg.HoldMs, 15, 450)) * time.Millisecond
	return ads, hold, nil
}

func (c *Controller) commitStreamTxLocked(target streamTarget, adCount int) {
	if target.dark {
		c.streamDark = true
		c.hasStreamTx = true
	} else {
		if target.sat < 10 {
			c.state.Mode = "white"
		} else {
			c.state.Mode = "color"
			c.state.Hue = target.hue
		}
		c.state.Sat = target.sat
		c.state.Bright = target.val
		c.lastStreamHue = target.hue
		c.lastStreamSat = target.sat
		c.lastStreamVal = target.val
		c.hasStreamTx = true
		c.streamDark = false
	}

	now := time.Now()
	c.openrgbFramesTx += uint64(adCount)
	c.lastError = ""

	if now.Sub(c.lastStatePersist) >= 5*time.Second {
		_ = writeJSONAtomic(filepath.Join(c.dir, "state.json"), &c.state, 0o644)
		c.lastStatePersist = now
	}
	c.broadcastThrottledLocked(now, false)
}

func (c *Controller) txLoop() {
	defer close(c.txDone)
	for {
		select {
		case <-c.stopCh:
			return
		case <-c.notifyCh:
		}

		for {
			select {
			case <-c.stopCh:
				return
			default:
			}

			c.mu.Lock()
			hasPending := len(c.pending) > 0
			hasStream := c.streamTarget != nil
			c.mu.Unlock()

			if hasPending {
				// Short debounce window so rapid slider drags coalesce into one BLE burst.
				timer := time.NewTimer(80 * time.Millisecond)
				select {
				case <-c.stopCh:
					timer.Stop()
					return
				case <-timer.C:
				}

				c.mu.Lock()
				ads := c.pending
				c.pending = nil
				rad := c.radio
				c.mu.Unlock()

				if len(ads) > 0 && rad != nil {
					if err := rad.Advertise(ads, 450*time.Millisecond); err != nil {
						c.mu.Lock()
						c.lastError = err.Error()
						c.broadcastLocked()
						c.mu.Unlock()
					}
				}
				continue
			}

			if !hasStream {
				break
			}

			c.mu.Lock()
			rem := time.Duration(c.openrgbCfg.MinIntervalMs)*time.Millisecond - time.Since(c.lastStreamTx)
			c.mu.Unlock()

			if rem > 0 {
				timer := time.NewTimer(rem)
				interruptedByPending := false
			waitLoop:
				for {
					select {
					case <-c.stopCh:
						timer.Stop()
						return
					case <-timer.C:
						break waitLoop
					case <-c.notifyCh:
						c.mu.Lock()
						if len(c.pending) > 0 {
							interruptedByPending = true
						}
						c.mu.Unlock()
						if interruptedByPending {
							timer.Stop()
							break waitLoop
						}
					}
				}
				if interruptedByPending {
					continue
				}
			}

			c.mu.Lock()
			if len(c.pending) > 0 {
				c.mu.Unlock()
				continue
			}
			target := c.streamTarget
			c.streamTarget = nil
			if target == nil || !c.openrgbCfg.Enabled || !c.state.On || c.cfg == nil {
				c.mu.Unlock()
				continue
			}

			if c.hasStreamTx && target.dark == c.streamDark {
				if target.dark {
					c.mu.Unlock()
					continue
				}
				targetMode := "color"
				if target.sat < 10 {
					targetMode = "white"
				}
				if c.state.Mode == targetMode {
					dH := hueDiff(target.hue, c.lastStreamHue)
					if targetMode == "white" {
						dH = 0
					}
					dS := absInt(target.sat - c.lastStreamSat)
					dV := absInt(target.val - c.lastStreamVal)
					if dH < 2 && dS < 2 && dV < 2 {
						c.mu.Unlock()
						continue
					}
				}
			}

			ads, hold, err := c.buildStreamAdsLocked(*target)
			if err != nil {
				c.lastError = err.Error()
				c.broadcastLocked()
				c.mu.Unlock()
				continue
			}
			rad := c.radio
			c.mu.Unlock()

			if len(ads) > 0 && rad != nil {
				if err := rad.Advertise(ads, hold); err != nil {
					c.mu.Lock()
					c.lastError = err.Error()
					c.hasLastDDP = false
					c.broadcastLocked()
					c.mu.Unlock()
					continue
				}
			}
			c.mu.Lock()
			c.commitStreamTxLocked(*target, len(ads))
			c.mu.Unlock()
		}
	}
}

// Close stops the background transmitter and flushes persisted config and state.
func (c *Controller) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		if c.idleTimer != nil {
			c.idleTimer.Stop()
		}
		c.mu.Unlock()

		close(c.stopCh)
		<-c.txDone

		c.mu.Lock()
		if c.cfg != nil {
			cfgCopy := *c.cfg
			if c.persistedSeq > cfgCopy.Seq {
				cfgCopy.Seq = c.persistedSeq
			}
			_ = writeJSONAtomic(filepath.Join(c.dir, "config.json"), &cfgCopy, 0o600)
		}
		_ = writeJSONAtomic(filepath.Join(c.dir, "state.json"), &c.state, 0o644)
		c.mu.Unlock()
	})
}
