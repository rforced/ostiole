package feeds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if err := cache.Save("drop", sources, entries, time.Now()); err != nil {
		t.Fatal(err)
	}

	// A new cache over the same directory has it, which is what makes a
	// box that boots without a working line still block what it blocked.
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

// An alias that has been removed should not leave its list on disk.
func TestPruneForgetsRemovedAliases(t *testing.T) {
	t.Parallel()
	cache := NewCache(t.TempDir())
	if err := cache.Save("gone", []string{"http://x"}, []string{"192.0.2.1"}, time.Now()); err != nil {
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
	if err := cache.Save("fresh", []string{"http://example.invalid/list"}, []string{"192.0.2.1"}, time.Now()); err != nil {
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

func TestRefreshPeriodFloor(t *testing.T) {
	t.Parallel()
	if got := RefreshPeriod(model.Alias{}); got != DefaultRefresh {
		t.Errorf("default = %v", got)
	}
	// Publishers ask not to be hammered, so an hour is the floor.
	if got := RefreshPeriod(model.Alias{RefreshHours: 0}); got < MinRefresh {
		t.Errorf("period = %v", got)
	}
}
