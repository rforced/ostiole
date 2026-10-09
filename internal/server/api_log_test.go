package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/dnslog"
	"ostiole/internal/fwlog"
	"ostiole/internal/model"
	"ostiole/internal/waflog"
)

// A log stream stays open for hours, so the session or token that opened
// it is looked at again on every keepalive, and the stream ends with it.
func TestLogStreamsEndWithTheirCaller(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/v1/log/stream", "/api/v1/dns/queries/stream", "/api/v1/proxy/events/stream"} {
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
				d.WAFLog = waflog.New()
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

// Each stream holds a goroutine and a buffer while it lasts, so one caller
// holds maxStreams at most, and gets a place back when one closes.
func TestACallerHoldsSoManyStreams(t *testing.T) {
	t.Parallel()
	srv := newTestServerWith(t, func(d *Deps) {
		tokens, err := auth.NewTokens(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		d.Tokens = tokens
		d.Log = fwlog.NewRing(16)
		d.Keepalive = time.Hour
	})
	token := mintToken(t, srv, "look", string(auth.RoleViewer))
	open := func() *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/log/stream", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	held := make([]*http.Response, 0, maxStreams)
	for range maxStreams {
		resp := open()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d", len(held)+1, resp.StatusCode)
		}
		held = append(held, resp)
	}
	if resp := open(); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("one past the cap: %d, want 429", resp.StatusCode)
	}
	// The signed-in admin is counted apart from the token.
	openStream(t, srv, "/api/v1/log/stream", "")
	_ = held[0].Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := open()
		if resp.StatusCode == http.StatusOK {
			break
		}
		_ = resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("a closed stream never gave its place back")
		}
		time.Sleep(10 * time.Millisecond)
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

// nextData reads the next event off a stream, past keepalives and the
// blank lines between events.
func nextData(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before an event: %v", err)
		}
		if data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: "); ok {
			return data
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

// logServer is a signed-in server with a firewall log holding packets of
// each verdict, one of them a line of the proxy's access list.
func logServer(t *testing.T) *httptest.Server {
	t.Helper()
	ring := fwlog.NewRing(100)
	srv := newTestServerWith(t, func(d *Deps) { d.Log = ring })
	cfg := starter()
	cfg.Services.Proxy = model.Proxy{Access: []model.ProxyAccess{{ID: "access-x1", Zone: "wan",
		Action: model.ActionDrop, Ports: []string{model.ProxyPortHTTPS}, Description: "Scanners"}}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	for i, e := range []fwlog.Entry{
		{Kind: "rule", RuleID: "web-in", Action: "accept", Proto: "tcp", Src: "203.0.113.9", SrcPort: 40000, Dst: "198.51.100.2", DstPort: 443},
		{Kind: "zone-drop", Zone: "wan", Action: "drop", Proto: "udp", Src: "198.18.0.1", Dst: "198.51.100.2", DstPort: 161},
		{Kind: "proxy", RuleID: "access-x1", Action: "drop", Proto: "tcp", Src: "192.0.2.66", Dst: "198.51.100.2", DstPort: 443},
		{Kind: "rule", RuleID: "old-rule", Proto: "icmp"},
	} {
		e.Time = time.Now().Add(time.Duration(i) * time.Millisecond)
		ring.Add(e)
	}
	return srv
}

func TestLogEntriesSearchesWhatTheRowShows(t *testing.T) {
	t.Parallel()
	srv := logServer(t)
	read := func(query string) []fwlog.Entry {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/log/entries"+query, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", query, resp.StatusCode, raw)
		}
		var page struct {
			Entries []fwlog.Entry `json:"entries"`
			Held    int           `json:"held"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if page.Held != 4 {
			t.Errorf("%s: held %d", query, page.Held)
		}
		return page.Entries
	}
	ids := func(es []fwlog.Entry) string {
		var out []string
		for _, e := range es {
			out = append(out, e.Kind+":"+e.RuleID+e.Zone)
		}
		return strings.Join(out, " ")
	}
	for query, want := range map[string]string{
		"":                            "rule:old-rule proxy:access-x1 zone-drop:wan rule:web-in",
		"?show=blocked":               "rule:old-rule proxy:access-x1 zone-drop:wan",
		"?show=allowed":               "rule:old-rule rule:web-in",
		"?q=scanners":                 "proxy:access-x1",
		"?q=wan+default":              "zone-drop:wan",
		"?q=203.0.113.9:40000":        "rule:web-in",
		"?q=unknown":                  "rule:old-rule",
		"?q=src":                      "",
		"?q=443&show=blocked&limit=1": "proxy:access-x1",
	} {
		if got := ids(read(query)); got != want {
			t.Errorf("%q: %q, want %q", query, got, want)
		}
	}
}

func TestLogEntriesRefusesNonsense(t *testing.T) {
	t.Parallel()
	srv := logServer(t)
	for _, query := range []string{"?show=some", "?limit=1001", "?before=-1"} {
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/log/entries"+query, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s", query, resp.StatusCode, raw)
		}
	}
}
