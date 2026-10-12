package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/dnsblock"
	"ostiole/internal/engine"
	"ostiole/internal/fetch"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// newBlockingServer is newTestServer with a blocklist refresher and lists
// applied.
func newBlockingServer(t *testing.T, lists ...model.BlockList) (*httptest.Server, *dnsblock.Cache) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	cache := dnsblock.NewCache(filepath.Join(dir, "dnsblock"))
	refresher := &dnsblock.Refresher{Cache: cache, Fetcher: dnsblock.NewFetcher(fetch.Inside()), Source: eng.Effective,
		Log: slog.New(slog.DiscardHandler)}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Blocklists: refresher}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	cfg := starter()
	cfg.Blocking.Lists = lists
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	return srv, cache
}

// The names a list holds can be read a page at a time, and a search for a
// name finds the entry that blocks it.
func TestABlocklistsNamesCanBeRead(t *testing.T) {
	t.Parallel()
	srv, cache := newBlockingServer(t, model.BlockList{Name: "ads", Enabled: true}, model.BlockList{Name: "empty", Enabled: true})
	if err := cache.Save(dnsblock.Meta{Name: "ads", FetchedAt: time.Now()}, []string{"example.com", "tracker.net", "ads.example.org"}); err != nil {
		t.Fatal(err)
	}

	read := func(path string) dnsblock.Page {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, path, nil)
		var p dnsblock.Page
		if err := json.Unmarshal(raw, &p); resp.StatusCode != http.StatusOK || err != nil {
			t.Fatalf("read %s: %d %s", path, resp.StatusCode, raw)
		}
		return p
	}
	if p := read("/api/v1/blocking/lists/ads/names"); !slices.Equal(p.Names, []string{"example.com", "tracker.net", "ads.example.org"}) || p.Total != 3 {
		t.Errorf("everything = %+v", p)
	}
	if p := read("/api/v1/blocking/lists/ads/names?q=cdn.example.com"); !slices.Equal(p.Names, []string{"example.com"}) || p.Matches != 1 {
		t.Errorf("a name under an entry = %+v", p)
	}
	if p := read("/api/v1/blocking/lists/ads/names?offset=2&limit=5"); !slices.Equal(p.Names, []string{"ads.example.org"}) || p.Matches != 3 {
		t.Errorf("the last page = %+v", p)
	}
	if p := read("/api/v1/blocking/lists/empty/names"); p.Names == nil || len(p.Names) != 0 || p.Total != 0 {
		t.Errorf("a list never fetched = %+v", p)
	}
	for path, want := range map[string]int{
		"/api/v1/blocking/lists/ads/names?limit=0": http.StatusBadRequest,
		"/api/v1/blocking/lists/gone/names":        http.StatusNotFound,
	} {
		if resp, raw := do(t, srv, http.MethodGet, path, nil); resp.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, raw, want)
		}
	}
}

// A refresh the publisher fails is a 502 and is logged as a scheduled one
// is. Not parallel: it swaps the default logger.
func TestABlocklistRefreshThePublisherFailsIsLogged(t *testing.T) {
	var buf safeBuffer
	was := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(was) })
	lists := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(lists.Close)
	srv, _ := newBlockingServer(t, model.BlockList{Name: "ads", Enabled: true, URL: lists.URL + "/ads.txt"},
		model.BlockList{Name: "house", Enabled: true})

	for path, want := range map[string]int{
		"/api/v1/blocking/lists/ads/refresh":   http.StatusBadGateway,
		"/api/v1/blocking/lists/house/refresh": http.StatusBadRequest,
		"/api/v1/blocking/lists/gone/refresh":  http.StatusNotFound,
	} {
		if resp, raw := do(t, srv, http.MethodPost, path, nil); resp.StatusCode != want {
			t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, raw, want)
		}
	}
	if got := buf.String(); !strings.Contains(got, `msg="could not refresh a blocklist" list=ads`) || strings.Count(got, "could not refresh") != 1 {
		t.Errorf("the log holds:\n%s", got)
	}
}
