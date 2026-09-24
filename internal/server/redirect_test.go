package server

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRedirectServer layers the listeners as Run does: TLS over the
// redirect over TCP.
func newRedirectServer(t *testing.T, logger *slog.Logger) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(Handler(Deps{}))
	srv.Listener = redirectListener{srv.Listener}
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = log.New(serverLog{logger}, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// plainRequest writes raw to addr and reads the answer.
func plainRequest(t *testing.T, addr, raw string) (*http.Response, error) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err == nil {
		resp.Body.Close()
	}
	return resp, err
}

func TestPlainHTTPIsRedirectedToHTTPS(t *testing.T) {
	t.Parallel()
	srv := newRedirectServer(t, slog.New(slog.DiscardHandler))
	addr := srv.Listener.Addr().String()
	resp, err := srv.Client().Get("http://" + addr + "/api/v1/health?x=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.String() != "https://"+addr+"/api/v1/health?x=1" || resp.ProtoMajor != 2 {
		t.Errorf("ended at %s with %d over %s", resp.Request.URL, resp.StatusCode, resp.Proto)
	}
}

func TestPlainHTTPRedirectTarget(t *testing.T) {
	t.Parallel()
	srv := newRedirectServer(t, slog.New(slog.DiscardHandler))
	addr := srv.Listener.Addr().String()
	for _, tc := range []struct{ name, req, want string }{
		{"host and port kept", "GET /rules?tab=nat HTTP/1.1\r\nHost: fw.lan:9443\r\n\r\n", "https://fw.lan:9443/rules?tab=nat"},
		{"no host", "GET /x HTTP/1.0\r\n\r\n", "https://" + addr + "/x"},
		{"bad host", "GET / HTTP/1.1\r\nHost: evil.example/phish\r\n\r\n", "https://" + addr + "/"},
		{"no path", "OPTIONS * HTTP/1.1\r\nHost: fw.lan:9443\r\n\r\n", "https://fw.lan:9443/"},
	} {
		resp, err := plainRequest(t, addr, tc.req)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := resp.Header.Get("Location"); resp.StatusCode != http.StatusTemporaryRedirect || got != tc.want {
			t.Errorf("%s: %d to %q, want 307 to %q", tc.name, resp.StatusCode, got, tc.want)
		}
	}
}

// A redirect is routine and logs at debug. A connection that is neither
// TLS nor HTTP gets no answer and still logs at error.
func TestRedirectLogsBelowError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	srv := newRedirectServer(t, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	addr := srv.Listener.Addr().String()
	if _, err := plainRequest(t, addr, "GET / HTTP/1.1\r\nHost: fw.lan\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if resp, err := plainRequest(t, addr, "\x00\r\n\r\n"); err == nil {
		t.Errorf("neither TLS nor HTTP answered %d", resp.StatusCode)
	}
	// Close waits for both connections, which log before they close.
	srv.Close()
	got := buf.String()
	if !strings.Contains(got, `level=DEBUG msg="http: TLS handshake error`) || strings.Count(got, "level=ERROR") != 1 {
		t.Errorf("log:\n%s", got)
	}
}
