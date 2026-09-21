package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
)

// unitCmd answers systemctl for a unit that is there and running, and
// records what it was asked to do.
type unitCmd struct {
	mu        sync.Mutex
	calls     []string
	missing   bool
	reloadErr bool
}

func (u *unitCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	u.mu.Lock()
	u.calls = append(u.calls, strings.Join(append([]string{name}, args...), " "))
	u.mu.Unlock()
	switch args[0] {
	case "cat":
		if u.missing {
			return nil, errors.New("no such unit")
		}
		return []byte("[Unit]"), nil
	case "is-active":
		return []byte("active\n"), nil
	case "reload":
		if u.reloadErr {
			return []byte("Job failed"), errors.New("exit 1")
		}
	}
	return nil, nil
}

func (u *unitCmd) did(call string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range u.calls {
		if c == call {
			return true
		}
	}
	return false
}

// dnsCacheServer applies cfg to a server wired to the two fakes.
func dnsCacheServer(t *testing.T, cfg *model.Config) (*httptest.Server, *unitCmd, *unitCmd) {
	t.Helper()
	dc, uc := &unitCmd{}, &unitCmd{}
	srv := newTestServerWith(t, func(d *Deps) {
		d.Services = &services.Dnsmasq{Cmd: dc}
		d.Resolver = &services.Unbound{Cmd: uc}
	})
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	return srv, dc, uc
}

func clearCache(t *testing.T, srv *httptest.Server) (*http.Response, []string) {
	t.Helper()
	resp, raw := do(t, srv, http.MethodDelete, "/api/v1/dns/cache", nil)
	var body struct {
		Cleared []string `json:"cleared"`
	}
	_ = json.Unmarshal(raw, &body)
	return resp, body.Cleared
}

// Forwarding, so dnsmasq holds every cached answer and unbound is not
// running at all.
func TestClearDNSCacheForwardingHUPsDnsmasqOnly(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	srv, dc, uc := dnsCacheServer(t, cfg)

	resp, cleared := clearCache(t, srv)
	if resp.StatusCode != http.StatusOK || len(cleared) != 1 || cleared[0] != "dnsmasq" {
		t.Fatalf("%d cleared = %v", resp.StatusCode, cleared)
	}
	if !dc.did("systemctl reload " + services.Unit) {
		t.Errorf("dnsmasq calls = %v", dc.calls)
	}
	if uc.did("systemctl reload " + services.UnboundUnit) {
		t.Errorf("reloaded a resolver that is not in use: %v", uc.calls)
	}
}

// Validating, so both caches hold answers and both are cleared.
func TestClearDNSCacheValidatingHUPsBoth(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Resolver = model.ResolverValidate
	srv, dc, uc := dnsCacheServer(t, cfg)

	resp, cleared := clearCache(t, srv)
	if resp.StatusCode != http.StatusOK || len(cleared) != 2 {
		t.Fatalf("%d cleared = %v", resp.StatusCode, cleared)
	}
	if !dc.did("systemctl reload "+services.Unit) || !uc.did("systemctl reload "+services.UnboundUnit) {
		t.Errorf("calls = %v %v", dc.calls, uc.calls)
	}
}

// A cache nothing is filling cannot be cleared, and saying so beats a
// success that did nothing.
func TestClearDNSCacheRefusesWhenOff(t *testing.T) {
	t.Parallel()
	srv, _, _ := dnsCacheServer(t, starter())
	if resp, _ := clearCache(t, srv); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("DNS off: %d, want 400", resp.StatusCode)
	}
}

// Without the unit, or without a daemon that can reach it, the page is
// told to repair rather than left with a button that fails.
func TestClearDNSCacheNeedsTheUnit(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}

	srv, dc, _ := dnsCacheServer(t, cfg)
	dc.missing = true
	if resp, _ := clearCache(t, srv); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("not installed: %d, want 503", resp.StatusCode)
	}

	noRoot := newTestServerWith(t, func(*Deps) {})
	if resp, raw := do(t, noRoot, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := clearCache(t, noRoot); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("no services backend: %d, want 503", resp.StatusCode)
	}
}

// A reload the unit refuses is reported, not swallowed.
func TestClearDNSCacheReportsAFailedReload(t *testing.T) {
	t.Parallel()
	cfg := starter()
	cfg.Services.DNS.Enabled = true
	cfg.Services.DNS.Upstreams = []string{"1.1.1.1"}
	srv, dc, _ := dnsCacheServer(t, cfg)
	dc.reloadErr = true
	if resp, _ := clearCache(t, srv); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("failed reload: %d, want 500", resp.StatusCode)
	}
}
