package policy

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Policy struct {
	Mode string `json:"mode"`
	IP   string `json:"ip,omitempty"`
	Name string `json:"name,omitempty"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	Devices map[string]Policy `json:"devices"`
}

func NewStore(path string) *Store {
	return &Store{path: path, Devices: map[string]Policy{}}
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var raw struct {
		Devices map[string]Policy `json:"devices"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Devices != nil {
		s.Devices = raw.Devices
	}
	return nil
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(struct {
		Devices map[string]Policy `json:"devices"`
	}{Devices: s.Devices}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) SetMode(mac, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mac = strings.ToLower(mac)
	p := s.Devices[mac]
	p.Mode = mode
	s.Devices[mac] = p
	return s.saveLocked()
}

func (s *Store) SetNote(mac, note string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mac = strings.ToLower(mac)
	p := s.Devices[mac]
	p.Name = note
	s.Devices[mac] = p
	return s.saveLocked()
}

func (s *Store) Get(mac string) (Policy, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Devices[strings.ToLower(mac)]
	return p, ok
}

func (s *Store) All() map[string]Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Policy, len(s.Devices))
	for k, v := range s.Devices {
		out[k] = v
	}
	return out
}

func (s *Store) Snapshot() map[string]Policy {
	return s.All()
}

func ValidIP(s string) bool { return net.ParseIP(s) != nil }
