package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
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

func newProxyServer(t *testing.T, unit proxyUnit, cfg *model.Config,
	journal func(context.Context, diag.JournalOptions) ([]diag.JournalEntry, error),
) *httptest.Server {
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
		Proxy:   &services.Proxy{Dir: dir, Cmd: unit},
		Journal: journal,
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

// Two lines captured from the router, one per mode, so the decoder is
// pinned against what Coraza really writes.
const (
	wouldBlockLine = `{"transaction":{"timestamp":"20/Sep/2026:14:02:11 +0000","unix_timestamp":1789999331,` +
		`"id":"XmQ1","client_ip":"192.168.1.55","client_port":51224,"host_ip":"10.130.0.1","host_port":8443,` +
		`"request":{"method":"GET","uri":"/?q=%3Cscript%3E","protocol":"HTTP/2.0"},` +
		`"response":{"status":200},` +
		`"producer":{"connector":"coraza-caddy","rule_engine":"DetectionOnly",` +
		`"rulesets":["OWASP_CRS/4.25.0","ostiole-site:shop"]},` +
		`"highest_severity":"critical","is_interrupted":false},` +
		`"messages":[{"message":"XSS Attack Detected","data":{"id":941100,"msg":"XSS Attack Detected via libinjection",` +
		`"data":"Matched Data: <script> found within ARGS:q","severity":2}},` +
		`{"message":"Inbound Anomaly Score Exceeded","data":{"id":949110,"msg":"Inbound Anomaly Score Exceeded (Total Score: 10)",` +
		`"data":"","severity":2}}]}`
	blockedLine = `{"transaction":{"timestamp":"20/Sep/2026:14:09:40 +0000","unix_timestamp":1789999780,` +
		`"id":"Ze77","client_ip":"192.168.1.55","request":{"method":"GET","uri":"/?q=%3Cscript%3E"},` +
		`"response":{"status":403},` +
		`"producer":{"rule_engine":"On","rulesets":["OWASP_CRS/4.25.0","ostiole-site:shop"]},` +
		`"is_interrupted":true},` +
		`"messages":[{"message":"XSS Attack Detected","data":{"id":941100,"msg":"XSS Attack Detected via libinjection",` +
		`"data":"Matched Data: <script> found within ARGS:q","severity":2}}]}`
)

func TestProxyEventsDecodeBothVerdicts(t *testing.T) {
	t.Parallel()
	var asked diag.JournalOptions
	journal := func(_ context.Context, o diag.JournalOptions) ([]diag.JournalEntry, error) {
		asked = o
		return []diag.JournalEntry{
			{Time: time.Now(), Message: blockedLine},
			{Time: time.Now(), Message: wouldBlockLine},
			{Time: time.Now(), Message: `{"level":"info","msg":"served"}`},
			{Time: time.Now(), Message: "starting up"},
		}, nil
	}
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(), journal)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events?since=-2h&limit=50", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %d %s", resp.StatusCode, raw)
	}
	var got []services.ProxyEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("%d events, want the two audit entries: %+v", len(got), got)
	}
	if got[0].Verdict != services.VerdictBlocked || got[0].Status != 403 {
		t.Errorf("the blocked event decoded as %+v", got[0])
	}
	if got[1].Verdict != services.VerdictWouldBlock {
		t.Errorf("the detection-only event decoded as %+v", got[1])
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
	if asked.Unit != services.ProxyUnit || asked.Since != "-2h" {
		t.Errorf("the journal was asked for %+v", asked)
	}
}

func TestProxyEventsRefuseAnImpossibleLimit(t *testing.T) {
	t.Parallel()
	srv := newProxyServer(t, proxyUnit{installed: true, active: true}, proxyConfig(),
		func(context.Context, diag.JournalOptions) ([]diag.JournalEntry, error) { return nil, nil })
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/proxy/events?limit=5000", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("limit: %d %s", resp.StatusCode, raw)
	}
}

// An audit entry that carries no rule data is still an event: something
// matched, and the row says so.
func TestProxyEventMatchedWithoutAScore(t *testing.T) {
	t.Parallel()
	const line = `{"transaction":{"unix_timestamp":1789999331,"id":"A1","client_ip":"10.0.0.2",` +
		`"request":{"method":"POST","uri":"/login"},"producer":{"rule_engine":"DetectionOnly"},` +
		`"is_interrupted":false},"messages":[{"message":"Odd","data":{"id":920300,"msg":"Missing Accept","severity":5}}]}`
	ev, ok := services.ParseProxyEvent(line, time.Now())
	if !ok {
		t.Fatal("an audit entry was not recognised")
	}
	if ev.Verdict != services.VerdictMatched {
		t.Errorf("verdict = %q", ev.Verdict)
	}
	if ev.Time.Unix() != 1789999331 {
		t.Errorf("time = %v", ev.Time)
	}
	if ev.Site != "" {
		t.Errorf("site = %q, want empty when the signature is not there", ev.Site)
	}
}
