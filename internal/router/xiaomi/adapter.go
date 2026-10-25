package xiaomi

import (
	"os"

	"github.com/goodnightzsj/router-device-proxy/internal/router"
	"github.com/goodnightzsj/router-device-proxy/internal/sshx"
)

// Settings returns the Xiaomi-specific fields rendered by the generic
// settings UI. Secrets are write-only; the UI only learns whether one exists.
func Settings() []router.Setting {
	return []router.Setting{
		{
			Key:         "xiaomi_web_password",
			Label:       "小米后台密码（可选）",
			Placeholder: "用于读取 Xiaomi 设备名称",
			Secret:      true,
			Aliases:     []string{"legacy_web_password"},
		},
		{
			Key:         "xiaomi_xqdb",
			Label:       "小米设备库路径",
			Placeholder: "/data/etc/xqDb",
			Default:     "/data/etc/xqDb",
		},
	}
}

// NewNames builds the Xiaomi name sources. xqDb needs only SSH; the web API
// is added when a password is available. Set xiaomi_xqdb=off to skip xqDb.
func NewNames(ssh *sshx.Client, host string, secrets, options map[string]string) []router.NameSource {
	var out []router.NameSource
	path := options["xiaomi_xqdb"]
	if path == "" {
		path = os.Getenv("XIAOMI_XQDB")
	}
	if path == "" {
		path = "/data/etc/xqDb"
	}
	if path != "off" {
		out = append(out, &Xqdb{SSH: ssh, Path: path})
	}
	password := secrets["xiaomi_web_password"]
	if password == "" {
		password = secrets["legacy_web_password"]
	}
	if password == "" {
		password = os.Getenv("ROUTER_WEB_PASSWORD")
	}
	if password != "" {
		out = append(out, &WebAPI{Host: host, Password: password})
	}
	return out
}
