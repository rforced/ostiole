package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/acme"
	"ostiole/internal/auth"
	"ostiole/internal/certs"
	"ostiole/internal/cron"
	"ostiole/internal/engine"
	"ostiole/internal/feeds"
	"ostiole/internal/fwlog"
	"ostiole/internal/install"
	"ostiole/internal/model"
	"ostiole/internal/network"
	"ostiole/internal/nft"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/services"
	"ostiole/internal/smart"
	"ostiole/internal/store"
	"ostiole/internal/traffic"
	"ostiole/internal/update"
)

// fakeTables stands in for the nft binary's table listing.
type fakeTables struct{ tables []string }

func (f fakeTables) ListTables(context.Context) ([]string, error) { return f.tables, nil }

// fakeUnits answers `systemctl is-active|is-enabled <unit>`; units that are
// absent from the map report not-found, as systemctl does.
type fakeUnits struct{ state map[string][2]string } // unit -> {active, enabled}

// Run answers the two calls install.ReadUnits makes, as systemd does:
// list-unit-files leaves out a unit with no file, and is-active answers
// a line for every unit.
func (f fakeUnits) Run(_ context.Context, args ...string) (string, error) {
	var lines []string
	switch {
	case len(args) > 3 && args[0] == "list-unit-files":
		for _, u := range args[3:] {
			if st, ok := f.state[u]; ok {
				lines = append(lines, u+" "+st[1]+" enabled")
			}
		}
		if len(lines) == 0 {
			return "", errors.New("exit status 1")
		}
	case len(args) > 1 && args[0] == "is-active":
		for _, u := range args[1:] {
			st, ok := f.state[u]
			if !ok {
				st[0] = "inactive"
			}
			lines = append(lines, st[0])
		}
	default:
		return "", errors.New("unexpected systemctl call")
	}
	return strings.Join(lines, "\n"), nil
}

// newOverviewServer returns a logged-in server whose environment says:
// firewalld is running, systemd-networkd is not, and a foreign nft table
// is loaded.
func newOverviewServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	fake := &nfttest.Fake{TableJSON: `{"nftables":[
		{"rule":{"chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":10,"bytes":1000}}]}},
		{"rule":{"chain":"filter_input","comment":"default-drop","expr":[{"counter":{"packets":7,"bytes":700}}]}},
		{"rule":{"chain":"zone_wan","comment":"zone-default","expr":[{"counter":{"packets":3,"bytes":300}}]}}]}`}
	eng := engine.New(store.New(dir), fake, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng,
		Auth:   as,
		Tables: fakeTables{tables: []string{nft.Table, "inet firewalld"}},
		Units: fakeUnits{state: map[string][2]string{
			"firewalld.service":        {"active", "enabled"},
			"systemd-networkd.service": {"inactive", "disabled"},
		}},
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

func getOverview(t *testing.T, srv *httptest.Server) Overview {
	t.Helper()
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/overview", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview: %d %s", resp.StatusCode, raw)
	}
	var ov Overview
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatalf("decode overview: %v (%s)", err, raw)
	}
	return ov
}

func warning(ov Overview, kind string) *Warning {
	for i := range ov.Warnings {
		if ov.Warnings[i].Kind == kind {
			return &ov.Warnings[i]
		}
	}
	return nil
}

// countingUnits keeps the verb of every systemctl call.
type countingUnits struct {
	fakeUnits
	mu    sync.Mutex
	verbs []string
}

func (c *countingUnits) Run(ctx context.Context, args ...string) (string, error) {
	c.mu.Lock()
	c.verbs = append(c.verbs, args[0])
	c.mu.Unlock()
	return c.fakeUnits.Run(ctx, args...)
}

func (c *countingUnits) reset() {
	c.mu.Lock()
	c.verbs = nil
	c.mu.Unlock()
}

// listings notes how the ruleset is listed.
type listings struct {
	*nfttest.Fake
	mu    sync.Mutex
	asked []string
}

func (l *listings) note(what string) {
	l.mu.Lock()
	l.asked = append(l.asked, what)
	l.mu.Unlock()
}

func (l *listings) ListTableJSON(ctx context.Context) ([]byte, error) {
	l.note("table")
	return l.Fake.ListTableJSON(ctx)
}

func (l *listings) ListTableTerseJSON(ctx context.Context) ([]byte, error) {
	l.note("terse table")
	return l.Fake.ListTableTerseJSON(ctx)
}

func (l *listings) ListChainJSON(ctx context.Context, chain string) ([]byte, error) {
	l.note("chain " + chain)
	return l.Fake.ListChainJSON(ctx, chain)
}

// The DHCP, DNS and UPnP pages open on the services strip. It asked
// systemd about each service one call at a time and listed the whole
// ruleset for the mappings, about half a second on a router. Two calls
// and the mappings' own chain answer it now.
func TestServicesStatusAsksSystemdTwiceAndListsOneChain(t *testing.T) {
	t.Parallel()
	units := &countingUnits{fakeUnits: fakeUnits{state: map[string][2]string{
		services.Unit:      {"active", "enabled"},
		services.UPnPUnit:  {"active", "enabled"},
		services.PPPoEUnit: {"", "static"},
	}}}
	nftRules := &listings{Fake: &nfttest.Fake{TableJSON: `{"nftables":[
		{"rule":{"chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":1,"bytes":10}}]}},
		{"rule":{"chain":"upnp_prerouting","expr":[
			{"match":{"op":"==","left":{"payload":{"protocol":"udp","field":"dport"}},"right":19132}},
			{"dnat":{"addr":"192.168.50.40","port":19132}}]}}]}`}}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Engine = engine.New(store.New(t.TempDir()), nftRules, nil, slog.New(slog.DiscardHandler))
		d.Units = units
		d.Services = &services.Dnsmasq{Cmd: noCommands{t}, Leases: filepath.Join(t.TempDir(), "leases")}
		d.PPPoE = &services.PPPoE{Cmd: noCommands{t}}
		d.UPnP = &services.UPnP{Cmd: noCommands{t}}
	})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	units.reset()
	nftRules.mu.Lock()
	nftRules.asked = nil
	nftRules.mu.Unlock()

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}
	var st servicesStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(units.verbs, []string{"list-unit-files", "is-active"}) {
		t.Errorf("systemctl calls = %q, want list-unit-files and is-active once each", units.verbs)
	}
	if !slices.Equal(nftRules.asked, []string{"chain " + nft.UPnPPreroutingChain}) {
		t.Errorf("listings = %q, want the mappings' chain alone", nftRules.asked)
	}
	if !st.SetUp || !st.Running || !st.PPPoESetUp || !st.UPnPSetUp || !st.UPnPRunning || st.Mappings != 1 {
		t.Errorf("status = %+v", st)
	}
}

// noCommands fails a test that runs anything through a service's own
// commander.
type noCommands struct{ t *testing.T }

func (n noCommands) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	n.t.Errorf("ran %s %q", name, args)
	return nil, errors.New("not here")
}

// The dashboard asked systemd about some thirty units one call at a time,
// a quarter of a second a view on a router. Two calls answer them all,
// the notification watcher's too.
func TestOverviewAsksSystemdTwice(t *testing.T) {
	t.Parallel()
	units := &countingUnits{fakeUnits: fakeUnits{state: map[string][2]string{
		services.Unit:              {"active", "enabled"},
		services.UnboundUnit:       {"failed", "enabled"},
		"firewalld.service":        {"active", "enabled"},
		"systemd-networkd.service": {"active", "enabled"},
	}}}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Units = units
		d.Services = &services.Dnsmasq{Cmd: noCommands{t}, Leases: filepath.Join(t.TempDir(), "leases")}
		d.Resolver = &services.Unbound{Cmd: noCommands{t}}
		d.UPnP = &services.UPnP{Cmd: noCommands{t}}
	})
	ov := getOverview(t, srv)
	if !slices.Equal(units.verbs, []string{"list-unit-files", "is-active"}) {
		t.Errorf("systemctl calls = %q, want list-unit-files and is-active once each", units.verbs)
	}
	tiles := map[string]string{}
	for _, s := range ov.Services {
		tiles[s.Name] = s.State
	}
	if tiles["DHCP and DNS"] != stateActive || tiles["Network"] != stateActive {
		t.Errorf("tiles = %v", tiles)
	}
	if w := warning(ov, "conflicting-services"); w == nil || !strings.Contains(w.Detail, "firewalld (active, enabled)") {
		t.Errorf("warnings = %+v", ov.Warnings)
	}
}

// A unit with no file is missing and says what to run; one systemd
// stopped is inactive; one systemctl could not answer for is unknown,
// not missing.
func TestServiceTilesFromWhatSystemdSaid(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		state map[string][2]string
		down  bool
		want  string
	}{
		{"running", map[string][2]string{services.UPnPUnit: {"active", "enabled"}}, false, stateActive},
		{"reloading", map[string][2]string{services.UPnPUnit: {"reloading", "enabled"}}, false, stateActive},
		{"stopped", map[string][2]string{services.UPnPUnit: {"failed", "enabled"}}, false, stateInactive},
		{"no file", map[string][2]string{}, false, stateMissing},
		{"no systemd", nil, true, stateUnknown},
	} {
		var sc install.Systemctl = fakeUnits{state: tc.state}
		if tc.down {
			sc = systemdDown{}
		}
		tile := ServiceState{Unit: services.UPnPUnit}
		fillTile(&tile, install.ReadUnits(t.Context(), sc, services.UPnPUnit), services.UPnPUnit, "run repair")
		if tile.State != tc.want || (tile.Detail != "") != (tc.want == stateMissing) {
			t.Errorf("%s: tile = %+v, want %s", tc.name, tile, tc.want)
		}
	}
}

// systemdDown is a systemctl on a machine systemd does not run.
type systemdDown struct{}

func (systemdDown) Run(context.Context, ...string) (string, error) {
	return "System has not been booted with systemd as init system (PID 1). Can't operate.", errors.New("exit status 1")
}

func TestOverviewUnconfigured(t *testing.T) {
	t.Parallel()
	srv := newOverviewServer(t)

	ov := getOverview(t, srv)
	if ov.Status.Configured || ov.Status.TableLoaded {
		t.Errorf("status = %+v, want unconfigured", ov.Status)
	}
	if len(ov.TopRules) != 0 {
		t.Errorf("topRules = %+v, want none", ov.TopRules)
	}
	// The loopback is never listed, but the router's own links are.
	for _, l := range ov.Interfaces {
		if l.Name == "lo" {
			t.Error("loopback should not appear in the interface summary")
		}
	}
	if w := warning(ov, "foreign-tables"); w == nil {
		t.Errorf("want a foreign-tables warning, got %+v", ov.Warnings)
	} else if !strings.Contains(w.Detail, "inet firewalld") || strings.Contains(w.Detail, nft.Table+",") {
		t.Errorf("foreign table detail = %q", w.Detail)
	}
	if w := warning(ov, "conflicting-services"); w == nil || !strings.Contains(w.Detail, "firewalld") {
		t.Errorf("want a conflicting-services warning, got %+v", ov.Warnings)
	}
	if w := warning(ov, "table-missing"); w != nil {
		t.Errorf("unconfigured router should not warn about a missing table: %+v", w)
	}
}

func TestOverviewAfterApply(t *testing.T) {
	t.Parallel()
	srv := newOverviewServer(t)
	cfg := model.Starter(model.StarterOptions{
		Hostname: "fw", LAN: "ost-lan0", LANAddress: "192.168.9.1/24", WAN: "ost-wan0",
		Services: true, DNSUpstreams: []string{"9.9.9.9"},
	})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	ov := getOverview(t, srv)
	if !ov.Status.Configured || !ov.Status.TableLoaded {
		t.Errorf("status = %+v, want configured and loaded", ov.Status)
	}
	if ov.Status.Hostname != "fw" || ov.Status.Zones != 2 || ov.Status.Rules != 1 {
		t.Errorf("status summary = %+v", ov.Status)
	}

	lan := linkByName(ov, "ost-lan0")
	if lan == nil {
		t.Fatalf("interfaces = %+v, want the configured lan", ov.Interfaces)
	}
	if !lan.Configured || !lan.Enabled || lan.Zone != "lan" || lan.IPv4Mode != "static" {
		t.Errorf("lan summary = %+v", lan)
	}
	if lan.Present {
		t.Errorf("ost-lan0 does not exist on this host, want present=false: %+v", lan)
	}
	if wan := linkByName(ov, "ost-wan0"); wan == nil || !wan.External {
		t.Errorf("wan summary = %+v, want an external zone", wan)
	}
	if w := warning(ov, "interface-missing"); w == nil || !strings.Contains(w.Detail, "ost-lan0") {
		t.Errorf("want an interface-missing warning, got %+v", ov.Warnings)
	}

	if len(ov.TopRules) != 1 || ov.TopRules[0].ID != "allow-lan" || ov.TopRules[0].Packets != 10 {
		t.Errorf("topRules = %+v", ov.TopRules)
	}
	if ov.Blocked.Packets != 10 || ov.Blocked.Bytes != 1000 {
		t.Errorf("blocked = %+v, want the default-drop and zone-default sums", ov.Blocked)
	}
	// Which default routes this host has is its own business, but the
	// list is always there, and nothing on it is a gateway already watched.
	if ov.UnwatchedGateways == nil {
		t.Error("unwatchedGateways is missing, want an array")
	}
	for _, d := range ov.UnwatchedGateways {
		if d.Configured != "" {
			t.Errorf("unwatched route %+v is covered by gateway %q", d, d.Configured)
		}
	}
	// No log listener here, and the dashboard must be able to tell that
	// from a quiet log; no lease reader, but the list is still a list.
	if ov.RecentBlocks != nil {
		t.Errorf("recentBlocks = %+v, want null without a listener", ov.RecentBlocks)
	}
	if ov.RecentLeases == nil {
		t.Error("recentLeases is missing, want an array")
	}
	if ov.Wireless != nil {
		t.Errorf("wireless = %+v, want none without a radio", ov.Wireless)
	}

	if !ov.DHCP.Enabled || ov.DHCP.Servers != 1 || ov.DHCP.Capacity != 100 {
		t.Errorf("dhcp = %+v, want one server of 100 addresses", ov.DHCP)
	}
	if !ov.DNS.Enabled || ov.DNS.Domain != "lan" {
		t.Errorf("dns = %+v", ov.DNS)
	}

	// dnsmasq is wanted but the daemon has no services backend, so its
	// state is unknown and must not raise a false alarm; networkd is known
	// to be down and must.
	if w := warning(ov, "service-down"); w == nil || !strings.Contains(w.Title, "Network") {
		t.Errorf("want a service-down warning for the network, got %+v", ov.Warnings)
	}
	var netd *ServiceState
	for i := range ov.Services {
		if ov.Services[i].Name == "Network" {
			netd = &ov.Services[i]
		}
	}
	if netd == nil || netd.State != stateInactive || !netd.Want {
		t.Errorf("network service state = %+v", netd)
	}
}

// Each resolver mode keeps the other's servers for switching back, so the
// dashboard names only the ones in use. Naming the rest would say the
// router forwards somewhere it does not.
func TestSummarizeDNSNamesTheServersInUse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*model.Config)
		mode model.ResolverMode
		want []string
	}{
		{"forward", func(*model.Config) {}, model.ResolverForward, []string{"192.0.2.53"}},
		{"forward to the system resolvers", func(c *model.Config) {
			c.Services.DNS.Upstreams = nil
			c.System.DNSServers = []string{"192.0.2.1"}
		}, model.ResolverForward, []string{"192.0.2.1"}},
		{"tls", func(c *model.Config) { c.Services.DNS.Resolver = model.ResolverTLS }, model.ResolverTLS, []string{"dns.quad9.net"}},
		{"recursive", func(c *model.Config) { c.Services.DNS.Resolver = model.ResolverRecursive }, model.ResolverRecursive, nil},
	}
	for _, tc := range cases {
		cfg := model.Starter(model.StarterOptions{
			LAN: "eth1", LANAddress: "192.168.1.1/24", Services: true, DNSUpstreams: []string{"192.0.2.53"},
		})
		cfg.Services.DNS.TLSUpstreams = []model.TLSUpstream{
			{Address: "9.9.9.9", Hostname: "dns.quad9.net"},
			{Address: "149.112.112.112", Hostname: "dns.quad9.net"},
		}
		tc.edit(cfg)
		if _, dns := summarizeServices(cfg); dns.Resolver != tc.mode || !slices.Equal(dns.Upstreams, tc.want) {
			t.Errorf("%s: resolver %q, upstreams %v; want %q, %v", tc.name, dns.Resolver, dns.Upstreams, tc.mode, tc.want)
		}
	}
}

// A pool on an interface that has been switched off hands out nothing.
// That is allowed — the interface comes back and so does the pool — so it
// is not counted as a server, and the dashboard says where the addresses
// went rather than leaving it to be worked out from an empty lease list.
func TestOverviewNotesDHCPOnADisabledInterface(t *testing.T) {
	t.Parallel()
	srv := newOverviewServer(t)
	cfg := model.Starter(model.StarterOptions{
		Hostname: "fw", LAN: "ost-lan0", LANAddress: "192.168.9.1/24", WAN: "ost-wan0",
		Services: true, DNSUpstreams: []string{"9.9.9.9"},
	})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: "ost-ap0", Zone: "lan", Enabled: false,
		IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.10.1/24"},
		IPv6: model.IPv6{Mode: model.AddrNone},
	})
	cfg.Services.DHCP.Servers = append(cfg.Services.DHCP.Servers, model.DHCPServer{
		Interface: "ost-ap0", Enabled: true, RangeStart: "192.168.10.100", RangeEnd: "192.168.10.199",
	})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	ov := getOverview(t, srv)
	if ov.DHCP.Servers != 1 || ov.DHCP.Capacity != 100 {
		t.Errorf("dhcp = %+v, want only the pool on the enabled interface", ov.DHCP)
	}
	if w := warning(ov, "dhcp-interface-off"); w == nil || !strings.Contains(w.Detail, "ost-ap0") {
		t.Errorf("want a dhcp-interface-off warning naming ost-ap0, got %+v", ov.Warnings)
	}
}

// A drive that says it is failing reaches the dashboard without anyone
// opening Diagnostics; a healthy one says nothing at all.
func TestOverviewWarnsAboutAFailingDrive(t *testing.T) {
	t.Parallel()
	passed := true
	client := &smart.Client{Bin: "smartctl", Run: func(_ context.Context, _ string, args ...string) ([]byte, int, error) {
		if slices.Contains(args, "--scan-open") {
			return []byte(`{"smartctl":{"version":[7,5],"exit_status":0},"devices":[` +
				`{"name":"/dev/sda","type":"sat","protocol":"ATA"}]}`), 0, nil
		}
		return []byte(`{"smartctl":{"version":[7,5],"exit_status":0},"model_name":"GENERIC 256GB SSD",` +
			`"smart_status":{"passed":` + strconv.FormatBool(passed) + `}}`), 0, nil
	}}
	mon := &smart.Monitor{Client: client, Log: slog.New(slog.DiscardHandler)}
	srv := newTestServerWith(t, func(d *Deps) { d.DriveHealth = mon })

	mon.Check(t.Context())
	if w := warning(getOverview(t, srv), "drive-failing"); w != nil {
		t.Errorf("a healthy drive warned: %+v", w)
	}

	passed = false
	mon.Check(t.Context())
	w := warning(getOverview(t, srv), "drive-failing")
	if w == nil {
		t.Fatalf("want a drive-failing warning, got %+v", getOverview(t, srv).Warnings)
	}
	if !strings.Contains(w.Title, "sda") || !strings.Contains(w.Detail, "GENERIC 256GB SSD") {
		t.Errorf("warning = %+v", w)
	}
	if !strings.Contains(w.Detail, "Diagnostics, Drives") {
		t.Errorf("warning does not say where to look: %+v", w)
	}
}

// fakeCrons reports one cron and runs nothing.
type fakeCrons struct{ statuses []cron.Status }

func (f *fakeCrons) Statuses() []cron.Status                  { return f.statuses }
func (f *fakeCrons) RunNow(_ context.Context, _ string) error { return nil }

// A copy that is not reaching the bucket is only discovered when it is
// needed, so it goes on the dashboard the way a failing drive does.
func TestOverviewWarnsAboutAFailedRemoteBackup(t *testing.T) {
	t.Parallel()
	crons := &fakeCrons{}
	srv := newTestServerWith(t, func(d *Deps) { d.Crons = crons })
	cfg := starter()
	cfg.Backup.Remote = model.RemoteBackup{
		Enabled: true, Endpoint: "https://s3.us-west-004.backblazeb2.com", Bucket: "router-backups",
		KeyID: "k", Secret: "s", Passphrase: "correct horse",
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	ran := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	clean := cron.Status{ID: model.CronIDRemoteBackup, LastRun: &ran, LastOutput: "uploaded"}
	crons.statuses = []cron.Status{clean}
	if w := warning(getOverview(t, srv), "remote-backup-failed"); w != nil {
		t.Errorf("a clean run warned: %+v", w)
	}

	failed := clean
	failed.LastError = "the key may not upload"
	crons.statuses = []cron.Status{failed}
	w := warning(getOverview(t, srv), "remote-backup-failed")
	if w == nil {
		t.Fatalf("want a remote-backup-failed warning, got %+v", getOverview(t, srv).Warnings)
	}
	if !strings.Contains(w.Detail, "the key may not upload") || !strings.Contains(w.Detail, "03:00") {
		t.Errorf("warning = %+v", w)
	}
	if !strings.Contains(w.Detail, "System › Configuration") {
		t.Errorf("warning does not say where to look: %+v", w)
	}

	// Switched off, an old failure is not news.
	cfg.Backup.Remote.Enabled = false
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if w := warning(getOverview(t, srv), "remote-backup-failed"); w != nil {
		t.Errorf("a disabled backup warned: %+v", w)
	}
}

func TestSummarizeLinksPrefersKernelState(t *testing.T) {
	t.Parallel()
	cfg := model.Starter(model.StarterOptions{LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0"})
	links := []network.Link{
		{Name: "lo", Kind: "loopback"},
		{Name: "eth1", Kind: "ethernet", Up: true, Carrier: true, MTU: 1500, Addresses: []string{"10.0.0.1/24"}, RXBytes: 5, RXErrors: 147},
		{Name: "wg0", Kind: "wireguard", Up: true},
	}
	got := summarizeLinks(cfg, links)
	if len(got) != 3 {
		t.Fatalf("summaries = %+v, want lan, wan, and the unconfigured wg0", got)
	}
	// The kernel's error totals never reach a summary.
	if got[0].Name != "eth1" || !got[0].Present || !got[0].Carrier || got[0].RXBytes != 5 || got[0].RXErrors != 0 {
		t.Errorf("eth1 = %+v", got[0])
	}
	if got[1].Name != "eth0" || got[1].Present {
		t.Errorf("eth0 = %+v, want configured but absent", got[1])
	}
	if got[2].Name != "wg0" || got[2].Configured {
		t.Errorf("wg0 = %+v, want live but unconfigured", got[2])
	}
}

// The dashboard and the Interfaces page show a link's errors of the last
// 72 hours from the traffic counter, never the kernel's totals.
func TestLinksCarryTheCountersErrors(t *testing.T) {
	t.Parallel()
	errs := map[string]traffic.Errors{"wg0": {RX: 3}}
	summaries := []LinkSummary{{Name: "eth1"}, {Name: "wg0"}}
	withErrors(summaries, errs)
	if summaries[0].RXErrors != 0 || summaries[0].TXErrors != 0 || summaries[1].RXErrors != 3 {
		t.Errorf("summaries = %+v", summaries)
	}
	raw, err := json.Marshal(liveLinks([]network.Link{
		{Name: "eth1", Kind: "ethernet", RXErrors: 147},
		{Name: "wg0", Kind: "wireguard", TXErrors: 9},
	}, errs))
	if err != nil {
		t.Fatal(err)
	}
	var live []map[string]any
	if err := json.Unmarshal(raw, &live); err != nil {
		t.Fatal(err)
	}
	if _, ok := live[0]["rxErrors"]; ok || live[0]["name"] != "eth1" {
		t.Errorf("eth1 = %v, want no errors", live[0])
	}
	if _, ok := live[1]["txErrors"]; ok || live[1]["rxErrors"] != 3.0 {
		t.Errorf("wg0 = %v, want the counter's 3 alone", live[1])
	}
}

func TestPoolSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		start, end string
		want       int
	}{
		{"192.168.1.100", "192.168.1.199", 100},
		{"10.0.0.1", "10.0.0.1", 1},
		{"10.0.0.9", "10.0.0.1", 0},
		{"", "", 0},
		{"fd00::1", "fd00::9", 0},
	}
	for _, c := range cases {
		if got := poolSize(c.start, c.end); got != c.want {
			t.Errorf("poolSize(%q, %q) = %d, want %d", c.start, c.end, got, c.want)
		}
	}
}

func linkByName(ov Overview, name string) *LinkSummary {
	for i := range ov.Interfaces {
		if ov.Interfaces[i].Name == name {
			return &ov.Interfaces[i]
		}
	}
	return nil
}

func TestRecentBlocksKeepsOnlyRefusals(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{Rules: []model.Rule{
		{ID: "allow-lan", Action: model.ActionAccept},
		{ID: "block-iot", Action: model.ActionDrop},
	}}

	log := []fwlog.Entry{
		{Kind: "rule", RuleID: "allow-lan", Src: "6"},
		{Kind: "zone-drop", Src: "5"},
		{Kind: "rule", RuleID: "block-iot", Src: "4"},
		{Kind: "other", Src: "3"},
		{Kind: "default-drop", Src: "2"},
		{Kind: "rule", RuleID: "allow-lan", Src: "1"},
	}
	got := recentBlocks(cfg, log, 2)
	if len(got) != 2 || got[0].Src != "5" || got[1].Src != "4" {
		t.Errorf("recentBlocks = %+v, want the two newest refusals, newest first", got)
	}
	if all := recentBlocks(cfg, log, 10); len(all) != 3 {
		t.Errorf("recentBlocks unlimited = %+v, want three refusals", all)
	}
	if none := recentBlocks(nil, log, 10); len(none) != 2 {
		t.Errorf("recentBlocks without a config = %+v, want only the policy drops", none)
	}
}

// TestRecentBlocksPrefersTheLoggedAction checks that a packet carrying its
// own verdict is judged on that and not on the rule's action today, which
// is what an edit since it was logged would give.
func TestRecentBlocksPrefersTheLoggedAction(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{Rules: []model.Rule{
		{ID: "was-accept", Action: model.ActionDrop},
		{ID: "was-drop", Action: model.ActionAccept},
	}}

	log := []fwlog.Entry{
		{Kind: "rule", RuleID: "was-accept", Action: "accept", Src: "4"},
		{Kind: "rule", RuleID: "was-drop", Action: "drop", Src: "3"},
		{Kind: "rule", RuleID: "was-accept", Action: "reject", Src: "2"},
		{Kind: "block-private", Action: "drop", Src: "1"},
	}
	got := recentBlocks(cfg, log, 10)
	if len(got) != 3 || got[0].Src != "3" || got[1].Src != "2" || got[2].Src != "1" {
		t.Errorf("recentBlocks = %+v, want the drop, the reject and the blocked source", got)
	}
}

// A certificate in the last third of its life whose renewal failed is a
// warning; one that is simply getting on is not.
func TestOverviewWarnsAboutACertificateThatStoppedRenewing(t *testing.T) {
	t.Parallel()
	cs := certs.NewStore(filepath.Join(t.TempDir(), "certs"))
	srv := newTestServerWith(t, func(d *Deps) {
		d.CertStore = cs
		d.Renewer = &acme.Renewer{Store: cs, Config: d.Engine.Effective}
	})
	cfg := starter()
	cfg.ACME.Accounts = []model.ACMEAccount{{ID: "le", Directory: model.ACMEDirectoryLetsEncryptStaging, PrivateKey: testAccountKey(t)}}
	cfg.Certificates = []model.Certificate{{
		ID: "router", Enabled: true, Source: model.SourceACME, Account: "le",
		Names: []string{"router.example.test"}, Challenge: model.ChallengeHTTP,
	}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	certPEM, keyPEM := expiringCert(t, "router.example.test", 90*24*time.Hour, 70*24*time.Hour)
	if err := cs.Write("router", certs.Files{Cert: certPEM, FullChain: certPEM, Key: keyPEM}); err != nil {
		t.Fatal(err)
	}
	if w := warning(getOverview(t, srv), "certificate"); w != nil {
		t.Errorf("a certificate nothing has refused warned: %+v", w)
	}

	attempted := time.Now().UTC()
	if err := cs.WriteState("router", certs.State{Names: []string{"router.example.test"}, LastAttempt: &attempted, LastError: "the CA said no"}); err != nil {
		t.Fatal(err)
	}
	w := warning(getOverview(t, srv), "certificate")
	if w == nil {
		t.Fatalf("want a certificate warning, got %+v", getOverview(t, srv).Warnings)
	}
	if !strings.HasPrefix(w.Title, "Certificate router expires") || !strings.Contains(w.Detail, "the CA said no") {
		t.Errorf("warning = %+v", w)
	}
}

// A router renamed or renumbered keeps a built-in certificate naming what
// it was. The dashboard says so for the names people type, not for the
// addresses the WAN and SLAAC move, and regenerating clears it.
func TestOverviewWarnsWhenTheBuiltInCertificateMissesAName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	m := certs.New(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if _, err := m.SelfSigned([]string{"test", "localhost", "192.168.1.153"}); err != nil {
		t.Fatal(err)
	}
	hosts := []string{"localhost", "127.0.0.1", "::1", "FW", "10.0.0.1", "203.0.113.7", "fe80::1"}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Certs = m
		d.CertHosts = func() []string { return hosts }
	})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	w := warning(getOverview(t, srv), "certificate-names")
	if w == nil {
		t.Fatalf("want a certificate-names warning, got %+v", getOverview(t, srv).Warnings)
	}
	if !strings.Contains(w.Detail, "cover fw, 10.0.0.1, so") || !strings.Contains(w.Detail, "System › Certificates") {
		t.Errorf("detail = %q, want the hostname and the LAN address, and where to regenerate", w.Detail)
	}

	if _, err := m.SelfSigned(hosts); err != nil {
		t.Fatal(err)
	}
	if w := warning(getOverview(t, srv), "certificate-names"); w != nil {
		t.Errorf("still warns after regenerating: %+v", w)
	}
}

// expiringCert is a self-signed certificate that has lived age of life.
func expiringCert(t *testing.T, name string, life, age time.Duration) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-age),
		NotAfter:     now.Add(life - age),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// testAccountKey is a P-256 key the validator accepts; nothing signs with it.
func testAccountKey(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// A router on its fallback ruleset says so on the dashboard until an apply
// puts a confirmed one back.
func TestOverviewWarnsAboutTheFallbackRuleset(t *testing.T) {
	t.Parallel()
	var eng *engine.Engine
	srv := newTestServerWith(t, func(d *Deps) { eng = d.Engine })
	raw, _ := json.Marshal(engine.FallbackStatus{Since: time.Now(), Reason: "nft refused it"})
	if err := eng.Store().WriteState(store.FallbackFile, raw); err != nil {
		t.Fatal(err)
	}
	if w := warning(getOverview(t, srv), "fallback-ruleset"); w == nil || !strings.Contains(w.Detail, "nft refused it") {
		t.Fatalf("want a fallback-ruleset warning, got %+v", w)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if w := warning(getOverview(t, srv), "fallback-ruleset"); w != nil {
		t.Errorf("a confirmed apply left the warning: %+v", w)
	}
}

// An update the restart script put back was installed by a cron nobody
// watched, so the dashboard says so until a newer version runs.
func TestOverviewWarnsAboutARolledBackUpdate(t *testing.T) {
	t.Parallel()
	note := filepath.Join(t.TempDir(), update.RolledBackFile)
	updater := &update.Manager{Current: "1.0.2", Installer: &update.Installer{RolledBack: note}}
	srv := newTestServerWith(t, func(d *Deps) { d.Updater = updater })
	if w := warning(getOverview(t, srv), "update-rolled-back"); w != nil {
		t.Errorf("warned with no rollback: %+v", w)
	}
	if err := os.WriteFile(note, []byte("1.0.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := warning(getOverview(t, srv), "update-rolled-back")
	if w == nil || !strings.Contains(w.Title, "1.0.3") || !strings.Contains(w.Detail, "1.0.2 was put back") {
		t.Errorf("warning = %+v", w)
	}
}

// sshd that goes on taking passwords after an apply that turned them off
// is a setting that is not in force, which is worth a line on the dashboard.
func TestOverviewWarnsWhenSSHDoesNotFollow(t *testing.T) {
	t.Parallel()
	srv := newTestServerWith(t, func(d *Deps) { d.Engine = d.Engine.WithSSH(sshRefuses{}) })
	cfg := starter()
	cfg.System.Management.SSHPasswords = false
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if w := warning(getOverview(t, srv), "ssh-settings"); w == nil || !strings.Contains(w.Detail, "still accepts passwords") {
		t.Errorf("warning = %+v", w)
	}
}

type sshRefuses struct{}

func (sshRefuses) Apply(context.Context, bool) error {
	return errors.New("sshd still accepts passwords")
}

// A fetched alias with no entries turns the rules over it inside out, so
// each one an enabled rule uses is named, with those rules.
func TestEmptyRuleAliases(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Rules = []model.Rule{
		{ID: "home-only", Enabled: true, Source: model.Endpoint{Alias: "home", NotAddresses: true}},
		{ID: "bad", Enabled: true, Destination: model.Endpoint{Alias: "bad"}},
		{ID: "off", Enabled: false, Source: model.Endpoint{Alias: "spare"}},
		{ID: "also-home", Enabled: true, Destination: model.Endpoint{Alias: "home"}},
	}
	statuses := []feeds.Status{{Alias: "home"}, {Alias: "bad", Entries: 12}, {Alias: "spare"}}
	got := emptyRuleAliases(cfg, statuses)
	if len(got) != 1 || got[0].alias != "home" || strings.Join(got[0].rules, ",") != "home-only,also-home" {
		t.Errorf("empty aliases = %+v", got)
	}
	if got := got[0].users(); got != "Rules home-only, also-home use it" {
		t.Errorf("users = %q", got)
	}

	// The proxy's access list counts too, while the proxy is serving.
	cfg.Services.Proxy = model.Proxy{
		Enabled: true,
		Sites:   []model.ProxySite{{ID: "shop", Enabled: true}},
		Access: []model.ProxyAccess{
			{ID: "us-only", Enabled: true, Source: model.Endpoint{Alias: "spare"}},
			{ID: "off", Source: model.Endpoint{Alias: "spare"}},
		},
	}
	got = emptyRuleAliases(cfg, statuses)
	if len(got) != 2 || got[1].alias != "spare" || got[1].users() != "Access rule us-only uses it" {
		t.Errorf("empty aliases = %+v", got)
	}
	both := emptyAlias{rules: []string{"a"}, access: []string{"b", "c"}}
	if got := both.users(); got != "Rule a and access rules b, c use it" {
		t.Errorf("users = %q", got)
	}
}

// An http-01 certificate checked through a proxy whose port 80 is open to
// some sources only is worth a line: the CA checks from several places.
func TestWarnsWhenCertificateChecksAreLimited(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "us", Type: model.AliasGeoIP, Entries: []string{"us"}}}
	cfg.ACME.Accounts = []model.ACMEAccount{{ID: "ca", Directory: model.ACMEDirectoryLetsEncryptStaging}}
	cfg.Certificates = []model.Certificate{{ID: "router", Enabled: true, Source: model.SourceACME,
		Names: []string{"router.example.com"}, Account: "ca", Challenge: model.ChallengeHTTP}}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true,
		Sites:   []model.ProxySite{{ID: "shop", Enabled: true}},
		Access: []model.ProxyAccess{{ID: "us", Enabled: true, Zone: "wan", Action: model.ActionAccept,
			Ports: []string{model.ProxyPortHTTP, model.ProxyPortHTTPS}, Source: model.Endpoint{Alias: "us"}}},
	}
	find := func() *Warning {
		ws := (&api{}).warnings(context.Background(), cfg, engine.Status{}, nil, nil, install.UnitStates{})
		return warning(Overview{Warnings: ws}, "challenges-limited")
	}
	if w := find(); w == nil || !strings.Contains(w.Detail, "Services, Reverse proxy") {
		t.Errorf("warning = %+v", w)
	}
	cfg.Services.Proxy.Access[0].Source = model.Endpoint{}
	if w := find(); w != nil {
		t.Errorf("port 80 open to anyone warned: %+v", w)
	}
}

// Full, the logs could crowd out the router, and each is sized on its own
// page, so the dashboard is where the total shows.
func TestWarnsWhenTheLogsCouldOutgrowTheirMemory(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.System.Management.FirewallLog.Entries = 1_800_000 // 630 MB, 945 MB at the peak
	find := func(mem uint64) *Warning {
		a := &api{memTotal: func() uint64 { return mem }}
		return warningKeyed(a.warnings(context.Background(), cfg, engine.Status{}, nil, nil, install.UnitStates{}), "logs-memory")
	}
	w := find(1_400_000_000)
	if w == nil || w.Kind != "logs" || w.Title != "The logs could take more memory than this router has for them" ||
		!strings.HasPrefix(w.Detail, "Full, the logs in memory take 945 MB. This router has 875 MB for them. Set fewer entries under Firewall › Log, ") ||
		!strings.HasSuffix(w.Detail, "Wireless › Log or VPN › Logs.") {
		t.Errorf("warning = %+v", w)
	}
	if w := find(1_500_000_000); w != nil {
		t.Errorf("945 MB in 975 MB warned: %+v", w)
	}
	// Memory it cannot read says nothing.
	if w := find(0); w != nil {
		t.Errorf("unknown memory warned: %+v", w)
	}
}

// A log set above what the memory holds keeps fewer entries, and the page
// it is set on does not say so.
func TestWarnsWhenALogKeepsFewerThanSet(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.QueryLog.Enabled = true
	cfg.Services.DNS.QueryLog.Entries = 5_000_000
	find := func(mem uint64) *Warning {
		a := &api{memTotal: func() uint64 { return mem }}
		return warningKeyed(a.warnings(context.Background(), cfg, engine.Status{}, nil, nil, install.UnitStates{}), "logs-clamped")
	}
	w := find(950_000_000)
	if w == nil || w.Kind != "logs" || w.Title != "A log keeps fewer entries than set" ||
		w.Detail != "The query log keeps 1,888,888 of the 5,000,000 set. This router has memory for that many." {
		t.Errorf("warning = %+v", w)
	}
	cfg.System.Management.FirewallLog.Entries = 1_000_000
	cfg.Services.Proxy = model.Proxy{Enabled: true, Sites: []model.ProxySite{{ID: "shop", Enabled: true}}, Events: model.ProxyEvents{Entries: 500_000}}
	if w := find(950_000_000); w == nil || w.Detail != "The firewall log keeps 577,142 of the 1,000,000 set. The query log keeps 1,346,666 of the 5,000,000 set. "+
		"The WAF events keep 131,510 of the 500,000 set. This router has memory for that many." {
		t.Errorf("warning = %+v", w)
	}
	// A log that is off keeps nothing, whatever it is set to.
	cfg.Services.DNS.QueryLog.Enabled = false
	cfg.System.Management.FirewallLog.Entries = 0
	cfg.Services.Proxy = model.Proxy{}
	if w := find(950_000_000); w != nil {
		t.Errorf("a log that is off warned: %+v", w)
	}
	if w := find(0); w != nil {
		t.Errorf("unknown memory warned: %+v", w)
	}
}

func warningKeyed(ws []Warning, key string) *Warning {
	for i := range ws {
		if ws[i].Key == key {
			return &ws[i]
		}
	}
	return nil
}

func TestGroupDigits(t *testing.T) {
	t.Parallel()
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 480_000: "480,000", 1_888_888: "1,888,888", -12_345: "-12,345"} {
		if got := groupDigits(n); got != want {
			t.Errorf("groupDigits(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	for n, want := range map[float64]string{0: "0 B", 999: "999 B", 1536: "1.5 kB", 150_000: "150 kB", 2_500_000_000: "2.5 GB"} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%.0f) = %q, want %q", n, got, want)
		}
	}
}

func TestWarnsWhenTheLogsWereNotReadBack(t *testing.T) {
	t.Parallel()
	cfg := starter()
	a := &api{readBackSkipped: true}
	w := warningKeyed(a.warnings(context.Background(), cfg, engine.Status{}, nil, nil, install.UnitStates{}), "logs-skipped")
	if w == nil || w.Kind != "logs" || w.Title != "The logs were not read back" {
		t.Errorf("warning = %+v", w)
	}
	a = &api{}
	if w := warningKeyed(a.warnings(context.Background(), cfg, engine.Status{}, nil, nil, install.UnitStates{}), "logs-skipped"); w != nil {
		t.Errorf("warned without a skipped read-back: %+v", w)
	}
}
