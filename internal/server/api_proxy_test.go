package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/services"
	"ostiole/internal/store"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
)

// proxyUnit answers `systemctl cat`, `is-active` and the release command
// for the sidecar.
type proxyUnit struct {
	installed bool
	active    bool
}

func (u proxyUnit) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if name == services.ProxyBinaryName {
		return []byte("v1.2.3\n"), nil
	}
	switch {
	case len(args) >= 1 && args[0] == "cat":
		if u.installed {
			return []byte("[Unit]"), nil
		}
		return nil, errors.New("no such unit")
	case len(args) >= 1 && args[0] == "is-active":
		if u.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	}
	return nil, nil
}

func newProxyServer(t *testing.T, unit proxyUnit, cfg *model.Config, events *waflog.Log) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)
	if cfg != nil {
		if _, err := st.Save(cfg, "table inet ostiole {}\n"); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng, Auth: as,
		Proxy:  &services.Proxy{Dir: dir, Cmd: unit},
		WAFLog: events,
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

func proxyConfig() *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 8443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "wan", External: true}, {Name: "lan", AntiLockout: true}},
		Interfaces: []model.Interface{
			{Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrNone}},
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
		Services: model.Services{Proxy: model.Proxy{
			Enabled: true, HTTP3: true,
			Access: []model.ProxyAccess{{ID: "wan", Enabled: true, Zone: "wan", Action: model.ActionAccept,
				Ports: []string{model.ProxyPortHTTP, model.ProxyPortHTTPS}}},
			Pools: []model.ProxyPool{{ID: "web", Upstreams: []model.ProxyUpstream{{Address: "192.168.1.20:80"}}}},
			Sites: []model.ProxySite{{ID: "shop", Enabled: true, Hosts: []string{"shop.example.com"}, Pool: "web"}},
		}},
	}
}

// Every way of having nothing to say is a status with the flags false
// rather than an error: the page's own text covers each of them.
func TestProxyStatusIsNeverAnError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		unit    proxyUnit
		setUp   bool
		running bool
	}{
		{"no unit", proxyUnit{}, false, false},
		{"unit but stopped", proxyUnit{installed: true}, true, false},
		{"running", proxyUnit{installed: true, active: true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newProxyServer(t, tc.unit, proxyConfig(), nil)
			resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/status", nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: %d %s", resp.StatusCode, raw)
			}
			var got proxyStatus
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.SetUp != tc.setUp || got.Running != tc.running {
				t.Errorf("setUp/running = %v/%v, want %v/%v", got.SetUp, got.Running, tc.setUp, tc.running)
			}
			if got.Upstreams == nil {
				t.Errorf("the upstream list came back null: %+v", got)
			}
			if got.Ports.HTTP != 80 || got.Ports.HTTPS != 443 || !got.Ports.HTTP3 {
				t.Errorf("ports = %+v", got.Ports)
			}
			if tc.setUp && got.Release != "v1.2.3" {
				t.Errorf("release = %q", got.Release)
			}
		})
	}
}

// wouldBlockEvent and blockedEvent are one event per mode, as the proxy
// writes them.
func wouldBlockEvent() wafevent.Event {
	return wafevent.Event{
		Time: time.Unix(1789999331, 0).UTC(), ID: "XmQ1", Site: "shop", Client: "192.168.1.55",
		Method: "GET", URI: "/?q=%3Cscript%3E", Status: 200,
		Verdict: wafevent.VerdictWouldBlock, Engine: "DetectionOnly",
		Rules: []wafevent.Hit{
			{ID: 941100, Message: "XSS Attack Detected via libinjection", Data: "Matched Data: <script> found within ARGS:q", Severity: "critical"},
			{ID: 949110, Message: "Inbound Anomaly Score Exceeded (Total Score: 10)", Severity: "critical"},
		},
	}
}

func blockedEvent() wafevent.Event {
	return wafevent.Event{
		Time: time.Unix(1789999780, 0).UTC(), ID: "Ze77", Site: "shop", Client: "192.168.1.55",
		Method: "GET", URI: "/?q=%3Cscript%3E", Status: 403,
		Verdict: wafevent.VerdictBlocked, Engine: "On",
		Rules: []wafevent.Hit{
			{ID: 941100, Message: "XSS Attack Detected via libinjection", Data: "Matched Data: <script> found within ARGS:q", Severity: "critical"},
		},
	}
}

// eventLog is a log holding events, logged when they ran as the daemon's
// reader would have put them there. It keeps them whatever their age,
// since the fixtures ran on a fixed day.
func eventLog(events ...wafevent.Event) *waflog.Log {
	l := waflog.New()
	l.Configure(100, 0)
	for _, ev := range events {
		l.Add(ev.Time, ev)
	}
	return l
}

func TestProxyEventsServeBothVerdicts(t *testing.T) {
	t.Parallel()
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(),
		eventLog(wouldBlockEvent(), blockedEvent()))
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events?limit=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d %s", resp.StatusCode, raw)
	}
	var page struct {
		Entries []waflog.Entry `json:"entries"`
		Held    int            `json:"held"`
		Oldest  time.Time      `json:"oldest"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	got := page.Entries
	if len(got) != 2 || page.Held != 2 {
		t.Fatalf("%d events of %d held, want both: %s", len(got), page.Held, raw)
	}
	// Newest first, each with its place in the log.
	if got[0].Verdict != wafevent.VerdictBlocked || got[0].Status != 403 || got[0].Seq != 2 {
		t.Errorf("the blocked event came as %+v", got[0])
	}
	if got[1].Verdict != wafevent.VerdictWouldBlock || got[1].Seq != 1 {
		t.Errorf("the detection-only event came as %+v", got[1])
	}
	if !page.Oldest.Equal(got[1].Logged) || !got[1].Logged.Equal(wouldBlockEvent().Time) {
		t.Errorf("oldest = %v, want %v", page.Oldest, got[1].Logged)
	}
	for _, ev := range got {
		if ev.Site != "shop" {
			t.Errorf("site = %q, want shop", ev.Site)
		}
		if ev.Client != "192.168.1.55" || ev.Method != "GET" {
			t.Errorf("client or method missing: %+v", ev)
		}
		if len(ev.Rules) == 0 || ev.Rules[0].ID != 941100 || ev.Rules[0].Severity != "critical" {
			t.Errorf("rules = %+v", ev.Rules)
		}
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/proxy/events?limit=1", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"held":2`) ||
		strings.Count(string(raw), `"seq":`) != 1 {
		t.Errorf("limit 1: %d %s", resp.StatusCode, raw)
	}
}

func TestProxyEventsRefuseNonsense(t *testing.T) {
	t.Parallel()
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(), waflog.New())
	for _, query := range []string{"?limit=0", "?limit=x", "?limit=1001", "?before=0", "?verdict=maybe"} {
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events"+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", query, resp.StatusCode, raw)
		}
	}
}

// The Events tab searches what its rows show and narrows to a verdict,
// on the router rather than in the page.
func TestProxyEventsSearchAndVerdict(t *testing.T) {
	t.Parallel()
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(),
		eventLog(wouldBlockEvent(), blockedEvent()))
	count := func(query string) int {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, raw)
		}
		var page struct {
			Entries []waflog.Entry `json:"entries"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		return len(page.Entries)
	}
	for query, want := range map[string]int{
		"":                         2,
		"?verdict=blocked":         1,
		"?verdict=would-block":     1,
		"?q=403":                   1,
		"?q=would+block":           1,
		"?q=941100+shop":           2,
		"?q=libinjection+192.168.": 2,
		"?q=nowhere":               0,
	} {
		if got := count(query); got != want {
			t.Errorf("%q: %d events, want %d", query, got, want)
		}
	}
}

// An empty log is a page with nothing in it, not an error: the tab says
// what the proxy is doing.
func TestProxyEventsEmptyLog(t *testing.T) {
	t.Parallel()
	srv := newProxyServer(t, proxyUnit{}, proxyConfig(), waflog.New())
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != `{"entries":[],"more":false,"held":0}`+"\n" {
		t.Errorf("events: %d %q", resp.StatusCode, raw)
	}
}

func TestProxyEventsStreamAsTheyArrive(t *testing.T) {
	t.Parallel()
	events := waflog.New()
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(), events)
	stream := openStream(t, srv, "/api/v1/proxy/events/stream", "")
	readUntil(t, stream, ": connected")
	logged := time.Now()
	events.Add(logged, blockedEvent())
	var got waflog.Entry
	if err := json.Unmarshal([]byte(nextData(t, stream)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Seq != 1 || got.ID != "Ze77" || got.Verdict != wafevent.VerdictBlocked || !got.Logged.Equal(logged) {
		t.Errorf("streamed %+v", got)
	}
}
