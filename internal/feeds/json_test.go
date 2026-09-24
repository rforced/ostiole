package feeds

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// oracle is a cut of Oracle's public_ip_ranges.json as published: two
// regions, both families, and a prefix with two tags.
func oracle(t *testing.T) []byte { return testdata(t, "oracle.json") }

// google is a cut of Google's ipranges/cloud.json as published: two
// scopes, both families, the address under a key per family, and a
// service every prefix shares.
func google(t *testing.T) []byte { return testdata(t, "google-cloud.json") }

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Shapes of the other lists people point aliases at, cut down to what
// matters: AWS writes the prefix before its region and lists one prefix
// once per service, GitHub files addresses under one key per service,
// Cloudflare has nothing to narrow by, and Microsoft 365 is a top-level
// array with numbers and booleans beside the addresses.
const (
	awsRanges = `{
  "syncToken": "1758700000",
  "createDate": "2026-09-24-10-00-00",
  "prefixes": [
    {"ip_prefix": "3.5.140.0/22", "region": "ap-northeast-2", "service": "AMAZON", "network_border_group": "ap-northeast-2"},
    {"ip_prefix": "3.5.140.0/22", "region": "ap-northeast-2", "service": "S3", "network_border_group": "ap-northeast-2"},
    {"ip_prefix": "15.230.39.0/24", "region": "us-east-1", "service": "AMAZON", "network_border_group": "us-east-1"},
    {"ip_prefix": "15.230.39.0/24", "region": "us-east-1", "service": "EC2", "network_border_group": "us-east-1"}
  ],
  "ipv6_prefixes": [
    {"ipv6_prefix": "2600:1f14::/35", "region": "us-east-1", "service": "EC2", "network_border_group": "us-east-1"}
  ]
}`
	githubMeta = `{
  "verifiable_password_authentication": false,
  "hooks": ["192.30.252.0/22", "2a0a:a440::/29"],
  "web": ["192.30.252.0/22", "140.82.112.0/20"],
  "actions": ["4.148.0.0/16", "2a01:111:f403::/48"],
  "domains": {"website": ["*.github.com", "github.com"]}
}`
	cloudflareIPs = `{"result": {"ipv4_cidrs": ["173.245.48.0/20"], "ipv6_cidrs": ["2400:cb00::/32"],
  "etag": "38f79d050aa027e3be3865e495dcc9bc"}, "success": true, "errors": [], "messages": []}`
	m365 = `[
  {"id": 1, "serviceArea": "Exchange", "ips": ["13.107.6.152/31", "2603:1006::/40"], "required": true},
  {"id": 2, "serviceArea": "SharePoint", "ips": ["13.107.136.0/22"], "required": false}
]`
)

func TestParseJSONFindsEveryAddress(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"oracle": {
			body: string(oracle(t)),
			want: "129.80.0.0/16,134.70.24.0/21,134.70.40.0/21,2603:c020:4000::/35,2603:c020:8000::/35,64.181.152.0/21,79.76.96.0/19,92.4.240.0/20",
		},
		"google": {
			body: string(google(t)),
			want: "2600:1900:4010::/44,2600:1900:4020::/44,34.23.0.0/16,34.3.76.0/22,34.4.16.0/22,8.34.208.0/23,8.34.211.0/24",
		},
		"aws, one prefix listed per service": {body: awsRanges, want: "15.230.39.0/24,2600:1f14::/35,3.5.140.0/22"},
		"github":                             {body: githubMeta, want: "140.82.112.0/20,192.30.252.0/22,2a01:111:f403::/48,2a0a:a440::/29,4.148.0.0/16"},
		"cloudflare":                         {body: cloudflareIPs, want: "173.245.48.0/20,2400:cb00::/32"},
		"a top-level array":                  {body: m365, want: "13.107.136.0/22,13.107.6.152/31,2603:1006::/40"},
		"one document per line":              {body: "{\"ip\": \"192.0.2.1\"}\n{\"ip\": \"198.51.100.0/24\"}\n", want: "192.0.2.1,198.51.100.0/24"},
		"a byte order mark":                  {body: "\xef\xbb\xbf{\"a\": [\"192.0.2.0/24\"]}", want: "192.0.2.0/24"},
		// The same normalising a text list gets: host bits masked, a host
		// without its length, and never a default route.
		"normalised": {
			body: `{"a": ["0.0.0.0/0", "::/0", "192.0.2.77/24", "198.51.100.7/32", " 203.0.113.9 "], "b": 12}`,
			want: "192.0.2.0/24,198.51.100.7,203.0.113.9",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _, err := ParseJSON([]byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != tc.want {
				t.Errorf("addresses = %v\nwant        %s", got, tc.want)
			}
		})
	}
}

func TestParseJSONRefusesWhatIsNotAList(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"truncated":          {`{"prefixes": [{"ip_prefix": "3.5.140.0/22"`, "not valid JSON"},
		"trailing garbage":   {`{"a": ["192.0.2.0/24"]} <html>`, "not valid JSON"},
		"no addresses":       {`{"region": "us-east-1", "count": 3}`, "no addresses in this JSON"},
		"only default route": {`{"a": "0.0.0.0/0"}`, "no addresses in this JSON"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := ParseJSON([]byte(tc.body), nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseJSONSelects(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		sel  []string
		want string
	}{
		"one region": {
			body: string(oracle(t)), sel: []string{"region=us-ashburn-1"},
			want: "129.80.0.0/16,134.70.24.0/21,2603:c020:4000::/35,64.181.152.0/21",
		},
		"a region and a tag must both hold": {
			body: string(oracle(t)), sel: []string{"region=us-ashburn-1", "tags=OCI"},
			want: "129.80.0.0/16,2603:c020:4000::/35",
		},
		"either tag, any element of the array": {
			body: string(oracle(t)), sel: []string{"tags=OCI", "tags=OBJECT_STORAGE"},
			want: "129.80.0.0/16,134.70.24.0/21,134.70.40.0/21,2603:c020:4000::/35,2603:c020:8000::/35,79.76.96.0/19",
		},
		"case is ignored": {
			body: string(oracle(t)), sel: []string{"TAGS=object_storage"},
			want: "134.70.24.0/21,134.70.40.0/21",
		},
		"a pattern": {
			body: string(oracle(t)), sel: []string{"region=eu-*"},
			want: "2603:c020:8000::/35,79.76.96.0/19,92.4.240.0/20,134.70.40.0/21",
		},
		"a key": {
			body: string(oracle(t)), sel: []string{"ipv6_cidrs"},
			want: "2603:c020:4000::/35,2603:c020:8000::/35",
		},
		"the fields after the prefix count": {
			body: awsRanges, sel: []string{"service=EC2"},
			want: "15.230.39.0/24,2600:1f14::/35",
		},
		"either key": {
			body: githubMeta, sel: []string{"hooks", "web"},
			want: "140.82.112.0/20,192.30.252.0/22,2a0a:a440::/29",
		},
		"a key and a field together": {
			body: githubMeta, sel: []string{"hooks", "verifiable_password_authentication=false"},
			want: "192.30.252.0/22,2a0a:a440::/29",
		},
		"numbers and booleans by their text": {
			body: m365, sel: []string{"required=true", "id=1"},
			want: "13.107.6.152/31,2603:1006::/40",
		},
		"a google scope": {
			body: string(google(t)), sel: []string{"scope=us-east1"},
			want: "2600:1900:4020::/44,34.23.0.0/16,34.3.76.0/22,34.4.16.0/22",
		},
		"spaces inside a value": {
			body: string(google(t)), sel: []string{"service=google cloud", "scope=europe-west1"},
			want: "2600:1900:4010::/44,8.34.208.0/23,8.34.211.0/24",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, _, err := ParseJSON([]byte(tc.body), tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Split(tc.want, ",")
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("addresses = %v\nwant        %v", got, want)
			}
		})
	}
}

// A selection that keeps nothing is a mistake, most likely a typo, and
// says how much there was to keep. It still reports the choices, which
// is where the typo shows.
func TestParseJSONSelectingNothingFails(t *testing.T) {
	t.Parallel()
	_, choices, err := ParseJSON(oracle(t), []string{"region=us-ashburn1"})
	if err == nil || !strings.Contains(err.Error(), "none of the 8 addresses in it matched the selection") {
		t.Fatalf("err = %v", err)
	}
	if len(choices) == 0 {
		t.Error("no choices came back with the error")
	}
}

func TestParseJSONOffersWhatNarrows(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		// The timestamp is the same for every address, and cidrs and
		// ipv6_cidrs only part the families.
		"oracle": {string(oracle(t)), "region: eu-frankfurt-1 us-ashburn-1 | tags: OBJECT_STORAGE OCI OSN"},
		// Every prefix is the same service, so only the scope narrows, and
		// ipv4Prefix and ipv6Prefix only part the families.
		"google": {string(google(t)), "scope: europe-west1 us-east1"},
		"aws": {awsRanges, "network_border_group: ap-northeast-2 us-east-1 | region: ap-northeast-2 us-east-1 | " +
			"service: AMAZON EC2 S3"},
		// The boolean covers every address, and the domains hold none.
		"github":     {githubMeta, "(keys): actions hooks web"},
		"cloudflare": {cloudflareIPs, ""},
		"m365":       {m365, "id: 1 2 | required: false true | serviceArea: Exchange SharePoint"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, choices, err := ParseJSON([]byte(tc.body), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := describe(choices); got != tc.want {
				t.Errorf("choices = %q\nwant      %q", got, tc.want)
			}
		})
	}
}

// A field with a value per record, or a document with a key per host,
// offers nothing anybody would pick from, and the other fields stand.
func TestParseJSONDropsChoicesNobodyPicksFrom(t *testing.T) {
	t.Parallel()
	var records, hosts []string
	for i := range 300 {
		zone := "a"
		if i%2 == 1 {
			zone = "b"
		}
		records = append(records, fmt.Sprintf(`{"id": "r%d", "zone": %q, "ip": "10.%d.%d.0/24"}`, i, zone, i/256, i%256))
		hosts = append(hosts, fmt.Sprintf(`"host%d": ["10.%d.%d.0/24"]`, i, i/256, i%256))
	}
	_, choices, err := ParseJSON([]byte("["+strings.Join(records, ",")+"]"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := describe(choices); got != "zone: a b" {
		t.Errorf("records offered %q", got)
	}
	got, choices, err := ParseJSON([]byte("{"+strings.Join(hosts, ",")+"}"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 300 || len(choices) != 0 {
		t.Errorf("hosts gave %d addresses and offered %q", len(got), describe(choices))
	}
}

// describe writes choices as "field: values | …", keys as "(keys)".
func describe(choices []Choice) string {
	var parts []string
	for _, c := range choices {
		field := c.Field
		if field == "" {
			field = "(keys)"
		}
		parts = append(parts, field+": "+strings.Join(c.Values, " "))
	}
	return strings.Join(parts, " | ")
}

// What comes back decides the parser, not the alias: JSON is searched,
// anything else is a list, and a selection needs something to select in.
func TestReadPicksTheParser(t *testing.T) {
	t.Parallel()
	hosts := model.Alias{Name: "l", Type: model.AliasHosts, URL: "https://example.test/l"}
	if got, _, err := read([]byte("\n  "+githubMeta), hosts); err != nil || len(got) != 5 {
		t.Errorf("JSON after blank lines: %v, %v", got, err)
	}
	if got, _, err := read([]byte("\xef\xbb\xbf192.0.2.0/24\n"), hosts); err != nil || strings.Join(got, ",") != "192.0.2.0/24" {
		t.Errorf("a list with a byte order mark: %v, %v", got, err)
	}
	selecting := hosts
	selecting.Select = []string{"tags=OCI"}
	if _, _, err := read([]byte("192.0.2.0/24\n"), selecting); err == nil || !strings.Contains(err.Error(), "plain text") {
		t.Errorf("a selection over a text list: err = %v", err)
	}
	// A port list is always a list.
	if _, _, err := read([]byte(`{"ports": ["80"]}`), model.Alias{Name: "p", Type: model.AliasPorts, URL: "https://example.test/p"}); err == nil {
		t.Error("JSON was read as a port list")
	}
}

func TestFetchKeepsWhatTheAliasSelects(t *testing.T) {
	t.Parallel()
	body := oracle(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg := config(model.Alias{Name: "oracle", Type: model.AliasHosts, URL: srv.URL + "/public_ip_ranges.json",
		Select: []string{"region=us-ashburn-1", "tags=OCI"}, Entries: []string{"203.0.113.9"}})
	alias := cfg.Aliases[0]
	entries, parts, err := NewFetcher("test").Fetch(context.Background(), cfg, alias)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(entries, ",") != "129.80.0.0/16,2603:c020:4000::/35" {
		t.Errorf("entries = %v", entries)
	}
	if len(parts) != 1 || parts[0].Entries != 2 || describe(parts[0].Choices) != "region: eu-frankfurt-1 us-ashburn-1 | tags: OBJECT_STORAGE OCI OSN" {
		t.Fatalf("parts = %+v", parts)
	}

	// The page reads the choices from the cache.
	cache := NewCache(t.TempDir())
	if err := cache.Save(alias.Name, parts, entries, time.Now()); err != nil {
		t.Fatal(err)
	}
	reopened := NewCache(cache.Dir)
	st := reopened.Statuses(cfg)
	if len(st) != 1 || st[0].Stale || len(st[0].Parts[0].Choices) != 2 {
		t.Errorf("statuses = %+v", st)
	}

	// A country list read from a JSON mirror offers nothing: it cannot select.
	geo := config(model.Alias{Name: "geo", Type: model.AliasGeoIP, Entries: []string{"us"}})
	geo.System.GeoIPv4URL = srv.URL + "/{country}.json"
	_, parts, err = NewFetcher("test").Fetch(context.Background(), geo, geo.Aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(parts[0].Choices) != 0 {
		t.Errorf("a country alias was offered %q", describe(parts[0].Choices))
	}
}

func TestInspectDescribesAList(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ranges.json":
			_, _ = w.Write(oracle(t))
		case "/drop.txt":
			_, _ = w.Write([]byte("192.0.2.0/24 ; SBL1\n198.51.100.7\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	f := NewFetcher("test")
	part, err := f.Inspect(context.Background(), srv.URL+"/ranges.json")
	if err != nil {
		t.Fatal(err)
	}
	if part.Source != srv.URL+"/ranges.json" || part.Entries != 8 || len(part.Choices) != 2 {
		t.Errorf("JSON: %+v", part)
	}
	part, err = f.Inspect(context.Background(), srv.URL+"/drop.txt")
	if err != nil || part.Entries != 2 || len(part.Choices) != 0 {
		t.Errorf("text: %+v, %v", part, err)
	}
	if _, err := f.Inspect(context.Background(), srv.URL+"/gone"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing: err = %v", err)
	}
}
