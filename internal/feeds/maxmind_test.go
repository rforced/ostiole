package feeds

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const testLicence = "Test0123456789abcdefghij_mmk"

// geoLite is a cut-down GeoLite2 country archive. 192.0.2.0/24 is written
// as two halves, the way MaxMind splits a block. 198.51.100.0/24 is
// registered in Canada and used in the US, and 203.0.113.0/24 is placed
// nowhere, so it counts where it is registered.
func geoLite(t *testing.T) []byte {
	t.Helper()
	files := map[string]string{
		"GeoLite2-Country-Locations-en.csv": "geoname_id,locale_code,continent_code,continent_name,country_iso_code,country_name,is_in_european_union\n" +
			"6252001,en,NA,\"North America\",US,\"United States\",0\n" +
			"6251999,en,NA,\"North America\",CA,Canada,0\n" +
			"6255148,en,EU,Europe,,,0\n",
		"GeoLite2-Country-Blocks-IPv4.csv": "network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,is_anycast\n" +
			"192.0.2.0/25,6252001,6252001,,0,0,\n" +
			"192.0.2.128/25,6252001,6252001,,0,0,\n" +
			"198.51.100.0/24,6252001,6251999,,0,0,\n" +
			"203.0.113.0/24,,6251999,,0,0,\n",
		"GeoLite2-Country-Blocks-IPv6.csv": "network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,is_anycast\n" +
			"2001:db8::/33,6252001,6252001,,0,0,\n" +
			"2001:db8:8000::/33,6255148,6251999,,0,0,\n",
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create("GeoLite2-Country-CSV_20261020/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type maxMindServer struct {
	*httptest.Server
	db        []byte
	requests  atomic.Int32
	sums      atomic.Int32
	downloads atomic.Int32
}

func newMaxMind(t *testing.T, db []byte) *maxMindServer {
	t.Helper()
	m := &maxMindServer{db: db}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.requests.Add(1)
		q := r.URL.Query()
		if q.Get("edition_id") != "GeoLite2-Country-CSV" || q.Get("license_key") != testLicence {
			http.Error(w, "Invalid license key", http.StatusUnauthorized)
			return
		}
		sum := sha256.Sum256(m.db)
		switch q.Get("suffix") {
		case "zip.sha256":
			m.sums.Add(1)
			_, _ = w.Write([]byte(hex.EncodeToString(sum[:]) + "  GeoLite2-Country-CSV_20261020.zip\n"))
		case "zip":
			m.downloads.Add(1)
			_, _ = w.Write(m.db)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *maxMindServer) fetcher() *Fetcher {
	f := testFetcher()
	f.MaxMindURL = m.URL + "/app/geoip_download?edition_id=GeoLite2-Country-CSV"
	return f
}

func maxMindConfig(key string, codes ...string) *model.Config {
	cfg := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: codes})
	cfg.System.MaxMindLicenceKey = key
	return cfg
}

// With a licence key a country is the networks MaxMind places there, read
// from one download, and the key is in no source.
func TestCountryAliasFetchesFromMaxMind(t *testing.T) {
	t.Parallel()
	m := newMaxMind(t, geoLite(t))
	dir := t.TempDir()
	cfg := maxMindConfig(testLicence, "US", "ca", "bv")
	entries, parts, _, err := m.fetcher().fetch(context.Background(), cfg, cfg.Aliases[0], dir)
	if err != nil {
		t.Fatal(err)
	}
	// The two IPv6 halves are the US's and Canada's, merged in the alias.
	want := "192.0.2.0/24,198.51.100.0/24,2001:db8::/32,203.0.113.0/24"
	if got := strings.Join(entries, ","); got != want {
		t.Errorf("entries = %v, want %s", entries, want)
	}
	counts := map[string]int{}
	for _, p := range parts {
		if strings.Contains(p.Source, testLicence) || !strings.HasPrefix(p.Source, MaxMindSource) {
			t.Errorf("source %q", p.Source)
		}
		counts[p.Country] = p.Entries
	}
	if counts["us"] != 3 || counts["ca"] != 2 || counts["bv"] != 0 {
		t.Errorf("parts = %+v", parts)
	}
	if m.sums.Load() != 1 || m.downloads.Load() != 1 {
		t.Errorf("sums %d, downloads %d, want one each", m.sums.Load(), m.downloads.Load())
	}

	// The aliases of one pass share the copy without asking again.
	if _, _, _, err := m.fetcher().fetch(context.Background(), cfg, cfg.Aliases[0], dir); err != nil {
		t.Fatal(err)
	}
	if m.sums.Load() != 1 || m.downloads.Load() != 1 {
		t.Errorf("a fetch inside the recheck asked MaxMind: sums %d, downloads %d", m.sums.Load(), m.downloads.Load())
	}

	// Later, an unchanged checksum keeps the copy.
	path := filepath.Join(dir, maxMindFile)
	old := time.Now().Add(-2 * maxMindRecheck)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := m.fetcher().fetch(context.Background(), cfg, cfg.Aliases[0], dir); err != nil {
		t.Fatal(err)
	}
	if m.sums.Load() != 2 || m.downloads.Load() != 1 {
		t.Errorf("an unchanged database: sums %d, downloads %d, want 2 and 1", m.sums.Load(), m.downloads.Load())
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 || time.Since(st.ModTime()) > time.Minute {
		t.Errorf("the kept database: %v, %v", st, err)
	}
}

func TestMaxMindRefusals(t *testing.T) {
	t.Parallel()
	m := newMaxMind(t, geoLite(t))
	cfg := maxMindConfig("Wrong0123456789abcdef_mmk", "us")
	_, _, err := m.fetcher().Fetch(context.Background(), cfg, cfg.Aliases[0])
	if err == nil || !strings.Contains(err.Error(), "MaxMind refused the licence key") || strings.Contains(err.Error(), "Wrong0123") {
		t.Errorf("a refused key: err = %v", err)
	}

	db := geoLite(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("suffix") == "zip.sha256" {
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  GeoLite2-Country-CSV_20261020.zip\n"))
			return
		}
		_, _ = w.Write(db)
	}))
	defer bad.Close()
	f := testFetcher()
	f.MaxMindURL = bad.URL + "/app/geoip_download?edition_id=GeoLite2-Country-CSV"
	cfg = maxMindConfig(testLicence, "us")
	dir := t.TempDir()
	if _, _, _, err := f.fetch(context.Background(), cfg, cfg.Aliases[0], dir); err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Errorf("a download that does not match: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, maxMindFile)); !os.IsNotExist(err) {
		t.Errorf("a download that does not match was kept: %v", err)
	}
}

// Taking the key out deletes the database at the next pass.
func TestPruneDropsTheMaxMindDatabase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, maxMindFile)
	if err := os.WriteFile(path, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCache(dir)
	c.Prune(maxMindConfig(testLicence, "us"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pruned while in use: %v", err)
	}
	c.Prune(maxMindConfig("", "us"))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("kept without a key: %v", err)
	}
}

// Setting or clearing the key moves every country to another source. Until
// the new one answers, each country still in the alias keeps the list it
// had; one taken out at the same time leaves at once.
func TestASourceSwitchKeepsTheCountries(t *testing.T) {
	t.Parallel()
	c := NewCache(t.TempDir())
	ripe := config(model.Alias{Name: "countries", Type: model.AliasGeoIP, Entries: []string{"us", "ca"}})
	a := ripe.Aliases[0]
	parts := SourceParts(ripe, a)
	lists := map[string][]string{parts[0].Source: {"192.0.2.0/24"}, parts[1].Source: {"198.51.100.0/24"}}
	parts[0].Entries, parts[1].Entries = 1, 1
	if err := c.save(a, parts, []string{"192.0.2.0/24", "198.51.100.0/24"}, lists, time.Now()); err != nil {
		t.Fatal(err)
	}

	keyed := maxMindConfig(testLicence, "us", "ca")
	if got := strings.Join(c.For(keyed)["countries"], ","); got != "192.0.2.0/24,198.51.100.0/24" {
		t.Errorf("after the key was set: %s", got)
	}
	if c.Current(keyed, keyed.Aliases[0]) {
		t.Error("the RIPEstat list reads as current for MaxMind")
	}
	narrowed := maxMindConfig(testLicence, "us")
	if got := strings.Join(c.For(narrowed)["countries"], ","); got != "192.0.2.0/24" {
		t.Errorf("key set and ca taken out: %s", got)
	}
	replaced := maxMindConfig(testLicence, "de")
	if got := c.For(replaced)["countries"]; len(got) != 0 {
		t.Errorf("every country replaced: %v", got)
	}
}

// A kept copy that does not read is downloaded again in the same fetch.
func TestACorruptCopyIsDownloadedAgain(t *testing.T) {
	t.Parallel()
	m := newMaxMind(t, geoLite(t))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, maxMindFile), []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := maxMindConfig(testLicence, "us")
	entries, _, _, err := m.fetcher().fetch(context.Background(), cfg, cfg.Aliases[0], dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries %v, err %v", entries, err)
	}
	if m.downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", m.downloads.Load())
	}
}

// One failed download stands for the other aliases of the pass, and a new
// key is tried at once.
func TestAFailureIsNotRepeatedByEachAlias(t *testing.T) {
	t.Parallel()
	m := newMaxMind(t, geoLite(t))
	f := m.fetcher()
	wrong := maxMindConfig("Wrong0123456789abcdef_mmk", "us")
	for range 3 {
		if _, _, _, err := f.fetch(context.Background(), wrong, wrong.Aliases[0], t.TempDir()); err == nil {
			t.Fatal("a refused key fetched")
		}
	}
	if m.requests.Load() != 1 {
		t.Errorf("three aliases asked MaxMind %d times, want once", m.requests.Load())
	}
	cfg := maxMindConfig(testLicence, "us")
	if _, _, _, err := f.fetch(context.Background(), cfg, cfg.Aliases[0], t.TempDir()); err != nil {
		t.Errorf("the corrected key: %v", err)
	}
}
