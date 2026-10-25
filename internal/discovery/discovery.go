package discovery

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	hdns "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/dns"
	hllmnr "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/llmnr"
	hmdns "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/mdns"
	hnetbios "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/netbios"
	hssdp "github.com/marcuoli/go-hostdiscovery/pkg/hostdiscovery/ssdp"

	"github.com/goodnightzsj/router-device-proxy/internal/device"
)

const nameCacheTTL = 10 * time.Minute

type Hit struct {
	Name     string
	Type     string
	Source   string
	Priority int
	At       time.Time
}

type Cache struct {
	mu      sync.Mutex
	entries map[string]Hit
}

func NewCache() *Cache { return &Cache{entries: make(map[string]Hit)} }

func (c *Cache) Get(ip string) (Hit, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.entries[ip]
	if !ok {
		return Hit{}, false
	}
	if time.Since(h.At) > nameCacheTTL {
		delete(c.entries, ip)
		return Hit{}, false
	}
	return h, true
}

func (c *Cache) Put(ip string, h Hit) {
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

type Options struct {
	SSDP    bool
	Timeout time.Duration
}

type Result struct {
	Names map[string]string
	Types map[string]string
	Hits  map[string]int
}

// Resolve probes only the supplied IPs with DNS, NetBIOS, mDNS, LLMNR and
// optionally SSDP. It never scans the whole subnet.
func Resolve(ctx context.Context, ips []string, opts Options) Result {
	out := Result{Names: map[string]string{}, Types: map[string]string{}, Hits: map[string]int{}}
	if len(ips) == 0 {
		return out
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, 3*timeout+2*time.Second)
	defer cancel()

	need := make(map[string]bool, len(ips))
	for _, ip := range ips {
		need[ip] = true
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	type best struct {
		name     string
		typ      string
		priority int
	}
	bestByIP := make(map[string]best, len(ips))
	record := func(source string, priority int, ip, name, typ string) {
		name = device.CleanHostname(name, ip)
		if name == "" && typ == "" {
			return
		}
		mu.Lock()
		out.Hits[source]++
		b := bestByIP[ip]
		if name != "" && (b.name == "" || priority > b.priority) {
			b.name = name
			b.priority = priority
		}
		if typ != "" && b.typ == "" {
			b.typ = typ
		}
		bestByIP[ip] = b
		mu.Unlock()
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
		m := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				m[r.IP] = r.Hostname
			}
		}
		return m
	})
	lookup("netbios", 3, func(ctx context.Context) map[string]string {
		d := hnetbios.NewDiscovery()
		d.Timeout = timeout
		m := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				m[r.IP] = r.Hostname
			}
		}
		return m
	})
	lookup("mdns", 2, func(ctx context.Context) map[string]string {
		d := hmdns.NewDiscovery()
		d.Timeout = timeout
		m := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				m[r.IP] = r.Hostname
			}
		}
		return m
	})
	lookup("llmnr", 1, func(ctx context.Context) map[string]string {
		d := hllmnr.NewDiscovery()
		d.Timeout = timeout
		m := make(map[string]string)
		for _, r := range d.LookupMultiple(ctx, ips) {
			if r != nil && r.Hostname != "" {
				m[r.IP] = r.Hostname
			}
		}
		return m
	})

	if opts.SSDP {
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
	for ip, b := range bestByIP {
		if b.name != "" {
			out.Names[ip] = b.name
		}
		if b.typ != "" {
			out.Types[ip] = b.typ
		}
	}
	log.Printf("discovery: candidates=%d dns=%d netbios=%d mdns=%d llmnr=%d ssdp=%d",
		len(ips), out.Hits["dns"], out.Hits["netbios"], out.Hits["mdns"], out.Hits["llmnr"], out.Hits["ssdp"])
	return out
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
