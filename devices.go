package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/csv"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed oui/oui.csv.gz
var ouiGz []byte

var ouiMap = map[string]string{}

type Device struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Brand  string `json:"brand"`
	Type   string `json:"type"`
	IP     string `json:"ip"`
	Online bool   `json:"online"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

func loadOUI() {
	zr, err := gzip.NewReader(bytes.NewReader(ouiGz))
	if err != nil {
		return
	}
	defer zr.Close()
	r := csv.NewReader(zr)
	r.FieldsPerRecord = -1
	for {
		rec, err := r.Read()
		if err != nil {
			break
		}
		if len(rec) < 3 || strings.EqualFold(rec[0], "Registry") {
			continue
		}
		prefix := strings.ToUpper(strings.TrimSpace(rec[1]))
		if len(prefix) >= 6 {
			ouiMap[prefix[:6]] = strings.TrimSpace(rec[2])
		}
	}
}

func vendor(mac string) string {
	key := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
	if len(key) < 6 {
		return ""
	}
	return ouiMap[key[:6]]
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func inferType(name, brand string) string {
	s := strings.ToLower(name + " " + brand)
	switch {
	case containsAny(s, "camera", "cam", "ipc", "摄像头"):
		return "camera"
	case containsAny(s, "washer", "washing", "dryer", "洗衣机", "冰箱", "fridge", "air conditioner", "空调"):
		return "appliance"
	case containsAny(s, "phone", "redmi", "iphone", "pixel", "手机", "galaxy"):
		return "phone"
	case containsAny(s, "macbook", "laptop", "notebook", "pc", "电脑", "thinkpad"):
		return "computer"
	case containsAny(s, "ipad", "tablet", "pad"):
		return "tablet"
	case containsAny(s, "tv", "电视", "display", "projector"):
		return "tv"
	case containsAny(s, "speaker", "音箱", "sound", "homepod", "echo"):
		return "speaker"
	case containsAny(s, "router", "istoreos", "openwrt", "路由"):
		return "router"
	case containsAny(s, "plug", "插座", "switch", "relay"):
		return "plug"
	case containsAny(s, "light", "bulb", "灯"):
		return "light"
	case containsAny(s, "watch", "手表", "band"):
		return "wearable"
	default:
		return "unknown"
	}
}

func (a *App) scanDevices() ([]Device, error) {
	if a.cfg.Mock {
		return a.withPolicy(a.mockDevices()), nil
	}
	devices, err := a.sshDevices()
	if err != nil {
		return nil, err
	}
	return a.withPolicy(devices), nil
}

func (a *App) withPolicy(devices []Device) []Device {
	for i := range devices {
		if p, ok := a.store.Get(devices[i].MAC); ok && p.Mode == "proxy" {
			devices[i].Mode = "proxy"
		} else {
			devices[i].Mode = "direct"
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Online != devices[j].Online {
			return devices[i].Online
		}
		return strings.ToLower(devices[i].Name) < strings.ToLower(devices[j].Name)
	})
	return devices
}

// sshDevices reads the DHCP lease table and the ARP/neighbour table over SSH.
// It is intentionally router-agnostic: any router that exposes dnsmasq leases
// and ip neigh / proc net arp works.
func (a *App) sshDevices() ([]Device, error) {
	leases, err := a.sshRun("cat " + shellQuote(a.cfg.LeaseFile) + " 2>/dev/null")
	if err != nil {
		return nil, err
	}
	neigh, _ := a.sshRun("ip neigh 2>/dev/null")
	arp, _ := a.sshRun("cat /proc/net/arp 2>/dev/null")

	online := map[string]bool{}
	neighIP := map[string]string{}
	for _, line := range strings.Split(neigh, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[3] != "lladdr" {
			continue
		}
		if ip := net.ParseIP(f[0]); ip == nil || ip.To4() == nil {
			continue
		}
		mac := strings.ToLower(f[4])
		state := strings.ToUpper(f[len(f)-1])
		neighIP[mac] = f[0]
		if state == "REACHABLE" || state == "STALE" || state == "DELAY" || state == "PROBE" {
			online[mac] = true
		}
	}
	for _, line := range strings.Split(arp, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[0] == "IP" {
			continue
		}
		if ip := net.ParseIP(f[0]); ip == nil || ip.To4() == nil {
			continue
		}
		mac := strings.ToLower(f[3])
		if !validMAC(mac) {
			continue
		}
		if _, ok := neighIP[mac]; !ok {
			neighIP[mac] = f[0]
		}
		if strings.EqualFold(f[2], "0x2") {
			online[mac] = true
		}
	}

	now := time.Now().Unix()
	seen := map[string]bool{}
	out := make([]Device, 0, 32)
	for _, line := range strings.Split(leases, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		exp, _ := strconv.ParseInt(f[0], 10, 64)
		mac := strings.ToLower(f[1])
		if !validMAC(mac) {
			continue
		}
		ip := f[2]
		name := f[3]
		if name == "*" || name == "" {
			name = mac
		}
		isOnline := exp == 0 || exp > now || online[mac]
		out = append(out, newDevice(mac, name, ip, isOnline, "lease"))
		seen[mac] = true
	}
	for mac, ip := range neighIP {
		if seen[mac] {
			continue
		}
		out = append(out, newDevice(mac, mac, ip, online[mac], "neigh"))
	}
	return out, nil
}

func newDevice(mac, name, ip string, online bool, source string) Device {
	brand := vendor(mac)
	if brand == "" {
		brand = "Unknown"
	}
	return Device{
		MAC:    mac,
		Name:   name,
		Brand:  brand,
		Type:   inferType(name, brand),
		IP:     ip,
		Online: online,
		Source: source,
	}
}

func (a *App) mockDevices() []Device {
	return []Device{
		{MAC: "02:00:00:00:00:01", Name: "Living Room TV", Brand: "Example Display Co.", Type: "tv", IP: "192.168.31.51", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:02", Name: "Kitchen Speaker", Brand: "Example Audio Labs", Type: "speaker", IP: "192.168.31.52", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:03", Name: "Office Laptop", Brand: "Acme Computers", Type: "computer", IP: "192.168.31.53", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:04", Name: "Front Door Camera", Brand: "Example Vision", Type: "camera", IP: "192.168.31.54", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:05", Name: "Bedroom Phone", Brand: "Example Mobile", Type: "phone", IP: "192.168.31.55", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:06", Name: "Washing Machine", Brand: "Example Appliances", Type: "appliance", IP: "192.168.31.56", Online: false, Source: "mock"},
	}
}
