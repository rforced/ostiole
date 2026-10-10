package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/discovery"
	"ostiole/internal/discoverylog"
	"ostiole/internal/engine"
	"ostiole/internal/install"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
)

// fakeRelay is a relay as the API reads it.
type fakeRelay struct {
	mu     sync.Mutex
	status discovery.Status
	seen   []discovery.Announcement
}

func (f *fakeRelay) Status() discovery.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeRelay) Announcements() []discovery.Announcement {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

func (f *fakeRelay) set(st discovery.Status, seen []discovery.Announcement) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.seen = st, seen
}

func discoveryAPIConfig() *model.Config {
	cfg := filesOnConfig()
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "things"})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{Name: "eth1.30", Zone: "things", Enabled: true,
		VLAN: &model.VLAN{Parent: "eth1", ID: 30},
		IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.30.1/24"}, IPv6: model.IPv6{Mode: model.AddrNone}})
	cfg.Services.Discovery = model.Discovery{Enabled: true, MDNS: true, SSDP: true, Interfaces: []model.DiscoveryInterface{
		{Interface: "eth1", Asks: true}, {Interface: "eth1.30", Answers: true},
	}}
	return cfg
}

// The discovery log reads on into its files, is searched by what was
// dropped and where a packet went, and streams what the relay sees.
func TestTheDiscoveryLogIsServed(t *testing.T) {
	t.Parallel()
	cfg := discoveryAPIConfig()
	if !cfg.DiscoveryActive() {
		t.Fatalf("the fixture does not relay: %+v", cfg.Services.Discovery)
	}
	l := discoverylog.New()
	srv, files := filesServer(t, cfg, func(d *Deps) { d.DiscoveryLog = l })
	files.Add(discoverylog.Files(l), logfile.ReadStats{})
	l.Add(discoverylog.Event{Protocol: "mdns", Kind: discovery.KindQuery, From: "eth1", To: []string{"eth1.30"},
		Name: "_printer._tcp.local.", Source: "192.168.1.20"})
	l.Add(discoverylog.Event{Protocol: "ssdp", Kind: discovery.KindSearch, From: "eth1", Source: "192.168.1.21", Dropped: "duplicate"})
	flush(t, files)
	l.Configure(1, 0)

	p := readPage[discoverylog.Event](t, srv, "/api/v1/discovery/log")
	if len(p.Entries) != 2 || p.Entries[0].Dropped != "duplicate" || p.Entries[1].Name != "_printer._tcp.local." {
		t.Errorf("read %+v", p.Entries)
	}
	if p := readPage[discoverylog.Event](t, srv, "/api/v1/discovery/log?q=eth1.30"); len(p.Entries) != 1 || p.Entries[0].Protocol != "mdns" {
		t.Errorf("searched by where it went: %+v", p.Entries)
	}
	if p := readPage[discoverylog.Event](t, srv, "/api/v1/discovery/log?q=duplicate"); len(p.Entries) != 1 || p.Entries[0].Protocol != "ssdp" {
		t.Errorf("searched by why it was dropped: %+v", p.Entries)
	}
	if _, raw := do(t, srv, http.MethodGet, "/api/v1/discovery/log", nil); !strings.Contains(string(raw), `"kept":true`) {
		t.Errorf("page %s", raw)
	}

	stream := openStream(t, srv, "/api/v1/discovery/log/stream", "")
	l.Add(discoverylog.Event{Protocol: "mdns", Kind: discovery.KindAnswer, From: "eth1.30", Name: "tv.local.", Source: "192.168.30.5"})
	for {
		line, err := stream.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended: %v", err)
		}
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "tv.local.") {
			break
		}
	}
}

// What the relay heard is a viewer's to read, an empty list when it heard
// nothing, and its status reaches the services strip.
func TestTheDiscoveryRelayIsReported(t *testing.T) {
	t.Parallel()
	relay := &fakeRelay{status: discovery.Status{Running: true, Interfaces: []string{"eth1", "eth1.30"}}}
	srv := withTokens(t, func(d *Deps) { d.Discovery = relay; d.DiscoveryLog = discoverylog.New() })
	viewer := mintToken(t, srv, "look", string(auth.RoleViewer))

	resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/discovery/announcements", viewer)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != `{"announcements":[]}` {
		t.Errorf("nothing heard: %d %s", resp.StatusCode, raw)
	}
	relay.set(relay.Status(), []discovery.Announcement{{Protocol: "mdns", Interface: "eth1.30", Type: "_printer._tcp",
		Name: "Inkwell 300._printer._tcp.local.", Host: "inkwell.local.", Port: 631, LastSeen: time.Now()}})
	var got struct{ Announcements []discovery.Announcement }
	_, raw = withToken(t, srv, http.MethodGet, "/api/v1/discovery/announcements", viewer)
	if err := json.Unmarshal(raw, &got); err != nil || len(got.Announcements) != 1 || got.Announcements[0].Port != 631 {
		t.Errorf("heard: %s", raw)
	}
	if resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/discovery/log", viewer); resp.StatusCode != http.StatusOK {
		t.Errorf("a viewer's read of the log: %d %s", resp.StatusCode, raw)
	}

	var st servicesStatus
	_, raw = do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
	if err := json.Unmarshal(raw, &st); err != nil || !st.DiscoveryRunning || len(st.DiscoveryInterfaces) != 2 {
		t.Errorf("status %s", raw)
	}
	relay.set(discovery.Status{Problem: "could not bind udp 5353: address in use"}, nil)
	_, raw = do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
	if err := json.Unmarshal(raw, &st); err != nil || st.DiscoveryRunning || st.DiscoveryProblem == "" {
		t.Errorf("status with a problem %s", raw)
	}
}

// A daemon without the relay says so rather than serving an empty page.
func TestDiscoveryWithoutTheRelay(t *testing.T) {
	t.Parallel()
	srv := newTestServerWith(t, func(*Deps) {})
	for _, path := range []string{"/api/v1/discovery/log", "/api/v1/discovery/log/stream", "/api/v1/discovery/announcements"} {
		if resp, raw := do(t, srv, http.MethodGet, path, nil); resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", path, resp.StatusCode, raw)
		}
	}
}

// A network where the relay drops packets over its cap is on the
// dashboard, once per interface, and gone when it calms down.
func TestOverviewWarnsWhenDiscoveryDropsPackets(t *testing.T) {
	t.Parallel()
	relay := &fakeRelay{status: discovery.Status{Running: true, Overrun: []string{"eth1", "eth1.30"}}}
	a := &api{discovery: relay}
	warnings := func() []Warning {
		return a.warnings(context.Background(), starter(), engine.Status{}, nil, nil, install.UnitStates{})
	}
	for _, name := range []string{"eth1", "eth1.30"} {
		if w := warningKeyed(warnings(), name); w == nil || w.Kind != "discovery-overrun" || w.Level != "warn" ||
			!strings.Contains(w.Title, name) {
			t.Errorf("%s: warning = %+v", name, w)
		}
	}
	relay.set(discovery.Status{Running: true}, nil)
	for _, w := range warnings() {
		if w.Kind == "discovery-overrun" {
			t.Errorf("a calm network warned: %+v", w)
		}
	}
}
