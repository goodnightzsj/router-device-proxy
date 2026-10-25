package device

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/csv"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

//go:embed oui.csv.gz
var ouiGz []byte

var (
	ouiMap = map[string]string{}
	macRe  = regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`)
)

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

func ValidMAC(s string) bool { return macRe.MatchString(s) }

func LoadOUI() {
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

func Vendor(mac string) string {
	key := NormalizeMAC(mac)
	if len(key) < 6 {
		return ""
	}
	return ouiMap[key[:6]]
}

func NormalizeMAC(mac string) string {
	mac = strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(mac))
	if len(mac) != 12 {
		return ""
	}
	for _, r := range mac {
		if (r < '0' || r > '9') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return mac
}

func IsLocallyAdministered(mac string) bool {
	key := NormalizeMAC(mac)
	if len(key) < 2 {
		return false
	}
	b, err := strconv.ParseUint(key[:2], 16, 8)
	if err != nil {
		return false
	}
	return b&0x02 != 0
}

func containsAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func InferType(name, brand string) string {
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

func New(mac, name, ip string, online bool, source string) Device {
	brand := Vendor(mac)
	if brand == "" && IsLocallyAdministered(mac) {
		brand = "随机 MAC"
	}
	return Device{
		MAC:    mac,
		Name:   name,
		Brand:  brand,
		Type:   InferType(name, brand),
		IP:     ip,
		Online: online,
		Source: source,
	}
}

func CleanHostname(name, ip string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".")
	if len(name) >= 6 && strings.EqualFold(name[len(name)-6:], ".local") {
		name = name[:len(name)-6]
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == ip {
		return ""
	}
	low := strings.ToLower(name)
	if low == "localhost" || strings.HasSuffix(low, ".in-addr.arpa") || strings.HasPrefix(low, "0.0.0.0") {
		return ""
	}
	if utf8.RuneCountInString(name) > 64 {
		name = string([]rune(name)[:64])
	}
	return name
}

func IsGenericName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "android", "unknown", "localhost", "device":
		return true
	}
	return NormalizeMAC(name) != ""
}

func MockDevices() []Device {
	return []Device{
		{MAC: "02:00:00:00:00:01", Name: "Living Room TV", Brand: "Example Display Co.", Type: "tv", IP: "192.168.31.51", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:02", Name: "Kitchen Speaker", Brand: "Example Audio Labs.", Type: "speaker", IP: "192.168.31.52", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:03", Name: "Office Laptop", Brand: "Acme Computers", Type: "computer", IP: "192.168.31.53", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:04", Name: "Front Door Camera", Brand: "Example Vision", Type: "camera", IP: "192.168.31.54", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:05", Name: "Bedroom Phone", Brand: "Example Mobile", Type: "phone", IP: "192.168.31.55", Online: true, Source: "mock"},
		{MAC: "02:00:00:00:00:06", Name: "Washing Machine", Brand: "Example Appliances", Type: "appliance", IP: "192.168.31.56", Online: false, Source: "mock"},
	}
}
