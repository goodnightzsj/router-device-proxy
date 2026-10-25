package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	var devices []Device
	var err error
	if a.cfg.WebPassword != "" {
		devices, err = a.xiaomiDevices()
	}
	if err != nil || len(devices) == 0 {
		leaseDevices, lerr := a.leaseDevices()
		if lerr != nil {
			if err != nil {
				return nil, fmt.Errorf("xiaomi: %v; lease: %v", err, lerr)
			}
			return nil, lerr
		}
		devices = leaseDevices
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

type xiaomiDevice struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Oname  string `json:"oname"`
	Online int    `json:"online"`
	Type   int    `json:"type"`
	IP     []struct {
		IP string `json:"ip"`
	} `json:"ip"`
	Company *struct {
		Name string `json:"name"`
	} `json:"company"`
}

func (a *App) xiaomiDevices() ([]Device, error) {
	client := &http.Client{Timeout: 8 * time.Second}
	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", a.cfg.WebPassword)
	form.Set("logtype", "2")
	resp, err := client.PostForm("http://"+a.cfg.RouterHost+"/cgi-bin/luci/api/xqsystem/login", form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var login struct {
		Token string `json:"token"`
		Code  int    `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		return nil, err
	}
	if login.Token == "" {
		return nil, fmt.Errorf("login returned no token")
	}

	apiURL := "http://" + a.cfg.RouterHost + "/cgi-bin/luci/;stok=" + login.Token + "/api/misystem/devicelist"
	resp2, err := client.Get(apiURL)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	var list struct {
		List []xiaomiDevice `json:"list"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&list); err != nil {
		return nil, err
	}

	out := make([]Device, 0, len(list.List))
	for _, d := range list.List {
		if d.MAC == "" {
			continue
		}
		ip := ""
		if len(d.IP) > 0 {
			ip = d.IP[0].IP
		}
		name := d.Name
		if name == "" || name == "*" {
			name = d.Oname
		}
		if name == "" {
			name = d.MAC
		}
		brand := ""
		if d.Company != nil {
			brand = d.Company.Name
		}
		if brand == "" {
			brand = vendor(d.MAC)
		}
		if brand == "" {
			brand = "Unknown"
		}
		out = append(out, Device{
			MAC:    strings.ToLower(d.MAC),
			Name:   name,
			Brand:  brand,
			Type:   inferType(name, brand),
			IP:     ip,
			Online: d.Online == 1,
			Source: "xiaomi",
		})
	}
	return out, nil
}

func (a *App) leaseDevices() ([]Device, error) {
	leases, err := a.sshRun("cat /tmp/dhcp.leases 2>/dev/null")
	if err != nil {
		return nil, err
	}
	neigh, _ := a.sshRun("ip neigh 2>/dev/null")

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
		ip := f[2]
		name := f[3]
		if name == "*" || name == "" {
			name = mac
		}
		isOnline := exp == 0 || exp > now || online[mac]
		brand := vendor(mac)
		if brand == "" {
			brand = "Unknown"
		}
		out = append(out, Device{
			MAC:    mac,
			Name:   name,
			Brand:  brand,
			Type:   inferType(name, brand),
			IP:     ip,
			Online: isOnline,
			Source: "lease",
		})
		seen[mac] = true
	}
	for mac, ip := range neighIP {
		if seen[mac] || !online[mac] {
			continue
		}
		brand := vendor(mac)
		if brand == "" {
			brand = "Unknown"
		}
		out = append(out, Device{
			MAC:    mac,
			Name:   mac,
			Brand:  brand,
			Type:   inferType("", brand),
			IP:     ip,
			Online: true,
			Source: "neigh",
		})
	}
	return out, nil
}

func (a *App) mockDevices() []Device {
	return []Device{
		{MAC: "0e:f4:ec:d9:83:b7", Name: "Redmi-K50", Brand: "Xiaomi Communications", Type: "phone", IP: "192.168.31.192", Online: true, Source: "mock"},
		{MAC: "1e:a1:55:77:56:2d", Name: "MacBook Pro", Brand: "Apple, Inc.", Type: "computer", IP: "192.168.31.23", Online: true, Source: "mock"},
		{MAC: "94:8c:d7:13:73:e8", Name: "mibx5-washer", Brand: "Beijing Xiaomi Mobile Software", Type: "appliance", IP: "192.168.31.56", Online: true, Source: "mock"},
		{MAC: "78:df:72:53:a2:3a", Name: "chuangmi_camera", Brand: "Shanghai Imilab Technology", Type: "camera", IP: "192.168.31.229", Online: true, Source: "mock"},
		{MAC: "68:1d:ef:3b:67:31", Name: "iStoreOS", Brand: "Intel Corporate", Type: "router", IP: "192.168.31.122", Online: true, Source: "mock"},
		{MAC: "aa:bb:cc:dd:ee:01", Name: "Living Room TV", Brand: "Samsung Electronics", Type: "tv", IP: "192.168.31.77", Online: false, Source: "mock"},
	}
}
