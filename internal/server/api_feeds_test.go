package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/dnsblock"
	"ostiole/internal/engine"
	"ostiole/internal/feeds"
	"ostiole/internal/fetch"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
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
		Fetcher:  &feeds.Fetcher{Getter: fetch.Inside(), Timeout: feeds.DefaultTimeout},
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
	if got, want := strings.TrimSpace(string(raw)), `{"source":"`+lists.URL+`/ranges.json","entries":3}`; got != want {
		t.Errorf("JSON list = %s, want %s", got, want)
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/aliases/inspect", map[string]string{"url": lists.URL + "/drop.txt"})
	if got, want := strings.TrimSpace(string(raw)), `{"source":"`+lists.URL+`/drop.txt","entries":2}`; resp.StatusCode != http.StatusOK || got != want {
		t.Errorf("text list: %d %s, want %s", resp.StatusCode, got, want)
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
	cfg.Aliases = []model.Alias{{Name: "cloud", Type: model.AliasHosts, Entries: []string{lists.URL + "/drop.txt", "192.0.2.1", source}}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	login(t, srv, "hand")
	if resp, raw := inspect(); resp.StatusCode != http.StatusOK {
		t.Errorf("a URL the router fetches: %d %s", resp.StatusCode, raw)
	}
}

// A list's URL may carry the key to it. A viewer reads it without, in the
// configuration and in both kinds of list's status; an operator reads it
// as written.
func TestAViewerReadsListURLsWithoutTheirKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := withTokens(t, func(d *Deps) {
		d.Feeds = &feeds.Refresher{Cache: feeds.NewCache(filepath.Join(dir, "feeds")), Fetcher: &feeds.Fetcher{Getter: fetch.Inside()}}
		d.Blocklists = &dnsblock.Refresher{Cache: dnsblock.NewCache(filepath.Join(dir, "blocklists")), Fetcher: &dnsblock.Fetcher{Getter: fetch.Inside()}}
	})
	cfg := model.Starter(model.StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0",
		Services: true, DNSUpstreams: []string{"192.0.2.53"}})
	cfg.Aliases = []model.Alias{{Name: "partners", Type: model.AliasHosts, Entries: []string{"192.0.2.1", "https://lists.example.com/drop.txt?token=list-key-1"}}}
	cfg.Blocking.Enabled = true
	cfg.Blocking.Lists = []model.BlockList{{Name: "private", Enabled: true, URL: "https://bob:list-pass-2@lists.example.com/block.txt"}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	viewer := mintToken(t, srv, "dashboard", "viewer")
	operator := mintToken(t, srv, "automation", "operator")
	for path, keys := range map[string][]string{
		"/api/v1/config":        {"list-key-1", "list-pass-2"},
		"/api/v1/aliases/feeds": {"list-key-1"},
		"/api/v1/blocking":      {"list-pass-2"},
	} {
		resp, raw := withToken(t, srv, http.MethodGet, path, viewer)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer %s: %d %s", path, resp.StatusCode, raw)
		}
		for _, key := range append(keys, "bob") {
			if strings.Contains(string(raw), key) {
				t.Errorf("a viewer reads %q in %s", key, path)
			}
		}
		if !strings.Contains(string(raw), "lists.example.com/") {
			t.Errorf("%s lost more than the keys: %s", path, raw)
		}
		_, raw = withToken(t, srv, http.MethodGet, path, operator)
		for _, key := range keys {
			if !strings.Contains(string(raw), key) {
				t.Errorf("an operator reads %s without %q", path, key)
			}
		}
	}
}

// An apply or a revert has the refresher look at once, so an alias whose
// URLs changed does not keep the old list until its next tick.
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

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(starter())}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	wait("the first apply")

	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "cloud", Type: model.AliasHosts, Entries: []string{lists.URL + "/ranges.json"}}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg), ConfirmTimeoutSeconds: 60}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply with the alias: %d %s", resp.StatusCode, raw)
	}
	wait("the alias was added")
	if hits.Load() != 1 {
		t.Fatalf("the list was fetched %d times, want once", hits.Load())
	}
	_, raw := do(t, srv, http.MethodGet, "/api/v1/aliases/feeds", nil)
	var st []feeds.Status
	if err := json.Unmarshal(raw, &st); err != nil || len(st) != 1 || st[0].Entries != 3 || st[0].Stale || len(st[0].Parts) != 1 {
		t.Fatalf("feeds = %s (%v)", raw, err)
	}

	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply/revert", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revert: %d", resp.StatusCode)
	}
	wait("the revert")
}

// What an alias fetched can be read a page at a time, and searched for an
// address, which the aliases page otherwise shows only as a count.
func TestAFetchedAliasCanBeRead(t *testing.T) {
	t.Parallel()
	lists, _ := listServer(t)
	srv, passes := newFeedServer(t)
	cfg := starter()
	cfg.Aliases = []model.Alias{{Name: "drop", Type: model.AliasHosts, Entries: []string{lists.URL + "/drop.txt"}}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("the alias was never fetched")
	}
	read := func(query string) feeds.Page {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/aliases/drop/entries"+query, nil)
		var p feeds.Page
		if err := json.Unmarshal(raw, &p); resp.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("read %q: %d %s", query, resp.StatusCode, raw)
		}
		return p
	}
	if p := read(""); !slices.Equal(p.Entries, []string{"192.0.2.0/24", "198.51.100.7"}) || p.Total != 2 || p.FetchedAt.IsZero() {
		t.Errorf("everything = %+v", p)
	}
	if p := read("?q=192.0.2.9"); !slices.Equal(p.Entries, []string{"192.0.2.0/24"}) || p.Matches != 1 || p.Total != 2 {
		t.Errorf("an address = %+v", p)
	}
	if p := read("?offset=1&limit=1"); !slices.Equal(p.Entries, []string{"198.51.100.7"}) || p.Matches != 2 {
		t.Errorf("the second page = %+v", p)
	}
	for query, want := range map[string]int{
		"/api/v1/aliases/drop/entries?limit=0":     http.StatusBadRequest,
		"/api/v1/aliases/drop/entries?limit=1001":  http.StatusBadRequest,
		"/api/v1/aliases/drop/entries?offset=-1":   http.StatusBadRequest,
		"/api/v1/aliases/drop/entries?offset=next": http.StatusBadRequest,
		"/api/v1/aliases/nothing/entries":          http.StatusNotFound,
	} {
		if resp, raw := do(t, srv, http.MethodGet, query, nil); resp.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", query, resp.StatusCode, raw, want)
		}
	}
}
