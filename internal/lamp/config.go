package lamp

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config holds the cryptographic keys and lamp node address.
type Config struct {
	Name      string `json:"name"`
	LocalKey  string `json:"local_key"`
	BeaconKey string `json:"beacon_key"`
	Dst       string `json:"dst"`
	Src       string `json:"src,omitempty"`
	Seq       uint16 `json:"seq"`
}

// Capture holds the captured ON and OFF P2 frames used to derive BeaconKey.
type Capture struct {
	ON  string `json:"on"`
	OFF string `json:"off"`
}

// State holds the current assumed state of the lamp.
type State struct {
	On     bool   `json:"on"`
	Mode   string `json:"mode"`   // "white" or "color"
	Bright int    `json:"bright"` // 1..100
	Temp   int    `json:"temp"`   // 0..100 (0=warm, 100=cold)
	Hue    int    `json:"hue"`    // 0..359
	Sat    int    `json:"sat"`    // 0..100
}

// DefaultState returns sensible initial lamp state.
func DefaultState() State {
	return State{
		On:     true,
		Mode:   "white",
		Bright: 100,
		Temp:   50,
		Hue:    270,
		Sat:    100,
	}
}

// OpenRGBConfig controls how incoming OpenRGB DDP frames synchronize with the lamp.
type OpenRGBConfig struct {
	Enabled       bool   `json:"enabled"`
	MaxBright     int    `json:"max_bright"`      // 10..100, default 100
	MinIntervalMs int    `json:"min_interval_ms"` // 20..1000, default 40
	HoldMs        int    `json:"hold_ms"`         // 15..450, default 30
	BlackMode     string `json:"black_mode"`      // "dim" or "off", default "dim"
}

// OpenRGBConfigPatch represents partial updates to OpenRGBConfig.
type OpenRGBConfigPatch struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	MaxBright     *int    `json:"max_bright,omitempty"`
	MinIntervalMs *int    `json:"min_interval_ms,omitempty"`
	HoldMs        *int    `json:"hold_ms,omitempty"`
	BlackMode     *string `json:"black_mode,omitempty"`
}

// DefaultOpenRGBConfig returns the default OpenRGB DDP synchronization configuration.
func DefaultOpenRGBConfig() OpenRGBConfig {
	return OpenRGBConfig{
		Enabled:       true,
		MaxBright:     100,
		MinIntervalMs: 40,
		HoldMs:        30,
		BlackMode:     "dim",
	}
}

// Normalize clamps and fills missing fields in OpenRGBConfig.
func (o *OpenRGBConfig) Normalize() {
	if o.MaxBright == 0 {
		o.MaxBright = 100
	} else {
		o.MaxBright = clamp(o.MaxBright, 10, 100)
	}
	if o.MinIntervalMs == 0 {
		o.MinIntervalMs = 40
	} else {
		o.MinIntervalMs = clamp(o.MinIntervalMs, 20, 1000)
	}
	if o.HoldMs == 0 {
		o.HoldMs = 30
	} else {
		o.HoldMs = clamp(o.HoldMs, 15, 450)
	}
	if o.HoldMs > o.MinIntervalMs-5 {
		o.HoldMs = clamp(o.MinIntervalMs-5, 15, 450)
	}
	if o.BlackMode != "off" {
		o.BlackMode = "dim"
	}
}

// ConfigDir returns ~/.config/tuya-lamp (or $TUYA_LAMP_CONFIG_DIR).
func ConfigDir() (string, error) {
	if env := os.Getenv("TUYA_LAMP_CONFIG_DIR"); env != "" {
		return env, nil
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "tuya-lamp"), nil
}

func readJSON(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func writeJSONAtomic(path string, src any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MaybeImportLegacy imports keys/capture/seq from ~/.config/tuya-beacon-linux if tuya-lamp is not yet configured.
func MaybeImportLegacy(dir string) bool {
	cfgPath := filepath.Join(dir, "config.json")
	if _, err := os.Stat(cfgPath); err == nil {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	legacyBase := filepath.Join(home, ".config", "tuya-beacon-linux")
	var legacyKeys struct {
		LocalKey  string `json:"localkey"`
		BeaconKey string `json:"beaconkey"`
		Dst       string `json:"dst"`
	}
	if err := readJSON(filepath.Join(legacyBase, "lamps", "lamp", "keys.json"), &legacyKeys); err != nil {
		return false
	}
	if legacyKeys.LocalKey == "" || legacyKeys.BeaconKey == "" || legacyKeys.Dst == "" {
		return false
	}

	var seqFile struct {
		Seq uint16 `json:"seq"`
	}
	seq := uint16(1200)
	if err := readJSON(filepath.Join(legacyBase, "seq.json"), &seqFile); err == nil && seqFile.Seq > 0 {
		seq = seqFile.Seq + 100
	}

	cfg := Config{
		Name:      "Zinnia Crux CR90",
		LocalKey:  legacyKeys.LocalKey,
		BeaconKey: legacyKeys.BeaconKey,
		Dst:       legacyKeys.Dst,
		Src:       "8001",
		Seq:       seq,
	}
	if err := writeJSONAtomic(cfgPath, &cfg, 0o600); err != nil {
		return false
	}

	var legacyCap Capture
	if err := readJSON(filepath.Join(legacyBase, "lamps", "lamp", "capture.json"), &legacyCap); err == nil {
		_ = writeJSONAtomic(filepath.Join(dir, "capture.json"), &legacyCap, 0o644)
	}
	return true
}
