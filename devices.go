package main

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/csv"
	"net"
	"os"
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
	case containsAny(s, "camera", "cam", "ipc", "doorbell", "nvr", "dvr", "webcam", "摄像头", "监控"):
		return "camera"
	case containsAny(s, "washer", "washing", "dryer", "dishwasher", "oven", "vacuum", "robot", "purifier", "humidifier", "heater", "fridge", "air conditioner", "洗衣机", "冰箱", "空调", "净化器", "扫地"):
		return "appliance"
	case containsAny(s, "printer", "打印"):
		return "printer"
	case containsAny(s, "phone", "iphone", "android", "pixel", "galaxy", "oneplus", "oppo", "vivo", "huawei", "honor", "realme", "redmi", "手机"):
		return "phone"
	case containsAny(s, "macbook", "laptop", "notebook", "desktop", "imac", "thinkpad", "电脑", "工作站"):
		return "computer"
	case containsAny(s, "ipad", "tablet", "pad"):
		return "tablet"
	case containsAny(s, "tv", "电视", "display", "projector", "stb", "appletv", "chromecast", "firetv", "roku"):
		return "tv"
	case containsAny(s, "speaker", "soundbar", "sonos", "homepod", "echo", "audio", "音箱"):
		return "speaker"
	case containsAny(s, "router", "istoreos", "openwrt", "unifi", "ubnt", "mesh", "gateway", "路由"):
		return "router"
	case containsAny(s, "server", "nas", "synology", "qnap", "proxmox", "esxi", "服务器"):
		return "server"
	case containsAny(s, "plug", "socket", "插座", "relay"):
		return "plug"
	case containsAny(s, "light", "bulb", "lamp", "灯"):
		return "light"
	case containsAny(s, "watch", "band", "手表", "手环"):
		return "wearable"
	default:
		return "unknown"
	}
}

// isLocallyAdministered reports whether the MAC uses the locally-administered
// bit, which is what phones and modern OSes set when randomizing their address.
func isLocallyAdministered(mac string) bool {
	key := strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac)
	if len(key) < 2 {
		return false
	}
	b, err := strconv.ParseUint(key[:2], 16, 8)
	if err != nil {
		return false
	}
	return b&0x02 != 0
}

func (a *App) scanDevices(ctx context.Context) ([]Device, error) {
	if a.cfg.Mock {
		return a.withPolicy(a.mockDevices()), nil
	}
	devices, err := a.sshDevices()
	if err != nil {
		return nil, err
	}
	devices = a.enrichDevices(ctx, devices)
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
		out = append(out, a.finishDevice(newDevice(mac, name, ip, isOnline, "lease")))
		seen[mac] = true
	}
	for mac, ip := range neighIP {
		if seen[mac] {
			continue
		}
		out = append(out, a.finishDevice(newDevice(mac, mac, ip, online[mac], "neigh")))
	}
	return out, nil
}

// finishDevice adds facts only the app knows, such as which IP is the proxy
// gateway. It never overwrites a name or type discovered from the router.
func (a *App) finishDevice(d Device) Device {
	if d.IP == a.cfg.ProxyGateway {
		if d.Type == "unknown" {
			d.Type = "router"
		}
		if (d.Name == "" || d.Name == d.MAC) && localIP(d.IP) {
			if h, err := os.Hostname(); err == nil && usableHostname(h) {
				d.Name = h
			}
		}
	}
	return d
}

func localIP(ip string) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.String() == ip {
			return true
		}
	}
	return false
}

// usableHostname rejects Docker's short container IDs, which are what
// os.Hostname returns when the container is not given --hostname.
func usableHostname(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || h == "localhost" {
		return false
	}
	if len(h) == 12 {
		if _, err := strconv.ParseUint(h, 16, 64); err == nil {
			return false
		}
	}
	return true
}

func newDevice(mac, name, ip string, online bool, source string) Device {
	brand := vendor(mac)
	if brand == "" && isLocallyAdministered(mac) {
		brand = "随机 MAC"
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
