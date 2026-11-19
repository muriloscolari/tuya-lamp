package lamp

import (
	"testing"
	"time"

	"github.com/muriloscolari/tuya-lamp/internal/p2"
)

type mockRadio struct {
	ads [][]byte
}

func (m *mockRadio) Advertise(ads [][]byte, hold time.Duration) error {
	m.ads = append(m.ads, ads...)
	return nil
}

func TestControllerApply(t *testing.T) {
	dir := t.TempDir()
	rad := &mockRadio{}
	ctrl, err := NewController(dir, rad)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	err = ctrl.SaveConfig(Config{
		Name:      "Test Lamp",
		LocalKey:  "0123456789abcdef",
		BeaconKey: "fedcba9876543210fedcba9876543210",
		Dst:       "0003",
		Src:       "8001",
		Seq:       100,
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

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

	if len(rad.ads) != 3 {
		t.Fatalf("expected 3 BLE ads sent, got %d", len(rad.ads))
	}
	for i, ad := range rad.ads {
		if len(ad) != 31 || p2.FindFrame(ad) == nil {
			t.Fatalf("ad %d invalid: %x", i, ad)
		}
	}
}
