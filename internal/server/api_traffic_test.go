package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/auth"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/network"
	"ostiole/internal/traffic"
)

// countingRouter is a counter over a router with one LAN device busy on
// eth1 and eth0 on the internet, stepped by hand.
func countingRouter(t *testing.T, adjust ...func(*model.Config)) (*traffic.Counter, func(time.Duration, uint64)) {
	t.Helper()
	var mu sync.Mutex
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var bytes uint64
	cfg := &model.Config{
		Zones:      []model.Zone{{Name: "wan", External: true}, {Name: "lan"}},
		Interfaces: []model.Interface{{Name: "eth1", Zone: "lan"}, {Name: "eth0", Zone: "wan", Description: "Fibre"}},
		Traffic:    model.Traffic{Devices: true},
	}
	for _, f := range adjust {
		f(cfg)
	}
	c := &traffic.Counter{
		Source: func() *model.Config { return cfg },
		Log:    slog.New(slog.DiscardHandler),
		Links: func() ([]network.Link, error) {
			mu.Lock()
			defer mu.Unlock()
			return []network.Link{
				{Name: "eth0", Index: 2, Addresses: []string{"203.0.113.2/24"}, RXBytes: bytes, TXBytes: bytes / 10},
				{Name: "eth1", Index: 3, Addresses: []string{"10.0.0.1/24"}},
				{Name: "wlan9", Index: 4},
			}, nil
		},
		Flows: func(fam int, fn func(netlink.Flow)) error {
			mu.Lock()
			bytes := bytes
			mu.Unlock()
			if fam == unix.AF_INET {
				fn(netlink.Flow{ID: 1,
					Forward: netlink.Tuple{Protocol: 6, SrcIP: net.ParseIP("10.0.0.5"), DstIP: net.ParseIP("198.51.100.7"), DstPort: 443, Bytes: bytes / 10},
					Reverse: netlink.Tuple{Protocol: 6, SrcIP: net.ParseIP("198.51.100.7"), DstIP: net.ParseIP("10.0.0.5"), SrcPort: 443, Bytes: bytes}})
			}
			return nil
		},
		Neighbours: func() ([]netlink.Neighbour, error) {
			hw, _ := net.ParseMAC("aa:bb:cc:00:00:05")
			return []netlink.Neighbour{{LinkIndex: 3, IP: net.ParseIP("10.0.0.5"), HardwareAddr: hw, State: unix.NUD_REACHABLE}}, nil
		},
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
	}
	step := func(d time.Duration, total uint64) {
		mu.Lock()
		now = now.Add(d)
		bytes = total
		at := now
		mu.Unlock()
		c.Tick(at)
	}
	return c, step
}

// The Traffic page's reads: links in the configuration's order with what
// it says of them, devices named as the leases page names them, one
// device's points, and the windows it knows.
func TestTrafficReads(t *testing.T) {
	t.Parallel()
	c, step := countingRouter(t)
	step(0, 1000)
	step(5*time.Second, 51000)
	srv := newTestServerWith(t, func(d *Deps) {
		d.Traffic = c
		d.Engine.Store()
	})

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/traffic/interfaces", nil)
	var links trafficLinks
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &links) != nil {
		t.Fatalf("interfaces: %d %s", resp.StatusCode, raw)
	}
	if links.Window != "5m" || len(links.Links) != 3 || links.Links[2].Name != "wlan9" || links.Links[2].Configured {
		t.Errorf("links = %s", raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/traffic/devices?window=24h", nil)
	var devices trafficDevices
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &devices) != nil {
		t.Fatalf("devices: %d %s", resp.StatusCode, raw)
	}
	if !devices.Counting || devices.Interval != 5 || len(devices.Devices) != 1 ||
		devices.Devices[0].ID != "aa:bb:cc:00:00:05" || devices.Devices[0].Totals.Down != 50000 {
		t.Errorf("devices = %s", raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/traffic/devices/aa:bb:cc:00:00:05?window=30d", nil)
	var one trafficDevice
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &one) != nil || len(one.Points) == 0 {
		t.Errorf("one device: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/traffic/devices/nobody", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("nobody: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/traffic/interfaces?window=1y", nil); resp.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(raw), "5m, 24h, 30d") {
		t.Errorf("a year: %d %s", resp.StatusCode, raw)
	}
}

// A viewer reads traffic, and only an admin may clear it.
func TestClearingTrafficTakesAnAdmin(t *testing.T) {
	t.Parallel()
	c, step := countingRouter(t)
	step(0, 1000)
	step(5*time.Second, 51000)
	srv := newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens, d.Traffic = tokens, c
	})
	for _, role := range []auth.Role{auth.RoleViewer, auth.RoleOperator} {
		token := mintToken(t, srv, string(role), string(role))
		for _, path := range []string{"/api/v1/traffic", "/api/v1/traffic/destinations"} {
			if resp, raw := withToken(t, srv, http.MethodDelete, path, token); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s cleared %s: %d %s", role, path, resp.StatusCode, raw)
			}
		}
	}
	if got := c.DeviceReports(traffic.Window24h); len(got) != 1 {
		t.Fatalf("devices before clear = %+v", got)
	}
	admin := mintToken(t, srv, "own", string(auth.RoleAdmin))
	if resp, raw := withToken(t, srv, http.MethodDelete, "/api/v1/traffic", admin); resp.StatusCode != http.StatusOK {
		t.Errorf("admin: %d %s", resp.StatusCode, raw)
	}
	if got := c.DeviceReports(traffic.Window24h); len(got) != 0 {
		t.Errorf("devices after clear = %+v", got)
	}
}

// Clear takes the devices' and the destinations' files with them; the
// links' stay, as they are always counted. Clearing the destinations
// leaves the devices and their files.
func TestClearingTrafficDeletesItsFiles(t *testing.T) {
	t.Parallel()
	c, step := countingRouter(t, func(cfg *model.Config) {
		cfg.Traffic.Destinations = model.TrafficDestinations{Enabled: true}
	})
	step(0, 1000)
	step(5*time.Second, 51000)
	dir := t.TempDir()
	mkdirs := func() {
		for _, name := range []string{traffic.LinksFile, traffic.DevicesFile, traffic.DestinationsFile} {
			if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	kept := func(want map[string]bool) {
		t.Helper()
		for name, keep := range want {
			if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != keep {
				t.Errorf("%s kept = %v, want %v", name, err == nil, keep)
			}
		}
	}
	mkdirs()
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return nil }, Log: slog.New(slog.DiscardHandler)}
	srv := newTestServerWith(t, func(d *Deps) { d.Traffic, d.LogFiles = c, files })
	if rows, held, _ := c.Destinations(time.Hour, ""); len(rows) == 0 || held == 0 {
		t.Fatal("no destinations to clear")
	}
	if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/traffic/destinations", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear destinations: %d %s", resp.StatusCode, raw)
	}
	kept(map[string]bool{traffic.LinksFile: true, traffic.DevicesFile: true, traffic.DestinationsFile: false})
	if rows, held, _ := c.Destinations(time.Hour, ""); len(rows) != 0 || held != 0 {
		t.Errorf("destinations after their clear = %+v", rows)
	}
	if got := c.DeviceReports(traffic.Window24h); len(got) != 1 {
		t.Errorf("devices after the destinations' clear = %+v", got)
	}
	mkdirs()
	if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/traffic", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: %d %s", resp.StatusCode, raw)
	}
	kept(map[string]bool{traffic.LinksFile: true, traffic.DevicesFile: false, traffic.DestinationsFile: false})
}

// The stream carries the links each second.
func TestTrafficStream(t *testing.T) {
	t.Parallel()
	c, step := countingRouter(t)
	srv := newTestServerWith(t, func(d *Deps) { d.Traffic = c })
	stream := openStream(t, srv, "/api/v1/traffic/stream", "")
	readUntil(t, stream, ": connected")
	go func() {
		for range 50 {
			step(time.Second, 1000)
			time.Sleep(20 * time.Millisecond)
		}
	}()
	if data := nextData(t, stream); !strings.Contains(data, `"kind":"links"`) || !strings.Contains(data, `"id":"eth0"`) {
		t.Errorf("event = %s", data)
	}
}

// Destinations read as the name the device asked for, with the service
// its port is, the most first, searched and paged.
func TestTrafficDestinations(t *testing.T) {
	t.Parallel()
	c, step := countingRouter(t, func(cfg *model.Config) {
		cfg.Traffic.Destinations = model.TrafficDestinations{Enabled: true}
	})
	step(0, 1000)
	c.Names().Answered(netip.MustParseAddr("10.0.0.5"), "example.com",
		[]netip.Addr{netip.MustParseAddr("198.51.100.7")}, time.Now())
	step(5*time.Second, 51000)
	srv := newTestServerWith(t, func(d *Deps) { d.Traffic = c })

	read := func(query string) trafficDestinations {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/traffic/destinations"+query, nil)
		var out trafficDestinations
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, raw)
		}
		return out
	}
	got := read("")
	if !got.Enabled || got.Window != "24h" || got.Held != 1 || len(got.Entries) != 1 {
		t.Fatalf("destinations = %+v", got)
	}
	if e := got.Entries[0]; e.Destination != "example.com" || e.Service != "HTTPS" || e.Down != 50000 ||
		e.Device != "aa:bb:cc:00:00:05" || e.Connections != 1 || e.Address != "198.51.100.7" {
		t.Errorf("row = %+v", e)
	}
	if n := len(read("?q=https+example").Entries); n != 1 {
		t.Errorf("a search for what it shows found %d", n)
	}
	if n := len(read("?q=nothing").Entries); n != 0 {
		t.Errorf("a search for nothing found %d", n)
	}
	if n := len(read("?device=10.0.0.9").Entries); n != 0 {
		t.Errorf("another device's rows = %d", n)
	}
	if got := read("?window=1h&limit=1&offset=1"); len(got.Entries) != 0 || got.More {
		t.Errorf("past the end = %+v", got)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/traffic/destinations?window=5m", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("5m: %d %s", resp.StatusCode, raw)
	}
}
