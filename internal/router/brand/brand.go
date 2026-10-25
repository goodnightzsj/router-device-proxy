package brand

import (
	"fmt"
	"os"

	"github.com/goodnightzsj/router-device-proxy/internal/config"
	"github.com/goodnightzsj/router-device-proxy/internal/router"
	"github.com/goodnightzsj/router-device-proxy/internal/router/generic"
	"github.com/goodnightzsj/router-device-proxy/internal/router/openwrt"
	"github.com/goodnightzsj/router-device-proxy/internal/router/xiaomi"
	"github.com/goodnightzsj/router-device-proxy/internal/sshx"
)

// New assembles the configured brand. auto keeps the generic OpenWrt device
// table and adds Xiaomi name sources when they are configured.
func New(cfg config.Config) (router.Set, error) {
	sshClient := sshx.New(cfg.RouterHost, cfg.RouterUser, cfg.RouterPassword, cfg.RouterKey)
	ow := &openwrt.Adapter{
		SSH:          sshClient,
		LeaseFile:    cfg.LeaseFile,
		ConfDir:      cfg.ConfDir,
		TmpDir:       cfg.TmpDir,
		RestartCmd:   cfg.RestartCmd,
		ProxyGateway: cfg.ProxyGateway,
	}
	set := router.Set{
		Devices:     ow,
		Policy:      ow,
		Persistence: ow,
		Health:      ow,
		Brand:       "openwrt",
		Caps:        router.Capabilities{Devices: true, Names: true, Policy: true, Persistence: true},
		Settings:    openwrt.Settings(),
	}
	switch cfg.Brand {
	case "", "auto":
		set.Brand = "auto"
		hasSecret := cfg.Secrets["xiaomi_web_password"] != "" || cfg.Secrets["legacy_web_password"] != ""
		explicit := cfg.Options["xiaomi_xqdb"] != "" || os.Getenv("XIAOMI_XQDB") != ""
		if hasSecret || explicit {
			set.Settings = append(set.Settings, xiaomi.Settings()...)
		}
		// Names are always mounted so a transient probe failure does not
		// disable xqDb; unsupported routers fail this read gracefully.
		set.Names = append(set.Names, xiaomi.NewNames(sshClient, cfg.RouterHost, cfg.Secrets, cfg.Options)...)
	case "xiaomi":
		set.Settings = append(set.Settings, xiaomi.Settings()...)
		set.Names = append(set.Names, xiaomi.NewNames(sshClient, cfg.RouterHost, cfg.Secrets, cfg.Options)...)
		set.Brand = "xiaomi"
	case "generic":
		gen := generic.New(ow)
		set.Devices = gen
		set.Policy = gen
		set.Persistence = gen
		set.Health = gen
		set.Brand = "generic"
		set.Caps = router.Capabilities{Devices: true, Names: true, Policy: false, Persistence: false}
	case "openwrt":
		set.Brand = "openwrt"
	default:
		return router.Set{}, fmt.Errorf("unknown router brand %q", cfg.Brand)
	}
	return set, nil
}
