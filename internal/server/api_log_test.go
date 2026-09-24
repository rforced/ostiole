package server

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/fwlog"
)

// A log stream stays open for hours, so the session or token that opened
// it is looked at again on every keepalive, and the stream ends with it.
func TestLogStreamsEndWithTheirCaller(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/log/stream", "/api/v1/dns/queries/stream"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			srv := newTestServerWith(t, func(d *Deps) {
				tokens, err := auth.NewTokens(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				d.Tokens = tokens
				d.Log = fwlog.NewRing(16)
				d.QueryLog = dnslog.New()
				d.QueryLog.Slog = slog.New(slog.DiscardHandler)
				d.Keepalive = 5 * time.Millisecond
			})
			token := mintToken(t, srv, "look", string(auth.RoleViewer))

			// Signed in, the stream outlasts its checks.
			stream := openStream(t, srv, path, "")
			for range 2 {
				readUntil(t, stream, ": keepalive")
			}
			if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("logout: %d %s", resp.StatusCode, raw)
			}
			streamEnds(t, stream)

			// A token that is revoked is done with too, even with a
			// session cookie beside it.
			if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login",
				credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusOK {
				t.Fatalf("login: %d %s", resp.StatusCode, raw)
			}
			stream = openStream(t, srv, path, token)
			readUntil(t, stream, ": keepalive")
			if resp, raw := do(t, srv, http.MethodDelete, "/api/v1/tokens/look", nil); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("revoke: %d %s", resp.StatusCode, raw)
			}
			streamEnds(t, stream)
		})
	}
}

// openStream opens a server-sent event stream with the client's cookies,
// and a bearer token when one is given.
func openStream(t *testing.T, srv *httptest.Server, path, token string) *bufio.Reader {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %d", path, resp.StatusCode)
	}
	return bufio.NewReader(resp.Body)
}

func readUntil(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before %q: %v", want, err)
		}
		if strings.TrimSpace(line) == want {
			return
		}
	}
}

// streamEnds fails unless the server closes the stream, which with a
// keepalive of a few milliseconds is at once.
func streamEnds(t *testing.T, r *bufio.Reader) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Errorf("the stream ended with %v, want the server closing it", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived its caller")
	}
}
