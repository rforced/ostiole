package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/modem"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
)

// The modem page reads through the cache: a bad address is the caller's
// mistake, a modem that does not answer is reported as beyond the router,
// and a status comes back as the modem gave it.
func TestDiagModem(t *testing.T) {
	t.Parallel()
	var reads []string
	fake := modem.NewCacheWith(time.Minute, func(_ context.Context, address string) (*modem.Status, error) {
		reads = append(reads, address)
		if address == "192.168.100.2" {
			return nil, modem.ErrNoModem
		}
		return &modem.Status{Address: address, Vendor: "Hitron", Model: "CODA", FetchedAt: time.Now()}, nil
	})
	srv := newTestServerWith(t, func(d *Deps) { d.Modems = fake })

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem?address=8.8.8.8", nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "192.168.100.0/24") {
		t.Errorf("public address: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem?address=192.168.1.1", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("address outside the modem subnet: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem?address=192.168.100.2", nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "no modem") {
		t.Errorf("silent modem: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default address: %d %s", resp.StatusCode, raw)
	}
	var st modem.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Address != modem.DefaultAddress || st.Model != "CODA" {
		t.Errorf("status = %+v", st)
	}
	// Read again within the minute: the cache answers, the modem is not asked.
	do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem", nil)
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/diagnostics/modem?refresh=1", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("refresh: %d", resp.StatusCode)
	}
	if want := []string{"192.168.100.2", modem.DefaultAddress, modem.DefaultAddress}; strings.Join(reads, ",") != strings.Join(want, ",") {
		t.Errorf("modem reads = %v, want %v", reads, want)
	}
}

// A viewer reads the journal of the units Ostiole runs, not the whole host:
// the rest has sshd's record of who tried to get in. An operator reads it
// all.
func TestJournalKeepsAViewerToOstiolesUnits(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var last diag.JournalOptions
	srv := newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens = tokens
		d.Journal = func(_ context.Context, o diag.JournalOptions) ([]diag.JournalEntry, error) {
			mu.Lock()
			last = o
			mu.Unlock()
			return []diag.JournalEntry{}, nil
		}
	})
	viewer := mintToken(t, srv, "look", string(auth.RoleViewer))
	operator := mintToken(t, srv, "change", string(auth.RoleOperator))
	for _, tc := range []struct {
		token, unit string
		status      int
		own         bool
	}{
		{viewer, "ostiole.service", http.StatusOK, true},
		{viewer, "ostiole-dnsmasq.service", http.StatusOK, true},
		{viewer, "", http.StatusOK, true},
		{viewer, "sshd.service", http.StatusForbidden, false},
		{operator, "sshd.service", http.StatusOK, false},
		{operator, "", http.StatusOK, false},
	} {
		mu.Lock()
		last = diag.JournalOptions{}
		mu.Unlock()
		resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/diagnostics/journal?unit="+tc.unit, tc.token)
		if resp.StatusCode != tc.status {
			t.Errorf("unit %q: %d %s, want %d", tc.unit, resp.StatusCode, raw, tc.status)
			continue
		}
		mu.Lock()
		got := last
		mu.Unlock()
		if tc.status == http.StatusOK && (got.Own != tc.own || got.Unit != tc.unit) {
			t.Errorf("unit %q read as %+v, want own %v", tc.unit, got, tc.own)
		}
	}
}

// newTestServerWith is newTestServer with the dependencies adjusted first.
func newTestServerWith(t *testing.T, adjust func(*Deps)) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Engine: eng, Auth: as}
	adjust(&deps)
	srv := httptest.NewServer(Handler(deps))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

// The overrides tab asks for the names the draft makes the router answer
// on its own, the way the rules page asks for the system rules.
func TestSystemHosts(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	cfg := starter()
	cfg.Services.DNS.Domain = "lan"
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{{MAC: "aa:bb:cc:00:00:01", IP: "10.0.0.20", Hostname: "calcifer"}}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/dns/system-hosts", configRequest{Config: (*draftConfig)(cfg)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var rows []services.SystemHost
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FQDN != "calcifer.lan" || rows[0].Setting != "dhcp" {
		t.Errorf("rows = %+v", rows)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/dns/system-hosts", configRequest{}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no config: %d, want 400", resp.StatusCode)
	}
}

// A network that blocks port 853 makes DNS over TLS fail with nothing in
// the resolver's state to say so. The status strip probes the upstreams
// and names the ones that do not answer, once a minute, and only when
// that is the resolver in use.
func TestServicesStatusNamesUnreachableTLSUpstreams(t *testing.T) {
	t.Parallel()
	// The upstreams are dialled at the same time, so what was asked for is
	// written from several goroutines and read from the test's own.
	var mu sync.Mutex
	var dials []string
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(dials)
	}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Dial = func(_ context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, address)
			mu.Unlock()
			if strings.HasPrefix(address, "9.9.9.9:") {
				return nil, errors.New("connection timed out")
			}
			a, b := net.Pipe()
			go b.Close()
			return a, nil
		}
	})
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	status := func() servicesStatus {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d %s", resp.StatusCode, raw)
		}
		var st servicesStatus
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := status(); st.ResolverUnreachable != nil || len(seen()) != 0 {
		t.Errorf("forwarding resolver: unreachable = %v, dials = %v", st.ResolverUnreachable, seen())
	}

	cfg.Services.DNS.Resolver = model.ResolverTLS
	cfg.Services.DNS.TLSUpstreams = []model.TLSUpstream{
		{Address: "1.1.1.1", Hostname: "cloudflare-dns.com"},
		{Address: "9.9.9.9", Hostname: "dns.quad9.net"},
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply tls: %d %s", resp.StatusCode, raw)
	}
	if st := status(); !reflect.DeepEqual(st.ResolverUnreachable, []string{"9.9.9.9"}) {
		t.Errorf("unreachable = %v, want [9.9.9.9]", st.ResolverUnreachable)
	}
	got := seen()
	sort.Strings(got)
	if want := []string{"1.1.1.1:853", "9.9.9.9:853"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dials = %v, want %v", got, want)
	}
	// Within the minute the answer is remembered, not probed again.
	status()
	if got := seen(); len(got) != 2 {
		t.Errorf("dials after a second read = %v", got)
	}
}
