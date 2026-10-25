package xiaomi

import (
	"testing"

	"github.com/goodnightzsj/router-device-proxy/internal/device"
)

func TestDeviceNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"aa:bb:cc:dd:ee:ff": "AABBCCDDEEFF",
		"AA-BB-CC-DD-EE-FF": "AABBCCDDEEFF",
		"aabbccddeeff":      "AABBCCDDEEFF",
		"bad":               "",
		"":                  "",
	}
	for in, want := range cases {
		if got := device.NormalizeMAC(in); got != want {
			t.Errorf("NormalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeviceIsGenericName(t *testing.T) {
	for _, name := range []string{"", "Android", "unknown", "localhost", "AA:BB:CC:DD:EE:FF"} {
		if !device.IsGenericName(name) {
			t.Errorf("IsGenericName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Redmi-K50", "iStoreOS", "Living Room TV"} {
		if device.IsGenericName(name) {
			t.Errorf("IsGenericName(%q) = true, want false", name)
		}
	}
}
