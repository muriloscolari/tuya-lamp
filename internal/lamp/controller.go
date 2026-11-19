package lamp

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muriloscolari/tuya-lamp/internal/p2"
)

const (
	DPPower  byte = 1
	DPMode   byte = 2
	DPBright byte = 3
	DPTemp   byte = 4
	DPColour byte = 11
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

// Status is the full public status returned by the API.
type Status struct {
	Configured bool   `json:"configured"`
	HasCapture bool   `json:"has_capture"`
	Name       string `json:"name"`
	Node       string `json:"node"`
	State      State  `json:"state"`
	LastError  string `json:"last_error,omitempty"`
}

// Controller manages lamp state, keys, debounced BLE radio transmission, and SSE subscribers.
type Controller struct {
	dir   string
	radio Advertiser

	mu         sync.Mutex
	cfg        *Config
	cap        *Capture
	state      State
	lastError  string
	pending    [][]byte
	txRunning  bool
	notifyCh   chan struct{}
	subs       map[chan Status]struct{}
}

// NewController loads config/state from dir and starts the background BLE transmitter.
func NewController(dir string, radio Advertiser) (*Controller, error) {
	MaybeImportLegacy(dir)

	c := &Controller{
		dir:      dir,
		radio:    radio,
		state:    DefaultState(),
		notifyCh: make(chan struct{}, 1),
		subs:     make(map[chan Status]struct{}),
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

	go c.txLoop()
	return c, nil
}

// Status returns a snapshot of the controller status.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Controller) statusLocked() Status {
	s := Status{
		Configured: c.cfg != nil,
		HasCapture: c.cap != nil,
		Name:       "Zinnia Crux CR90",
		State:      c.state,
		LastError:  c.lastError,
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

// Apply updates lamp state and queues the corresponding P2 BLE frame(s).
// If sync is true, it waits for the radio transmission to finish (used by CLI).
func (c *Controller) Apply(cmd Command, syncTx bool) (Status, error) {
	c.mu.Lock()
	if c.cfg == nil {
		c.mu.Unlock()
		return Status{}, errors.New("lamp not configured yet (run login/setup first)")
	}

	localKey, err := p2.KeyFromString(c.cfg.LocalKey)
	if err != nil {
		c.mu.Unlock()
		return Status{}, err
	}
	beaconKey, err := hex.DecodeString(c.cfg.BeaconKey)
	if err != nil {
		c.mu.Unlock()
		return Status{}, err
	}
	dstSlice, _ := hex.DecodeString(c.cfg.Dst)
	srcSlice, _ := hex.DecodeString(c.cfg.Src)
	var src, dst [2]byte
	copy(src[:], srcSlice)
	copy(dst[:], dstSlice)

	var payloads [][]byte
	action := strings.ToLower(strings.TrimSpace(cmd.Action))
	switch action {
	case "on":
		c.state.On = true
		payloads = append(payloads, p2.PayloadON)
	case "off":
		c.state.On = false
		payloads = append(payloads, p2.PayloadOFF)
	case "toggle":
		c.state.On = !c.state.On
		if c.state.On {
			payloads = append(payloads, p2.PayloadON)
		} else {
			payloads = append(payloads, p2.PayloadOFF)
		}
	case "bright":
		if cmd.Bright == nil {
			c.mu.Unlock()
			return Status{}, errors.New("bright requires value 1-100")
		}
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
		c.state.Mode = "white"
		c.state.Temp = clamp(*cmd.Temp, 0, 100)
		payloads = append(payloads, p2.MustDPPayload([]p2.DP{
			{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
			{ID: DPTemp, Type: p2.DPTypeValue, Value: encodeTemp(c.state.Temp)},
		}))
	case "white":
		c.state.Mode = "white"
		if cmd.Bright != nil {
			c.state.Bright = clamp(*cmd.Bright, 1, 100)
		}
		payloads = append(payloads, p2.MustDPPayload([]p2.DP{
			{ID: DPMode, Type: p2.DPTypeEnum, Value: []byte{0}},
			{ID: DPBright, Type: p2.DPTypeValue, Value: encodeBright(c.state.Bright)},
		}))
	case "color":
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
				payloads = append(payloads, p2.PayloadOFF)
			}
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

	ads := make([][]byte, 0, len(payloads))
	for _, pl := range payloads {
		c.cfg.Seq++
		frame, err := p2.BuildFrame(src, dst, c.cfg.Seq, pl, localKey, beaconKey)
		if err != nil {
			c.mu.Unlock()
			return Status{}, err
		}
		ads = append(ads, p2.Advertisement(frame))
	}

	_ = writeJSONAtomic(filepath.Join(c.dir, "config.json"), c.cfg, 0o600)
	_ = writeJSONAtomic(filepath.Join(c.dir, "state.json"), &c.state, 0o644)
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

func (c *Controller) txLoop() {
	for range c.notifyCh {
		// Short debounce window so rapid slider drags coalesce into one BLE burst.
		time.Sleep(80 * time.Millisecond)
		for {
			c.mu.Lock()
			ads := c.pending
			c.pending = nil
			c.mu.Unlock()
			if len(ads) == 0 || c.radio == nil {
				break
			}
			if err := c.radio.Advertise(ads, 450*time.Millisecond); err != nil {
				c.mu.Lock()
				c.lastError = err.Error()
				c.broadcastLocked()
				c.mu.Unlock()
			}
		}
	}
}
