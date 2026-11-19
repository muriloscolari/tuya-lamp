package ddp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOpenRGBRunning is returned when OpenRGB is currently running and would overwrite OpenRGB.json on exit.
var ErrOpenRGBRunning = errors.New("OpenRGB está em execução no momento; feche o OpenRGB antes de gravar em OpenRGB.json (ou use --force / adicione manualmente em Configurações → Dispositivos DDP)")

// DDPDeviceEntry mirrors an entry inside OpenRGB.json's DDPDevices.devices array.
type DDPDeviceEntry struct {
	IP            string `json:"ip"`
	KeepaliveTime int    `json:"keepalive_time"`
	Name          string `json:"name"`
	NumLEDs       int    `json:"num_leds"`
	Port          int    `json:"port"`
}

// RegisterResult describes the outcome of adding the lamp to OpenRGB.json.
type RegisterResult struct {
	AlreadyConfigured bool   `json:"already_configured"`
	ConfigPath        string `json:"config_path"`
	Message           string `json:"message"`
}

// DefaultOpenRGBConfigPath returns ~/.config/OpenRGB/OpenRGB.json.
func DefaultOpenRGBConfigPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", err
		}
		cfg = filepath.Join(home, ".config")
	}
	return filepath.Join(cfg, "OpenRGB", "OpenRGB.json"), nil
}

// IsOpenRGBRunning scans /proc/*/comm for a running openrgb process.
func IsOpenRGBRunning() bool {
	matches, err := filepath.Glob("/proc/[0-9]*/comm")
	if err != nil {
		return false
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(string(data)), "openrgb") {
			return true
		}
	}
	return false
}

// RegisterDevice adds a DDP device entry to OpenRGB.json while preserving unrelated settings.
func RegisterDevice(configPath, name, ip string, port, numLEDs, keepaliveMs int, force bool) (RegisterResult, error) {
	if configPath == "" {
		var err error
		configPath, err = DefaultOpenRGBConfigPath()
		if err != nil {
			return RegisterResult{}, err
		}
	}
	if name == "" {
		name = "Zinnia Crux CR90"
	}
	if ip == "" {
		ip = "127.0.0.1"
	}
	if port <= 0 {
		port = DefaultPort
	}
	if numLEDs <= 0 {
		numLEDs = 1
	}
	if keepaliveMs <= 0 {
		keepaliveMs = 1000
	}

	top := make(map[string]json.RawMessage)
	origData, err := os.ReadFile(configPath)
	fileExisted := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RegisterResult{}, err
	}
	if len(origData) > 0 {
		if err := json.Unmarshal(origData, &top); err != nil {
			return RegisterResult{}, fmt.Errorf("openrgb config inválido (%s): %w", configPath, err)
		}
	}

	var ddpSection struct {
		Devices []DDPDeviceEntry `json:"devices"`
	}
	if rawDDP, ok := top["DDPDevices"]; ok && len(rawDDP) > 0 && string(rawDDP) != "null" {
		if err := json.Unmarshal(rawDDP, &ddpSection); err != nil {
			return RegisterResult{}, fmt.Errorf("DDPDevices inválido: %w", err)
		}
	}

	for _, existing := range ddpSection.Devices {
		if existing.IP == ip && existing.Port == port {
			return RegisterResult{
				AlreadyConfigured: true,
				ConfigPath:        configPath,
				Message:           fmt.Sprintf("Dispositivo DDP %s:%d (%s) já está configurado no OpenRGB.", ip, port, existing.Name),
			}, nil
		}
	}

	if !force && IsOpenRGBRunning() {
		return RegisterResult{}, ErrOpenRGBRunning
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return RegisterResult{}, err
	}
	if fileExisted {
		if err := os.WriteFile(configPath+".bak", origData, 0o644); err != nil {
			return RegisterResult{}, err
		}
	}

	ddpSection.Devices = append(ddpSection.Devices, DDPDeviceEntry{
		IP:            ip,
		KeepaliveTime: keepaliveMs,
		Name:          name,
		NumLEDs:       numLEDs,
		Port:          port,
	})
	rawNewDDP, err := json.Marshal(ddpSection)
	if err != nil {
		return RegisterResult{}, err
	}
	top["DDPDevices"] = rawNewDDP

	// Ensure DDP detector is not explicitly disabled if Detectors.detectors.DDP is false.
	if rawDet, ok := top["Detectors"]; ok && len(rawDet) > 0 && string(rawDet) != "null" {
		var detMap map[string]json.RawMessage
		if json.Unmarshal(rawDet, &detMap) == nil {
			if rawInner, ok := detMap["detectors"]; ok && len(rawInner) > 0 && string(rawInner) != "null" {
				var inner map[string]json.RawMessage
				if json.Unmarshal(rawInner, &inner) == nil {
					if rawFlag, hasDDP := inner["DDP"]; hasDDP {
						var enabled bool
						if json.Unmarshal(rawFlag, &enabled) == nil && !enabled {
							inner["DDP"] = json.RawMessage("true")
							if updatedInner, err := json.Marshal(inner); err == nil {
								detMap["detectors"] = updatedInner
								if updatedDet, err := json.Marshal(detMap); err == nil {
									top["Detectors"] = updatedDet
								}
							}
						}
					}
				}
			}
		}
	}

	out, err := json.MarshalIndent(top, "", "    ")
	if err != nil {
		return RegisterResult{}, err
	}
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0o644); err != nil {
		return RegisterResult{}, err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return RegisterResult{}, err
	}

	return RegisterResult{
		AlreadyConfigured: false,
		ConfigPath:        configPath,
		Message:           fmt.Sprintf("Dispositivo DDP '%s' (%s:%d) adicionado em %s.", name, ip, port, configPath),
	}, nil
}
