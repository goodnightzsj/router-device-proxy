package xiaomi

import (
	"strings"
	"testing"
)

func TestPwdHash(t *testing.T) {
	got := pwdHash("test-password", "0__1234567890_1234")
	want := "9c58970a6b0ef630a1a9c6b216cf4ffbd8673503"
	if got != want {
		t.Fatalf("pwdHash = %q, want %q", got, want)
	}
}

func TestParseDevices(t *testing.T) {
	payload := `{"code":0,"list":[
		{"mac":"AA:BB:CC:DD:EE:FF","oname":"Redmi-K50","name":"Android"},
		{"mac":"11-22-33-44-55-66","oname":"","name":"Living Room TV"},
		{"mac":"bad","oname":"x","name":"y"},
		{"mac":"22:33:44:55:66:77","oname":"Android","name":"Pixel 9"}
	]}`
	names, err := parseDevices(strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"AABBCCDDEEFF": "Redmi-K50",
		"112233445566": "Living Room TV",
		"223344556677": "Pixel 9",
	}
	if len(names) != len(want) {
		t.Fatalf("got %d names, want %d: %v", len(names), len(want), names)
	}
	for mac, name := range want {
		if names[mac] != name {
			t.Errorf("names[%s] = %q, want %q", mac, names[mac], name)
		}
	}
}

func TestParseDevicesError(t *testing.T) {
	if _, err := parseDevices(strings.NewReader(`{"code":401,"msg":"Invalid token"}`)); err == nil {
		t.Fatal("expected error for non-zero code")
	}
}
