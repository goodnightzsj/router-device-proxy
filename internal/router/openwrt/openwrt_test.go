package openwrt

import (
	"context"
	"strings"
	"testing"

	"github.com/goodnightzsj/router-device-proxy/internal/policy"
)

func TestRenderDnsmasq(t *testing.T) {
	out := renderDnsmasq(map[string]policy.Policy{
		"aa:bb:cc:dd:ee:01": {Mode: "proxy", IP: "192.168.31.50"},
		"aa:bb:cc:dd:ee:02": {Mode: "direct"},
	}, "192.168.31.10")

	want := []string{
		"dhcp-host=aa:bb:cc:dd:ee:01,set:proxy,192.168.31.50",
		"dhcp-option=tag:proxy,3,192.168.31.10",
		"dhcp-option=tag:proxy,6,192.168.31.10",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("missing %q in:\n%s", w, out)
		}
	}
	if strings.Contains(out, "ee:02") {
		t.Fatalf("direct device leaked into proxy config:\n%s", out)
	}
}

// An empty gateway must fail loudly; otherwise dhcp-option lines end with a
// bare comma and the router silently keeps sending clients to itself.
func TestApplyRejectsEmptyGateway(t *testing.T) {
	a := &Adapter{}
	if _, err := a.Apply(context.Background(), map[string]policy.Policy{}); err == nil {
		t.Fatal("expected error when proxy gateway is unset")
	}
}
