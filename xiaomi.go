package main

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// Public constant embedded in the Xiaomi web UI login helper.
	xiaomiKey      = "a2ffa5c9be07488bbb04a3a47d3c5f6a"
	xiaomiNamesTTL = 5 * time.Minute
	xiaomiStokTTL  = 30 * time.Minute
	xiaomiFailTTL  = 5 * time.Minute
)

var xiaomiHTTP = &http.Client{Timeout: 5 * time.Second}

type xiaomiClient struct {
	host     string
	password string

	mu      sync.Mutex
	stok    string
	stokAt  time.Time
	names   map[string]string
	namesAt time.Time
	failAt  time.Time
	failErr error
}

func newXiaomiClient(host, password string) *xiaomiClient {
	return &xiaomiClient{host: strings.TrimSpace(host), password: password}
}

func (x *xiaomiClient) configured() bool {
	return x != nil && x.host != "" && x.password != ""
}

func (a *App) xiaomiClient() *xiaomiClient {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.xiaomi
}

// xiaomiPwdHash mirrors Helper.Encrypt.oldPwd from the router's web UI:
// SHA1(nonce + SHA1(password + key)).
func xiaomiPwdHash(password, nonce string) string {
	inner := sha1.Sum([]byte(password + xiaomiKey))
	innerHex := hex.EncodeToString(inner[:])
	outer := sha1.Sum([]byte(nonce + innerHex))
	return hex.EncodeToString(outer[:])
}

func xiaomiNonce(now time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("0__%d_0", now.Unix())
	}
	r := int(b[0])<<8 | int(b[1])
	return fmt.Sprintf("0__%d_%d", now.Unix(), r%10000)
}

func normalizeMAC(mac string) string {
	mac = strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(mac))
	if len(mac) != 12 {
		return ""
	}
	for _, r := range mac {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return mac
}

func isGenericName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "android", "unknown", "localhost", "device":
		return true
	}
	return normalizeMAC(name) != ""
}

func (x *xiaomiClient) fetchNames(ctx context.Context) (map[string]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.names != nil && time.Since(x.namesAt) < xiaomiNamesTTL {
		return x.names, nil
	}
	if x.failErr != nil && time.Since(x.failAt) < xiaomiFailTTL {
		return nil, x.failErr
	}
	names, err := x.fetchLocked(ctx)
	if err != nil {
		x.failAt = time.Now()
		x.failErr = err
		return nil, err
	}
	x.failErr = nil
	x.names = names
	x.namesAt = time.Now()
	return names, nil
}

func (x *xiaomiClient) fetchLocked(ctx context.Context) (map[string]string, error) {
	if x.stok == "" || time.Since(x.stokAt) > xiaomiStokTTL {
		if err := x.login(ctx); err != nil {
			return nil, err
		}
	}
	names, err := x.devices(ctx)
	if err == nil {
		return names, nil
	}
	// The stok may have expired; log in once more and retry.
	x.stok = ""
	if loginErr := x.login(ctx); loginErr != nil {
		return nil, loginErr
	}
	return x.devices(ctx)
}

func (x *xiaomiClient) login(ctx context.Context) error {
	nonce := xiaomiNonce(time.Now())
	u := "http://" + x.host + "/cgi-bin/luci/api/xqsystem/login?username=admin&password=" +
		xiaomiPwdHash(x.password, nonce) + "&logtype=2&nonce=" + nonce
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := xiaomiHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Code  int    `json:"code"`
		Token string `json:"token"`
		Stok  string `json:"stok"`
		Msg   string `json:"msg"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return fmt.Errorf("xiaomi login: %w", err)
	}
	if out.Code != 0 {
		return fmt.Errorf("xiaomi login code %d: %s", out.Code, out.Msg)
	}
	token := out.Token
	if token == "" {
		token = out.Stok
	}
	if token == "" {
		return fmt.Errorf("xiaomi login: empty token")
	}
	x.stok = token
	x.stokAt = time.Now()
	return nil
}

func (x *xiaomiClient) devices(ctx context.Context) (map[string]string, error) {
	u := "http://" + x.host + "/cgi-bin/luci/;stok=" + url.PathEscape(x.stok) + "/api/misystem/devicelist"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := xiaomiHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseXiaomiDevices(resp.Body)
}

func parseXiaomiDevices(r io.Reader) (map[string]string, error) {
	type device struct {
		MAC   string `json:"mac"`
		Name  string `json:"name"`
		OName string `json:"oname"`
	}
	var out struct {
		Code       int      `json:"code"`
		Msg        string   `json:"msg"`
		List       []device `json:"list"`
		DeviceList []device `json:"devicelist"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("xiaomi devicelist: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("xiaomi devicelist code %d: %s", out.Code, out.Msg)
	}
	names := make(map[string]string, len(out.List)+len(out.DeviceList))
	add := func(d device) {
		mac := normalizeMAC(d.MAC)
		if mac == "" {
			return
		}
		display := strings.TrimSpace(d.OName)
		if isGenericName(display) {
			if n := strings.TrimSpace(d.Name); !isGenericName(n) {
				display = n
			}
		}
		if display == "" {
			display = strings.TrimSpace(d.Name)
		}
		if display == "" || isGenericName(display) {
			return
		}
		names[mac] = display
	}
	for _, d := range out.List {
		add(d)
	}
	for _, d := range out.DeviceList {
		add(d)
	}
	return names, nil
}

func (a *App) applyXiaomiNames(ctx context.Context, devices []Device, xc *xiaomiClient) []Device {
	names, err := xc.fetchNames(ctx)
	if err != nil {
		log.Printf("xiaomi names: %v", err)
		return devices
	}
	applied := 0
	for i := range devices {
		name, ok := names[normalizeMAC(devices[i].MAC)]
		if !ok {
			continue
		}
		devices[i].Name = name
		if t := inferType(name, devices[i].Brand); t != "unknown" {
			devices[i].Type = t
		}
		applied++
	}
	log.Printf("xiaomi names: %d/%d devices", applied, len(devices))
	return devices
}
