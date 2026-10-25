package main

import (
	"strings"
	"testing"
	"time"
)

func TestCleanHostname(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want string
	}{
		{"iStoreOS.local.", "192.168.31.122", "iStoreOS"},
		{"  printer.local ", "192.168.31.50", "printer"},
		{"192.168.31.56", "192.168.31.56", ""},
		{"localhost", "192.168.31.1", ""},
		{"bad\x00name", "192.168.31.2", "badname"},
		{"", "192.168.31.3", ""},
		{strings.Repeat("a", 80) + ".local", "192.168.31.4", strings.Repeat("a", 64)},
	}
	for _, c := range cases {
		if got := cleanHostname(c.name, c.ip); got != c.want {
			t.Errorf("cleanHostname(%q, %q) = %q, want %q", c.name, c.ip, got, c.want)
		}
	}
}

func TestSSDPType(t *testing.T) {
	cases := map[string]string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1": "router",
		"urn:schemas-upnp-org:device:MediaRenderer:1":         "tv",
		"urn:schemas-upnp-org:device:MediaServer:1":           "server",
		"urn:schemas-upnp-org:service:PrintBasic:1":           "printer",
		"upnp:rootdevice": "",
	}
	for st, want := range cases {
		if got := ssdpType(st); got != want {
			t.Errorf("ssdpType(%q) = %q, want %q", st, got, want)
		}
	}
}

func TestNameCachePriority(t *testing.T) {
	c := newNameCache()
	c.put("192.168.31.50", nameHit{Name: "ssdp-name", Priority: 1, At: time.Now()})
	c.put("192.168.31.50", nameHit{Name: "dns-name", Priority: 4, At: time.Now()})
	if h, _ := c.get("192.168.31.50"); h.Name != "dns-name" {
		t.Fatalf("higher priority name lost: %q", h.Name)
	}
	c.put("192.168.31.50", nameHit{Name: "ssdp-again", Priority: 1, At: time.Now()})
	if h, _ := c.get("192.168.31.50"); h.Name != "dns-name" {
		t.Fatalf("lower priority overwrote name: %q", h.Name)
	}
	c.put("192.168.31.50", nameHit{At: time.Now()})
	if h, _ := c.get("192.168.31.50"); h.Name != "dns-name" {
		t.Fatalf("empty result erased name: %q", h.Name)
	}
}

func TestNameCacheNegativeAndTypeMerge(t *testing.T) {
	c := newNameCache()
	c.put("192.168.31.60", nameHit{At: time.Now()})
	if _, ok := c.get("192.168.31.60"); !ok {
		t.Fatal("negative cache entry not stored")
	}
	c.put("192.168.31.61", nameHit{Name: "host", Priority: 2, At: time.Now()})
	c.put("192.168.31.61", nameHit{Type: "router", Priority: 1, At: time.Now()})
	h, _ := c.get("192.168.31.61")
	if h.Name != "host" || h.Type != "router" {
		t.Fatalf("type merge failed: name=%q type=%q", h.Name, h.Type)
	}
}
