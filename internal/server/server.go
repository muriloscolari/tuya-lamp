package server

import (
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/muriloscolari/tuya-lamp/internal/ddp"
	"github.com/muriloscolari/tuya-lamp/internal/lamp"
	"github.com/muriloscolari/tuya-lamp/internal/p2"
	"github.com/muriloscolari/tuya-lamp/internal/radio"
	"github.com/muriloscolari/tuya-lamp/internal/tuya"
)

//go:embed web/*
var webFS embed.FS

// Server serves the REST API, SSE stream, setup endpoints, and web frontend.
type Server struct {
	ctrl *lamp.Controller
	rad  *radio.Radio
	tuya *tuya.Client
	mux  *http.ServeMux

	capMu     sync.Mutex
	capActive bool
}

// New creates an HTTP server for the lamp controller.
func New(ctrl *lamp.Controller, rad *radio.Radio) *Server {
	s := &Server{
		ctrl: ctrl,
		rad:  rad,
		tuya: tuya.NewClient(),
		mux:  http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	sub, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(sub))

	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/command", s.handleCommand)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/openrgb", s.handleOpenRGBGet)
	s.mux.HandleFunc("POST /api/openrgb", s.handleOpenRGBPost)
	s.mux.HandleFunc("POST /api/openrgb/register", s.handleOpenRGBRegister)
	s.mux.HandleFunc("POST /api/setup/capture", s.handleCapture)
	s.mux.HandleFunc("POST /api/setup/qr", s.handleQRStart)
	s.mux.HandleFunc("POST /api/setup/qr/poll", s.handleQRPoll)
	s.mux.HandleFunc("POST /api/setup/derive", s.handleDerive)
	s.mux.Handle("/", fileServer)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Status())
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	var cmd lamp.Command
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	st, err := s.ctrl.Apply(cmd, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleOpenRGBGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.ctrl.Status().OpenRGB)
}

func (s *Server) handleOpenRGBPost(w http.ResponseWriter, r *http.Request) {
	var patch lamp.OpenRGBConfigPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	st, err := s.ctrl.SetOpenRGBConfig(patch)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func parseListenHostPort(listenAddr string) (string, int) {
	host := "127.0.0.1"
	port := ddp.DefaultPort
	if listenAddr == "" {
		return host, port
	}
	h, pStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return host, port
	}
	if h != "" && h != "0.0.0.0" {
		host = h
	}
	if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
		port = p
	}
	return host, port
}

func (s *Server) handleOpenRGBRegister(w http.ResponseWriter, r *http.Request) {
	st := s.ctrl.Status()
	host, port := parseListenHostPort(st.OpenRGB.ListenAddr)
	res, err := ddp.RegisterDevice("", st.Name, host, port, 1, 1000, false)
	if err != nil {
		if errors.Is(err, ddp.ErrOpenRGBRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"already_configured": res.AlreadyConfigured,
		"config_path":        res.ConfigPath,
		"message":            res.Message,
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.ctrl.Subscribe()
	defer s.ctrl.Unsubscribe(ch)

	send := func(st lamp.Status) {
		b, _ := json.Marshal(st)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	send(s.ctrl.Status())
	for {
		select {
		case <-r.Context().Done():
			return
		case st, ok := <-ch:
			if !ok {
				return
			}
			send(st)
		}
	}
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if s.rad == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bluetooth radio unavailable"})
		return
	}
	s.capMu.Lock()
	if s.capActive {
		s.capMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "capture already in progress"})
		return
	}
	s.capActive = true
	s.capMu.Unlock()
	defer func() {
		s.capMu.Lock()
		s.capActive = false
		s.capMu.Unlock()
	}()

	onBuf := make([]byte, 16)
	offBuf := make([]byte, 16)
	copy(onBuf, p2.PayloadON)
	copy(offBuf, p2.PayloadOFF)
	crcON := p2.CRC8(onBuf)
	crcOFF := p2.CRC8(offBuf)

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	var capData lamp.Capture
	err := s.rad.Scan(ctx, func(adv []byte) bool {
		frame := p2.FindFrame(adv)
		if len(frame) != p2.FrameLen || frame[0] != 0x0B || frame[7] != p2.SetDPs {
			return false
		}
		if frame[8] == crcON && capData.ON == "" {
			capData.ON = hex.EncodeToString(frame)
		} else if frame[8] == crcOFF && capData.OFF == "" {
			capData.OFF = hex.EncodeToString(frame)
		}
		return capData.ON != "" && capData.OFF != ""
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if capData.ON == "" || capData.OFF == "" {
		writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": "timeout waiting for both OFF and ON from phone app"})
		return
	}
	if err := s.ctrl.SaveCapture(capData); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node": capData.ON[6:10], "capture": capData})
}

func (s *Server) handleQRStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_code is required"})
		return
	}
	token, qrURL, err := s.tuya.RequestQRCode(req.UserCode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	qr, err := qrcode.New(qrURL, qrcode.Medium)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	pngBytes, err := qr.PNG(256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"qr_url":     qrURL,
		"qr_png_b64": pngBytes,
	})
}

func (s *Server) handleQRPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		UserCode string `json:"user_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" || req.UserCode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token and user_code are required"})
		return
	}
	ok, loginRes, err := s.tuya.PollLogin(req.Token, req.UserCode)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"confirmed": false})
		return
	}
	devices, err := s.tuya.FetchDeviceKeys(loginRes)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	// Try auto-matching against captured frames if present.
	for _, d := range devices {
		if err := s.ctrl.DeriveAndSaveFromLocalKey(d.Name, d.LocalKey); err == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"confirmed": true,
				"matched":   d.Name,
				"status":    s.ctrl.Status(),
			})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"confirmed": true,
		"devices":   devices,
	})
}

func (s *Server) handleDerive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		LocalKey string `json:"local_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LocalKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "local_key is required"})
		return
	}
	if req.Name == "" {
		req.Name = "Zinnia Crux CR90"
	}
	if err := s.ctrl.DeriveAndSaveFromLocalKey(req.Name, req.LocalKey); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.ctrl.Status())
}
