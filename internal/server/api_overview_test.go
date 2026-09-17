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

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// fakeTables stands in for the nft binary's table listing.
type fakeTables struct{ tables []string }

func (f fakeTables) ListTables(context.Context) ([]string, error) { return f.tables, nil }

// fakeUnits answers `systemctl is-active|is-enabled <unit>`; units that are
// absent from the map report not-found, as systemctl does.
type fakeUnits struct{ state map[string][2]string } // unit -> {active, enabled}

func (f fakeUnits) Run(_ context.Context, args ...string) (string, error) {
	if len(args) != 2 {
		return "", errors.New("unexpected systemctl call")
	}
	st, ok := f.state[args[1]]
	if !ok {
		return "not-found", errors.New("exit status 4")
	}
	switch args[0] {
	case "is-active":
		return st[0], nil
	case "is-enabled":
		return st[1], nil
	}
	return "", errors.New("unexpected systemctl verb")
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
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
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

func TestOverviewNeedsSession(t *testing.T) {
	t.Parallel()
	srv, _ := newUnauthenticatedServer(t)
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/overview", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("overview without a session: %d, want 401", resp.StatusCode)
	}
}

func TestSummarizeLinksPrefersKernelState(t *testing.T) {
	t.Parallel()
	cfg := model.Starter(model.StarterOptions{LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0"})
	links := []network.Link{
		{Name: "lo", Kind: "loopback"},
		{Name: "eth1", Kind: "ethernet", Up: true, Carrier: true, MTU: 1500, Addresses: []string{"10.0.0.1/24"}, RXBytes: 5},
		{Name: "wg0", Kind: "wireguard", Up: true},
	}
	got := summarizeLinks(cfg, links)
	if len(got) != 3 {
		t.Fatalf("summaries = %+v, want lan, wan, and the unconfigured wg0", got)
	}
	if got[0].Name != "eth1" || !got[0].Present || !got[0].Carrier || got[0].RXBytes != 5 {
		t.Errorf("eth1 = %+v", got[0])
	}
	if got[1].Name != "eth0" || got[1].Present {
		t.Errorf("eth0 = %+v, want configured but absent", got[1])
	}
	if got[2].Name != "wg0" || got[2].Configured {
		t.Errorf("wg0 = %+v, want live but unconfigured", got[2])
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
