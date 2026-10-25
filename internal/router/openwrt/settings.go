package openwrt

import "github.com/goodnightzsj/router-device-proxy/internal/router"

// Settings are the fields needed by the SSH/dnsmasq device source. They are
// rendered by the generic settings UI and stored as brand options.
func Settings() []router.Setting {
	return []router.Setting{
		{
			Key:         "lease_file",
			Label:       "DHCP leases 路径",
			Placeholder: "/tmp/dhcp.leases",
			Default:     "/tmp/dhcp.leases",
		},
	}
}
