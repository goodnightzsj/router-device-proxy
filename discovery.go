package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	hdns "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/dns"
	hllmnr "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/llmnr"
	hmdns "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/mdns"
	hnetbios "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/netbios"
	hssdp "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/ssdp"
)

const nameCacheTTL = 10 * time.Minute

type nameHit struct {
	Name     string
	Type     string
	Source   string
	Priority int
	At       time.Time
}

type nameCache struct {
	mu      sync.Mutex
	entries map[string]nameHit
}

func newNameCache() *nameCache {
	return &nameCache{entries: make(map[string]nameHit)}
}

func (c *nameCache) get(ip string) (nameHit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.entries[ip]
	if !ok {
		return nameHit{}, false
	}
	if time.Since(h.At) > nameCacheTTL {
		delete(c.entries, ip)
		return nameHit{}, false
	}
	return h, true
}

func (c *nameCache) put(ip string, h nameHit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[ip]; ok && time.Since(old.At) <= nameCacheTTL {
		if old.Name != "" && h.Name == "" {
			if old.Type == "" && h.Type != "" {
				old.Type = h.Type
				c.entries[ip] = old
			}
			return
		}
		if old.Name != "" && old.Priority >= h.Priority {
			return
		}
		if old.Name == "" && h.Name == "" {
			return
		}
	}
	c.entries[ip] = h
}

// enrichDevices adds names found by DNS/NetBIOS/mDNS/LLMNR/SSDP for devices
// whose DHCP lease or ARP entry has no hostname. A DHCP name always wins.
func (a *App) enrichDevices(ctx context.Context, devices []Device) []Device {
	if a.cfg.Mock {
		return devices
	}
	devices = a.applyXqdbNames(ctx, devices)
	if xc := a.xiaomiClient(); xc != nil && xc.configured() {
		xctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		devices = a.applyXiaomiNames(xctx, devices, xc)
		cancel()
	}
	if !a.cfg.Discovery || a.names == nil {
		return devices
	}

	need := make(map[string]bool)
	ips := make([]string, 0, len(devices))
	for _, d := range devices {
		if d.IP == "" {
			continue
		}
		if d.Name != "" && d.Name != d.MAC && d.Type != "unknown" {
			continue
		}
		if _, ok := a.names.get(d.IP); ok {
			continue
		}
		if !need[d.IP] {
			need[d.IP] = true
			ips = append(ips, d.IP)
		}
	}
	if len(ips) == 0 {
		return a.applyNames(devices)
	}

	timeout := a.cfg.DiscoveryTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, 3*timeout+2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var mu sync.Mutex
	hits := map[string]int{}

	record := func(source string, priority int, ip, name, typ string) {
		name = cleanHostname(name, ip)
		if name == "" && typ == "" {
			return
		}
		mu.Lock()
		hits[source]++
		mu.Unlock()
		a.names.put(ip, nameHit{Name: name, Type: typ, Source: source, Priority: priority, At: time.Now()})
	}

	lookup := func(source string, priority int, fn func(context.Context) map[string]string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip, name := range fn(ctx) {
				record(source, priority, ip, name, "")
			}
		}()
	}

	lookup("dns", 4, func(ctx context.Context) map[string]string {
		d := hdns.NewDiscovery()
		d.Timeout = timeout
		out := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				out[r.IP] = r.Hostname
			}
		}
		return out
	})

	lookup("netbios", 3, func(ctx context.Context) map[string]string {
		d := hnetbios.NewDiscovery()
		d.Timeout = timeout
		out := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				out[r.IP] = r.Hostname
			}
		}
		return out
	})

	lookup("mdns", 2, func(ctx context.Context) map[string]string {
		d := hmdns.NewDiscovery()
		d.Timeout = timeout
		out := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				out[r.IP] = r.Hostname
			}
		}
		return out
	})

	lookup("llmnr", 1, func(ctx context.Context) map[string]string {
		d := hllmnr.NewDiscovery()
		d.Timeout = timeout
		out := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				out[r.IP] = r.Hostname
			}
		}
		return out
	})

	if a.cfg.DiscoverySSDP {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := hssdp.NewDiscovery()
			d.Timeout = timeout
			results, err := d.Discover(ctx, "ssdp:all")
			if err != nil {
				return
			}
			relevant := make([]*hssdp.Result, 0, len(results))
			for _, r := range results {
				if r != nil && need[r.IP] {
					relevant = append(relevant, r)
				}
			}
			if len(relevant) == 0 {
				return
			}
			d.EnrichResults(ctx, relevant)
			for _, r := range relevant {
				record("ssdp", 1, r.IP, r.FriendlyName, ssdpType(r.ST))
			}
		}()
	}

	wg.Wait()

	// Negative-cache devices that no protocol could name, so a page refresh
	// does not probe the same silent devices again.
	for _, ip := range ips {
		if _, ok := a.names.get(ip); !ok {
			a.names.put(ip, nameHit{At: time.Now()})
		}
	}

	log.Printf("discovery: candidates=%d dns=%d netbios=%d mdns=%d llmnr=%d ssdp=%d",
		len(ips), hits["dns"], hits["netbios"], hits["mdns"], hits["llmnr"], hits["ssdp"])

	return a.applyNames(devices)
}

func (a *App) applyNames(devices []Device) []Device {
	for i := range devices {
		if devices[i].IP == "" {
			continue
		}
		hit, ok := a.names.get(devices[i].IP)
		if !ok {
			continue
		}
		if devices[i].Name == "" || devices[i].Name == devices[i].MAC {
			if hit.Name != "" {
				devices[i].Name = hit.Name
			}
		}
		if devices[i].Type == "unknown" {
			if t := inferType(devices[i].Name, devices[i].Brand); t != "unknown" {
				devices[i].Type = t
			} else if hit.Type != "" {
				devices[i].Type = hit.Type
			}
		}
	}
	return devices
}

// cleanHostname normalises network-supplied names and rejects values that
// are not useful to display.
func cleanHostname(name, ip string) string {
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

func ssdpType(st string) string {
	s := strings.ToLower(st)
	switch {
	case strings.Contains(s, "mediarenderer"), strings.Contains(s, "dial-multiscreen"):
		return "tv"
	case strings.Contains(s, "mediaserver"):
		return "server"
	case strings.Contains(s, "internetgatewaydevice"), strings.Contains(s, "wandevice"), strings.Contains(s, "wanconnectiondevice"):
		return "router"
	case strings.Contains(s, "printer"), strings.Contains(s, "printbasic"):
		return "printer"
	default:
		return ""
	}
}
