package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goodnightzsj/router-device-proxy/internal/config"
	"github.com/goodnightzsj/router-device-proxy/internal/device"
	"github.com/goodnightzsj/router-device-proxy/internal/discovery"
	"github.com/goodnightzsj/router-device-proxy/internal/policy"
	"github.com/goodnightzsj/router-device-proxy/internal/router"
	"github.com/goodnightzsj/router-device-proxy/internal/router/brand"
	"github.com/goodnightzsj/router-device-proxy/internal/webui"
)

type App struct {
	cfg   config.Config
	store *policy.Store
	names *discovery.Cache
	set   router.Set
	saved config.SavedConfig
	cfgMu sync.Mutex
	gwMu  sync.RWMutex
	gwMAC string
}

func (a *App) buildSet() router.Set {
	set, err := brand.New(a.cfg)
	if err != nil {
		log.Printf("router brand: %v", err)
		return router.Set{}
	}
	return set
}

func (a *App) snapshot() (config.Config, router.Set) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	return a.cfg, a.set
}

func (a *App) applyNames(ctx context.Context, devs []device.Device) []device.Device {
	a.cfgMu.Lock()
	sources := append([]router.NameSource(nil), a.set.Names...)
	a.cfgMu.Unlock()
	if len(sources) == 0 {
		return devs
	}
	byMAC := make(map[string]string)
	for _, source := range sources {
		names, err := source.Names(ctx)
		if err != nil {
			continue
		}
		for mac, name := range names {
			if _, ok := byMAC[mac]; !ok {
				byMAC[mac] = name
			}
		}
	}
	for i := range devs {
		name, ok := byMAC[device.NormalizeMAC(devs[i].MAC)]
		if !ok {
			continue
		}
		devs[i].Name = name
		if t := device.InferType(name, devs[i].Brand); t != "unknown" {
			devs[i].Type = t
		}
	}
	return devs
}

func (a *App) scanDevices(ctx context.Context) ([]device.Device, error) {
	cfg, set := a.snapshot()
	if cfg.Mock {
		return a.applyPolicies(device.MockDevices(), cfg.ProxyGateway), nil
	}
	if set.Devices == nil {
		return nil, router.ErrUnsupported
	}
	devs, err := set.Devices.Devices(ctx)
	if err != nil {
		return nil, err
	}
	devs = a.applyNames(ctx, devs)
	devs = a.discoverNames(ctx, devs)
	return a.applyPolicies(devs, cfg.ProxyGateway), nil
}

func (a *App) discoverNames(ctx context.Context, devs []device.Device) []device.Device {
	cfg, _ := a.snapshot()
	if !cfg.Discovery || a.names == nil {
		return devs
	}
	need := map[string]bool{}
	var ips []string
	for _, d := range devs {
		if d.IP == "" {
			continue
		}
		if d.Name != "" && d.Name != d.MAC && d.Type != "unknown" {
			continue
		}
		if _, ok := a.names.Get(d.IP); ok {
			continue
		}
		if !need[d.IP] {
			need[d.IP] = true
			ips = append(ips, d.IP)
		}
	}
	if len(ips) > 0 {
		res := discovery.Resolve(ctx, ips, discovery.Options{SSDP: cfg.DiscoverySSDP, Timeout: cfg.DiscoveryTimeout})
		for ip, name := range res.Names {
			a.names.Put(ip, discovery.Hit{Name: name, Priority: 2, At: time.Now()})
		}
		for ip, typ := range res.Types {
			a.names.Put(ip, discovery.Hit{Type: typ, Priority: 1, At: time.Now()})
		}
		for _, ip := range ips {
			if _, ok := a.names.Get(ip); !ok {
				a.names.Put(ip, discovery.Hit{At: time.Now()})
			}
		}
	}
	for i := range devs {
		hit, ok := a.names.Get(devs[i].IP)
		if !ok {
			continue
		}
		if devs[i].Name == "" || devs[i].Name == devs[i].MAC {
			if hit.Name != "" {
				devs[i].Name = hit.Name
			}
		}
		if devs[i].Type == "unknown" {
			if t := device.InferType(devs[i].Name, devs[i].Brand); t != "unknown" {
				devs[i].Type = t
			} else if hit.Type != "" {
				devs[i].Type = hit.Type
			}
		}
	}
	return devs
}

func (a *App) applyPolicies(devs []device.Device, gateway string) []device.Device {
	for i := range devs {
		p, _ := a.store.Get(devs[i].MAC)
		if p.Name != "" {
			devs[i].Note = p.Name
		}
		if p.Mode == "proxy" {
			devs[i].Mode = "proxy"
		} else {
			devs[i].Mode = "direct"
		}
		if gateway != "" && devs[i].IP == gateway {
			// Proxying the proxy gateway itself would create a loop.
			devs[i].Mode = "direct"
			a.setGatewayMAC(devs[i].MAC)
		}
	}
	return devs
}

func (a *App) setGatewayMAC(mac string) {
	a.gwMu.Lock()
	a.gwMAC = strings.ToLower(mac)
	a.gwMu.Unlock()
}

func (a *App) gatewayMAC() string {
	a.gwMu.RLock()
	defer a.gwMu.RUnlock()
	return a.gwMAC
}

func (a *App) sessionSecret() []byte {
	cfg, _ := a.snapshot()
	h := sha256.Sum256([]byte("session:" + cfg.AdminPassword))
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

func secretPresence(settings []router.Setting, secrets map[string]string) map[string]bool {
	out := make(map[string]bool)
	for _, s := range settings {
		if !s.Secret {
			continue
		}
		present := secrets[s.Key] != ""
		for _, alias := range s.Aliases {
			present = present || secrets[alias] != ""
		}
		out[s.Key] = present
	}
	return out
}

func expandClearSecrets(settings []router.Setting, keys []string) []string {
	out := append([]string(nil), keys...)
	for _, s := range settings {
		for _, key := range keys {
			if s.Key == key {
				out = append(out, s.Aliases...)
			}
		}
	}
	return out
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
	cfg, _ := a.snapshot()
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(cfg.AdminPassword)) != 1 {
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
	cfg, set := a.snapshot()
	ok := false
	if set.Health != nil {
		ok = set.Health.OK(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"router_ok":    ok,
		"gateway":      cfg.ProxyGateway,
		"router_host":  cfg.RouterHost,
		"mock":         cfg.Mock,
		"brand":        set.Brand,
		"capabilities": set.Capabilities(),
	})
}

func (a *App) handleDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := a.scanDevices(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"devices": devs, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
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
	if !device.ValidMAC(body.MAC) || (body.Mode != "proxy" && body.Mode != "direct") {
		http.Error(w, "invalid mac or mode", http.StatusBadRequest)
		return
	}
	if gm := a.gatewayMAC(); gm != "" && strings.EqualFold(gm, body.MAC) {
		http.Error(w, "代理网关不能设置为透明代理", http.StatusBadRequest)
		return
	}
	if err := a.store.SetMode(body.MAC, body.Mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		MAC  string `json:"mac"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !device.ValidMAC(body.MAC) {
		http.Error(w, "invalid mac", http.StatusBadRequest)
		return
	}
	if err := a.store.SetNote(body.MAC, device.CleanNote(body.Note)); err != nil {
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
	_, set := a.snapshot()
	if set.Policy == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": router.ErrUnsupported.Error()})
		return
	}
	snapshot := a.store.Snapshot()
	if gm := a.gatewayMAC(); gm != "" {
		delete(snapshot, strings.ToLower(gm))
	}
	out, err := set.Policy.Apply(r.Context(), snapshot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func (a *App) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	set := a.set
	authMethod := a.saved.AuthMethod
	if authMethod == "" {
		if a.cfg.RouterKey != "" {
			authMethod = "key"
		} else {
			authMethod = "password"
		}
	}
	options := make(map[string]string, len(a.cfg.Options)+1)
	for k, v := range a.cfg.Options {
		options[k] = v
	}
	options["lease_file"] = a.cfg.LeaseFile
	writeJSON(w, http.StatusOK, map[string]any{
		"router_host":   a.cfg.RouterHost,
		"router_user":   a.cfg.RouterUser,
		"auth_method":   authMethod,
		"has_password":  a.cfg.RouterPassword != "",
		"has_key":       a.cfg.RouterKey != "",
		"settings":      set.Settings,
		"options":       options,
		"has_secrets":   secretPresence(set.Settings, a.cfg.Secrets),
		"proxy_gateway": a.cfg.ProxyGateway,
		"lease_file":    a.cfg.LeaseFile,
		"conf_dir":      a.cfg.ConfDir,
		"tmp_dir":       a.cfg.TmpDir,
		"brand":         set.Brand,
		"capabilities":  set.Capabilities(),
	})
}

func (a *App) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		RouterHost   string            `json:"router_host"`
		RouterUser   string            `json:"router_user"`
		AuthMethod   string            `json:"auth_method"`
		Password     string            `json:"password"`
		Key          string            `json:"key"`
		Options      map[string]string `json:"options"`
		Secrets      map[string]string `json:"secrets"`
		ClearSecrets []string          `json:"clear_secrets"`
		ProxyGateway string            `json:"proxy_gateway"`
		LeaseFile    string            `json:"lease_file"`
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
	clearSecrets := expandClearSecrets(a.set.Settings, body.ClearSecrets)
	sc, newCfg, err := a.cfg.BuildSaved(config.Update{
		RouterHost:   body.RouterHost,
		RouterUser:   body.RouterUser,
		AuthMethod:   body.AuthMethod,
		Password:     body.Password,
		Key:          body.Key,
		Options:      body.Options,
		Secrets:      body.Secrets,
		ClearSecrets: clearSecrets,
		ProxyGateway: body.ProxyGateway,
		LeaseFile:    body.LeaseFile,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := config.Save(config.Path(a.cfg.DataDir), sc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.cfg = newCfg
	a.saved = sc
	a.set = a.buildSet()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleTest(w http.ResponseWriter, r *http.Request) {
	_, set := a.snapshot()
	if set.Health == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": router.ErrUnsupported.Error()})
		return
	}
	out, err := set.Health.Test(r.Context())
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func (a *App) handleInstallHook(w http.ResponseWriter, r *http.Request) {
	_, set := a.snapshot()
	if set.Persistence == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": router.ErrUnsupported.Error()})
		return
	}
	out, err := set.Persistence.Install(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "output": out})
}

func main() {
	cfg := config.Load()
	if cfg.AdminPassword == "" {
		log.Fatal("ADMIN_PASSWORD is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	sc, err := config.LoadSaved(config.Path(cfg.DataDir))
	if err != nil {
		log.Printf("load saved config: %v", err)
	}
	if err := config.ApplySaved(&cfg, sc, cfg.AdminPassword); err != nil {
		log.Printf("apply saved config: %v", err)
	}
	a := &App{cfg: cfg, saved: sc, names: discovery.NewCache()}
	a.set = a.buildSet()
	a.store = policy.NewStore(filepath.Join(cfg.DataDir, "policy.json"))
	if err := a.store.Load(); err != nil {
		log.Printf("load policy: %v", err)
	}
	device.LoadOUI()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/logout", a.handleLogout)
	mux.HandleFunc("/api/status", a.auth(a.handleStatus))
	mux.HandleFunc("/api/devices", a.auth(a.handleDevices))
	mux.HandleFunc("/api/policy", a.auth(a.handlePolicy))
	mux.HandleFunc("/api/note", a.auth(a.handleNote))
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

	sub, err := fs.Sub(webui.FS, ".")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	log.Printf("router-device-proxy listening on %s (brand=%s, mock=%v, router=%s)", cfg.Addr, a.set.Brand, cfg.Mock, cfg.RouterHost)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
