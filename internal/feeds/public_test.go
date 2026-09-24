package feeds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

// What an operator's URL may reach: the internet, and nothing of the
// router's own or behind it.
func TestPublicAddresses(t *testing.T) {
	t.Parallel()
	own := []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::7")}
	for addr, want := range map[string]bool{
		"1.1.1.1":              true,
		"2606:4700:4700::1111": true,
		"::ffff:1.1.1.1":       true,
		"198.51.100.8":         true,
		"198.51.100.7":         false,
		"2001:db8::7":          false,
		"127.0.0.1":            false,
		"::1":                  false,
		"::ffff:127.0.0.1":     false,
		"0.0.0.0":              false,
		"::":                   false,
		"10.130.0.1":           false,
		"172.16.0.1":           false,
		"192.168.100.1":        false,
		"::ffff:10.0.0.1":      false,
		"169.254.169.254":      false,
		"fe80::1%eth0":         false,
		"fd7a:115c:a1e0::1":    false,
		"100.64.0.1":           false,
		"100.100.100.100":      false,
		"224.0.0.1":            false,
		"255.255.255.255":      false,
		"ff02::1":              false,
	} {
		if got := public(netip.MustParseAddr(addr), own); got != want {
			t.Errorf("%s: public = %v, want %v", addr, got, want)
		}
	}
}

// A URL on the router itself is refused before anything connects to it,
// however the address is written.
func TestInspectPublicOnlyRefusesTheRouter(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]

	f := NewFetcher("test")
	for _, u := range []string{srv.URL + "/list.txt", "http://[::ffff:127.0.0.1]" + port + "/list.txt"} {
		if _, err := f.Inspect(context.Background(), u, true); !errors.Is(err, ErrNotPublic) || err.Error() != ErrNotPublic.Error() {
			t.Errorf("%s: err = %v, want ErrNotPublic alone", u, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
	if part, err := f.Inspect(context.Background(), srv.URL+"/list.txt", false); err != nil || part.Entries != 1 {
		t.Errorf("unrestricted: %+v, %v", part, err)
	}
}
