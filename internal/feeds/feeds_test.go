package feeds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

func config(aliases ...model.Alias) *model.Config {
	return &model.Config{
		Version:    model.SchemaVersion,
		Zones:      []model.Zone{{Name: "wan", External: true}},
		Interfaces: []model.Interface{{Name: "eth0", Zone: "wan", Enabled: true, IPv4: model.IPv4{Mode: model.AddrDHCP}, IPv6: model.IPv6{Mode: model.AddrNone}}},
		Aliases:    aliases,
		NAT:        model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
}

// Real lists are untidy: comments in three styles, trailing notes, blank
// lines, duplicates, and the odd line that is not an address at all.
func TestParseRealWorldLists(t *testing.T) {
	t.Parallel()
	body := `
# Spamhaus DROP List
192.0.2.0/24 ; SBL123
198.51.100.0/24 ; SBL456

203.0.113.5
203.0.113.5
// a comment in another style
2001:db8::/32
not-an-address
10.0.0.1, 10.0.0.2
`
	got, err := Parse(body, model.AliasHosts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.5", "2001:db8::/32", "10.0.0.1", "10.0.0.2"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Parse = %v, want %v", got, want)
	}
}

// A default route covers every address, so one in a list would turn an
// allow rule over the alias into allow-all. It is dropped however it is
// written, from a list, from RIPEstat, and from a cache written before.
func TestDefaultRoutesAreDropped(t *testing.T) {
	t.Parallel()
	got, err := Parse("192.0.2.0/24\n0.0.0.0/0\n10.1.2.3/0\n::/0\n2001:db8::/32\n", model.AliasHosts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "192.0.2.0/24,2001:db8::/32" {
		t.Errorf("Parse = %v", got)
	}
	if _, err := Parse("0.0.0.0/0\n", model.AliasHosts); err == nil {
		t.Error("a list holding nothing but a default route was accepted")
	}

	announced, err := parseAnnounced([]byte(`{"status": "ok", "data": {"prefixes": [
		{"prefix": "0.0.0.0/0"}, {"prefix": "8.8.8.0/24"}, {"prefix": "::/0"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(announced, ",") != "8.8.8.0/24" {
		t.Errorf("parseAnnounced = %v", announced)
	}

	dir := t.TempDir()
	old := `{"alias": "drop", "fetchedAt": "2026-09-01T00:00:00Z", "entries": ["0.0.0.0/0", "192.0.2.0/24", "::/0"]}`
	if err := os.WriteFile(filepath.Join(dir, "drop.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewCache(dir).Entries()["drop"]; strings.Join(got, ",") != "192.0.2.0/24" {
		t.Errorf("cached entries = %v", got)
	}
}

func TestParseRejectsUnusableLists(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"empty":     "",
		"comments":  "# nothing here\n; nor here\n",
		"html":      "<html><body>404 not found</body></html>",
		"not ports": "hello\nworld\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(body, model.AliasHosts); err == nil {
				t.Error("an unusable list was accepted")
			}
		})
	}
	if _, err := Parse("80\n443\n8000-8100\n", model.AliasPorts); err != nil {
		t.Errorf("a port list was rejected: %v", err)
	}
}

func TestFetchAndCache(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("192.0.2.0/24 ; SBL1\n198.51.100.7\n"))
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "drop", Type: model.AliasHosts, URL: srv.URL})
	cache := NewCache(t.TempDir())
	entries, sources, err := NewFetcher("test").Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(sources) != 1 {
		t.Fatalf("entries = %v, sources = %v", entries, sources)
	}
	if err := cache.Save(cfg.Aliases[0], sources, entries, time.Now()); err != nil {
		t.Fatal(err)
	}

	// A new cache over the same directory has it, which is what makes a
	// router that boots without a working line still block what it blocked.
	reopened := NewCache(cache.Dir)
	if got := reopened.Entries()["drop"]; len(got) != 2 {
		t.Errorf("reopened cache = %v", got)
	}
	if hits != 1 {
		t.Errorf("fetched %d times", hits)
	}
}

// One source failing fails the alias: half a blocklist is worse than
// yesterday's whole one.
func TestFetchFailsWholesale(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "ru") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: []string{"cn", "ru"}})
	cfg.System.GeoIPv4URL = srv.URL + "/{country}.zone"
	cfg.System.GeoIPv6URL = ""

	_, _, err := NewFetcher("test").Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err == nil {
		t.Fatal("a failing source was ignored")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v", err)
	}
}

func TestGeoIPSourcesPerCountryAndFamily(t *testing.T) {
	t.Parallel()
	cfg := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: []string{"DE", "fr"}})
	got := Sources(cfg, cfg.Aliases[0])
	if len(got) != 4 {
		t.Fatalf("sources = %v, want one per country and family", got)
	}
	for _, want := range []string{"/de-", "/fr-"} {
		found := false
		for _, s := range got {
			if strings.Contains(s, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no source contains %q: %v", want, got)
		}
	}
}

func TestRefreshOnlyWhenDue(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "drop", Type: model.AliasHosts, URL: srv.URL, RefreshHours: 12})
	r := &Refresher{
		Cache:   NewCache(t.TempDir()),
		Fetcher: NewFetcher("test"),
		Source:  func() *model.Config { return cfg },
		Log:     slog.New(slog.DiscardHandler),
	}
	r.Tick(context.Background(), false)
	r.Tick(context.Background(), false)
	if hits != 1 {
		t.Errorf("fetched %d times, want once: the second tick was not due", hits)
	}
	// "Refresh now" ignores the schedule.
	r.Tick(context.Background(), true)
	if hits != 2 {
		t.Errorf("a forced refresh did not fetch: %d", hits)
	}
}

// A list fetched for an alias as it was is not the list it asks for now: a
// new URL or selection is fetched on the next pass, not a refresh period
// later, and until then the page calls the old list stale.
func TestChangedAliasRefetches(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"regions": [{"region": "a", "ips": ["192.0.2.0/24"]}, {"region": "b", "ips": ["198.51.100.0/24"]}]}`))
	}))
	defer srv.Close()
	count := func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return hits[path]
	}

	cfg := config(model.Alias{Name: "list", Type: model.AliasHosts, URL: srv.URL + "/one.json"})
	r := &Refresher{
		Cache:   NewCache(t.TempDir()),
		Fetcher: NewFetcher("test"),
		Source:  func() *model.Config { return cfg },
		Log:     slog.New(slog.DiscardHandler),
	}
	ctx := context.Background()
	r.Tick(ctx, false)
	r.Tick(ctx, false)
	if count("/one.json") != 1 {
		t.Fatalf("hits = %v, want one fetch while nothing changed", hits)
	}

	cfg.Aliases[0].URL = srv.URL + "/two.json"
	if st := r.Cache.Statuses(cfg); !st[0].Stale {
		t.Errorf("a list from the old URL is not stale: %+v", st[0])
	}
	r.Tick(ctx, false)
	if count("/two.json") != 1 {
		t.Fatalf("hits = %v, want the new URL fetched at once", hits)
	}

	cfg.Aliases[0].Select = []string{"region=b"}
	r.Tick(ctx, false)
	if count("/two.json") != 2 {
		t.Fatalf("hits = %v, want a changed selection fetched at once", hits)
	}
	if got := r.Cache.Entries()["list"]; strings.Join(got, ",") != "198.51.100.0/24" {
		t.Errorf("entries = %v", got)
	}
	r.Tick(ctx, false)
	if count("/two.json") != 2 {
		t.Errorf("hits = %v, want nothing more once the cache matches", hits)
	}
	if st := r.Cache.Statuses(cfg); st[0].Stale {
		t.Errorf("stale after the refetch: %+v", st[0])
	}

	// A country added to a country list is a new source too.
	geo := config(model.Alias{Name: "geo", Type: model.AliasGeoIP, Entries: []string{"de"}})
	geo.System.GeoIPv4URL = srv.URL + "/{country}.zone"
	r.Source = func() *model.Config { return geo }
	r.Tick(ctx, false)
	geo.Aliases[0].Entries = []string{"de", "fr"}
	r.Tick(ctx, false)
	if count("/de.zone") != 2 || count("/fr.zone") != 1 {
		t.Errorf("hits = %v, want both countries fetched after the change", hits)
	}
}

// A cache written before it recorded its sources is fetched once more,
// and then left to its schedule.
func TestOldCacheIsRefetchedOnce(t *testing.T) {
	t.Parallel()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	old := `{"alias": "drop", "fetchedAt": "` + time.Now().UTC().Format(time.RFC3339) + `", "entries": ["192.0.2.0/24"]}`
	if err := os.WriteFile(filepath.Join(dir, "drop.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config(model.Alias{Name: "drop", Type: model.AliasHosts, URL: srv.URL})
	r := &Refresher{Cache: NewCache(dir), Fetcher: NewFetcher("test"), Source: func() *model.Config { return cfg },
		Log: slog.New(slog.DiscardHandler)}
	r.Tick(context.Background(), false)
	r.Tick(context.Background(), false)
	if hits != 1 {
		t.Errorf("fetched %d times, want once", hits)
	}
}

// Wake makes Run look now rather than at its next tick, which is how an
// apply gets a changed list fetched within seconds.
func TestWakeRunsAPass(t *testing.T) {
	t.Parallel()
	passes := make(chan struct{}, 4)
	r := &Refresher{
		Cache:    NewCache(t.TempDir()),
		Fetcher:  NewFetcher("test"),
		Source:   func() *model.Config { return config() },
		Log:      slog.New(slog.DiscardHandler),
		Interval: time.Hour,
		OnTick:   func() { passes <- struct{}{} },
	}
	// Waking with nothing running never blocks the caller, and two Wakes
	// before a pass ask for one pass, not two.
	r.Wake()
	r.Wake()
	if n := len(r.woken()); n != 1 {
		t.Fatalf("%d passes pending, want one", n)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	wait := func(what string) {
		t.Helper()
		select {
		case <-passes:
		case <-time.After(5 * time.Second):
			t.Fatalf("no pass %s", what)
		}
	}
	wait("at start")
	wait("for the Wakes before the start")
	select {
	case <-passes:
		t.Fatal("a pass nobody asked for, an hour early")
	case <-time.After(50 * time.Millisecond):
	}
	r.Wake()
	wait("after Wake")
}

// An alias that has been removed should not leave its list on disk.
func TestPruneForgetsRemovedAliases(t *testing.T) {
	t.Parallel()
	cache := NewCache(t.TempDir())
	if err := cache.Save(model.Alias{Name: "gone"}, []Part{{Source: "http://x", Entries: 1}}, []string{"192.0.2.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cache.Prune(config())
	if got := cache.Entries(); len(got) != 0 {
		t.Errorf("entries = %v", got)
	}
	if got := NewCache(cache.Dir).Entries(); len(got) != 0 {
		t.Errorf("the file survived: %v", got)
	}
}

func TestStatusesReportStaleAndErrors(t *testing.T) {
	t.Parallel()
	cfg := config(
		model.Alias{Name: "fresh", Type: model.AliasHosts, URL: "http://example.invalid/list"},
		model.Alias{Name: "never", Type: model.AliasHosts, URL: "http://example.invalid/other"},
		model.Alias{Name: "static", Type: model.AliasHosts, Entries: []string{"10.0.0.1"}},
	)
	cache := NewCache(t.TempDir())
	if err := cache.Save(cfg.Aliases[0], []Part{{Source: "http://example.invalid/list", Entries: 1}}, []string{"192.0.2.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	got := cache.Statuses(cfg)
	if len(got) != 2 {
		t.Fatalf("statuses = %+v, want only the fetched aliases", got)
	}
	byName := map[string]Status{}
	for _, s := range got {
		byName[s.Alias] = s
	}
	if s := byName["fresh"]; s.Entries != 1 || s.Stale {
		t.Errorf("fresh = %+v", s)
	}
	if s := byName["never"]; s.Entries != 0 || !s.Stale {
		t.Errorf("never fetched = %+v, want it marked stale", s)
	}
}

// setsFake records the fragments the refresher loads.
type setsFake struct{ loaded []string }

func (s *setsFake) Apply(_ context.Context, fragment string) error {
	s.loaded = append(s.loaded, fragment)
	return nil
}

// Push puts the cache in the kernel, leaving a list it has nothing for as
// the loaded ruleset has it rather than emptying the set.
func TestPushLeavesAListTheCacheLacks(t *testing.T) {
	t.Parallel()
	cfg := config(
		model.Alias{Name: "cached", Type: model.AliasHosts, URL: "http://example.invalid/a"},
		model.Alias{Name: "lost", Type: model.AliasHosts, URL: "http://example.invalid/b"},
	)
	cache := NewCache(t.TempDir())
	if err := cache.Save(cfg.Aliases[0], nil, []string{"192.0.2.0/24"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	sets := &setsFake{}
	r := &Refresher{Cache: cache, Source: func() *model.Config { return cfg }, Sets: sets}
	r.Push(context.Background())
	if len(sets.loaded) != 1 {
		t.Fatalf("loaded %d fragments", len(sets.loaded))
	}
	if got := sets.loaded[0]; !strings.Contains(got, "alias_cached_v4 { 192.0.2.0/24 }") || strings.Contains(got, "alias_lost") {
		t.Errorf("fragment:\n%s", got)
	}
}

func TestSetFragmentReplacesElements(t *testing.T) {
	t.Parallel()
	cfg := config(
		model.Alias{Name: "drop", Type: model.AliasHosts, URL: "http://example.invalid/l", Entries: []string{"203.0.113.1"}},
		model.Alias{Name: "static", Type: model.AliasHosts, Entries: []string{"10.0.0.1"}},
	)
	got := SetFragment(cfg, map[string][]string{"drop": {"192.0.2.0/24", "2001:db8::/32"}})

	for _, want := range []string{
		"flush set inet ostiole alias_drop_v4",
		"add element inet ostiole alias_drop_v4 { 203.0.113.1, 192.0.2.0/24 }",
		"flush set inet ostiole alias_drop_v6",
		"add element inet ostiole alias_drop_v6 { 2001:db8::/32 }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment is missing %q:\n%s", want, got)
		}
	}
	// A static alias is not touched: it was never fetched.
	if strings.Contains(got, "alias_static") {
		t.Errorf("a hand-written alias was in the fragment:\n%s", got)
	}
}

// The bogon list is fetched like any other feed, but nobody writes it as
// an alias: an interface asks for it by turning its block on.
func TestBogonListIsFetchedWhenAnInterfaceAsks(t *testing.T) {
	t.Parallel()
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if strings.Contains(r.URL.Path, "v6") {
			_, _ = w.Write([]byte("2001:db8::/32\n"))
			return
		}
		_, _ = w.Write([]byte("192.0.2.0/24\n198.51.100.0/24\n"))
	}))
	defer srv.Close()

	cfg := config()
	cfg.System.BogonV4URL = srv.URL + "/v4.txt"
	cfg.System.BogonV6URL = srv.URL + "/v6.txt"

	// Nothing blocks bogons yet, so nothing is fetched.
	r := &Refresher{
		Cache:   NewCache(t.TempDir()),
		Fetcher: NewFetcher("test"),
		Source:  func() *model.Config { return cfg },
		Log:     slog.New(slog.DiscardHandler),
	}
	r.Tick(context.Background(), true)
	if len(hits) != 0 {
		t.Fatalf("fetched %v with nothing asking for it", hits)
	}

	cfg.Interfaces[0].BlockBogons = true
	r.Tick(context.Background(), true)
	if hits["/v4.txt"] != 1 || hits["/v6.txt"] != 1 {
		t.Fatalf("hits = %v, want one per family", hits)
	}
	got := r.Cache.Entries()[BogonAlias]
	if len(got) != 3 {
		t.Errorf("cached %v, want both families", got)
	}

	// It is reported beside the aliases, so the page can say when it was
	// last updated.
	found := false
	for _, s := range r.Cache.Statuses(cfg) {
		if s.Alias == BogonAlias {
			found = true
			if s.Entries != 3 || s.Stale || len(s.Sources) != 2 {
				t.Errorf("status = %+v", s)
			}
		}
	}
	if !found {
		t.Error("the bogon list is not reported")
	}

	// Turning the block off again drops the cached list.
	cfg.Interfaces[0].BlockBogons = false
	r.Tick(context.Background(), true)
	if got := r.Cache.Entries()[BogonAlias]; len(got) != 0 {
		t.Errorf("the list outlived the block: %v", got)
	}
}

// A refreshed bogon list has to reach the sets the rules already match
// on, without rebuilding the ruleset.
func TestSetFragmentCarriesTheBogons(t *testing.T) {
	t.Parallel()
	cfg := config()
	cfg.Interfaces[0].BlockBogons = true
	got := SetFragment(cfg, map[string][]string{BogonAlias: {"192.0.2.0/24", "2001:db8::/32"}})
	for _, want := range []string{
		"flush set inet ostiole bogons_v4",
		"add element inet ostiole bogons_v4 { 192.0.2.0/24 }",
		"add element inet ostiole bogons_v6 { 2001:db8::/32 }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment is missing %q:\n%s", want, got)
		}
	}
}

// A country list is fetched one country at a time, and the page shows how
// much of the ruleset each one is: somebody picking twelve countries is
// asking how big the answer will be.
func TestFetchReportsWhatEachCountryHeld(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "cn"):
			_, _ = w.Write([]byte("1.0.1.0/24\n1.0.2.0/23\n"))
		case strings.Contains(r.URL.Path, "nl"):
			// One range this publisher lists under both countries, to
			// prove the parts are not the deduplicated total.
			_, _ = w.Write([]byte("1.0.1.0/24\n2.2.2.0/24\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "geo", Type: model.AliasGeoIP, Entries: []string{"CN", "NL"}})
	// One template only — an empty one is skipped — because this is about
	// the breakdown per country, not about address families.
	cfg.System.GeoIPv4URL = srv.URL + "/{country}.zone"
	alias := cfg.Aliases[0]

	entries, parts, err := NewFetcher("test").Fetch(context.Background(), cfg, alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("entries = %v, want the three distinct ranges", entries)
	}
	byCountry := map[string]int{}
	for _, p := range parts {
		byCountry[p.Country] = p.Entries
	}
	if byCountry["cn"] != 2 || byCountry["nl"] != 2 {
		t.Errorf("parts = %+v, want two each", parts)
	}

	// And the breakdown survives a round trip through the cache, which is
	// where the page reads it from.
	cache := NewCache(t.TempDir())
	if err := cache.Save(alias, parts, entries, time.Now()); err != nil {
		t.Fatal(err)
	}
	st := NewCache(cache.Dir).Statuses(cfg)
	if len(st) != 1 || len(st[0].Parts) != 2 || st[0].Parts[0].Country != "cn" {
		t.Errorf("statuses = %+v", st)
	}
	if st[0].Entries != 3 {
		t.Errorf("entries = %d, want the deduplicated total", st[0].Entries)
	}
}
