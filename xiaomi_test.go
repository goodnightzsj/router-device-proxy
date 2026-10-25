package main

import (
	"strings"
	"testing"
)

func TestXiaomiPwdHash(t *testing.T) {
	got := xiaomiPwdHash("test-password", "0__1234567890_1234")
	want := "9c58970a6b0ef630a1a9c6b216cf4ffbd8673503"
	if got != want {
		t.Fatalf("xiaomiPwdHash = %q, want %q", got, want)
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := map[string]string{
		"aa:bb:cc:dd:ee:ff": "AABBCCDDEEFF",
		"AA-BB-CC-DD-EE-FF": "AABBCCDDEEFF",
		"aabbccddeeff":      "AABBCCDDEEFF",
		"bad":               "",
		"":                  "",
	}
	for in, want := range cases {
		if got := normalizeMAC(in); got != want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsGenericName(t *testing.T) {
	for _, name := range []string{"", "Android", "unknown", "localhost", "AA:BB:CC:DD:EE:FF"} {
		if !isGenericName(name) {
			t.Errorf("isGenericName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Redmi-K50", "iStoreOS", "Living Room TV"} {
		if isGenericName(name) {
			t.Errorf("isGenericName(%q) = true, want false", name)
		}
	}
}

func TestParseXiaomiDevices(t *testing.T) {
	payload := `{"code":0,"list":[
		{"mac":"AA:BB:CC:DD:EE:FF","oname":"Redmi-K50","name":"Android"},
		{"mac":"11-22-33-44-55-66","oname":"","name":"Living Room TV"},
		{"mac":"bad","oname":"x","name":"y"},
		{"mac":"22:33:44:55:66:77","oname":"Android","name":"Pixel 9"}
	]}`
	names, err := parseXiaomiDevices(strings.NewReader(payload))
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

func TestParseXiaomiDevicesError(t *testing.T) {
	if _, err := parseXiaomiDevices(strings.NewReader(`{"code":401,"msg":"Invalid token"}`)); err == nil {
		t.Fatal("expected error for non-zero code")
	}
}
