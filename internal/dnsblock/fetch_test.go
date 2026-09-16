package dnsblock

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

func serve(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestFetchReadsEachPublishedShape(t *testing.T) {
	cases := []struct {
		name string
		body string
		want model.ListFormat
	}{
		{
			name: "steven black hosts",
			body: "# Title: Unified hosts\n127.0.0.1 localhost\n::1 localhost\n0.0.0.0 ads.example.com\n0.0.0.0 tracker.example.net\n",
			want: model.FormatHosts,
		},
		{
			name: "oisd wildcard domains",
			body: "! Title: oisd\n*.ads.example.com\n*.tracker.example.net\n",
			want: model.FormatDomains,
		},
		{
			name: "hagezi dnsmasq",
			body: "# Title\nlocal=/ads.example.com/\nlocal=/tracker.example.net/\n",
			want: model.FormatDnsmasq,
		},
		{
			name: "adguard adblock",
			body: "[Adblock Plus]\n! Title\n||ads.example.com^\n||tracker.example.net^\n@@||good.example.com^\n",
			want: model.FormatAdblock,
		},
		{
			name: "unbound local zones",
			body: `local-zone: "ads.example.com." always_nxdomain` + "\n" + `local-zone: "tracker.example.net." always_nxdomain` + "\n",
			want: model.FormatUnbound,
		},
	}
	f := NewFetcher("test")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			url := serve(t, c.body)
			domains, _, format, err := f.Fetch(context.Background(), model.BlockList{Name: "l", URL: url})
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if format != c.want {
				t.Errorf("format = %s; want %s", format, c.want)
			}
			got := map[string]bool{}
			for _, d := range domains {
				got[d] = true
			}
			for _, want := range []string{"ads.example.com", "tracker.example.net"} {
				if !got[want] {
					t.Errorf("missing %s; got %v", want, domains)
				}
			}
			if got["localhost"] {
				t.Error("localhost was taken from a hosts file as a name to block")
			}
			if got["good.example.com"] {
				t.Error("an adblock exception rule was read as something to block")
			}
		})
	}
}

// An error page is what a dead URL usually returns, and it must not quietly
// replace yesterday's list with nothing.
func TestFetchRefusesAListWithNothingInIt(t *testing.T) {
	url := serve(t, "<html><body><h1>404 Not Found</h1></body></html>")
	f := NewFetcher("test")
	_, _, _, err := f.Fetch(context.Background(), model.BlockList{Name: "l", URL: url})
	if err == nil {
		t.Fatal("an HTML error page was accepted as a blocklist")
	}
	if !strings.Contains(err.Error(), "nothing usable") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestFetchReportsHTTPFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	f := NewFetcher("test")
	_, _, _, err := f.Fetch(context.Background(), model.BlockList{Name: "l", URL: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v; want one naming HTTP 503", err)
	}
}

func TestFetchHonoursTheDeclaredFormat(t *testing.T) {
	// Written as hosts, but declared as domains: the second field is not a
	// name to block, so nothing usable comes out rather than nonsense.
	url := serve(t, "0.0.0.0 ads.example.com\n0.0.0.0 tracker.example.net\n")
	f := NewFetcher("test")
	_, _, _, err := f.Fetch(context.Background(), model.BlockList{Name: "l", URL: url, Format: model.FormatDomains})
	if err == nil {
		t.Fatal("a hosts file read as a domain list produced entries")
	}
}

func TestStoreAndRefreshRoundTrip(t *testing.T) {
	url := serve(t, "0.0.0.0 ads.example.com\n0.0.0.0 deep.ads.example.com\n0.0.0.0 other.example.org\n")
	cfg := &model.Config{}
	cfg.Services.DNS.Enabled = true
	cfg.Blocking = model.Blocking{
		Enabled: true,
		Lists:   []model.BlockList{{Name: "ads", Enabled: true, URL: url}},
	}
	loader := &recordingLoader{}
	r := &Refresher{
		Cache:   NewCache(t.TempDir()),
		Fetcher: NewFetcher("test"),
		Source:  func() *model.Config { return cfg },
		Loader:  loader,
	}
	r.Tick(context.Background(), true)

	m, ok := r.Cache.Meta("ads")
	if !ok {
		t.Fatal("nothing was cached")
	}
	// deep.ads.example.com is dropped: its parent already covers it.
	if m.Domains != 2 {
		t.Errorf("cached %d names; want 2", m.Domains)
	}
	if loader.loads != 1 {
		t.Errorf("installed %d times; want once", loader.loads)
	}
	if !strings.Contains(loader.last, "local=/ads.example.com/") {
		t.Errorf("the list did not reach the loader:\n%s", loader.last)
	}

	// Fetching the same unchanged list again must not install anything:
	// installing means restarting dnsmasq, and DNS stops while it does.
	r.Tick(context.Background(), true)
	if loader.loads != 1 {
		t.Errorf("a list that had not changed was installed again (%d times)", loader.loads)
	}
}

type recordingLoader struct {
	loads int
	last  string
}

func (l *recordingLoader) Load(_ context.Context, fn func(w io.Writer) error) (bool, error) {
	var b strings.Builder
	if err := fn(&b); err != nil {
		return false, err
	}
	if b.String() == l.last {
		return false, nil
	}
	l.last = b.String()
	l.loads++
	return true, nil
}
