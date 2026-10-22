package feeds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ostiole/internal/model"
)

// countryList is RIPEstat's country-resource-list answer, cut down to what
// is read: a repeated prefix, default routes, and both families beside the
// AS numbers, which are not addresses.
const countryList = `{
  "messages": [],
  "data_call_name": "country-resource-list",
  "status": "ok",
  "data": {
    "query_time": "2026-10-21T00:00:00",
    "resources": {
      "asn": ["64500", "64501"],
      "ipv4": ["192.0.2.0/24", "198.51.100.0/24", "192.0.2.0/24", "0.0.0.0/0"],
      "ipv6": ["2001:db8::/32", "::/0"]
    }
  }
}`

// emptyCountry is the answer for a territory with no addresses of its own,
// and for a code RIPEstat does not know.
const emptyCountry = `{"data_call_name": "country-resource-list", "status": "ok",
  "data": {"resources": {"asn": [], "ipv4": [], "ipv6": []}}}`

// v4Only is an answer whose IPv6 list came back empty.
const v4Only = `{"data_call_name": "country-resource-list", "status": "ok",
  "data": {"resources": {"ipv4": ["192.0.2.0/24"], "ipv6": []}}}`

func geoConfig(srv *httptest.Server, codes ...string) *model.Config {
	cfg := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: codes})
	cfg.System.GeoIPv4URL = srv.URL + "/data.json?resource={country}"
	return cfg
}

// A country is one fetch with both families in the answer. A territory
// with nothing is an answer; an alias with nothing at all is not.
func TestCountryAliasFetchesThroughRIPEstat(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("resource") {
		case "de":
			_, _ = w.Write([]byte(countryList))
		case "bv", "uk":
			_, _ = w.Write([]byte(emptyCountry))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := geoConfig(srv, "DE", "bv")
	entries, parts, err := testFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entries, ","); got != "192.0.2.0/24,198.51.100.0/24,2001:db8::/32" {
		t.Errorf("entries = %v", entries)
	}
	if len(parts) != 2 || parts[0].Country != "de" || parts[0].Entries != 3 || parts[1].Country != "bv" || parts[1].Entries != 0 {
		t.Errorf("parts = %+v", parts)
	}

	cfg = geoConfig(srv, "bv", "uk")
	if _, _, err := testFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0]); err == nil || !strings.Contains(err.Error(), "(bv, uk)") {
		t.Errorf("an alias that came back empty: err = %v, want the codes named", err)
	}
}

// The same URL in both settings is asked once per country, so the page
// does not count every country twice.
func TestCountryTemplateGivenTwiceIsAskedOnce(t *testing.T) {
	t.Parallel()
	cfg := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: []string{"de"}})
	cfg.System.GeoIPv4URL = model.DefaultGeoIPURL
	cfg.System.GeoIPv6URL = model.DefaultGeoIPURL
	if got := Sources(cfg, cfg.Aliases[0]); len(got) != 1 {
		t.Errorf("sources = %v, want one", got)
	}
}

// RIPEstat answers a bad resource with HTTP 400 and may put an error
// inside a 200; a mirror may serve other JSON or a plain list.
func TestCountrySourceShapes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		code    int
		body    string
		want    string
		wantErr string
	}{
		"plain text": {body: "192.0.2.0/24\n2001:db8::/32\n", want: "192.0.2.0/24,2001:db8::/32"},
		"other json": {body: `{"prefixes": ["198.51.100.0/24"]}`, want: "198.51.100.0/24"},
		// What ipdeny served on 2026-10-22.
		"empty body": {body: "", wantErr: "nothing usable"},
		"error inside": {
			body:    `{"data_call_name": "country-resource-list", "status": "error", "messages": [["error", "xx is not a country"]], "data": {}}`,
			wantErr: "xx is not a country",
		},
		"maintenance": {
			body:    `{"data_call_name": "country-resource-list", "status": "maintenance", "data": {}}`,
			wantErr: `"maintenance"`,
		},
		"bad resource": {
			code:    http.StatusBadRequest,
			body:    `{"data_call_name": "country-resource-list", "status": "error", "status_code": 400, "data": {}}`,
			wantErr: "HTTP 400",
		},
		"no lists": {
			body:    `{"data_call_name": "country-resource-list", "status": "ok", "data": {"resources": {"asn": []}}}`,
			wantErr: "no address lists",
		},
		"ranges": {
			body: `{"data_call_name": "country-resource-list", "status": "ok",
			  "data": {"resources": {"ipv4": ["192.0.2.0/24", "198.51.100.0-198.51.100.191"], "ipv6": []}}}`,
			wantErr: "v4_format=prefix",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.code != 0 {
					w.WriteHeader(tc.code)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cfg := geoConfig(srv, "de")
			entries, _, err := testFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(entries, ","); got != tc.want {
				t.Errorf("entries = %q, want %q", got, tc.want)
			}
		})
	}
}

// A country, or a family, that held addresses at the last fetch from the
// same sources and comes back empty fails the refresh: a gap in the
// source's data reads just like a territory with nothing, and yesterday's
// list is better than an open deny rule.
func TestCountryThatEmptiedKeepsTheLastList(t *testing.T) {
	t.Parallel()
	var body atomic.Value
	body.Store(countryList)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") == "fr" {
			_, _ = w.Write([]byte(strings.ReplaceAll(countryList, "192.0.2.0/24", "203.0.113.0/24")))
			return
		}
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()

	var cfg atomic.Pointer[model.Config]
	cfg.Store(geoConfig(srv, "de", "fr"))
	r := &Refresher{Cache: NewCache(t.TempDir()), Fetcher: testFetcher(), Source: cfg.Load,
		Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	refresh := func() error {
		t.Helper()
		_, err := r.RefreshOne(ctx, "countries")
		return err
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join(r.Cache.Entries()["countries"], ",")

	body.Store(emptyCountry)
	if err := refresh(); err == nil || !strings.Contains(err.Error(), "de came back empty; it held 3") {
		t.Errorf("err = %v", err)
	}
	if got := strings.Join(r.Cache.Entries()["countries"], ","); got != want {
		t.Errorf("entries = %s, want the last list %s", got, want)
	}

	// fr still has IPv6, so de losing its own is not a lost family; a
	// single-country alias losing it is.
	body.Store(v4Only)
	if err := refresh(); err != nil {
		t.Errorf("a family another country still holds: %v", err)
	}
	cfg.Store(geoConfig(srv, "de"))
	body.Store(countryList)
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	body.Store(v4Only)
	if err := refresh(); err == nil || !strings.Contains(err.Error(), "no IPv6") {
		t.Errorf("a lost family: err = %v", err)
	}

	// Taking a country out is not it emptying.
	cfg.Store(geoConfig(srv, "de", "fr"))
	body.Store(countryList)
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	cfg.Store(geoConfig(srv, "fr"))
	if err := refresh(); err != nil {
		t.Errorf("a country taken out: %v", err)
	}
}

// An alias that failed is not asked again every tick: an hour, then
// doubling up to its period. Refresh, and a change of sources, ask at once.
func TestFailingAliasBacksOff(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var cfg atomic.Pointer[model.Config]
	cfg.Store(geoConfig(srv, "de"))
	r := &Refresher{Cache: NewCache(t.TempDir()), Fetcher: testFetcher(), Source: cfg.Load,
		Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	r.Tick(ctx, false)
	r.Tick(ctx, false)
	if n := hits.Load(); n != 1 {
		t.Errorf("asked %d times, want once until the backoff is over", n)
	}
	r.Tick(ctx, true)
	if n := hits.Load(); n != 2 {
		t.Errorf("a forced pass was held back: %d", n)
	}
	cfg.Store(geoConfig(srv, "de", "fr"))
	r.Tick(ctx, false)
	if n := hits.Load(); n != 3 {
		t.Errorf("changed sources waited: %d requests, want 3", n)
	}

	c := cfg.Load()
	a := c.Aliases[0]
	now := time.Now()
	for fails, wait := range map[int]time.Duration{1: time.Hour, 2: 2 * time.Hour, 5: 16 * time.Hour, 9: 24 * time.Hour} {
		cache := NewCache(t.TempDir())
		cache.errs[a.Name] = feedError{when: now, fails: fails, sources: Sources(c, a)}
		if !cache.backingOff(c, a, now.Add(wait-time.Minute)) || cache.backingOff(c, a, now.Add(wait)) {
			t.Errorf("after %d failures the wait is not %s", fails, wait)
		}
	}
}

// A country taken out of an alias leaves the sets at once, even while the
// refetch fails, because the cache knows what each country held. The
// file keeps the merged list for an older release, and one written
// without the lists is used as it stands.
func TestCountryTakenOutLeavesTheSetsAtOnce(t *testing.T) {
	t.Parallel()
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case failing.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Query().Get("resource") == "fr":
			_, _ = w.Write([]byte(`{"data_call_name": "country-resource-list", "status": "ok",
			  "data": {"resources": {"ipv4": ["203.0.113.0/24", "192.0.2.0/24"], "ipv6": []}}}`))
		default:
			_, _ = w.Write([]byte(countryList))
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	both := geoConfig(srv, "de", "fr")
	var cfg atomic.Pointer[model.Config]
	cfg.Store(both)
	r := &Refresher{Cache: NewCache(dir), Fetcher: testFetcher(), Source: cfg.Load,
		Log: slog.New(slog.DiscardHandler)}
	ctx := context.Background()
	if _, err := r.RefreshOne(ctx, "countries"); err != nil {
		t.Fatal(err)
	}
	const all = "192.0.2.0/24,198.51.100.0/24,2001:db8::/32,203.0.113.0/24"

	failing.Store(true)
	onlyFR := geoConfig(srv, "fr")
	cfg.Store(onlyFR)
	r.Tick(ctx, false)
	for name, c := range map[string]*Cache{"in memory": r.Cache, "from disk": NewCache(dir)} {
		// 192.0.2.0/24 is France's too, so it stays.
		if got := strings.Join(c.For(onlyFR)["countries"], ","); got != "192.0.2.0/24,203.0.113.0/24" {
			t.Errorf("%s: for the narrowed alias = %s", name, got)
		}
		if got := strings.Join(c.For(both)["countries"], ","); got != all {
			t.Errorf("%s: for the alias as it was = %s, want it whole", name, got)
		}
		if got := strings.Join(c.Entries()["countries"], ","); got != all {
			t.Errorf("%s: entries = %s, want what was fetched", name, got)
		}
	}

	old := `{"alias": "countries", "parts": [{"source": "` + Sources(both, both.Aliases[0])[0] + `", "entries": 1}], "fetchedAt": "2026-10-01T00:00:00Z", "entries": ["198.51.100.0/24"]}`
	if err := os.WriteFile(filepath.Join(dir, "countries.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(NewCache(dir).For(onlyFR)["countries"], ","); got != "198.51.100.0/24" {
		t.Errorf("a file without lists = %s", got)
	}
}
