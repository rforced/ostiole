package modem

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hitronPages serves the fixture captured from a CODA, the way the modem
// does: HTTP/1.0, text/html, no login.
func hitronPages(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/data/") {
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		raw, err := os.ReadFile(filepath.Join("testdata", "hitron-coda", filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(raw)
	})
}

// address turns a test server's URL into the host:port a page would ask for.
func address(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestHitronOverHTTPS(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(hitronPages(t))
	defer srv.Close()
	st, err := fetchWith(context.Background(), newHTTPClient(), address(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "Hitron" || st.Model != "CODA" || st.Firmware != "7.3.5.3.2b1" || st.MAC != "02:1a:2b:00:00:aa" {
		t.Errorf("identity = %+v", *st)
	}
	if !st.Link.Up || st.Link.Speed != "2500Mbps" {
		t.Errorf("link = %+v", st.Link)
	}
	if len(st.Provisioning) != 10 || st.Provisioning[0].Name != "Hardware" || st.Provisioning[9].Name != "Traffic" {
		t.Fatalf("steps = %+v", st.Provisioning)
	}
	for _, s := range st.Provisioning {
		if !s.OK {
			t.Errorf("step %s = %q, want ok", s.Name, s.Status)
		}
	}
	// 32 QAM channels and one OFDM block in use; the idle receiver is left out.
	if len(st.Downstream) != 33 {
		t.Fatalf("downstream = %d channels", len(st.Downstream))
	}
	first := st.Downstream[0]
	if first.Channel != 1 || first.Kind != "qam" || first.Frequency != 447000000 || first.Modulation != "QAM256" || first.Power != 5.7 || first.SNR < 40 || first.SNR > 41 {
		t.Errorf("first downstream = %+v", first)
	}
	ofdm := st.Downstream[32]
	if ofdm.Kind != "ofdm" || ofdm.Channel != 2 || ofdm.Frequency != 659600000 || !ofdm.Locked || ofdm.Modulation != "OFDM 4K" || ofdm.Corrected != 75915354 {
		t.Errorf("ofdm = %+v", ofdm)
	}
	// 5 upstream channels; both OFDMA channels are disabled and left out.
	if len(st.Upstream) != 5 {
		t.Fatalf("upstream = %+v", st.Upstream)
	}
	up := st.Upstream[0]
	if up.Channel != 10 || up.Frequency != 29200000 || up.Bandwidth != 6400000 || up.Modulation != "64QAM" || up.Mode != "ATDMA" || up.Power != 41.021 {
		t.Errorf("first upstream = %+v", up)
	}
	if st.Address != address(t, srv) || st.FetchedAt.IsZero() {
		t.Errorf("address/fetched = %q %v", st.Address, st.FetchedAt)
	}
}

// A modem that speaks only plain HTTP is found after the HTTPS attempt fails.
func TestFallsBackToHTTP(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(hitronPages(t))
	defer srv.Close()
	st, err := fetchWith(context.Background(), newHTTPClient(), address(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "CODA" {
		t.Errorf("model = %q", st.Model)
	}
}

// A web server that is not a modem's is "no known modem", not a crash;
// an address nobody listens on says so too, with the reason.
func TestNotAModem(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()
	if _, err := fetchWith(context.Background(), newHTTPClient(), address(t, srv)); !errors.Is(err, ErrNoModem) {
		t.Errorf("html server: err = %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	_, err = fetchWith(context.Background(), newHTTPClient(), closed)
	if !errors.Is(err, ErrNoModem) || !strings.Contains(err.Error(), "refused") {
		t.Errorf("closed port: err = %v", err)
	}
}

// The address has to be a private IP: the router must not become a way
// to read pages on the internet.
func TestAddressIsPrivate(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "modem", "1.1.1.1", "8.8.8.8:80", "2001:db8::1", "192.168.100.1:x"} {
		if _, err := fetchWith(context.Background(), newHTTPClient(), bad); !errors.Is(err, ErrBadAddress) {
			t.Errorf("%q: err = %v, want ErrBadAddress", bad, err)
		}
	}
	for _, ok := range []string{"192.168.100.1", "10.0.0.1:8080", "fe80::1", "[fd00::1]:80", "127.0.0.1"} {
		if err := checkAddress(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

// A page refreshed twice in a minute reads the modem once; a refresh or
// an old status reads it again; a failure keeps nothing.
func TestCacheReadsOnceAMinute(t *testing.T) {
	t.Parallel()
	var reads int
	var fail bool
	c := NewCache(time.Minute)
	c.fetch = func(_ context.Context, address string) (*Status, error) {
		reads++
		if fail {
			return nil, errors.New("down")
		}
		return &Status{Address: address, FetchedAt: time.Now()}, nil
	}
	ctx := context.Background()
	for range 3 {
		if _, err := c.Get(ctx, "192.168.100.1", false); err != nil {
			t.Fatal(err)
		}
	}
	if reads != 1 {
		t.Errorf("reads = %d after three gets, want 1", reads)
	}
	if _, err := c.Get(ctx, "192.168.100.1", true); err != nil || reads != 2 {
		t.Errorf("refresh: err = %v, reads = %d", err, reads)
	}
	c.last["192.168.100.1"].FetchedAt = time.Now().Add(-2 * time.Minute)
	if _, err := c.Get(ctx, "192.168.100.1", false); err != nil || reads != 3 {
		t.Errorf("stale: err = %v, reads = %d", err, reads)
	}
	fail = true
	if _, err := c.Get(ctx, "192.168.100.1", true); err == nil {
		t.Error("a failed read was not reported")
	}
	if c.last["192.168.100.1"] == nil {
		t.Error("the last good status was dropped by a failed read")
	}
	if _, err := c.Get(ctx, "8.8.8.8", false); !errors.Is(err, ErrBadAddress) {
		t.Errorf("public address through the cache: %v", err)
	}
}
