package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAllowed(t *testing.T) {
	t.Parallel()
	own := []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::7")}
	// What each rule says of an address: nil, or the refusal.
	type says struct{ anywhere, inside, public, typedPlain error }
	never := says{ErrNotAllowed, ErrNotAllowed, ErrNotAllowed, ErrNotAllowed}
	internet := says{nil, ErrPlainHTTP, nil, ErrPlainHTTP}
	network := says{nil, nil, ErrNotPublic, ErrNotPublic}
	for addr, want := range map[string]says{
		"1.1.1.1":              internet,
		"2606:4700:4700::1111": internet,
		"::ffff:1.1.1.1":       internet,
		"198.51.100.8":         internet,
		"198.51.100.7":         never,
		"2001:db8::7":          never,
		"127.0.0.1":            never,
		"::1":                  never,
		"::ffff:127.0.0.1":     never,
		"0.0.0.0":              never,
		"::":                   never,
		"169.254.169.254":      never,
		"fe80::1%eth0":         never,
		"224.0.0.1":            never,
		"255.255.255.255":      never,
		"ff02::1":              never,
		"192.168.1.1":          network,
		"172.16.0.1":           network,
		"::ffff:10.0.0.1":      network,
		"fd7a:115c:a1e0::1":    network,
		"100.64.0.1":           network,
		"100.100.100.100":      network,
	} {
		ip := netip.MustParseAddr(addr)
		got := says{allowed(anywhere, ip, own), allowed(inside, ip, own), allowed(public, ip, own), allowed(typedPlain, ip, own)}
		if got != want {
			t.Errorf("%s: %v, want %v", addr, got, want)
		}
	}
}

// Nothing is read from the router itself, however the URL writes it, and
// the error does not repeat what the URL carried.
func TestGetRefusesTheRouterItself(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	count := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	})
	plain := httptest.NewServer(count)
	defer plain.Close()
	secure := httptest.NewTLSServer(count)
	defer secure.Close()
	port := plain.URL[strings.LastIndex(plain.URL, ":"):]

	for _, u := range []string{
		plain.URL + "/list.txt",
		"http://[::ffff:127.0.0.1]" + port + "/list.txt",
		strings.Replace(secure.URL, "https://", "https://alice:hunter2@", 1) + "/list.txt?token=abc123",
	} {
		for _, reach := range []Reach{Named, Internet} {
			_, err := (*Getter)(nil).Get(context.Background(), u, "ostiole", reach)
			if !errors.Is(err, ErrNotAllowed) {
				t.Errorf("%s, reach %d: %v, want ErrNotAllowed", u, reach, err)
			}
			if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), "alice")) {
				t.Errorf("the error repeats a credential: %v", err)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the router was reached %d times", n)
	}
}

// pretend has the loopback stand for an address inside this network, or
// on the internet, and holds it to the real rules.
func pretend(srv *httptest.Server, inner bool) *Getter {
	stand := netip.MustParseAddr("198.51.100.1")
	if inner {
		stand = netip.MustParseAddr("192.168.1.10")
	}
	conf := &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	return newGetter(func(r rule, ip netip.Addr) error {
		if ip.IsLoopback() {
			ip = stand
		}
		return allowed(r, ip, nil)
	}, conf)
}

func TestRedirectsAreHeldToTheRulesHopByHop(t *testing.T) {
	t.Parallel()
	var listed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/list.txt", func(w http.ResponseWriter, _ *http.Request) {
		listed.Add(1)
		_, _ = w.Write([]byte("192.0.2.0/24\n"))
	})
	plain := httptest.NewServer(mux)
	defer plain.Close()
	secure := httptest.NewTLSServer(mux)
	defer secure.Close()
	mux.HandleFunc("/to-plain", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/list.txt", http.StatusFound)
	})
	mux.HandleFunc("/to-secure", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, secure.URL+"/list.txt", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/list.txt", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})

	inside, internet := pretend(secure, true), pretend(secure, false)
	for _, c := range []struct {
		name  string
		g     *Getter
		url   string
		reach Reach
		want  error
	}{
		{"a list inside over plain http", inside, plain.URL + "/moved", Named, nil},
		{"plain http up to https", inside, plain.URL + "/to-secure", Named, nil},
		{"https down to plain http", inside, secure.URL + "/to-plain", Named, ErrPlainHTTP},
		{"plain http from the internet", internet, plain.URL + "/list.txt", Named, ErrPlainHTTP},
		{"a list on the internet over https", internet, secure.URL + "/moved", Named, nil},
		{"a typed URL inside", inside, secure.URL + "/list.txt", Internet, ErrNotPublic},
		{"a typed URL on the internet", internet, secure.URL + "/moved", Internet, nil},
		{"a typed URL over plain http", internet, plain.URL + "/list.txt", Internet, ErrPlainHTTP},
		{"a typed URL inside over plain http", inside, plain.URL + "/list.txt", Internet, ErrNotPublic},
	} {
		before := listed.Load()
		resp, err := c.g.Get(context.Background(), c.url, "ostiole", c.reach)
		if resp != nil {
			_ = resp.Body.Close()
		}
		switch {
		case c.want == nil && (err != nil || resp.StatusCode != http.StatusOK):
			t.Errorf("%s: %v, want the list", c.name, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		case c.want != nil && listed.Load() != before:
			t.Errorf("%s: the list was read anyway", c.name)
		}
	}

	_, err := inside.Get(context.Background(), secure.URL+"/loop", "ostiole", Named)
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("a loop: %v", err)
	}
}

// Inside reads a test's loopback server as a list inside the network:
// plain http and https alike once applied, never for whoever may only
// read the internet.
func TestInsideTakesTheLoopbackForTheNetwork(t *testing.T) {
	t.Parallel()
	var agent atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		agent.Store(r.UserAgent())
	}))
	defer srv.Close()
	resp, err := Inside().Get(context.Background(), srv.URL, "ostiole", Named)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || agent.Load() != "ostiole" {
		t.Errorf("status %d, User-Agent %v", resp.StatusCode, agent.Load())
	}
	secure := strings.Replace(srv.URL, "http://", "https://", 1)
	if _, err := Inside().Get(context.Background(), secure, "ostiole", Internet); !errors.Is(err, ErrNotPublic) {
		t.Errorf("for a typed URL: %v, want ErrNotPublic", err)
	}
}
