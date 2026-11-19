package lamp

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muriloscolari/tuya-lamp/internal/p2"
)

type mockRadio struct {
	mu      sync.Mutex
	ads     [][]byte
	holds   []time.Duration
	txCh    chan struct{}
	nextErr error
}

func newMockRadio() *mockRadio {
	return &mockRadio{txCh: make(chan struct{}, 64)}
}

func (m *mockRadio) Advertise(ads [][]byte, hold time.Duration) error {
	m.mu.Lock()
	if m.nextErr != nil {
		err := m.nextErr
		m.nextErr = nil
		m.mu.Unlock()
		if m.txCh != nil {
			select {
			case m.txCh <- struct{}{}:
			default:
			}
		}
		return err
	}
	for _, ad := range ads {
		cp := make([]byte, len(ad))
		copy(cp, ad)
		m.ads = append(m.ads, cp)
		m.holds = append(m.holds, hold)
	}
	m.mu.Unlock()
	if m.txCh != nil {
		select {
		case m.txCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *mockRadio) snapshot() ([][]byte, []time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ads := make([][]byte, len(m.ads))
	copy(ads, m.ads)
	holds := make([]time.Duration, len(m.holds))
	copy(holds, m.holds)
	return ads, holds
}

func (m *mockRadio) waitCount(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m.mu.Lock()
		got := len(m.ads)
		m.mu.Unlock()
		if got >= n {
			return
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			t.Fatalf("timed out waiting for %d radio ads (got %d)", n, got)
		}
		select {
		case <-m.txCh:
		case <-time.After(rem):
		}
	}
}

func newTestController(t *testing.T, seq uint16) (*Controller, *mockRadio, string) {
	t.Helper()
	dir := t.TempDir()
	rad := newMockRadio()
	ctrl, err := NewController(dir, rad)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	t.Cleanup(ctrl.Close)

	err = ctrl.SaveConfig(Config{
		Name:      "Test Lamp",
		LocalKey:  "0123456789abcdef",
		BeaconKey: "fedcba9876543210fedcba9876543210",
		Dst:       "0003",
		Src:       "8001",
		Seq:       seq,
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	return ctrl, rad, dir
}

func decodeAdDPs(t *testing.T, ad []byte) (*p2.DecodedFrame, []p2.DP) {
	t.Helper()
	frame := p2.FindFrame(ad)
	if frame == nil {
		t.Fatalf("missing P2 frame in ad %x", ad)
	}
	bk, _ := hex.DecodeString("fedcba9876543210fedcba9876543210")
	dec, err := p2.ParseFrame(frame, []byte("0123456789abcdef"), bk)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	return dec, p2.DPParse(dec.Payload)
}

func TestControllerApply(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)

	st, err := ctrl.Apply(Command{Action: "off"}, true)
	if err != nil || st.State.On {
		t.Fatalf("expected off, got %+v (err=%v)", st, err)
	}

	b := 45
	st, err = ctrl.Apply(Command{Action: "bright", Bright: &b}, true)
	if err != nil || st.State.Bright != 45 {
		t.Fatalf("expected bright=45, got %+v (err=%v)", st, err)
	}

	st, err = ctrl.Apply(Command{Action: "color", Color: "purple"}, true)
	if err != nil || st.State.Mode != "color" || st.State.Hue != 270 {
		t.Fatalf("expected purple hue=270, got %+v (err=%v)", st, err)
	}

	ads, _ := rad.snapshot()
	if len(ads) != 3 {
		t.Fatalf("expected 3 BLE ads sent, got %d", len(ads))
	}
	for i, ad := range ads {
		if len(ad) != 31 || p2.FindFrame(ad) == nil {
			t.Fatalf("ad %d invalid: %x", i, ad)
		}
	}
}

func TestHandleDDPKeepaliveDeduplication(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)

	for i := 0; i < 10; i++ {
		ctrl.HandleDDP(255, 0, 0)
	}
	rad.waitCount(t, 1, time.Second)
	time.Sleep(180 * time.Millisecond)

	ads, holds := rad.snapshot()
	if len(ads) != 1 {
		t.Fatalf("expected 1 BLE tx for 10 identical keepalive frames, got %d", len(ads))
	}
	if holds[0] != 30*time.Millisecond {
		t.Fatalf("expected stream hold 30ms, got %v", holds[0])
	}
	st := ctrl.Status()
	if st.OpenRGB.PacketsRx != 10 || st.OpenRGB.FramesTx != 1 || !st.OpenRGB.Connected {
		t.Fatalf("unexpected openrgb status: %+v", st.OpenRGB)
	}
}

func TestHandleDDPRateLimitAndCoalesce(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)
	minInt := 120
	hold := 90
	if _, err := ctrl.SetOpenRGBConfig(OpenRGBConfigPatch{MinIntervalMs: &minInt, HoldMs: &hold}); err != nil {
		t.Fatal(err)
	}

	// Trigger initial frame, wait for it to send, then burst distinct colors inside 120ms window.
	ctrl.HandleDDP(255, 0, 0)
	rad.waitCount(t, 1, time.Second)

	ctrl.HandleDDP(0, 255, 0)
	time.Sleep(15 * time.Millisecond)
	ctrl.HandleDDP(0, 0, 255)
	time.Sleep(15 * time.Millisecond)
	ctrl.HandleDDP(255, 255, 0) // yellow (hue 60) should win

	rad.waitCount(t, 2, time.Second)
	time.Sleep(140 * time.Millisecond)

	ads, holds := rad.snapshot()
	if len(ads) != 2 {
		t.Fatalf("expected 2 coalesced BLE frames, got %d", len(ads))
	}
	if holds[1] > 150*time.Millisecond {
		t.Fatalf("expected short streaming hold <= 150ms, got %v", holds[1])
	}
	_, dps := decodeAdDPs(t, ads[1])
	if len(dps) != 2 || dps[1].ID != DPColour {
		t.Fatalf("unexpected DPs: %+v", dps)
	}
	hue := int(dps[1].Value[0])<<8 | int(dps[1].Value[1])
	if hue != 60 {
		t.Fatalf("expected latest coalesced hue 60 (yellow), got %d", hue)
	}
}

func TestHandleDDPDeadbandSkipsSmallChanges(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)
	minInt := 80
	hold := 60
	_, _ = ctrl.SetOpenRGBConfig(OpenRGBConfigPatch{MinIntervalMs: &minInt, HoldMs: &hold})

	ctrl.HandleDDP(255, 0, 0)
	rad.waitCount(t, 1, time.Second)
	time.Sleep(100 * time.Millisecond)

	// Tiny RGB shift (hue ~1 deg, sat 100, val 100 -> within HSV deadband dH<2, dS<2, dV<2).
	ctrl.HandleDDP(255, 4, 0)
	time.Sleep(150 * time.Millisecond)

	ads, _ := rad.snapshot()
	if len(ads) != 1 {
		t.Fatalf("expected deadband to skip tiny HSV delta, got %d tx", len(ads))
	}
}

func TestLampOffIgnoresDDPAndOnResumes(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)

	if _, err := ctrl.Apply(Command{Action: "off"}, true); err != nil {
		t.Fatal(err)
	}
	ctrl.HandleDDP(0, 255, 0)
	time.Sleep(180 * time.Millisecond)

	ads, _ := rad.snapshot()
	if len(ads) != 1 { // only the off frame
		t.Fatalf("expected DDP ignored while lamp off, got %d ads", len(ads))
	}
	if !ctrl.Status().OpenRGB.Config.Enabled {
		t.Fatal("expected openrgb.enabled to remain true after turning lamp off")
	}

	if _, err := ctrl.Apply(Command{Action: "on"}, true); err != nil {
		t.Fatal(err)
	}
	ctrl.HandleDDP(0, 0, 255)
	rad.waitCount(t, 3, time.Second)

	ads, _ = rad.snapshot()
	if len(ads) != 3 { // off, on, blue
		t.Fatalf("expected 3 ads after resuming on, got %d", len(ads))
	}
}

func TestManualWhiteHoldsOffStreamingDuringCooldown(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)

	ctrl.HandleDDP(255, 0, 128)
	rad.waitCount(t, 1, time.Second)

	if !ctrl.Status().OpenRGB.Streaming {
		t.Fatal("expected openrgb streaming=true right after active DDP change")
	}

	if _, err := ctrl.Apply(Command{Action: "white"}, true); err != nil {
		t.Fatal(err)
	}
	if !ctrl.Status().OpenRGB.Config.Enabled {
		t.Fatal("expected manual white command to keep openrgb.enabled=true while applying cooldown")
	}

	// DDP frames during the 3-second manual cooldown must not overwrite manual white.
	ctrl.HandleDDP(0, 255, 0)
	time.Sleep(180 * time.Millisecond)
	if ctrl.Status().State.Mode != "white" {
		t.Fatalf("expected lamp mode to stay white during cooldown, got %s", ctrl.Status().State.Mode)
	}
}

func TestStreamRadioErrorDoesNotPoisonDeadband(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)
	minInt := 80
	hold := 60
	_, _ = ctrl.SetOpenRGBConfig(OpenRGBConfigPatch{MinIntervalMs: &minInt, HoldMs: &hold})

	rad.mu.Lock()
	rad.nextErr = os.ErrPermission
	rad.mu.Unlock()

	ctrl.HandleDDP(0, 255, 0)
	select {
	case <-rad.txCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for failed tx attempt")
	}
	time.Sleep(30 * time.Millisecond)

	st := ctrl.Status()
	if st.OpenRGB.FramesTx != 0 || st.LastError == "" {
		t.Fatalf("expected FramesTx=0 and non-empty LastError after radio failure, got %+v", st)
	}

	time.Sleep(90 * time.Millisecond)
	// Retry with the exact same RGB must succeed and not be blocked by deadband or sameAsLastDDP.
	ctrl.HandleDDP(0, 255, 0)
	rad.waitCount(t, 1, time.Second)
	time.Sleep(30 * time.Millisecond)

	st = ctrl.Status()
	if st.OpenRGB.FramesTx != 1 || st.LastError != "" || st.State.Hue != 120 {
		t.Fatalf("expected retry to succeed with Hue=120, FramesTx=1, LastError='', got %+v", st)
	}
}

func TestBlackModeOffAndWake(t *testing.T) {
	ctrl, rad, _ := newTestController(t, 100)
	minInt := 80
	hold := 60
	blackOff := "off"
	_, _ = ctrl.SetOpenRGBConfig(OpenRGBConfigPatch{
		MinIntervalMs: &minInt,
		HoldMs:        &hold,
		BlackMode:     &blackOff,
	})

	ctrl.HandleDDP(255, 0, 0)
	rad.waitCount(t, 1, time.Second)
	time.Sleep(100 * time.Millisecond)

	// Black frame -> PayloadOFF while state.On remains true.
	ctrl.HandleDDP(0, 0, 0)
	rad.waitCount(t, 2, time.Second)
	if !ctrl.Status().State.On {
		t.Fatal("expected logical state.On to stay true during black_mode=off")
	}
	time.Sleep(100 * time.Millisecond)

	// Non-black returns -> PayloadON followed by DPColour.
	ctrl.HandleDDP(0, 255, 0)
	rad.waitCount(t, 4, time.Second)

	ads, _ := rad.snapshot()
	if len(ads) != 4 { // red, OFF, ON, green
		t.Fatalf("expected 4 ads (red, OFF, ON, green), got %d", len(ads))
	}
	_, dpOff := decodeAdDPs(t, ads[1])
	if len(dpOff) != 1 || dpOff[0].ID != DPPower || dpOff[0].Value[0] != 0 {
		t.Fatalf("expected PayloadOFF, got %+v", dpOff)
	}
	_, dpOn := decodeAdDPs(t, ads[2])
	if len(dpOn) != 1 || dpOn[0].ID != DPPower || dpOn[0].Value[0] != 1 {
		t.Fatalf("expected PayloadON on wake, got %+v", dpOn)
	}
}

func TestSequenceLeaseAndSrcRotation(t *testing.T) {
	ctrl, rad, dir := newTestController(t, 64998)
	minInt := 80
	hold := 60
	_, _ = ctrl.SetOpenRGBConfig(OpenRGBConfigPatch{MinIntervalMs: &minInt, HoldMs: &hold})

	// First stream frame -> seq 64999 (src 8001), leases persistedSeq up to 64999.
	ctrl.HandleDDP(255, 0, 0)
	rad.waitCount(t, 1, time.Second)
	time.Sleep(100 * time.Millisecond)

	// Second stream frame -> 64999+1 == 65000 >= seqRotateThreshold -> rotates src to 8002, seq=11!
	ctrl.HandleDDP(0, 255, 0)
	rad.waitCount(t, 2, time.Second)

	ads, _ := rad.snapshot()
	if len(ads) != 2 {
		t.Fatalf("expected 2 ads, got %d", len(ads))
	}
	dec1, _ := decodeAdDPs(t, ads[0])
	dec2, _ := decodeAdDPs(t, ads[1])
	if hex.EncodeToString(dec1.Src[:]) != "8001" || dec1.Seq != 64999 {
		t.Fatalf("frame 1 src/seq = %x/%d, want 8001/64999", dec1.Src, dec1.Seq)
	}
	if hex.EncodeToString(dec2.Src[:]) != "8002" || dec2.Seq != 11 {
		t.Fatalf("frame 2 src/seq = %x/%d, want 8002/11", dec2.Src, dec2.Seq)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Src != "8002" || cfg.Seq != 11 {
		t.Fatalf("persisted config src/seq = %s/%d, want 8002/11", cfg.Src, cfg.Seq)
	}
}
