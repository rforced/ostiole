package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
	"ostiole/internal/sysupdate"
)

// recordSite is a server with an audit log whose requests are made with an
// address of the test's choosing, and whose first account, alice, is an
// administrator set up from 192.0.2.1.
type recordSite struct {
	h     http.Handler
	log   *audit.Log
	dir   string
	alice string
}

func recordServer(t *testing.T) *recordSite {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	packages := sysupdate.New(sysupdate.Options{
		PackageManager: "dnf", StateDir: dir, Run: dnfScript, Root: true, Log: slog.New(slog.DiscardHandler),
	})
	s := &recordSite{log: audit.Open(dir), dir: dir}
	s.h = Handler(Deps{Engine: eng, Auth: as, Tokens: tokens, Packages: packages, Audit: s.log})
	rec := s.do(t, http.MethodPost, "/api/v1/setup", credentials{Username: "alice", Password: testPassword}, "192.0.2.1", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	s.alice = recordSession(t, rec)
	return s
}

func recordSession(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func recordCookie(id string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookie, Value: id}) }
}

func recordBearer(secret string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+secret) }
}

func (s *recordSite) do(t *testing.T, method, path string, body any, address string, as func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "[" + address + "]:40000"
	req.Header.Set(RequestHeader, RequestHeaderValue)
	if as != nil {
		as(req)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

// events reads the log, oldest first.
func (s *recordSite) events(t *testing.T) []audit.Event {
	t.Helper()
	all, err := s.log.Events()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func (s *recordSite) last(t *testing.T, action string) audit.Event {
	t.Helper()
	var found *audit.Event
	for _, e := range s.events(t) {
		if e.Action == action {
			found = &e
		}
	}
	if found == nil {
		t.Fatalf("no %s recorded in %+v", action, s.events(t))
	}
	return *found
}

func recordCount(events []audit.Event, action string) int {
	n := 0
	for _, e := range events {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestRecordSetupAndSignIn(t *testing.T) {
	t.Parallel()
	s := recordServer(t)
	if got, want := s.last(t, audit.Setup).By, (audit.Actor{Name: "alice", Kind: audit.Account, Role: "admin", Address: "192.0.2.1"}); got != want {
		t.Errorf("setup by %+v, want %+v", got, want)
	}

	rec := s.do(t, http.MethodPost, "/api/v1/users", map[string]string{"username": "bob", "password": testPassword, "role": "operator"}, "192.0.2.1", recordCookie(s.alice))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create bob: %d %s", rec.Code, rec.Body)
	}

	for _, c := range []credentials{{Username: "bob", Password: "not the password"}, {Username: "nobody", Password: testPassword}} {
		if rec := s.do(t, http.MethodPost, "/api/v1/auth/login", c, "192.0.2.66", nil); rec.Code == http.StatusOK {
			t.Fatalf("sign-in as %s was let in", c.Username)
		}
	}
	if n := recordCount(s.events(t), audit.SignIn); n != 0 {
		t.Fatalf("%d sign-ins recorded for refused attempts", n)
	}

	rec = s.do(t, http.MethodPost, "/api/v1/auth/login", credentials{Username: "bob", Password: testPassword}, "2001:db8::7", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sign in: %d %s", rec.Code, rec.Body)
	}
	events := s.events(t)
	if n := recordCount(events, audit.SignIn); n != 1 {
		t.Fatalf("%d sign-ins recorded, want 1", n)
	}
	if got, want := s.last(t, audit.SignIn).By, (audit.Actor{Name: "bob", Kind: audit.Account, Role: "operator", Address: "2001:db8::7"}); got != want {
		t.Errorf("sign-in by %+v, want %+v", got, want)
	}

	bob := recordSession(t, rec)
	rec = s.do(t, http.MethodPost, "/api/v1/auth/password", passwordChange{Current: testPassword, New: "a much longer passphrase"}, "2001:db8::7", recordCookie(bob))
	if rec.Code != http.StatusOK {
		t.Fatalf("password: %d %s", rec.Code, rec.Body)
	}
	if got := s.last(t, audit.Password).By; got.Name != "bob" || got.Kind != audit.Account || got.Address != "2001:db8::7" {
		t.Errorf("password changed by %+v", got)
	}
}

func TestRecordAccountChanges(t *testing.T) {
	t.Parallel()
	s := recordServer(t)
	admin := recordCookie(s.alice)
	steps := []struct {
		method, path string
		body         any
		action       string
		target       string
		detail       string
	}{
		{http.MethodPost, "/api/v1/users", map[string]string{"username": "bob", "password": testPassword, "role": "operator"}, audit.AccountCreate, "bob", "operator"},
		{http.MethodPost, "/api/v1/users/bob/role", map[string]string{"role": "viewer"}, audit.AccountRole, "bob", "viewer"},
		{http.MethodPost, "/api/v1/users/bob/password", map[string]string{"password": "another long passphrase"}, audit.AccountPassword, "bob", ""},
		{http.MethodPost, "/api/v1/users/bob/username", map[string]string{"username": "carol"}, audit.AccountRename, "bob", "carol"},
		{http.MethodDelete, "/api/v1/users/carol", nil, audit.AccountDelete, "carol", ""},
	}
	for _, step := range steps {
		rec := s.do(t, step.method, step.path, step.body, "192.0.2.1", admin)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", step.method, step.path, rec.Code, rec.Body)
		}
		e := s.last(t, step.action)
		if e.Target != step.target || e.Detail != step.detail {
			t.Errorf("%s recorded %q %q, want %q %q", step.action, e.Target, e.Detail, step.target, step.detail)
		}
		if e.By.Name != "alice" || e.By.Kind != audit.Account || e.By.Address != "192.0.2.1" {
			t.Errorf("%s by %+v", step.action, e.By)
		}
	}

	// A change refused records nothing, nor does one that changes nothing.
	before := len(s.events(t))
	s.do(t, http.MethodPost, "/api/v1/users/alice/role", map[string]string{"role": "viewer"}, "192.0.2.1", admin)
	s.do(t, http.MethodPost, "/api/v1/users/alice/username", map[string]string{"username": "alice"}, "192.0.2.1", admin)
	if after := len(s.events(t)); after != before {
		t.Errorf("%d entries recorded for a refused change and a rename to the same name", after-before)
	}
}

func TestRecordTokens(t *testing.T) {
	t.Parallel()
	s := recordServer(t)
	mint := func(body map[string]any) (id, secret string) {
		t.Helper()
		rec := s.do(t, http.MethodPost, "/api/v1/tokens", body, "192.0.2.1", recordCookie(s.alice))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create token: %d %s", rec.Code, rec.Body)
		}
		var out struct{ ID, Secret string }
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID, out.Secret
	}

	id, secret := mint(map[string]any{"name": "deploy", "role": "admin"})
	e := s.last(t, audit.TokenCreate)
	if e.Target != "deploy" || e.Detail != "admin" || e.By.Name != "alice" || e.By.Kind != audit.Account {
		t.Errorf("token create recorded %+v", e)
	}
	mint(map[string]any{"name": "scrape", "role": "viewer", "metrics": true})
	if e := s.last(t, audit.TokenCreate); e.Target != "scrape" || e.Detail != "metrics" {
		t.Errorf("metrics token recorded %q %q", e.Target, e.Detail)
	}

	rec := s.do(t, http.MethodPost, "/api/v1/users", map[string]string{"username": "dave", "password": testPassword}, "2001:db8::5", recordBearer(secret))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with a token: %d %s", rec.Code, rec.Body)
	}
	e = s.last(t, audit.AccountCreate)
	if want := (audit.Actor{Name: "deploy", Kind: audit.Token, Role: "admin", Address: "2001:db8::5"}); e.By != want {
		t.Errorf("token's create by %+v, want %+v", e.By, want)
	}
	if e.Target != "dave" || e.Detail != "viewer" {
		t.Errorf("token's create recorded %q %q", e.Target, e.Detail)
	}

	if rec := s.do(t, http.MethodDelete, "/api/v1/tokens/"+id, nil, "192.0.2.1", recordCookie(s.alice)); rec.Code != http.StatusNoContent {
		t.Fatalf("delete token: %d %s", rec.Code, rec.Body)
	}
	if e := s.last(t, audit.TokenDelete); e.Target != "deploy" {
		t.Errorf("token delete recorded target %q, want the name", e.Target)
	}
}

func TestRecordBackupAndReboot(t *testing.T) {
	t.Parallel()
	s := recordServer(t)
	if _, err := store.New(s.dir).Save(starter(), "", store.Author{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		body   map[string]any
		detail string
	}{
		{map[string]any{"users": true}, "with accounts"},
		{map[string]any{"redact": true}, "without secrets"},
		{map[string]any{}, ""},
	} {
		rec := s.do(t, http.MethodPost, "/api/v1/config/backup", c.body, "192.0.2.1", recordCookie(s.alice))
		if rec.Code != http.StatusOK {
			t.Fatalf("backup %v: %d %s", c.body, rec.Code, rec.Body)
		}
		if e := s.last(t, audit.Backup); e.Detail != c.detail || e.By.Name != "alice" {
			t.Errorf("backup %v recorded %+v", c.body, e)
		}
	}
	if n := recordCount(s.events(t), audit.Backup); n != 3 {
		t.Errorf("%d backups recorded, want 3", n)
	}

	if rec := s.do(t, http.MethodPost, "/api/v1/system/reboot", nil, "192.0.2.1", recordCookie(s.alice)); rec.Code != http.StatusOK {
		t.Fatalf("reboot: %d %s", rec.Code, rec.Body)
	}
	if e := s.last(t, audit.Reboot); e.By.Name != "alice" || e.By.Address != "192.0.2.1" {
		t.Errorf("reboot by %+v", e.By)
	}
}
