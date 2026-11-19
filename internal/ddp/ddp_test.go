package ddp

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func buildTestDDP(flags, seq, dataType, destID byte, offset uint32, payload []byte, timecode bool) []byte {
	headerLen := 10
	if timecode {
		headerLen = 14
	}
	pkt := make([]byte, headerLen+len(payload))
	pkt[0] = flags
	pkt[1] = seq & 0x0F
	pkt[2] = dataType
	pkt[3] = destID
	pkt[4] = byte(offset >> 24)
	pkt[5] = byte(offset >> 16)
	pkt[6] = byte(offset >> 8)
	pkt[7] = byte(offset)
	pkt[8] = byte(len(payload) >> 8)
	pkt[9] = byte(len(payload))
	copy(pkt[headerLen:], payload)
	return pkt
}

func TestParsePacketAndExtractRGB(t *testing.T) {
	// Exact OpenRGB DDPController packet format (data_type=1, flags=0x41).
	raw := buildTestDDP(0x41, 3, 1, 1, 0, []byte{0xAA, 0xBB, 0xCC}, false)
	pkt, err := ParsePacket(raw)
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if pkt.Seq != 3 || pkt.DataType != 1 || pkt.DestID != 1 || !pkt.Push || pkt.Offset != 0 || pkt.Length != 3 {
		t.Fatalf("unexpected packet fields: %+v", pkt)
	}
	r, g, b, ok := ExtractRGB(pkt)
	if !ok || r != 0xAA || g != 0xBB || b != 0xCC {
		t.Fatalf("ExtractRGB = (%d,%d,%d,%v), want (170,187,204,true)", r, g, b, ok)
	}

	// Timecode packet (flags=0x51 -> 14-byte header).
	tc := buildTestDDP(0x51, 1, 0x0B, 1, 0, []byte{10, 20, 30}, true)
	pkt, err = ParsePacket(tc)
	if err != nil {
		t.Fatalf("ParsePacket timecode: %v", err)
	}
	r, g, b, ok = ExtractRGB(pkt)
	if !ok || r != 10 || g != 20 || b != 30 {
		t.Fatalf("ExtractRGB timecode = (%d,%d,%d,%v)", r, g, b, ok)
	}

	// Multi-LED averaging (3 LEDs).
	multi := buildTestDDP(0x41, 2, 1, 1, 0, []byte{
		0, 100, 255,
		30, 101, 255,
		60, 102, 255,
	}, false)
	pkt, err = ParsePacket(multi)
	if err != nil {
		t.Fatalf("ParsePacket multi: %v", err)
	}
	r, g, b, ok = ExtractRGB(pkt)
	if !ok || r != 30 || g != 101 || b != 255 {
		t.Fatalf("ExtractRGB multi = (%d,%d,%d,%v), want (30,101,255,true)", r, g, b, ok)
	}

	// RGBW dataType 0x1B (stride 4).
	rgbw := buildTestDDP(0x41, 4, 0x1B, 1, 0, []byte{12, 34, 56, 200}, false)
	pkt, err = ParsePacket(rgbw)
	if err != nil {
		t.Fatalf("ParsePacket rgbw: %v", err)
	}
	r, g, b, ok = ExtractRGB(pkt)
	if !ok || r != 12 || g != 34 || b != 56 {
		t.Fatalf("ExtractRGB rgbw = (%d,%d,%d,%v)", r, g, b, ok)
	}

	// Non-zero offset ignored by ExtractRGB.
	cont := buildTestDDP(0x41, 5, 1, 1, 30, []byte{1, 2, 3}, false)
	pkt, err = ParsePacket(cont)
	if err != nil {
		t.Fatalf("ParsePacket cont: %v", err)
	}
	if _, _, _, ok := ExtractRGB(pkt); ok {
		t.Fatalf("expected non-zero offset to return ok=false")
	}
}

func TestParsePacketRejectsAndIgnores(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want error
	}{
		{"too short", []byte{0x41, 0, 1}, ErrInvalidPacket},
		{"bad version", buildTestDDP(0x81, 0, 1, 1, 0, []byte{1, 2, 3}, false), ErrInvalidPacket},
		{"truncated payload", []byte{0x41, 0, 1, 1, 0, 0, 0, 0, 0, 10, 1, 2, 3}, ErrInvalidPacket},
		{"query flag", buildTestDDP(0x42, 0, 1, 1, 0, nil, false), ErrIgnored},
		{"reply flag", buildTestDDP(0x44, 0, 1, 1, 0, nil, false), ErrIgnored},
		{"json dest 251", buildTestDDP(0x41, 0, 1, 251, 0, []byte("{}"), false), ErrIgnored},
	}

	for _, tc := range cases {
		_, err := ParsePacket(tc.buf)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: got err=%v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestRGBToHSV(t *testing.T) {
	cases := []struct {
		r, g, b uint8
		h, s, v int
	}{
		{255, 0, 0, 0, 100, 100},
		{0, 255, 0, 120, 100, 100},
		{0, 0, 255, 240, 100, 100},
		{255, 255, 0, 60, 100, 100},
		{0, 255, 255, 180, 100, 100},
		{255, 0, 255, 300, 100, 100},
		{128, 0, 255, 270, 100, 100},
		{255, 255, 255, 0, 0, 100},
		{0, 0, 0, 0, 0, 0},
		{128, 128, 128, 0, 0, 50},
	}
	for _, tc := range cases {
		h, s, v := RGBToHSV(tc.r, tc.g, tc.b)
		if h != tc.h || s != tc.s || v != tc.v {
			t.Errorf("RGBToHSV(%d,%d,%d) = (%d,%d,%d), want (%d,%d,%d)",
				tc.r, tc.g, tc.b, h, s, v, tc.h, tc.s, tc.v)
		}
	}
}

func TestServerLoopback(t *testing.T) {
	got := make(chan [3]uint8, 4)
	srv := NewServer("127.0.0.1:0", func(r, g, b uint8) {
		got <- [3]uint8{r, g, b}
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	conn, err := net.Dial("udp4", srv.Addr())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	pkt := buildTestDDP(0x41, 1, 1, 1, 0, []byte{0x12, 0x34, 0x56}, false)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("Write: %v", err)
	}

	select {
	case rgb := <-got:
		if rgb != [3]uint8{0x12, 0x34, 0x56} {
			t.Fatalf("got rgb=%v, want [18 52 86]", rgb)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for DDP frame")
	}
}

func TestRegisterDevice(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "OpenRGB.json")
	initial := `{
    "DDPDevices": {
        "devices": [
            {
                "ip": "192.168.0.160",
                "keepalive_time": 1000,
                "name": "WLED",
                "num_leds": 30,
                "port": 4048
            }
        ]
    },
    "Detectors": {
        "detectors": {
            "DDP": false
        }
    },
    "Server": {
        "default_port": 6742
    }
}`
	if err := os.WriteFile(cfgPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := RegisterDevice(cfgPath, "Zinnia Crux CR90", "127.0.0.1", 4048, 1, 1000, true)
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if res.AlreadyConfigured {
		t.Fatal("expected first call to add device")
	}
	if _, err := os.Stat(cfgPath + ".bak"); err != nil {
		t.Fatalf("expected backup file: %v", err)
	}

	// Verify existing WLED, Detectors.DDP=true, and Server preserved.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		DDPDevices struct {
			Devices []DDPDeviceEntry `json:"devices"`
		} `json:"DDPDevices"`
		Detectors struct {
			Detectors map[string]bool `json:"detectors"`
		} `json:"Detectors"`
		Server map[string]int `json:"Server"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal written OpenRGB.json: %v", err)
	}
	if len(parsed.DDPDevices.Devices) != 2 {
		t.Fatalf("expected 2 DDP devices, got %d", len(parsed.DDPDevices.Devices))
	}
	if parsed.DDPDevices.Devices[0].Name != "WLED" || parsed.DDPDevices.Devices[1].IP != "127.0.0.1" {
		t.Fatalf("unexpected devices: %+v", parsed.DDPDevices.Devices)
	}
	if !parsed.Detectors.Detectors["DDP"] {
		t.Fatalf("expected Detectors.detectors.DDP to be enabled")
	}
	if parsed.Server["default_port"] != 6742 {
		t.Fatalf("expected Server.default_port=6742 preserved, got %+v", parsed.Server)
	}

	// Second call must detect already configured without duplicating.
	res2, err := RegisterDevice(cfgPath, "Zinnia Crux CR90", "127.0.0.1", 4048, 1, 1000, true)
	if err != nil {
		t.Fatalf("RegisterDevice 2nd: %v", err)
	}
	if !res2.AlreadyConfigured {
		t.Fatal("expected AlreadyConfigured=true on second call")
	}
}
