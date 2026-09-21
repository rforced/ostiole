package acme

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTP01AnswersOnlyWhatWasPresented(t *testing.T) {
	t.Parallel()
	// Port 0 so the test does not need root and does not collide.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	h := &HTTP01{Addr: func() string { return addr }}
	if err := h.Present("router.example.test", "tok", "tok.key"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for path, want := range map[string]int{
		challengePath + "tok":   http.StatusOK,
		challengePath + "other": http.StatusNotFound,
		"/":                     http.StatusNotFound,
		"/index.html":           http.StatusNotFound,
	} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, resp.StatusCode, want)
		}
		if want == http.StatusOK && string(body) != "tok.key" {
			t.Errorf("GET %s = %q, want the key authorisation", path, body)
		}
	}

	// A second challenge shares the listener, and the last cleanup closes
	// it: nothing is on the port the rest of the time.
	if err := h.Present("other.example.test", "two", "two.key"); err != nil {
		t.Fatal(err)
	}
	if err := h.CleanUp("router.example.test", "tok", "tok.key"); err != nil {
		t.Fatal(err)
	}
	if resp, err := client.Get("http://" + addr + challengePath + "two"); err != nil {
		t.Fatalf("the listener went away too early: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	if err := h.CleanUp("other.example.test", "two", "two.key"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get("http://" + addr + challengePath + "two"); err == nil {
		t.Error("the listener is still up after the last cleanup")
	}
}

// Addr is read when the listener binds, not when the solver is built, so
// a proxy switched on between two orders moves the next one to loopback
// without the daemon being restarted.
func TestHTTP01ReadsTheAddressAtEveryBind(t *testing.T) {
	t.Parallel()
	first, second := freeAddr(t), freeAddr(t)
	where := first
	h := &HTTP01{Addr: func() string { return where }}
	client := &http.Client{Timeout: 5 * time.Second}

	if err := h.Present("a.example.test", "a", "a.key"); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get("http://" + first + challengePath + "a")
	if err != nil {
		t.Fatalf("GET on the first address: %v", err)
	}
	_ = resp.Body.Close()
	if err := h.CleanUp("a.example.test", "a", "a.key"); err != nil {
		t.Fatal(err)
	}

	where = second
	if err := h.Present("b.example.test", "b", "b.key"); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.CleanUp("b.example.test", "b", "b.key") }()
	resp, err = client.Get("http://" + second + challengePath + "b")
	if err != nil {
		t.Fatalf("the next order did not move to the new address: %v", err)
	}
	_ = resp.Body.Close()
	if _, err := client.Get("http://" + first + challengePath + "b"); err == nil {
		t.Error("the old address is still answering")
	}
}

// freeAddr is a loopback address nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestHTTP01ReportsABindFailure(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	h := &HTTP01{Addr: func() string { return l.Addr().String() }}
	if err := h.Present("router.example.test", "tok", "tok.key"); err == nil {
		t.Fatal("binding a port something else holds was not reported")
	}
	// And the token was not left behind on a listener that never came up.
	if err := h.CleanUp("router.example.test", "tok", "tok.key"); err != nil {
		t.Fatal(err)
	}
}

// Two orders at once share the listener: the second Present does not bind
// again, the first CleanUp leaves the port up, and the last one takes it
// down so a third order can bind afresh.
func TestHTTP01ServesTwoChallengesOnOneListener(t *testing.T) {
	t.Parallel()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	h := &HTTP01{Addr: func() string { return addr }}
	client := &http.Client{Timeout: 5 * time.Second}
	status := func(token string) int {
		t.Helper()
		resp, err := client.Get("http://" + addr + challengePath + token)
		if err != nil {
			return 0
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if err := h.Present("a.example.test", "a", "a.key"); err != nil {
		t.Fatal(err)
	}
	if err := h.Present("b.example.test", "b", "b.key"); err != nil {
		t.Fatalf("second Present on the same listener: %v", err)
	}
	if status("a") != http.StatusOK || status("b") != http.StatusOK {
		t.Error("both challenges should be answered")
	}
	if err := h.CleanUp("a.example.test", "a", "a.key"); err != nil {
		t.Fatal(err)
	}
	if status("a") != http.StatusNotFound || status("b") != http.StatusOK {
		t.Error("cleaning up one challenge should leave the other answered")
	}
	if err := h.CleanUp("b.example.test", "b", "b.key"); err != nil {
		t.Fatal(err)
	}
	if status("b") != 0 {
		t.Error("the listener should be gone with the last challenge")
	}
	if err := h.Present("c.example.test", "c", "c.key"); err != nil {
		t.Fatalf("Present after the listener closed: %v", err)
	}
	if status("c") != http.StatusOK {
		t.Error("the listener should bind again for a new order")
	}
	if err := h.CleanUp("c.example.test", "c", "c.key"); err != nil {
		t.Fatal(err)
	}
}
