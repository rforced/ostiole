package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

const testPassword = "correct horse battery"

// newTestServer returns a server whose client is already logged in as
// "admin" and sends the CSRF header.
func newTestServer(t *testing.T) (*httptest.Server, *nfttest.Fake) {
	t.Helper()
	srv, fake := newUnauthenticatedServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, fake
}

func newUnauthenticatedServer(t *testing.T) (*httptest.Server, *nfttest.Fake) {
	t.Helper()
	dir := t.TempDir()
	fake := &nfttest.Fake{TableJSON: `{"nftables":[{"rule":{"chain":"zone_lan","comment":"id:allow-lan","expr":[{"counter":{"packets":1,"bytes":2}}]}}]}`}
	eng := engine.New(store.New(dir), fake, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	return srv, fake
}

func do(t *testing.T, srv *httptest.Server, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(RequestHeader, RequestHeaderValue)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func starter() *model.Config {
	return model.Starter(model.StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0"})
}

func TestHealth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(Deps{}))
	defer srv.Close()

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("missing Content-Security-Policy")
	}
	// The test client is on the loopback, which is where the updater's
	// own probe comes from, so the build is reported.
	if body.Version == "" {
		t.Error("the loopback was told no version")
	}
}

// A version number is what tells a scanner which bugs to try, so it goes
// only to the loopback, a session, or a token.
func TestHealthKeepsTheVersionFromStrangers(t *testing.T) {
	t.Parallel()
	srv, as, tokens := roleServer(t)
	a := &api{auth: as, tokens: tokens}

	probe := func(remote string, auth func(*http.Request)) healthResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		req.RemoteAddr = remote
		if auth != nil {
			auth(req)
		}
		rec := httptest.NewRecorder()
		a.health(rec, req)
		var body healthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "ok" {
			t.Errorf("status = %q", body.Status)
		}
		return body
	}

	if got := probe("203.0.113.5:44321", nil); got.Version != "" || got.Commit != "" {
		t.Errorf("a stranger was told %+v", got)
	}
	if got := probe("127.0.0.1:44321", nil); got.Version == "" {
		t.Error("the loopback was told no version")
	}
	secret := mintToken(t, srv, "monitor", "viewer")
	withTok := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
	if got := probe("203.0.113.5:44321", withTok); got.Version == "" {
		t.Error("a token holder was told no version")
	}
	// A token that may only fetch a certificate is not inside.
	_, restricted, err := tokens.Create("proxy", auth.RoleAdmin, 0, "test", []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	withRestricted := func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+restricted) }
	if got := probe("203.0.113.5:44321", withRestricted); got.Version != "" || got.Commit != "" {
		t.Errorf("a restricted token was told %+v", got)
	}
}

func TestUnknownAPIRouteIsJSON404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(Deps{}))
	defer srv.Close()

	resp, _ := do(t, srv, http.MethodGet, "/api/v1/nope", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", ct)
	}
}

func TestEngineEndpointsWithoutDeps(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(Handler(Deps{}))
	defer srv.Close()
	resp, _ := do(t, srv, http.MethodGet, "/api/v1/status", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	resp, _ = do(t, srv, http.MethodGet, "/api/v1/setup", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("setup = %d, want 503", resp.StatusCode)
	}
}

func TestAuthFlow(t *testing.T) {
	t.Parallel()
	srv, _ := newUnauthenticatedServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/setup", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"needed":true`) {
		t.Fatalf("setup status: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/status", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status without session: %d, want 401", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("login before setup: %d, want 401", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: "short"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("weak password: %d, want 422", resp.StatusCode)
	}
	resp, _ = do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" {
		t.Fatalf("session cookie = %+v", cookie)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "x", Password: testPassword}); resp.StatusCode != http.StatusConflict {
		t.Errorf("second setup: %d, want 409", resp.StatusCode)
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"username":"admin"`) {
		t.Fatalf("me: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/status", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("status with session: %d", resp.StatusCode)
	}

	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/logout", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/status", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status after logout: %d, want 401", resp.StatusCode)
	}

	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: "wrong password!"}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad login: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Errorf("good login: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/status", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("status after login: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	for range auth.MaxFailures {
		do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: "wrong password!"})
	}
	resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

func TestCSRFGuard(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	body := func() io.Reader { return strings.NewReader(`{"username":"admin","password":"` + testPassword + `"}`) }

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", body())
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("no header: %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", body())
	req.Header.Set(RequestHeader, RequestHeaderValue)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", body())
	req.Header.Set(RequestHeader, RequestHeaderValue)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, _ = srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site: %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/auth/login", body())
	req.Header.Set(RequestHeader, RequestHeaderValue)
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, _ = srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same origin: %d, want 200", resp.StatusCode)
	}

	// GETs are never blocked by the guard.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/health", nil)
	resp, _ = srv.Client().Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET without header: %d", resp.StatusCode)
	}
}

func TestApplyImmediateFlow(t *testing.T) {
	t.Parallel()
	srv, fake := newTestServer(t)

	resp, _ := do(t, srv, http.MethodGet, "/api/v1/config", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("config before apply: %d, want 404", resp.StatusCode)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: starter()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	var res engine.ApplyResult
	if err := json.Unmarshal(raw, &res); err != nil || res.Pending || res.Ruleset == "" {
		t.Fatalf("apply result = %+v, %v", res, err)
	}
	if fake.Last() != res.Ruleset {
		t.Error("runner did not receive the ruleset")
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/config", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"hostname":"fw"`) {
		t.Fatalf("config after apply: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/ruleset", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != res.Ruleset {
		t.Fatalf("ruleset: %d", resp.StatusCode)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/status", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"tableLoaded":true`) {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/counters", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"allow-lan"`) {
		t.Fatalf("counters: %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/config/revisions", nil)
	if resp.StatusCode != http.StatusOK || string(bytes.TrimSpace(raw)) != "[]" {
		t.Fatalf("revisions: %d %s", resp.StatusCode, raw)
	}
}

func TestApplyConfirmAndRevertFlow(t *testing.T) {
	t.Parallel()
	srv, fake := newTestServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: starter()}); resp.StatusCode != http.StatusOK {
		t.Fatalf("first apply: %d %s", resp.StatusCode, raw)
	}
	second := starter()
	second.System.Hostname = "second"
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: second, ConfirmTimeoutSeconds: 60})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"pending":true`) {
		t.Fatalf("pending apply: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: second}); resp.StatusCode != http.StatusConflict {
		t.Errorf("apply while pending: %d, want 409", resp.StatusCode)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply/confirm", nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"archived"`) {
		t.Fatalf("confirm: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply/confirm", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("double confirm: %d, want 409", resp.StatusCode)
	}
	_, raw = do(t, srv, http.MethodGet, "/api/v1/config/revisions", nil)
	var revs []store.Revision
	if err := json.Unmarshal(raw, &revs); err != nil || len(revs) != 1 {
		t.Fatalf("revisions = %s (%v)", raw, err)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/config/revisions/"+revs[0].ID, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"hostname":"fw"`) {
		t.Fatalf("revision: %d %s", resp.StatusCode, raw)
	}

	third := starter()
	third.System.Hostname = "third"
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: third, ConfirmTimeoutSeconds: 60}); resp.StatusCode != http.StatusOK {
		t.Fatal("third apply")
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/apply/revert", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revert: %d", resp.StatusCode)
	}
	if !strings.Contains(fake.Last(), "second") && !strings.Contains(fake.Last(), "table inet ostiole {") {
		t.Error("revert did not restore a full ruleset")
	}
	_, raw = do(t, srv, http.MethodGet, "/api/v1/config", nil)
	if !strings.Contains(string(raw), `"hostname":"second"`) {
		t.Errorf("config after revert: %s", raw)
	}
}

func TestApplyValidationAndBadRequests(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: &model.Config{Version: model.SchemaVersion}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid config: %d %s", resp.StatusCode, raw)
	}
	var er errorResponse
	if err := json.Unmarshal(raw, &er); err != nil || len(er.Issues) == 0 || er.Error != "invalid configuration" {
		t.Fatalf("error body = %s", raw)
	}

	for _, body := range []string{`{}`, `not json`, `{"config":{},"bogus":1}`, `{"config":{"version":1},"confirmTimeoutSeconds":-1}`} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/apply", strings.NewReader(body))
		req.Header.Set(RequestHeader, RequestHeaderValue)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: %d, want 400", body, resp.StatusCode)
		}
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/check", configRequest{Config: starter()})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "table inet ostiole") {
		t.Fatalf("check: %d %s", resp.StatusCode, raw)
	}
}

// The rules page asks for the system rules of the draft it is editing, so
// the endpoint takes a configuration rather than reading the saved one.
func TestSystemRules(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/rules/system", configRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no config: %d %s", resp.StatusCode, raw)
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/rules/system", configRequest{Config: starter()})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system rules: %d %s", resp.StatusCode, raw)
	}
	var rows []nft.SystemRule
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	var lockout, tail bool
	for _, r := range rows {
		if strings.HasPrefix(r.Description, "Anti-lockout") && len(r.Zones) == 1 && r.Zones[0] == "lan" {
			lockout = true
		}
		if r.After && r.Action == "drop" {
			tail = true
		}
	}
	if !lockout || !tail {
		t.Errorf("rows lack the anti-lockout row for lan or a closing drop: %s", raw)
	}
}

// The NAT page reads the automatic rules of its draft the same way, so a
// mode switched a moment ago shows what it writes before it is applied.
func TestSystemNAT(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/nat/system", configRequest{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no config: %d %s", resp.StatusCode, raw)
	}

	cfg := starter()
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/nat/system", configRequest{Config: cfg})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("automatic: %d %s", resp.StatusCode, raw)
	}
	var rows []nft.SystemNAT
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Zone != "wan" || !slices.Equal(rows[0].Interfaces, []string{"eth0"}) {
		t.Errorf("automatic: want one row for wan on eth0, got %s", raw)
	}

	// Manual writes nothing of its own, and says so with a list, not null.
	cfg.NAT.Outbound.Mode = model.OutboundManual
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/nat/system", configRequest{Config: cfg})
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Errorf("manual: %d %s", resp.StatusCode, raw)
	}
}

func TestLiveInterfaces(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/interfaces/live", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"name":"lo"`) {
		t.Fatalf("live interfaces: %d %s", resp.StatusCode, raw)
	}
}

func TestTimezones(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/system/timezones", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("timezones: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		Zones   []string `json:"zones"`
		Current string   `json:"current"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	// UTC leads the list whatever the host has installed, and the clock
	// always reads as something.
	if len(got.Zones) == 0 || got.Zones[0] != "UTC" {
		t.Errorf("zones = %v", got.Zones)
	}
	if got.Current == "" {
		t.Error("current is empty")
	}
}

func TestStarter(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/starter", starterRequest{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("starter: %d %s", resp.StatusCode, raw)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil || len(cfg.Interfaces) != 2 || cfg.System.Hostname != "fw" {
		t.Fatalf("starter config = %s (%v)", raw, err)
	}
	// The anti-lockout rule keeps open the port the wizard is reached on.
	if want := srv.Listener.Addr().(*net.TCPAddr).Port; int(cfg.System.Management.WebPort) != want {
		t.Errorf("starter webPort = %d, want the UI's own %d", cfg.System.Management.WebPort, want)
	}
	// DNS forwards to Quad9, not to whatever this host's resolv.conf
	// names, which is usually the ISP.
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/starter", starterRequest{LAN: "eth1", LANAddress: "192.168.1.1/24", Services: true})
	cfg = model.Config{}
	if err := json.Unmarshal(raw, &cfg); resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("starter with services: %d %s (%v)", resp.StatusCode, raw, err)
	}
	if dns := cfg.Services.DNS; dns.Resolver != "" || !slices.Equal(dns.Upstreams, model.Quad9()) {
		t.Errorf("starter dns = %+v, want forwarding to Quad9", dns)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/config/starter", starterRequest{LAN: "eth1"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing lanAddress: %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/config/starter", starterRequest{LAN: "eth1", LANAddress: "not-cidr"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad address: %d, want 422", resp.StatusCode)
	}
}

func TestHSTSOnlyOverTLS(t *testing.T) {
	t.Parallel()
	plain := httptest.NewServer(Handler(Deps{}))
	defer plain.Close()
	resp, _ := do(t, plain, http.MethodGet, "/api/v1/health", nil)
	if resp.Header.Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}
	secure := httptest.NewTLSServer(Handler(Deps{}))
	defer secure.Close()
	req, _ := http.NewRequest(http.MethodGet, secure.URL+"/api/v1/health", nil)
	resp, err := secure.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Strict-Transport-Security") == "" {
		t.Error("HSTS missing over TLS")
	}
}

func TestChangePassword(t *testing.T) {
	t.Parallel()
	srv, _ := newTestServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/password", passwordChange{Current: "wrong password!", New: "a completely new one"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong current: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/password", passwordChange{Current: testPassword, New: "short"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("weak new: %d", resp.StatusCode)
	}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/password", passwordChange{Current: testPassword, New: "a completely new one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("change: %d %s", resp.StatusCode, raw)
	}
	// Still logged in with the re-issued session.
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("me after change: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("old password still works: %d", resp.StatusCode)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "admin", Password: "a completely new one"}); resp.StatusCode != http.StatusOK {
		t.Errorf("new password rejected: %d", resp.StatusCode)
	}
}

func TestFirewallLogEndpoints(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := &nfttest.Fake{}
	eng := engine.New(store.New(dir), fake, nil, slog.New(slog.DiscardHandler))
	as, _ := auth.NewService(dir)
	ring := fwlog.NewRing(10)
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Log: ring}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatal("setup")
	}

	// A rule the running ruleset logged without a verdict, from before the
	// prefix carried one. The API answers for it from the configuration;
	// without that the log page calls every such packet "unknown" and its
	// blocked/allowed filter has nothing to sort on.
	cfg := model.Starter(model.StarterOptions{
		Hostname: "fw", LAN: "ost-lan0", LANAddress: "192.168.9.1/24", WAN: "ost-wan0",
	})
	cfg.Rules = append(cfg.Rules, model.Rule{
		ID: "old-drop", Enabled: true, Zone: "wan", Action: model.ActionDrop,
		Protocol: model.ProtocolAny, Log: true,
	})
	if _, err := eng.Store().Save(cfg, ""); err != nil {
		t.Fatal(err)
	}
	ring.Add(fwlog.Entry{Prefix: "ostiole:old-drop: ", Kind: "rule", RuleID: "old-drop", Proto: "tcp", Src: "203.0.113.9"})
	if _, raw := do(t, srv, http.MethodGet, "/api/v1/log/recent?limit=5", nil); !strings.Contains(string(raw), `"action":"drop"`) {
		t.Errorf("an untagged rule entry was served without the rule's action: %s", raw)
	}

	ring.Add(fwlog.Entry{Prefix: "ostiole:z:lan:drop: ", Kind: "zone-drop", Zone: "lan", Action: "drop", Proto: "tcp", Src: "10.0.0.5", Dst: "10.0.0.1", DstPort: 22})
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/log/recent?limit=5", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"zone":"lan"`) {
		t.Fatalf("recent: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/log/recent?limit=0", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad limit: %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/log/stream", nil)
	resp2, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if ct := resp2.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		ring.Add(fwlog.Entry{Prefix: "ostiole:r:web-in:accept: ", Kind: "rule", RuleID: "web-in", Action: "accept", Proto: "tcp", DstPort: 443})
	}()
	buf := make([]byte, 4096)
	var got string
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(got, `"ruleId":"web-in"`) && time.Now().Before(deadline) {
		n, err := resp2.Body.Read(buf)
		got += string(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(got, "data: ") || !strings.Contains(got, `"ruleId":"web-in"`) {
		t.Errorf("stream = %q", got)
	}

	plain := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as}))
	defer plain.Close()
	plain.Client().Jar = jar
	if resp, _ := do(t, plain, http.MethodGet, "/api/v1/log/recent", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("without ring: %d, want 503", resp.StatusCode)
	}
}
