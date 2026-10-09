package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ostiole/internal/model"
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
// Cloudflare nests them in a result, and Microsoft 365 is a top-level
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
			got, err := ParseJSON([]byte(tc.body))
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
			if _, err := ParseJSON([]byte(tc.body)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// What comes back decides the parser, not the alias: JSON is searched and
// anything else is a list.
func TestReadPicksTheParser(t *testing.T) {
	t.Parallel()
	if got, err := read([]byte("\n  "+githubMeta), model.AliasHosts); err != nil || len(got) != 5 {
		t.Errorf("JSON after blank lines: %v, %v", got, err)
	}
	if got, err := read([]byte("\xef\xbb\xbf192.0.2.0/24\n"), model.AliasHosts); err != nil || strings.Join(got, ",") != "192.0.2.0/24" {
		t.Errorf("a list with a byte order mark: %v, %v", got, err)
	}
	// A port list is always a list.
	if _, err := read([]byte(`{"ports": ["80"]}`), model.AliasPorts); err == nil {
		t.Error("JSON was read as a port list")
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

	f := testFetcher()
	part, err := f.Inspect(context.Background(), srv.URL+"/ranges.json", false)
	if err != nil {
		t.Fatal(err)
	}
	if part != (Part{Source: srv.URL + "/ranges.json", Entries: 8}) {
		t.Errorf("JSON: %+v", part)
	}
	part, err = f.Inspect(context.Background(), srv.URL+"/drop.txt", false)
	if err != nil || part != (Part{Source: srv.URL + "/drop.txt", Entries: 2}) {
		t.Errorf("text: %+v, %v", part, err)
	}
	if _, err := f.Inspect(context.Background(), srv.URL+"/gone", false); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("missing: err = %v", err)
	}
}
