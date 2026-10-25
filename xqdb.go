package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

// xqdb is a read-only view of Xiaomi's /data/etc/xqDb SQLite database.
// It only reads DEVICE_INFO(MAC, ONAME, NICKNAME, ...) and never writes.
type xqdbCache struct {
	mu      sync.Mutex
	names   map[string]string
	at      time.Time
	failAt  time.Time
	failErr error
}

func newXqdbCache() *xqdbCache {
	return &xqdbCache{}
}

func (a *App) xqdbNames(ctx context.Context) (map[string]string, error) {
	if a.cfg.XqdbPath == "" || a.cfg.Mock || a.xqdb == nil {
		return nil, fmt.Errorf("xqdb disabled")
	}
	c := a.xqdb
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names != nil && time.Since(c.at) < 5*time.Minute {
		return c.names, nil
	}
	if c.failErr != nil && time.Since(c.failAt) < 5*time.Minute {
		return nil, c.failErr
	}
	out, err := a.sshRun("cat " + shellQuote(a.cfg.XqdbPath))
	if err == nil {
		var names map[string]string
		names, err = readXqdbNames([]byte(out))
		if err == nil {
			c.names = names
			c.at = time.Now()
			c.failErr = nil
			return names, nil
		}
	}
	c.failAt = time.Now()
	c.failErr = err
	return nil, err
}

func (a *App) applyXqdbNames(ctx context.Context, devices []Device) []Device {
	names, err := a.xqdbNames(ctx)
	if err != nil {
		return devices
	}
	applied := 0
	for i := range devices {
		name, ok := names[normalizeMAC(devices[i].MAC)]
		if !ok {
			continue
		}
		devices[i].Name = name
		if t := inferType(name, devices[i].Brand); t != "unknown" {
			devices[i].Type = t
		}
		applied++
	}
	if applied > 0 {
		log.Printf("xqdb names: %d/%d devices", applied, len(devices))
	}
	return devices
}

// readXqdbNames parses the minimal SQLite subset used by xqDb. It is
// deliberately read-only and only supports table B-trees with local payloads,
// which is sufficient for this small database.
func readXqdbNames(data []byte) (map[string]string, error) {
	f, err := newSQLiteFile(data)
	if err != nil {
		return nil, err
	}
	root, err := f.findTableRoot("DEVICE_INFO")
	if err != nil {
		return nil, err
	}
	rows, err := f.tableRows(root)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			continue
		}
		mac := normalizeMAC(row[0])
		if mac == "" {
			continue
		}
		name := strings.TrimSpace(row[1]) // ONAME
		if isGenericName(name) && len(row) > 2 {
			if n := strings.TrimSpace(row[2]); !isGenericName(n) {
				name = n
			}
		}
		if name == "" || isGenericName(name) {
			continue
		}
		names[mac] = name
	}
	return names, nil
}

type sqliteFile struct {
	data     []byte
	pageSize int
	pages    int
}

func newSQLiteFile(data []byte) (*sqliteFile, error) {
	if len(data) < 100 || string(data[:16]) != "SQLite format 3\x00" {
		return nil, fmt.Errorf("not a sqlite database")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps > 65536 || ps&(ps-1) != 0 {
		return nil, fmt.Errorf("invalid sqlite page size %d", ps)
	}
	if data[56] != 0 && data[56] != 1 {
		return nil, fmt.Errorf("unsupported sqlite text encoding %d", data[56])
	}
	return &sqliteFile{data: data, pageSize: ps, pages: len(data) / ps}, nil
}

func (f *sqliteFile) page(n int) ([]byte, error) {
	if n < 1 || n > f.pages {
		return nil, fmt.Errorf("sqlite page %d out of range", n)
	}
	start := (n - 1) * f.pageSize
	end := start + f.pageSize
	if end > len(f.data) {
		end = len(f.data)
	}
	return f.data[start:end], nil
}

func (f *sqliteFile) findTableRoot(name string) (int, error) {
	rows, err := f.tableRows(1)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if len(row) >= 4 && row[0] == "table" && row[1] == name {
			root, err := strconv.Atoi(row[3])
			if err != nil {
				return 0, fmt.Errorf("sqlite rootpage for %s: %w", name, err)
			}
			return root, nil
		}
	}
	return 0, fmt.Errorf("sqlite table %s not found", name)
}

func (f *sqliteFile) tableRows(root int) ([][]string, error) {
	var rows [][]string
	var walk func(page, depth int) error
	walk = func(page, depth int) error {
		if depth > 20 {
			return fmt.Errorf("sqlite btree too deep")
		}
		p, err := f.page(page)
		if err != nil {
			return err
		}
		hdr := 0
		if page == 1 {
			hdr = 100
		}
		if hdr+12 > len(p) {
			return fmt.Errorf("short sqlite page %d", page)
		}
		typ := p[hdr]
		count := int(binary.BigEndian.Uint16(p[hdr+3 : hdr+5]))
		switch typ {
		case 0x0d: // leaf table
			base := hdr + 8
			for i := 0; i < count; i++ {
				if base+2*i+2 > len(p) {
					return fmt.Errorf("bad leaf cell pointer")
				}
				off := int(binary.BigEndian.Uint16(p[base+2*i : base+2*i+2]))
				payload, err := f.leafPayload(p, off)
				if err != nil {
					return err
				}
				row, err := parseRecord(payload)
				if err != nil {
					return err
				}
				rows = append(rows, row)
			}
		case 0x05: // interior table
			base := hdr + 12
			for i := 0; i < count; i++ {
				if base+2*i+2 > len(p) {
					return fmt.Errorf("bad interior cell pointer")
				}
				off := int(binary.BigEndian.Uint16(p[base+2*i : base+2*i+2]))
				if off+4 > len(p) {
					return fmt.Errorf("bad interior cell")
				}
				child := int(binary.BigEndian.Uint32(p[off : off+4]))
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
			right := int(binary.BigEndian.Uint32(p[hdr+8 : hdr+12]))
			if err := walk(right, depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported sqlite page type 0x%x", typ)
		}
		return nil
	}
	if err := walk(root, 0); err != nil {
		return nil, err
	}
	return rows, nil
}

func (f *sqliteFile) leafPayload(p []byte, off int) ([]byte, error) {
	if off >= len(p) {
		return nil, fmt.Errorf("sqlite cell offset out of range")
	}
	plen, n1 := readVarint(p[off:])
	if n1 == 0 {
		return nil, fmt.Errorf("bad sqlite payload length")
	}
	_, n2 := readVarint(p[off+n1:])
	if n2 == 0 {
		return nil, fmt.Errorf("bad sqlite rowid")
	}
	start := off + n1 + n2
	if start < 0 || start > len(p) || plen > uint64(len(p)-start) {
		return nil, fmt.Errorf("sqlite payload overflow")
	}
	return p[start : start+int(plen)], nil
}

func readVarint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 9; i++ {
		if i >= len(b) {
			return 0, 0
		}
		if i == 8 {
			return v<<8 | uint64(b[i]), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return v, 9
}

func parseRecord(p []byte) ([]string, error) {
	hlen, n := readVarint(p)
	if n == 0 || hlen > uint64(len(p)) {
		return nil, fmt.Errorf("bad sqlite record header")
	}
	var types []uint64
	pos := n
	for pos < int(hlen) {
		st, m := readVarint(p[pos:])
		if m == 0 {
			return nil, fmt.Errorf("bad sqlite serial type")
		}
		types = append(types, st)
		pos += m
	}
	vals := make([]string, 0, len(types))
	vpos := int(hlen)
	for _, st := range types {
		switch {
		case st == 0:
			vals = append(vals, "")
		case st >= 1 && st <= 6:
			size := [...]int{0, 1, 2, 3, 4, 6, 8}[st]
			if vpos+size > len(p) {
				return nil, fmt.Errorf("short sqlite integer")
			}
			var v uint64
			for i := 0; i < size; i++ {
				v = v<<8 | uint64(p[vpos+i])
			}
			vals = append(vals, strconv.FormatUint(v, 10))
			vpos += size
		case st == 7:
			if vpos+8 > len(p) {
				return nil, fmt.Errorf("short sqlite float")
			}
			vpos += 8
			vals = append(vals, "")
		case st == 8:
			vals = append(vals, "0")
		case st == 9:
			vals = append(vals, "1")
		case st >= 12 && st%2 == 0:
			size := int((st - 12) / 2)
			if vpos+size > len(p) {
				return nil, fmt.Errorf("short sqlite blob")
			}
			vpos += size
			vals = append(vals, "")
		case st >= 13:
			size := int((st - 13) / 2)
			if vpos+size > len(p) {
				return nil, fmt.Errorf("short sqlite text")
			}
			vals = append(vals, string(p[vpos:vpos+size]))
			vpos += size
		default:
			vals = append(vals, "")
		}
	}
	return vals, nil
}
