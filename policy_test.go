package main

import (
	"strings"
	"testing"
)

func TestValidMAC(t *testing.T) {
	if !validMAC("aa:bb:cc:dd:ee:ff") {
		t.Fatal("valid MAC rejected")
	}
	if validMAC("aa:bb:cc:dd:ee") {
		t.Fatal("invalid MAC accepted")
	}
	if validMAC("aa-bb-cc-dd-ee-ff") {
		t.Fatal("dash MAC accepted")
	}
}

func TestRenderDnsmasq(t *testing.T) {
	a := &App{
		cfg:   Config{ProxyGateway: "192.168.31.122"},
		store: NewStore("/tmp/router-device-proxy-test.json"),
	}
	a.store.Devices["aa:bb:cc:dd:ee:ff"] = Policy{Mode: "proxy", IP: "192.168.31.50"}
	a.store.Devices["11:22:33:44:55:66"] = Policy{Mode: "direct"}

	out := a.renderDnsmasq()
	if !strings.Contains(out, "dhcp-host=aa:bb:cc:dd:ee:ff,set:proxy,192.168.31.50") {
		t.Fatalf("missing proxy host line:\n%s", out)
	}
	if strings.Contains(out, "11:22:33:44:55:66") {
		t.Fatalf("direct device leaked into config:\n%s", out)
	}
	if !strings.Contains(out, "dhcp-option=tag:proxy,3,192.168.31.122") {
		t.Fatalf("missing gateway option:\n%s", out)
	}
	if !strings.Contains(out, "dhcp-option=tag:proxy,6,192.168.31.122") {
		t.Fatalf("missing dns option:\n%s", out)
	}
}

func TestVendor(t *testing.T) {
	loadOUI()
	if v := vendor("00:00:0c:11:22:33"); v == "" {
		t.Fatal("expected OUI vendor for 00:00:0c")
	}
}

func TestInferType(t *testing.T) {
	cases := map[string]string{
		"chuangmi_camera": "camera",
		"mibx5-washer":    "appliance",
		"Redmi-K50":       "phone",
		"MacBook Pro":     "computer",
		"Living Room TV":  "tv",
	}
	for name, want := range cases {
		if got := inferType(name, ""); got != want {
			t.Errorf("inferType(%q) = %q, want %q", name, got, want)
		}
	}
}
