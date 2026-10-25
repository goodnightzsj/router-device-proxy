package main

import "testing"

func TestReadVarint(t *testing.T) {
	cases := []struct {
		in   []byte
		want uint64
		n    int
	}{
		{[]byte{0x00}, 0, 1},
		{[]byte{0x7f}, 127, 1},
		{[]byte{0x81, 0x00}, 128, 2},
		{[]byte{0x82, 0x01}, 257, 2},
	}
	for _, c := range cases {
		got, n := readVarint(c.in)
		if got != c.want || n != c.n {
			t.Errorf("readVarint(%x) = %d,%d, want %d,%d", c.in, got, n, c.want, c.n)
		}
	}
}

func TestParseRecord(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []string
	}{
		{"two texts", []byte{0x03, 0x11, 0x11, 'A', 'B', 'C', 'D'}, []string{"AB", "CD"}},
		{"integer", []byte{0x02, 0x01, 0x2a}, []string{"42"}},
		{"null", []byte{0x02, 0x00}, []string{""}},
	}
	for _, c := range cases {
		got, err := parseRecord(c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
				break
			}
		}
	}
}
