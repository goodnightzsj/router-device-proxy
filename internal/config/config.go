package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	Addr              string
	DataDir           string
	AdminPassword     string
	Brand             string
	RouterHost        string
	RouterUser        string
	RouterPassword    string
	RouterKey         string
	RouterWebPassword string
	XqdbPath          string
	ProxyGateway      string
	LeaseFile         string
	ConfDir           string
	TmpDir            string
	RestartCmd        string
	Discovery         bool
	DiscoverySSDP     bool
	DiscoveryTimeout  time.Duration
	Mock              bool
}

type SavedConfig struct {
	RouterHost           string `json:"router_host"`
	RouterUser           string `json:"router_user"`
	AuthMethod           string `json:"auth_method"`
	RouterPasswordEnc    string `json:"router_password_enc,omitempty"`
	RouterKeyEnc         string `json:"router_key_enc,omitempty"`
	RouterWebPasswordEnc string `json:"router_web_password_enc,omitempty"`
	ProxyGateway         string `json:"proxy_gateway"`
	LeaseFile            string `json:"lease_file,omitempty"`
	Salt                 string `json:"salt"`
}

type Update struct {
	RouterHost       string
	RouterUser       string
	AuthMethod       string
	Password         string
	Key              string
	WebPassword      string
	ClearWebPassword bool
	ProxyGateway     string
	LeaseFile        string
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

func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

func Load() Config {
	return Config{
		Addr:              env("ADDR", ":8090"),
		DataDir:           env("DATA_DIR", "/data"),
		AdminPassword:     os.Getenv("ADMIN_PASSWORD"),
		Brand:             env("ROUTER_BRAND", "auto"),
		RouterHost:        env("ROUTER_HOST", "192.168.31.1"),
		RouterUser:        env("ROUTER_USER", "root"),
		RouterPassword:    os.Getenv("ROUTER_PASSWORD"),
		RouterKey:         os.Getenv("ROUTER_KEY"),
		RouterWebPassword: os.Getenv("ROUTER_WEB_PASSWORD"),
		XqdbPath:          env("XIAOMI_XQDB", "/data/etc/xqDb"),
		ProxyGateway:      env("PROXY_GATEWAY", "192.168.31.122"),
		LeaseFile:         env("ROUTER_LEASE_FILE", "/tmp/dhcp.leases"),
		ConfDir:           env("ROUTER_CONF_DIR", "/data/dnsmasq.d"),
		TmpDir:            env("ROUTER_TMP_DIR", "/tmp/dnsmasq.d"),
		RestartCmd:        env("ROUTER_RESTART_CMD", "/etc/init.d/dnsmasq restart"),
		Discovery:         envBool("DISCOVERY", true),
		DiscoverySSDP:     envBool("DISCOVERY_SSDP", true),
		DiscoveryTimeout:  envDuration("DISCOVERY_TIMEOUT", 2*time.Second),
		Mock:              envBool("MOCK", false),
	}
}

func Path(dataDir string) string {
	return filepath.Join(dataDir, "config.json")
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

func LoadSaved(path, admin string) (SavedConfig, error) {
	var sc SavedConfig
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sc, nil
		}
		return sc, err
	}
	if len(b) == 0 {
		return sc, nil
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		return sc, err
	}
	return sc, nil
}

func ApplySaved(cfg *Config, sc SavedConfig, admin string) error {
	salt, err := base64.StdEncoding.DecodeString(sc.Salt)
	if err != nil {
		return err
	}
	if sc.RouterHost != "" {
		cfg.RouterHost = sc.RouterHost
	}
	if sc.RouterUser != "" {
		cfg.RouterUser = sc.RouterUser
	}
	if sc.ProxyGateway != "" {
		cfg.ProxyGateway = sc.ProxyGateway
	}
	if sc.LeaseFile != "" {
		cfg.LeaseFile = sc.LeaseFile
	}
	if v, err := decryptString(sc.RouterPasswordEnc, admin, salt); err == nil && v != "" {
		cfg.RouterPassword = v
		cfg.RouterKey = ""
	}
	if v, err := decryptString(sc.RouterKeyEnc, admin, salt); err == nil && v != "" {
		cfg.RouterKey = v
		cfg.RouterPassword = ""
	}
	if v, err := decryptString(sc.RouterWebPasswordEnc, admin, salt); err == nil && v != "" {
		cfg.RouterWebPassword = v
	}
	return nil
}

func (cfg Config) BuildSaved(u Update) (SavedConfig, Config, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return SavedConfig{}, cfg, err
	}
	password, key, web := u.Password, u.Key, u.WebPassword
	if u.AuthMethod == "password" && password == "" {
		password = cfg.RouterPassword
	}
	if u.AuthMethod == "key" && key == "" {
		key = cfg.RouterKey
	}
	if web == "" && !u.ClearWebPassword {
		web = cfg.RouterWebPassword
	}
	if u.ClearWebPassword {
		web = ""
	}
	sc := SavedConfig{
		RouterHost:   u.RouterHost,
		RouterUser:   u.RouterUser,
		AuthMethod:   u.AuthMethod,
		ProxyGateway: u.ProxyGateway,
		LeaseFile:    u.LeaseFile,
		Salt:         base64.StdEncoding.EncodeToString(salt),
	}
	var err error
	if u.AuthMethod == "password" {
		sc.RouterPasswordEnc, err = encryptString(password, cfg.AdminPassword, salt)
		if err != nil {
			return sc, cfg, err
		}
	}
	if u.AuthMethod == "key" {
		sc.RouterKeyEnc, err = encryptString(key, cfg.AdminPassword, salt)
		if err != nil {
			return sc, cfg, err
		}
	}
	sc.RouterWebPasswordEnc, err = encryptString(web, cfg.AdminPassword, salt)
	if err != nil {
		return sc, cfg, err
	}
	cfg.RouterHost = u.RouterHost
	cfg.RouterUser = u.RouterUser
	cfg.ProxyGateway = u.ProxyGateway
	if u.LeaseFile != "" {
		cfg.LeaseFile = u.LeaseFile
	}
	if u.AuthMethod == "password" {
		cfg.RouterPassword = password
		cfg.RouterKey = ""
	} else {
		cfg.RouterKey = key
		cfg.RouterPassword = ""
	}
	cfg.RouterWebPassword = web
	return sc, cfg, nil
}

func Save(path, admin string, sc SavedConfig) error {
	b, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
