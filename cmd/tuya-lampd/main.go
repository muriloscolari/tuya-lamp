package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/muriloscolari/tuya-lamp/internal/ddp"
	"github.com/muriloscolari/tuya-lamp/internal/lamp"
	"github.com/muriloscolari/tuya-lamp/internal/p2"
	"github.com/muriloscolari/tuya-lamp/internal/radio"
	"github.com/muriloscolari/tuya-lamp/internal/server"
	"github.com/muriloscolari/tuya-lamp/internal/tuya"
)

const defaultAddr = "127.0.0.1:18765"

func main() {
	if len(os.Args) < 2 || os.Args[1] == "serve" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "serve" {
			args = args[1:]
		}
		runServe(args)
		return
	}

	cmd := strings.ToLower(os.Args[1])
	rest := os.Args[2:]

	switch cmd {
	case "-h", "--help", "help":
		printUsage()
	case "login":
		runLogin(rest)
	case "capture":
		runCapture(rest)
	case "status":
		runStatus()
	case "openrgb":
		runOpenRGB(rest)
	case "on", "off", "toggle", "bright", "temp", "white", "color":
		runCLI(cmd, rest)
	default:
		fmt.Fprintf(os.Stderr, "Comando desconhecido: %s\n\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Print(`tuya-lampd — Controle local Bluetooth (Tuya Beacon P2) para Zinnia Crux CR90

Uso:
  tuya-lampd serve [--addr 127.0.0.1:18765] [--ddp-addr 127.0.0.1:4048]
                                             Inicia o backend HTTP + DDP OpenRGB + frontend web
  tuya-lampd on | off | toggle               Liga/desliga a lâmpada
  tuya-lampd bright <1-100>                  Ajusta o brilho
  tuya-lampd temp <0-100>                    Temperatura do branco (0=quente, 100=frio)
  tuya-lampd white [1-100]                   Modo branco (com brilho opcional)
  tuya-lampd color <nome|0-359>              Cor RGB (red, orange, yellow, green, cyan, blue, purple, pink)
  tuya-lampd openrgb [status|on|off|bright <10-100>|interval <20-1000>|black <dim|off>|setup [--force]]
                                             Gerencia integração OpenRGB (DDP UDP)
  tuya-lampd status                          Mostra estado atual
  tuya-lampd capture [segundos]              Captura frames OFF/ON do app Tuya (requer CAP_NET_RAW/sudo)
  tuya-lampd login [user_code]               Login Tuya por QR Code para recuperar local_key e beacon_key
`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", envOr("TUYA_LAMP_ADDR", defaultAddr), "HTTP listen address")
	ddpAddr := fs.String("ddp-addr", envOr("TUYA_LAMP_DDP_ADDR", ddp.DefaultAddr), "OpenRGB DDP UDP listen address (or 'off')")
	adapter := fs.String("adapter", "", "Bluetooth HCI adapter (default hci0)")
	_ = fs.Parse(args)

	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatalf("config dir: %v", err)
	}
	rad, err := radio.New(*adapter)
	if err != nil {
		log.Fatalf("radio: %v", err)
	}
	ctrl, err := lamp.NewController(dir, rad)
	if err != nil {
		log.Fatalf("controller: %v", err)
	}
	defer ctrl.Close()

	var ddpSrv *ddp.Server
	if *ddpAddr != "" && strings.ToLower(*ddpAddr) != "off" {
		ddpSrv = ddp.NewServer(*ddpAddr, ctrl.HandleDDP)
		if err := ddpSrv.Start(); err != nil {
			log.Printf("aviso: DDP OpenRGB (%s): %v", *ddpAddr, err)
			ctrl.SetOpenRGBListen(*ddpAddr, err.Error())
			ddpSrv = nil
		} else {
			ctrl.SetOpenRGBListen(ddpSrv.Addr(), "")
			log.Printf("DDP OpenRGB escutando em udp://%s", ddpSrv.Addr())
		}
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(ctrl, rad),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		if ddpSrv != nil {
			_ = ddpSrv.Close()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("tuya-lampd escutando em http://%s (config: %s, adapter: %s)", *addr, dir, rad.Adapter)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("http server: %v", err)
	}
}

func runStatus() {
	baseURL := "http://" + envOr("TUYA_LAMP_ADDR", defaultAddr)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(baseURL + "/api/status")
	if err == nil {
		defer resp.Body.Close()
		var st lamp.Status
		if json.NewDecoder(resp.Body).Decode(&st) == nil {
			out, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(out))
			return
		}
	}
	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	ctrl, err := lamp.NewController(dir, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ctrl.Close()
	out, _ := json.MarshalIndent(ctrl.Status(), "", "  ")
	fmt.Println(string(out))
}

func runOpenRGB(rest []string) {
	sub := "status"
	if len(rest) > 0 {
		sub = strings.ToLower(rest[0])
		rest = rest[1:]
	}

	baseURL := "http://" + envOr("TUYA_LAMP_ADDR", defaultAddr)
	client := &http.Client{Timeout: 3 * time.Second}

	if sub == "setup" {
		force := false
		for _, a := range rest {
			if a == "--force" || a == "-f" {
				force = true
			}
		}

		lampName := "Zinnia Crux CR90"
		ddpListen := envOr("TUYA_LAMP_DDP_ADDR", ddp.DefaultAddr)
		if resp, err := client.Get(baseURL + "/api/status"); err == nil {
			var st lamp.Status
			if json.NewDecoder(resp.Body).Decode(&st) == nil {
				if st.Name != "" {
					lampName = st.Name
				}
				if st.OpenRGB.ListenAddr != "" {
					ddpListen = st.OpenRGB.ListenAddr
				}
			}
			resp.Body.Close()
		} else if dir, err := lamp.ConfigDir(); err == nil {
			if ctrl, err := lamp.NewController(dir, nil); err == nil {
				st := ctrl.Status()
				if st.Name != "" {
					lampName = st.Name
				}
				ctrl.Close()
			}
		}

		host, port := "127.0.0.1", ddp.DefaultPort
		if h, pStr, err := net.SplitHostPort(ddpListen); err == nil {
			if h != "" && h != "0.0.0.0" {
				host = h
			}
			if p, err := strconv.Atoi(pStr); err == nil && p > 0 {
				port = p
			}
		}

		res, err := ddp.RegisterDevice("", lampName, host, port, 1, 1000, force)
		if err != nil {
			log.Fatalf("erro: %v", err)
		}
		fmt.Println(res.Message)
		if !res.AlreadyConfigured {
			fmt.Println("Abra o OpenRGB (ou clique em 'Detectar dispositivos') para a lâmpada aparecer junto ao WLED.")
		}
		return
	}

	if sub == "status" {
		if resp, err := client.Get(baseURL + "/api/openrgb"); err == nil {
			defer resp.Body.Close()
			var ost lamp.OpenRGBStatus
			if json.NewDecoder(resp.Body).Decode(&ost) == nil {
				out, _ := json.MarshalIndent(ost, "", "  ")
				fmt.Println(string(out))
				return
			}
		}
		dir, err := lamp.ConfigDir()
		if err != nil {
			log.Fatal(err)
		}
		ctrl, err := lamp.NewController(dir, nil)
		if err != nil {
			log.Fatal(err)
		}
		defer ctrl.Close()
		out, _ := json.MarshalIndent(ctrl.Status().OpenRGB, "", "  ")
		fmt.Println(string(out))
		return
	}

	var patch lamp.OpenRGBConfigPatch
	switch sub {
	case "on":
		v := true
		patch.Enabled = &v
	case "off":
		v := false
		patch.Enabled = &v
	case "bright":
		if len(rest) == 0 {
			log.Fatal("openrgb bright: informe um valor de 10 a 100")
		}
		v, err := strconv.Atoi(rest[0])
		if err != nil {
			log.Fatalf("openrgb bright: número inválido %q", rest[0])
		}
		patch.MaxBright = &v
	case "interval":
		if len(rest) == 0 {
			log.Fatal("openrgb interval: informe um valor em ms (20 a 1000)")
		}
		v, err := strconv.Atoi(rest[0])
		if err != nil {
			log.Fatalf("openrgb interval: número inválido %q", rest[0])
		}
		hold := v - 5
		if hold < 15 {
			hold = 15
		}
		if hold > 180 {
			hold = 180
		}
		patch.MinIntervalMs = &v
		patch.HoldMs = &hold
	case "black":
		if len(rest) == 0 || (rest[0] != "dim" && rest[0] != "off") {
			log.Fatal("openrgb black: use 'dim' ou 'off'")
		}
		mode := rest[0]
		patch.BlackMode = &mode
	default:
		log.Fatalf("subcomando openrgb desconhecido: %s", sub)
	}

	body, _ := json.Marshal(patch)
	if resp, err := client.Post(baseURL+"/api/openrgb", "application/json", bytes.NewReader(body)); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println("ok openrgb", sub)
			return
		}
		var errObj map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errObj)
		log.Fatalf("erro: %s", errObj["error"])
	}

	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	ctrl, err := lamp.NewController(dir, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ctrl.Close()
	if _, err := ctrl.SetOpenRGBConfig(patch); err != nil {
		log.Fatal(err)
	}
	fmt.Println("ok openrgb", sub)
}

func runCLI(action string, rest []string) {
	cmd := lamp.Command{Action: action}
	if len(rest) > 0 {
		switch action {
		case "bright", "white":
			v, err := strconv.Atoi(rest[0])
			if err != nil {
				log.Fatalf("%s: número inválido %q", action, rest[0])
			}
			cmd.Bright = &v
		case "temp":
			v, err := strconv.Atoi(rest[0])
			if err != nil {
				log.Fatalf("temp: número inválido %q", rest[0])
			}
			cmd.Temp = &v
		case "color":
			cmd.Color = rest[0]
		}
	}

	// Prefer talking to the running daemon over HTTP (no sudo required).
	baseURL := "http://" + envOr("TUYA_LAMP_ADDR", defaultAddr)
	body, _ := json.Marshal(cmd)
	client := &http.Client{Timeout: 3 * time.Second}
	if resp, err := client.Post(baseURL+"/api/command", "application/json", bytes.NewReader(body)); err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			fmt.Println("ok", action)
			return
		}
		var errObj map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errObj)
		log.Fatalf("erro: %s", errObj["error"])
	}

	// Fallback: direct local HCI transmission (requires CAP_NET_RAW / sudo).
	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	rad, err := radio.New("")
	if err != nil {
		log.Fatal(err)
	}
	ctrl, err := lamp.NewController(dir, rad)
	if err != nil {
		log.Fatal(err)
	}
	defer ctrl.Close()
	if _, err := ctrl.Apply(cmd, true); err != nil {
		log.Fatal(err)
	}
	fmt.Println("ok", action)
}

func runCapture(rest []string) {
	secs := 45
	if len(rest) > 0 {
		if v, err := strconv.Atoi(rest[0]); err == nil && v > 0 {
			secs = v
		}
	}
	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	rad, err := radio.New("")
	if err != nil {
		log.Fatal(err)
	}
	ctrl, err := lamp.NewController(dir, rad)
	if err != nil {
		log.Fatal(err)
	}
	defer ctrl.Close()

	onBuf := make([]byte, 16)
	offBuf := make([]byte, 16)
	copy(onBuf, p2.PayloadON)
	copy(offBuf, p2.PayloadOFF)
	crcON := p2.CRC8(onBuf)
	crcOFF := p2.CRC8(offBuf)

	fmt.Printf("Escutando %ds... No app Tuya/Smart Life, desligue e ligue a lâmpada.\n", secs)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs)*time.Second)
	defer cancel()

	var capData lamp.Capture
	err = rad.Scan(ctx, func(adv []byte) bool {
		frame := p2.FindFrame(adv)
		if len(frame) != p2.FrameLen || frame[0] != 0x0B || frame[7] != p2.SetDPs {
			return false
		}
		if frame[8] == crcON && capData.ON == "" {
			capData.ON = hex.EncodeToString(frame)
			fmt.Printf("  capturado ON  (nó %s)\n", capData.ON[6:10])
		} else if frame[8] == crcOFF && capData.OFF == "" {
			capData.OFF = hex.EncodeToString(frame)
			fmt.Printf("  capturado OFF (nó %s)\n", capData.OFF[6:10])
		}
		return capData.ON != "" && capData.OFF != ""
	})
	if err != nil {
		log.Fatal(err)
	}
	if capData.ON == "" || capData.OFF == "" {
		log.Fatal("Não foram capturados ambos OFF e ON dentro do tempo limite.")
	}
	if err := ctrl.SaveCapture(capData); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Captura salva! Próximo passo: tuya-lampd login")
}

func runLogin(rest []string) {
	var userCode string
	if len(rest) > 0 {
		userCode = rest[0]
	} else {
		fmt.Print("Código do Usuário (App Tuya: Eu > Config > Conta e Segurança > Código do Usuário): ")
		if _, err := fmt.Scanln(&userCode); err != nil || strings.TrimSpace(userCode) == "" {
			log.Fatal("Código do usuário inválido.")
		}
	}

	client := tuya.NewClient()
	token, qrURL, err := client.RequestQRCode(userCode)
	if err != nil {
		log.Fatal(err)
	}
	qr, err := qrcode.New(qrURL, qrcode.Medium)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(qr.ToSmallString(false))
	fmt.Println("Escaneie o QR Code no app Tuya/Smart Life (Eu > ícone de scanner) e confirme...")

	var loginRes *tuya.LoginResult
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		ok, res, err := client.PollLogin(token, userCode)
		if err != nil {
			log.Fatal(err)
		}
		if ok {
			loginRes = res
			break
		}
		time.Sleep(3 * time.Second)
	}
	if loginRes == nil {
		log.Fatal("Tempo esgotado aguardando confirmação do QR Code.")
	}

	devices, err := client.FetchDeviceKeys(loginRes)
	if err != nil {
		log.Fatal(err)
	}
	dir, err := lamp.ConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	ctrl, err := lamp.NewController(dir, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ctrl.Close()

	for _, d := range devices {
		if err := ctrl.DeriveAndSaveFromLocalKey(d.Name, d.LocalKey); err == nil {
			fmt.Printf("Sucesso! Lâmpada '%s' configurada (nó %s).\n", d.Name, ctrl.Status().Node)
			return
		}
	}
	log.Fatal("Login confirmado, mas nenhuma chave bateu com a captura. Rode 'tuya-lampd capture' primeiro.")
}

func envOr(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
