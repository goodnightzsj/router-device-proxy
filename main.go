package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var webFS embed.FS

type Config struct {
	Addr           string
	DataDir        string
	AdminPassword  string
	RouterHost     string
	RouterUser     string
	RouterPassword string
	RouterKey      string
	ProxyGateway   string
	LeaseFile      string
	ConfDir        string
	TmpDir         string
	RestartCmd     string
	Mock           bool
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func loadConfig() Config {
	return Config{
		Addr:           env("ADDR", ":8090"),
		DataDir:        env("DATA_DIR", "/data"),
		AdminPassword:  os.Getenv("ADMIN_PASSWORD"),
		RouterHost:     env("ROUTER_HOST", "192.168.31.1"),
		RouterUser:     env("ROUTER_USER", "root"),
		RouterPassword: os.Getenv("ROUTER_PASSWORD"),
		RouterKey:      os.Getenv("ROUTER_KEY"),
		ProxyGateway:   env("PROXY_GATEWAY", "192.168.31.122"),
		LeaseFile:      env("ROUTER_LEASE_FILE", "/tmp/dhcp.leases"),
		ConfDir:        env("ROUTER_CONF_DIR", "/data/dnsmasq.d"),
		TmpDir:         env("ROUTER_TMP_DIR", "/tmp/dnsmasq.d"),
		RestartCmd:     env("ROUTER_RESTART_CMD", "/etc/init.d/dnsmasq restart"),
		Mock:           envBool("MOCK", false),
	}
}

type SavedConfig struct {
	RouterHost        string `json:"router_host"`
	RouterUser        string `json:"router_user"`
	AuthMethod        string `json:"auth_method"`
	RouterPasswordEnc string `json:"router_password_enc,omitempty"`
	RouterKeyEnc      string `json:"router_key_enc,omitempty"`
	ProxyGateway      string `json:"proxy_gateway"`
	LeaseFile         string `json:"lease_file,omitempty"`
	Salt              string `json:"salt"`
}

type App struct {
	cfg   Config
	store *Store
	cfgMu sync.Mutex
	saved SavedConfig
}

func deriveKey(password string, salt []byte) []byte {
	h := sha256.New()
	h.Write([]byte(password))
	h.Write(salt)
	return h.Sum(nil)
}

func encryptString(plain, password string, salt []byte) (string, error) {
	if plain == "" {
		return "", nil
	}
	block, err := aes.NewCipher(deriveKey(password, salt))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

func decryptString(enc, password string, salt []byte) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveKey(password, salt))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (a *App) configPath() string {
	return filepath.Join(a.cfg.DataDir, "config.json")
}

func (a *App) loadSaved() error {
	b, err := os.ReadFile(a.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var sc SavedConfig
	if err := json.Unmarshal(b, &sc); err != nil {
		return err
	}
	a.saved = sc
	salt, err := base64.StdEncoding.DecodeString(sc.Salt)
	if err != nil {
		return err
	}
	if sc.RouterHost != "" {
		a.cfg.RouterHost = sc.RouterHost
	}
	if sc.RouterUser != "" {
		a.cfg.RouterUser = sc.RouterUser
	}
	if sc.ProxyGateway != "" {
		a.cfg.ProxyGateway = sc.ProxyGateway
	}
	if sc.LeaseFile != "" {
		a.cfg.LeaseFile = sc.LeaseFile
	}
	if v, err := decryptString(sc.RouterPasswordEnc, a.cfg.AdminPassword, salt); err == nil && v != "" {
		a.cfg.RouterPassword = v
		a.cfg.RouterKey = ""
	}
	if v, err := decryptString(sc.RouterKeyEnc, a.cfg.AdminPassword, salt); err == nil && v != "" {
		a.cfg.RouterKey = v
		a.cfg.RouterPassword = ""
	}
	return nil
}

func (a *App) saveSaved(host, user, authMethod, password, key, gateway, leaseFile string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	sc := SavedConfig{
		RouterHost:   host,
		RouterUser:   user,
		AuthMethod:   authMethod,
		ProxyGateway: gateway,
		LeaseFile:    leaseFile,
		Salt:         base64.StdEncoding.EncodeToString(salt),
	}
	var err error
	if authMethod == "password" {
		if password == "" {
			password = a.cfg.RouterPassword
		}
		sc.RouterPasswordEnc, err = encryptString(password, a.cfg.AdminPassword, salt)
		if err != nil {
			return err
		}
	}
	if authMethod == "key" {
		if key == "" {
			key = a.cfg.RouterKey
		}
		sc.RouterKeyEnc, err = encryptString(key, a.cfg.AdminPassword, salt)
		if err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.configPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.configPath()); err != nil {
		return err
	}
	a.saved = sc
	a.cfg.RouterHost = host
	a.cfg.RouterUser = user
	a.cfg.ProxyGateway = gateway
	if leaseFile != "" {
		a.cfg.LeaseFile = leaseFile
	}
	if authMethod == "password" {
		a.cfg.RouterPassword = password
		a.cfg.RouterKey = ""
	} else {
		a.cfg.RouterKey = key
		a.cfg.RouterPassword = ""
	}
	return nil
}

func (a *App) sessionSecret() []byte {
	h := sha256.Sum256([]byte("session:" + a.cfg.AdminPassword))
	return h[:]
}

func (a *App) makeSession() string {
	exp := strconv.FormatInt(time.Now().Add(12*time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, a.sessionSecret())
	mac.Write([]byte(exp))
	return base64.RawURLEncoding.EncodeToString([]byte(exp)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *App) validSession(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 2 {
		return false
	}
	expRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.sessionSecret())
	mac.Write(expRaw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	exp, err := strconv.ParseInt(string(expRaw), 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	return true
}

func (a *App) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("session")
		if err != nil || !a.validSession(c.Value) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(a.cfg.AdminPassword)) != 1 {
		time.Sleep(300 * time.Millisecond)
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    a.makeSession(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   12 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	// Status is polled on every page load; keep it to one quick SSH echo so it
	// does not trigger a second full device scan alongside /api/devices.
	writeJSON(w, http.StatusOK, map[string]any{
		"router_ok":   a.sshOK(),
		"gateway":     a.cfg.ProxyGateway,
		"router_host": a.cfg.RouterHost,
		"mock":        a.cfg.Mock,
	})
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices, err := a.scanDevices()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"devices": devices, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (a *App) handlePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		MAC  string `json:"mac"`
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !validMAC(body.MAC) || (body.Mode != "proxy" && body.Mode != "direct") {
		http.Error(w, "invalid mac or mode", http.StatusBadRequest)
		return
	}
	if err := a.store.SetMode(body.MAC, body.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.applyPolicy()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func (a *App) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	authMethod := a.saved.AuthMethod
	if authMethod == "" {
		if a.cfg.RouterKey != "" {
			authMethod = "key"
		} else {
			authMethod = "password"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"router_host":   a.cfg.RouterHost,
		"router_user":   a.cfg.RouterUser,
		"auth_method":   authMethod,
		"has_password":  a.cfg.RouterPassword != "",
		"has_key":       a.cfg.RouterKey != "",
		"proxy_gateway": a.cfg.ProxyGateway,
		"lease_file":    a.cfg.LeaseFile,
		"conf_dir":      a.cfg.ConfDir,
		"tmp_dir":       a.cfg.TmpDir,
	})
}

func (a *App) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RouterHost   string `json:"router_host"`
		RouterUser   string `json:"router_user"`
		AuthMethod   string `json:"auth_method"`
		Password     string `json:"password"`
		Key          string `json:"key"`
		ProxyGateway string `json:"proxy_gateway"`
		LeaseFile    string `json:"lease_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.RouterHost == "" || body.RouterUser == "" {
		http.Error(w, "host and user required", http.StatusBadRequest)
		return
	}
	if body.AuthMethod != "password" && body.AuthMethod != "key" {
		http.Error(w, "auth_method must be password or key", http.StatusBadRequest)
		return
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	if err := a.saveSaved(body.RouterHost, body.RouterUser, body.AuthMethod, body.Password, body.Key, body.ProxyGateway, body.LeaseFile); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleTest(w http.ResponseWriter, r *http.Request) {
	out, err := a.sshRun("echo ROUTER_DEVICE_PROXY_OK")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": strings.TrimSpace(out)})
}

func (a *App) handleInstallHook(w http.ResponseWriter, r *http.Request) {
	out, err := a.installHook()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func main() {
	cfg := loadConfig()
	if cfg.AdminPassword == "" {
		log.Fatal("ADMIN_PASSWORD is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	a := &App{cfg: cfg}
	if err := a.loadSaved(); err != nil {
		log.Printf("load saved config: %v", err)
	}
	a.store = NewStore(filepath.Join(cfg.DataDir, "policy.json"))
	if err := a.store.Load(); err != nil {
		log.Printf("load policy: %v", err)
	}
	loadOUI()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/logout", a.handleLogout)
	mux.HandleFunc("/api/status", a.auth(a.handleStatus))
	mux.HandleFunc("/api/devices", a.auth(a.handleDevices))
	mux.HandleFunc("/api/policy", a.auth(a.handlePolicy))
	mux.HandleFunc("/api/apply", a.auth(a.handleApply))
	mux.HandleFunc("/api/config", a.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			a.handleSaveConfig(w, r)
			return
		}
		a.handleGetConfig(w, r)
	}))
	mux.HandleFunc("/api/test", a.auth(a.handleTest))
	mux.HandleFunc("/api/install-hook", a.auth(a.handleInstallHook))

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	log.Printf("router-device-proxy listening on %s (mock=%v, router=%s)", cfg.Addr, cfg.Mock, cfg.RouterHost)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
