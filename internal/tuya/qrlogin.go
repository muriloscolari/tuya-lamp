package tuya

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ClientID = "HA_3y9q4ak7g4ephrvke"
	Schema   = "haauthorize"
	BaseURL  = "https://apigw.iotbing.com"
)

// DeviceKey represents a Tuya device and its local encryption key.
type DeviceKey struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	LocalKey string `json:"local_key"`
}

// LoginResult holds the credentials returned by Tuya after QR scan confirmation.
type LoginResult struct {
	UID          string `json:"uid"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpireTime   int64  `json:"expire_time"`
	Endpoint     string `json:"endpoint"`
	TerminalID   string `json:"terminal_id"`
	T            int64  `json:"t"`
}

// Client talks to Tuya's home-assistant QR login and device sharing API.
type Client struct {
	http *http.Client
}

// NewClient creates a Tuya API client.
func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// RequestQRCode requests a login QR token for the given Tuya/Smart Life user code.
func (c *Client) RequestQRCode(userCode string) (token string, qrURL string, err error) {
	u := fmt.Sprintf("%s/v1.0/m/life/home-assistant/qrcode/tokens?clientid=%s&usercode=%s&schema=%s",
		BaseURL, url.QueryEscape(ClientID), url.QueryEscape(strings.TrimSpace(userCode)), url.QueryEscape(Schema))

	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	var body struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Code    any    `json:"code"`
		Result  struct {
			QRCode string `json:"qrcode"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	if !body.Success || body.Result.QRCode == "" {
		return "", "", fmt.Errorf("tuya refused user code (%v: %s)", body.Code, body.Msg)
	}
	return body.Result.QRCode, "tuyaSmart--qrLogin?token=" + body.Result.QRCode, nil
}

// PollLogin checks whether the user has scanned and confirmed the QR code.
func (c *Client) PollLogin(token, userCode string) (bool, *LoginResult, error) {
	u := fmt.Sprintf("%s/v1.0/m/life/home-assistant/qrcode/tokens/%s?clientid=%s&usercode=%s",
		BaseURL, url.PathEscape(token), url.QueryEscape(ClientID), url.QueryEscape(strings.TrimSpace(userCode)))

	resp, err := c.http.Get(u)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Success bool        `json:"success"`
		T       int64       `json:"t"`
		Result  LoginResult `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return false, nil, err
	}
	if !body.Success {
		return false, nil, nil
	}
	body.Result.T = body.T
	return true, &body.Result, nil
}

// FetchDeviceKeys fetches all devices and their local keys for an authenticated session.
func (c *Client) FetchDeviceKeys(login *LoginResult) ([]DeviceKey, error) {
	if login == nil || login.Endpoint == "" || login.AccessToken == "" {
		return nil, errors.New("tuya: incomplete login result")
	}

	rawHomes, err := c.signedGet(login, "/v1.0/m/life/users/homes", nil)
	if err != nil {
		return nil, fmt.Errorf("query homes: %w", err)
	}
	var homes []struct {
		OwnerID any    `json:"ownerId"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(rawHomes, &homes); err != nil {
		return nil, fmt.Errorf("decode homes: %w", err)
	}

	var out []DeviceKey
	seen := make(map[string]bool)
	for _, h := range homes {
		homeID := fmt.Sprint(h.OwnerID)
		rawDevs, err := c.signedGet(login, "/v1.0/m/life/ha/home/devices", map[string]string{"homeId": homeID})
		if err != nil {
			continue
		}
		var devs []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Category string `json:"category"`
			LocalKey string `json:"local_key"`
		}
		if err := json.Unmarshal(rawDevs, &devs); err != nil {
			continue
		}
		for _, d := range devs {
			if (len(d.LocalKey) == 16 || len(d.LocalKey) == 32) && !seen[d.ID] {
				seen[d.ID] = true
				out = append(out, DeviceKey{
					ID:       d.ID,
					Name:     d.Name,
					Category: d.Category,
					LocalKey: d.LocalKey,
				})
			}
		}
	}
	return out, nil
}

func (c *Client) signedGet(login *LoginResult, path string, params map[string]string) (json.RawMessage, error) {
	rid := newUUIDv4()
	sum := md5.Sum([]byte(rid + login.RefreshToken))
	hashKey := hex.EncodeToString(sum[:])
	secret := secretGenerating(rid, "", hashKey)

	queryEnc := ""
	fullURL := strings.TrimRight(login.Endpoint, "/") + path
	if len(params) > 0 {
		raw, _ := json.Marshal(params)
		enc, err := aesGCMEncrypt(raw, []byte(secret))
		if err != nil {
			return nil, err
		}
		queryEnc = enc
		fullURL += "?encdata=" + url.QueryEscape(queryEnc)
	}

	t := fmt.Sprintf("%d", time.Now().UnixMilli())
	headers := map[string]string{
		"X-appKey":    ClientID,
		"X-requestId": rid,
		"X-time":      t,
		"X-token":     login.AccessToken,
	}
	sign := restfulSign(hashKey, queryEnc, "", headers)

	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-sign", sign)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Success bool   `json:"success"`
		Code    any    `json:"code"`
		Msg     string `json:"msg"`
		Result  string `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, err
	}
	if !envelope.Success {
		return nil, fmt.Errorf("tuya api error %v: %s", envelope.Code, envelope.Msg)
	}
	if envelope.Result == "" {
		return json.RawMessage("null"), nil
	}
	plain, err := aesGCMDecrypt(envelope.Result, []byte(secret))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(plain), nil
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomNonce12() string {
	const alphabet = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"
	var b [12]byte
	var r [12]byte
	_, _ = rand.Read(r[:])
	for i := range b {
		b[i] = alphabet[int(r[i])%len(alphabet)]
	}
	return string(b[:])
}

func aesGCMEncrypt(raw, secret []byte) (string, error) {
	nonce := []byte(randomNonce12())
	block, err := aes.NewCipher(secret)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, raw, nil)
	return base64.StdEncoding.EncodeToString(nonce) + base64.StdEncoding.EncodeToString(ct), nil
}

func aesGCMDecrypt(cipherData string, secret []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherData)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 {
		return nil, errors.New("tuya: ciphertext too short")
	}
	nonce := raw[:12]
	ct := raw[12:]
	block, err := aes.NewCipher(secret)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, nil)
}

func secretGenerating(rid, sid, hashKey string) string {
	msg := hashKey
	if sid != "" {
		l := len(sid)
		if l > 16 {
			l = 16
		}
		var ecode bytes.Buffer
		for i := 0; i < l; i++ {
			idx := int(sid[i]) % 16
			ecode.WriteByte(sid[idx])
		}
		msg += "_" + ecode.String()
	}
	mac := hmac.New(sha256.New, []byte(rid))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

func restfulSign(hashKey, queryEnc, bodyEnc string, headers map[string]string) string {
	keys := []string{"X-appKey", "X-requestId", "X-sid", "X-time", "X-token"}
	var parts []string
	for _, k := range keys {
		if v := headers[k]; v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	signStr := strings.Join(parts, "||") + queryEnc + bodyEnc
	mac := hmac.New(sha256.New, []byte(hashKey))
	mac.Write([]byte(signStr))
	return hex.EncodeToString(mac.Sum(nil))
}
