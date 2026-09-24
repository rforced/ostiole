package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// newFeedServer is newTestServer with an alias refresher running, as the
// daemon has one, and a signal for each pass it makes.
func newFeedServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	passes := make(chan struct{}, 8)
	refresher := &feeds.Refresher{
		Cache:    feeds.NewCache(filepath.Join(dir, "feeds")),
		Fetcher:  feeds.NewFetcher("test"),
		Source:   eng.Effective,
		Log:      slog.New(slog.DiscardHandler),
		Interval: time.Hour,
		OnTick: func() {
			select {
			case passes <- struct{}{}:
			default:
			}
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go refresher.Run(ctx)

	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Feeds: refresher}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, passes
}

// listServer stands in for a publisher: a JSON document with two regions,
// and a plain list.
func listServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/ranges.json":
			_, _ = w.Write([]byte(`{"regions": [{"region": "a", "ips": ["192.0.2.0/24"]},
				{"region": "b", "ips": ["198.51.100.0/24", "2001:db8::/32"]}]}`))
		case "/drop.txt":
			_, _ = w.Write([]byte("192.0.2.0/24 ; SBL1\n198.51.100.7\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestInspectReadsAList(t *testing.T) {
	t.Parallel()
	lists, _ := listServer(t)
	srv, _ := newFeedServer(t)

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": lists.URL + "/ranges.json"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect: %d %s", resp.StatusCode, raw)
	}
	var part feeds.Part
	if err := json.Unmarshal(raw, &part); err != nil {
		t.Fatal(err)
	}
	if part.Entries != 3 || len(part.Choices) != 1 || part.Choices[0].Field != "region" ||
		strings.Join(part.Choices[0].Values, ",") != "a,b" {
		t.Errorf("JSON list = %s", raw)
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": lists.URL + "/drop.txt"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"entries":2`) || strings.Contains(string(raw), "choices") {
		t.Errorf("text list: %d %s", resp.StatusCode, raw)
	}

	for _, u := range []string{"ftp://example.test/list", "not a url", lists.URL + "/gone"} {
		if resp, raw := do(t, srv, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": u}); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", u, resp.StatusCode, raw)
		}
	}

	plain, _ := newTestServer(t)
	if resp, _ := do(t, plain, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": lists.URL + "/drop.txt"}); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("without a refresher: %d, want 503", resp.StatusCode)
	}
}

// An operator's URL is read from the internet only, and the list server
// here is on the loopback. Once the router fetches it, they may read it.
func TestInspectKeepsAnOperatorToPublicAddresses(t *testing.T) {
	t.Parallel()
	lists, hits := listServer(t)
	srv, _ := newFeedServer(t)
	source := lists.URL + "/ranges.json"
	inspect := func() (*http.Response, []byte) {
		t.Helper()
		return do(t, srv, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": source})
	}

	user := map[string]string{"username": "hand", "password": testPassword, "role": "operator"}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/users", user); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create the operator: %d %s", resp.StatusCode, raw)
	}
	login(t, srv, "hand")
	if resp, raw := inspect(); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "not a public address") {
		t.Errorf("an operator's loopback URL: %d %s", resp.StatusCode, raw)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the list was fetched %d times, want none", n)
	}

	login(t, srv, "admin")
	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "cloud", Type: model.AliasHosts, URL: source}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	login(t, srv, "hand")
	if resp, raw := inspect(); resp.StatusCode != http.StatusOK {
		t.Errorf("a URL the router fetches: %d %s", resp.StatusCode, raw)
	}
}

// An apply or a revert has the refresher look at once, so an alias whose
// URL or selection changed does not keep the old list until its next tick.
func TestApplyAndRevertWakeTheRefresher(t *testing.T) {
	t.Parallel()
	lists, hits := listServer(t)
	srv, passes := newFeedServer(t)
	wait := func(what string) {
		t.Helper()
		select {
		case <-passes:
		case <-time.After(5 * time.Second):
			t.Fatalf("no pass after %s", what)
		}
	}

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: starter()}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	wait("the first apply")

	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "cloud", Type: model.AliasHosts, URL: lists.URL + "/ranges.json", Select: []string{"region=b"}}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: cfg, ConfirmTimeoutSeconds: 60}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply with the alias: %d %s", resp.StatusCode, raw)
	}
	wait("the alias was added")
	if hits.Load() != 1 {
		t.Fatalf("the list was fetched %d times, want once", hits.Load())
	}
	_, raw := do(t, srv, http.MethodGet, "/api/v1/aliases/feeds", nil)
	var st []feeds.Status
	if err := json.Unmarshal(raw, &st); err != nil || len(st) != 1 || st[0].Entries != 2 || st[0].Stale || len(st[0].Parts[0].Choices) != 1 {
		t.Fatalf("feeds = %s (%v)", raw, err)
	}

	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply/revert", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revert: %d", resp.StatusCode)
	}
	wait("the revert")
}
