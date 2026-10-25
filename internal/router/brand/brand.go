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
	}
	xiaomiSetting := router.Setting{
		Key:         "xiaomi_web_password",
		Label:       "小米后台密码（可选）",
		Placeholder: "用于读取 Xiaomi 设备名称",
		Secret:      true,
	}
	xiaomiPath := router.Setting{
		Key:         "xiaomi_xqdb",
		Label:       "小米设备库路径",
		Placeholder: "/data/etc/xqDb",
		Default:     "/data/etc/xqDb",
	}
	switch cfg.Brand {
	case "", "auto":
		set.Brand = "auto"
		set.Settings = append(set.Settings, xiaomiSetting, xiaomiPath)
		if path := xqdbPath(cfg); path != "" {
			set.Names = append(set.Names, &xiaomi.Xqdb{SSH: sshClient, Path: path})
		}
		if pw := xiaomiPassword(cfg); pw != "" {
			set.Names = append(set.Names, &xiaomi.WebAPI{Host: cfg.RouterHost, Password: pw})
		}
	case "xiaomi":
		set.Settings = append(set.Settings, xiaomiSetting, xiaomiPath)
		if path := xqdbPath(cfg); path != "" {
			set.Names = append(set.Names, &xiaomi.Xqdb{SSH: sshClient, Path: path})
		}
		if pw := xiaomiPassword(cfg); pw != "" {
			set.Names = append(set.Names, &xiaomi.WebAPI{Host: cfg.RouterHost, Password: pw})
		}
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

func xiaomiPassword(cfg config.Config) string {
	if v := cfg.Secrets["xiaomi_web_password"]; v != "" {
		return v
	}
	if v := cfg.Secrets["legacy_web_password"]; v != "" {
		return v
	}
	return os.Getenv("ROUTER_WEB_PASSWORD")
}

func xqdbPath(cfg config.Config) string {
	if v := cfg.Option("xiaomi_xqdb", ""); v != "" {
		return v
	}
	if v := os.Getenv("XIAOMI_XQDB"); v != "" {
		return v
	}
	return "/data/etc/xqDb"
}
