package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// The policy promises "self only": no directive may name another origin,
// and scripts may not be inline. A loosened header would ship with the
// old test still green, which only asked that the header exist.
func TestSecurityHeadersAreSelfOnly(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(Deps{}))
	defer srv.Close()
	resp, _ := do(t, srv, http.MethodGet, "/api/v1/health", nil)
	csp := resp.Header.Get("Content-Security-Policy")
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		name, sources, _ := strings.Cut(d, " ")
		for _, src := range strings.Fields(sources) {
			switch src {
			case "'self'", "'none'", "data:":
			case "'unsafe-inline'":
				if name != "style-src" {
					t.Errorf("%s allows inline", name)
				}
			default:
				t.Errorf("%s names %q, which is not this origin", name, src)
			}
		}
	}
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "connect-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy lacks %q: %s", want, csp)
		}
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("headers = %v", resp.Header)
	}
}

// The session cookie is marked Secure exactly when it arrived over TLS:
// a plain-HTTP dev run still gets a cookie, a real router's is never
// sent in the clear.
func TestSessionCookieIsSecureOverTLS(t *testing.T) {
	t.Parallel()
	for _, tls := range []bool{true, false} {
		dir := t.TempDir()
		as, err := auth.NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		h := Handler(Deps{Engine: engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler)), Auth: as})
		var srv *httptest.Server
		if tls {
			srv = httptest.NewTLSServer(h)
		} else {
			srv = httptest.NewServer(h)
		}
		jar, _ := cookiejar.New(nil)
		srv.Client().Jar = jar
		resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("setup: %d %s", resp.StatusCode, raw)
		}
		var session *http.Cookie
		for _, c := range resp.Cookies() {
			if strings.Contains(strings.ToLower(c.Name), "session") || c.HttpOnly {
				session = c
			}
		}
		if session == nil {
			t.Fatalf("tls=%v: no session cookie in %v", tls, resp.Cookies())
		}
		if session.Secure != tls || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode && session.SameSite != http.SameSiteLaxMode {
			t.Errorf("tls=%v: cookie = Secure %v HttpOnly %v SameSite %v", tls, session.Secure, session.HttpOnly, session.SameSite)
		}
		srv.Close()
	}
}

// A bearer header is the credential when it is there. One that does not
// authenticate must not fall through to the cookie beside it: the CSRF
// guard stands down for a bearer request, so the cookie alone would let
// a cross-site request through.
func TestBadBearerDoesNotFallThroughToTheSession(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/config", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer not-a-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad bearer with a good cookie: %d, want 401", resp.StatusCode)
	}
	// The cookie on its own still works.
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/config", nil); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		t.Errorf("cookie alone: %d", resp.StatusCode)
	}
}

// Everything under /api answers in JSON, a wrong method on a known path
// included; the Allow header still says what would have worked.
func TestWrongMethodOnAnAPIPathIsJSON(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, raw := do(t, srv, http.MethodDelete, "/api/v1/health", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil || body["error"] == "" {
		t.Errorf("body = %q, err = %v", raw, err)
	}
	if !strings.Contains(resp.Header.Get("Allow"), http.MethodGet) {
		t.Errorf("Allow = %q", resp.Header.Get("Allow"))
	}
	// A page request outside /api is untouched.
	if resp, _ := do(t, srv, http.MethodGet, "/nowhere", nil); resp.StatusCode == http.StatusMethodNotAllowed {
		t.Error("the SPA fallback was turned into a 405")
	}
}

// The diagnostics run commands as root on whatever they are given. What
// they are given is checked first: a missing target, a hop count or port
// out of range, and a capture with no interface are the caller's mistake
// and say so, rather than running anything.
func TestDiagnosticsRefuseBadInput(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	cases := []struct {
		path string
		body any
	}{
		{"/api/v1/diagnostics/ping", pingRequest{Target: ""}},
		{"/api/v1/diagnostics/ping", pingRequest{Target: "9.9.9.9", Count: 99}},
		{"/api/v1/diagnostics/traceroute", traceRequest{Target: "9.9.9.9", MaxHops: 99}},
		{"/api/v1/diagnostics/capture", map[string]any{"interface": "", "seconds": 5}},
		{"/api/v1/diagnostics/capture", map[string]any{"interface": "eth0", "port": 70000}},
	}
	for _, c := range cases {
		resp, raw := do(t, srv, http.MethodPost, c.path, c.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s %v: %d %s, want 400", c.path, c.body, resp.StatusCode, raw)
		}
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/diagnostics/states?protocol=gre", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("states with an unknown protocol: %d, want 400", resp.StatusCode)
	}
}

// An upload that is not a backup is refused as the caller's mistake, with
// a message, not a crash or a 500.
func TestRestoreRefusesWhatIsNotABackup(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	for _, body := range []string{"not json", "{}", "{\"data\":\"bm9wZQ==\"}"} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/config/restore", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(RequestHeader, RequestHeaderValue)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%q: %d, want 400", body, resp.StatusCode)
		}
	}
}
