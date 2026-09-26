package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// renewingNet is a network backend that records the leases it was asked
// to renew, as "link" or "link!" when the lease was to be released first.
type renewingNet struct {
	network.Networkd
	renews []string
}

func (r *renewingNet) Apply(context.Context, network.Files) error { return nil }
func (r *renewingNet) Snapshot() (network.Files, error)           { return network.Files{}, nil }
func (r *renewingNet) Renew(_ context.Context, link string, release bool) error {
	if release {
		link += "!"
	}
	r.renews = append(r.renews, link)
	return nil
}

func newRenewServer(t *testing.T) (*httptest.Server, *renewingNet) {
	t.Helper()
	dir := t.TempDir()
	net := &renewingNet{LinkExists: func(string) bool { return true }}
	eng := engine.New(store.New(dir), &nfttest.Fake{}, net, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, net
}

// The WAN holds a lease, so it can be renewed or released; the LAN is
// static and a name that is not configured is not a link to touch.
func TestRenewLease(t *testing.T) {
	t.Parallel()
	srv, net := newRenewServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	cases := []struct {
		name string
		body any
		want int
	}{
		{"eth0", nil, http.StatusNoContent},
		{"eth0", renewRequest{Release: true}, http.StatusNoContent},
		{"eth1", nil, http.StatusUnprocessableEntity},
		{"nope", nil, http.StatusNotFound},
		{"eth0", map[string]any{"release": "soon"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		resp, raw := do(t, srv, http.MethodPost, "/api/v1/interfaces/"+c.name+"/renew", c.body)
		if resp.StatusCode != c.want {
			t.Errorf("%s %v: %d %s, want %d", c.name, c.body, resp.StatusCode, raw, c.want)
		}
	}
	if want := []string{"eth0", "eth0!"}; !reflect.DeepEqual(net.renews, want) {
		t.Errorf("renews = %v, want %v", net.renews, want)
	}
}

// A dev run has no network backend, and says so rather than pretending.
func TestRenewLeaseWithoutANetworkBackend(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/interfaces/eth0/renew", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
