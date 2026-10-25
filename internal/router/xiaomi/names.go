package xiaomi

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

	"github.com/goodnightzsj/router-device-proxy/internal/device"
	"github.com/goodnightzsj/router-device-proxy/internal/sshx"
)

const (
	key      = "a2ffa5c9be07488bbb04a3a47d3c5f6a"
	namesTTL = 5 * time.Minute
	stokTTL  = 30 * time.Minute
	failTTL  = 5 * time.Minute
)

var httpClient = &http.Client{Timeout: 5 * time.Second}

// Xqdb reads Xiaomi's local device database over SSH.
type Xqdb struct {
	SSH  *sshx.Client
	Path string

	mu      sync.Mutex
	names   map[string]string
	at      time.Time
	failAt  time.Time
	failErr error
}

func (x *Xqdb) Names(ctx context.Context) (map[string]string, error) {
	if x == nil || x.SSH == nil || x.Path == "" {
		return nil, fmt.Errorf("xqdb disabled")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.names != nil && time.Since(x.at) < namesTTL {
		return x.names, nil
	}
	if x.failErr != nil && time.Since(x.failAt) < failTTL {
		return nil, x.failErr
	}
	out, err := x.SSH.Run("cat " + sshx.Quote(x.Path))
	if err == nil {
		var names map[string]string
		names, err = readXqdbNames([]byte(out))
		if err == nil {
			x.names = names
			x.at = time.Now()
			x.failErr = nil
			return names, nil
		}
	}
	x.failAt = time.Now()
	x.failErr = err
	return nil, err
}

// WebAPI reads Xiaomi's device list from the router's local web API.
type WebAPI struct {
	Host     string
	Password string

	mu      sync.Mutex
	stok    string
	stokAt  time.Time
	names   map[string]string
	namesAt time.Time
	failAt  time.Time
	failErr error
}

func (x *WebAPI) configured() bool {
	return x != nil && x.Host != "" && x.Password != ""
}

func (x *WebAPI) Names(ctx context.Context) (map[string]string, error) {
	if !x.configured() {
		return nil, fmt.Errorf("xiaomi web api disabled")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.names != nil && time.Since(x.namesAt) < namesTTL {
		return x.names, nil
	}
	if x.failErr != nil && time.Since(x.failAt) < failTTL {
		return nil, x.failErr
	}
	names, err := x.fetchLocked(ctx)
	if err != nil {
		x.failAt = time.Now()
		x.failErr = err
		log.Printf("xiaomi names: %v", err)
		return nil, err
	}
	x.failErr = nil
	x.names = names
	x.namesAt = time.Now()
	return names, nil
}

func (x *WebAPI) fetchLocked(ctx context.Context) (map[string]string, error) {
	if x.stok == "" || time.Since(x.stokAt) > stokTTL {
		if err := x.login(ctx); err != nil {
			return nil, err
		}
	}
	names, err := x.devices(ctx)
	if err == nil {
		return names, nil
	}
	x.stok = ""
	if loginErr := x.login(ctx); loginErr != nil {
		return nil, loginErr
	}
	return x.devices(ctx)
}

func (x *WebAPI) login(ctx context.Context) error {
	nonce := nonceNow(time.Now())
	u := "http://" + x.Host + "/cgi-bin/luci/api/xqsystem/login?username=admin&password=" +
		pwdHash(x.Password, nonce) + "&logtype=2&nonce=" + nonce
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
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

func (x *WebAPI) devices(ctx context.Context) (map[string]string, error) {
	u := "http://" + x.Host + "/cgi-bin/luci/;stok=" + url.PathEscape(x.stok) + "/api/misystem/devicelist"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return parseDevices(resp.Body)
}

func pwdHash(password, nonce string) string {
	inner := sha1.Sum([]byte(password + key))
	innerHex := hex.EncodeToString(inner[:])
	outer := sha1.Sum([]byte(nonce + innerHex))
	return hex.EncodeToString(outer[:])
}

func nonceNow(now time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("0__%d_0", now.Unix())
	}
	r := int(b[0])<<8 | int(b[1])
	return fmt.Sprintf("0__%d_%d", now.Unix(), r%10000)
}

func parseDevices(r io.Reader) (map[string]string, error) {
	type item struct {
		MAC   string `json:"mac"`
		Name  string `json:"name"`
		OName string `json:"oname"`
	}
	var out struct {
		Code       int    `json:"code"`
		Msg        string `json:"msg"`
		List       []item `json:"list"`
		DeviceList []item `json:"devicelist"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("xiaomi devicelist: %w", err)
	}
	if out.Code != 0 {
		return nil, fmt.Errorf("xiaomi devicelist code %d: %s", out.Code, out.Msg)
	}
	names := make(map[string]string, len(out.List)+len(out.DeviceList))
	add := func(d item) {
		mac := device.NormalizeMAC(d.MAC)
		if mac == "" {
			return
		}
		display := strings.TrimSpace(d.OName)
		if device.IsGenericName(display) {
			if n := strings.TrimSpace(d.Name); !device.IsGenericName(n) {
				display = n
			}
		}
		if display == "" {
			display = strings.TrimSpace(d.Name)
		}
		if display == "" || device.IsGenericName(display) {
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
