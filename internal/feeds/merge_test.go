package feeds

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ostiole/internal/model"
)

func TestCollapse(t *testing.T) {
	t.Parallel()
	var in []netip.Prefix
	for _, s := range []string{
		"192.0.2.128/26", "192.0.2.0/25", "192.0.2.192/26", "192.0.2.7/32",
		"198.51.100.0/25", "203.0.113.0/24", "203.0.113.4/30",
		"0.0.0.0/1", "128.0.0.0/1",
		"2001:db8::/33", "2001:db8:8000::/33",
	} {
		in = append(in, netip.MustParsePrefix(s))
	}
	var got []string
	for _, p := range collapse(in) {
		got = append(got, p.String())
	}
	want := "0.0.0.0/1,128.0.0.0/1,2001:db8::/32"
	if strings.Join(got, ",") != want {
		t.Errorf("collapse = %v, want %s", got, want)
	}

	in = nil
	for _, s := range []string{"192.0.2.128/26", "192.0.2.0/25", "192.0.2.192/26", "198.51.100.0/25", "198.51.101.0/25", "203.0.113.4/30", "203.0.113.0/24"} {
		in = append(in, netip.MustParsePrefix(s))
	}
	got = nil
	for _, p := range collapse(in) {
		got = append(got, p.String())
	}
	if want := "192.0.2.0/24,198.51.100.0/25,198.51.101.0/25,203.0.113.0/24"; strings.Join(got, ",") != want {
		t.Errorf("collapse = %v, want %s", got, want)
	}
}

// Neighbours from different sources merge, both families; a port list and
// a default route are not merged into anything.
func TestMergeAggregates(t *testing.T) {
	t.Parallel()
	got := merge([][]string{
		{"192.0.2.0/25", "2001:db8::/33", "198.51.100.7"},
		{"192.0.2.128/25", "2001:db8:8000::/33", "198.51.100.7", "0.0.0.0/0"},
	})
	if want := "192.0.2.0/24,198.51.100.7,2001:db8::/32"; strings.Join(got, ",") != want {
		t.Errorf("merge = %v, want %s", got, want)
	}
	if got := merge([][]string{{"443", "80", "8000-8100"}, {"80"}}); strings.Join(got, ",") != "443,80,8000-8100" {
		t.Errorf("ports = %v", got)
	}
}

// The limit counts what is left once merged, and names where it is set.
func TestTheLimitCountsMergedEntries(t *testing.T) {
	t.Parallel()
	var side, apart strings.Builder
	for i := range 1500 {
		fmt.Fprintf(&side, "10.%d.%d.0/24\n", i/256, i%256)
		fmt.Fprintf(&apart, "10.%d.%d.0/24\n", i/128, (i%128)*2)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/side" {
			_, _ = w.Write([]byte(side.String()))
			return
		}
		_, _ = w.Write([]byte(apart.String()))
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "list", Type: model.AliasHosts, Entries: []string{srv.URL + "/side"}})
	cfg.System.MaxAliasEntries = 1000
	entries, parts, err := testFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) >= 10 || parts[0].Entries != 1500 {
		t.Errorf("%d entries from %d published", len(entries), parts[0].Entries)
	}

	cfg.Aliases[0].Entries = []string{srv.URL + "/apart"}
	_, _, err = testFetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err == nil || !strings.Contains(err.Error(), "1500 entries once merged, more than the 1000 allowed in System › General") {
		t.Errorf("over the limit: err = %v", err)
	}
}

// A file from before merging is merged as it loads.
func TestAnOldCacheIsMergedOnLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := `{"alias": "old", "fetchedAt": "2026-09-01T00:00:00Z", "entries": ["192.0.2.0/25", "192.0.2.128/25"]}`
	if err := os.WriteFile(filepath.Join(dir, "old.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := NewCache(dir).Entries()["old"]; strings.Join(got, ",") != "192.0.2.0/24" {
		t.Errorf("entries = %v", got)
	}
}
