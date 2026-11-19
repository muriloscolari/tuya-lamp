package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muriloscolari/tuya-lamp/internal/lamp"
)

func TestOpenRGBEndpoints(t *testing.T) {
	dir := t.TempDir()
	ctrl, err := lamp.NewController(dir, nil)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	defer ctrl.Close()

	ctrl.SetOpenRGBListen("127.0.0.1:4048", "")
	srv := New(ctrl, nil)

	// GET /api/status includes openrgb object.
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", rec.Code)
	}
	var st lamp.Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.OpenRGB.ListenAddr != "127.0.0.1:4048" || !st.OpenRGB.Config.Enabled {
		t.Fatalf("unexpected openrgb status: %+v", st.OpenRGB)
	}

	// GET /api/openrgb returns OpenRGBStatus directly.
	req = httptest.NewRequest(http.MethodGet, "/api/openrgb", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/openrgb = %d, want 200", rec.Code)
	}
	var oStatus lamp.OpenRGBStatus
	if err := json.NewDecoder(rec.Body).Decode(&oStatus); err != nil {
		t.Fatalf("decode openrgb: %v", err)
	}
	if oStatus.Config.MaxBright != 100 || oStatus.Config.MinIntervalMs != 40 {
		t.Fatalf("unexpected openrgb defaults: %+v", oStatus.Config)
	}

	// POST /api/openrgb partial update (max_bright + black_mode) preserves enabled & interval.
	body := bytes.NewBufferString(`{"max_bright": 65, "black_mode": "off"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/openrgb", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/openrgb = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var updated lamp.Status
	if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated status: %v", err)
	}
	if !updated.OpenRGB.Config.Enabled || updated.OpenRGB.Config.MaxBright != 65 || updated.OpenRGB.Config.BlackMode != "off" || updated.OpenRGB.Config.MinIntervalMs != 40 {
		t.Fatalf("unexpected patched openrgb config: %+v", updated.OpenRGB.Config)
	}
}
