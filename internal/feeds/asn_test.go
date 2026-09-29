package feeds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// ripestat is RIPEstat's announced-prefixes answer, cut down to what is
// read: a repeated prefix, a host written as /32, and both families.
const ripestat = `{
  "status": "ok",
  "messages": [["info", "Results exclude routes with very low visibility (less than 10 RIS full-feed peers seeing)."]],
  "data": {
    "prefixes": [
      {"prefix": "8.8.8.0/24", "timelines": [{"starttime": "2026-09-06T00:00:00", "endtime": "2026-09-20T00:00:00"}]},
      {"prefix": "2001:4860::/32", "timelines": []},
      {"prefix": "8.8.8.0/24", "timelines": []},
      {"prefix": "192.0.2.1/32", "timelines": []}
    ],
    "resource": "15169"
  }
}`

func asnConfig(srv *httptest.Server, numbers ...string) *model.Config {
	cfg := config(model.Alias{Name: "google", Type: model.AliasASN, Entries: numbers})
	cfg.System.ASNURL = srv.URL + "/prefixes?resource=AS{asn}"
	cfg.System.ASNNamesURL = srv.URL + "/names?resource={asns}"
	return cfg
}

func asnFetcher() *Fetcher {
	f := NewFetcher("test")
	f.Log = slog.New(slog.DiscardHandler)
	return f
}

// An AS alias is one fetch per number, both families in the answer, and
// one more for the names, which the page shows beside each number.
func TestASAliasFetchesEachNumber(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path+"?"+r.URL.RawQuery]++
		mu.Unlock()
		switch {
		case r.URL.Path == "/prefixes" && r.URL.Query().Get("resource") == "AS15169":
			_, _ = w.Write([]byte(ripestat))
		case r.URL.Path == "/prefixes" && r.URL.Query().Get("resource") == "AS64512":
			// A private AS announces nothing, and that is an answer.
			_, _ = w.Write([]byte(`{"status": "ok", "data": {"prefixes": []}}`))
		case r.URL.Path == "/names":
			_, _ = w.Write([]byte(`{"status": "ok", "data": {"names": {"15169": " GOOGLE - Google LLC "}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := asnConfig(srv, "as15169", "64512")
	entries, parts, err := asnFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(entries, ","); got != "192.0.2.1,2001:4860::/32,8.8.8.0/24" {
		t.Errorf("entries = %v", entries)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want one per AS", parts)
	}
	if p := parts[0]; p.ASN != "AS15169" || p.Holder != "GOOGLE - Google LLC" || p.Entries != 3 {
		t.Errorf("parts[0] = %+v", p)
	}
	if p := parts[1]; p.ASN != "AS64512" || p.Holder != "" || p.Entries != 0 {
		t.Errorf("parts[1] = %+v", p)
	}
	if hits["/names?resource=15169,64512"] != 1 {
		t.Errorf("hits = %v, want the names asked for once, as bare numbers", hits)
	}

	// The breakdown is what the page reads, through the cache.
	cache := NewCache(t.TempDir())
	if err := cache.Save(cfg.Aliases[0], parts, entries, time.Now()); err != nil {
		t.Fatal(err)
	}
	st := NewCache(cache.Dir).Statuses(cfg)
	if len(st) != 1 || len(st[0].Sources) != 2 || len(st[0].Parts) != 2 || st[0].Parts[0].Holder == "" {
		t.Errorf("statuses = %+v", st)
	}
}

// Names are for the page, not the firewall: the lookup failing leaves the
// alias whole.
func TestASNamesAreBestEffort(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/names" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(ripestat))
	}))
	defer srv.Close()

	cfg := asnConfig(srv, "AS15169")
	entries, parts, err := asnFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err != nil {
		t.Fatalf("a names failure failed the alias: %v", err)
	}
	if len(entries) != 3 || len(parts) != 1 || parts[0].Holder != "" {
		t.Errorf("entries = %v, parts = %+v", entries, parts)
	}

	// Pointing only the prefix URL at a mirror is a way of saying "no
	// names", so nothing is asked.
	cfg.System.ASNNamesURL = ""
	if _, parts, err := asnFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0]); err != nil || parts[0].Holder != "" {
		t.Errorf("without a names URL: parts = %+v, err = %v", parts, err)
	}
}

// A mirror may serve a plain list; the default source may answer with an
// error inside a 200.
func TestASSourceShapes(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body    string
		want    string
		wantErr string
	}{
		"plain text": {body: "8.8.8.0/24 # google\n2001:4860::/32\n", want: "2001:4860::/32,8.8.8.0/24"},
		"error inside": {
			body:    `{"status": "error", "messages": [["error", "AS0 is not an AS"]], "data": {}}`,
			wantErr: "AS0 is not an AS",
		},
		"not json after all": {body: "{not json", wantErr: "not a RIPEstat answer"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/names" {
					_, _ = w.Write([]byte(`{"data": {"names": {}}}`))
					return
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cfg := asnConfig(srv, "AS15169")
			entries, _, err := asnFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
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

func TestASSourcesOnePerNumber(t *testing.T) {
	t.Parallel()
	cfg := config(model.Alias{Name: "google", Type: model.AliasASN, Entries: []string{"AS15169", "as36040", "396982"}})
	got := Sources(cfg, cfg.Aliases[0])
	if len(got) != 3 {
		t.Fatalf("sources = %v, want one per AS", got)
	}
	for i, want := range []string{"resource=AS15169&", "resource=AS36040&", "resource=AS396982&"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("sources[%d] = %q, want it to contain %q", i, got[i], want)
		}
	}
	// The numbers are keys, never addresses: only what was fetched
	// reaches the set.
	fragment := SetFragment(cfg, map[string][]string{"google": {"8.8.8.0/24"}})
	if !strings.Contains(fragment, "add element inet ostiole alias_google_v4 { 8.8.8.0/24 }") || strings.Contains(fragment, "AS15169") {
		t.Errorf("fragment:\n%s", fragment)
	}
}
