package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ostiole/internal/auth"
	"ostiole/internal/diag"
	"ostiole/internal/engine"
	"ostiole/internal/model"
	"ostiole/internal/modem"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// roleServer is a server with tokens enabled and an admin signed in.
func roleServer(t *testing.T) (*httptest.Server, *auth.Service, *auth.Tokens) {
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
	// A real modem read dials the WAN link and waits out both schemes, so
	// the tests that walk every route answer from here instead.
	modems := modem.NewCacheWith(time.Minute, func(_ context.Context, address string) (*modem.Status, error) {
		return &modem.Status{Address: address, FetchedAt: time.Now()}, nil
	})
	// Nor is this machine's journal read.
	journal := func(context.Context, diag.JournalOptions) ([]diag.JournalEntry, error) {
		return []diag.JournalEntry{}, nil
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Tokens: tokens, Modems: modems, Journal: journal}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, as, tokens
}

// withToken sends a request authenticated by a bearer token and nothing
// else: no cookie, and no CSRF header.
func withToken(t *testing.T, srv *httptest.Server, method, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func mintToken(t *testing.T, srv *httptest.Server, name, role string) string {
	t.Helper()
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/tokens",
		map[string]any{"name": name, "role": role})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create token: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		Secret string `json:"secret"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Secret, auth.TokenPrefix) {
		t.Fatalf("secret = %q", out.Secret)
	}
	return out.Secret
}

func TestTokenAuthenticatesWithoutASession(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	secret := mintToken(t, srv, "monitoring", "viewer")

	// A token needs no cookie and no CSRF header.
	resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/status", secret)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status with a token: %d %s", resp.StatusCode, raw)
	}
	// And a viewer cannot change anything, even with a valid token.
	resp, raw = withToken(t, srv, http.MethodPost, "/api/v1/apply/confirm", secret)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer confirming an apply: %d %s", resp.StatusCode, raw)
	}
	// Nor can it manage tokens.
	resp, _ = withToken(t, srv, http.MethodGet, "/api/v1/tokens", secret)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer listing tokens = %d, want 403", resp.StatusCode)
	}

	// A made-up token is refused.
	resp, _ = withToken(t, srv, http.MethodGet, "/api/v1/status", auth.TokenPrefix+"deadbeef_nope")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged token = %d, want 401", resp.StatusCode)
	}
}

func TestOperatorTokenCanApplyButNotAdminister(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	secret := mintToken(t, srv, "deploy", "operator")

	// The engine says there is nothing pending, which means the operator
	// got past authorisation and into the handler.
	resp, raw := withToken(t, srv, http.MethodPost, "/api/v1/apply/revert", secret)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("operator reverting: %d %s", resp.StatusCode, raw)
	}
	resp, _ = withToken(t, srv, http.MethodPost, "/api/v1/config/backup", secret)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator downloading a backup = %d, want 403", resp.StatusCode)
	}
}

func TestTokensAreListedAndDeleted(t *testing.T) {
	t.Parallel()
	srv, _, tokens := roleServer(t)
	secret := mintToken(t, srv, "monitoring", "viewer")

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/tokens", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var list []auth.Token
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "monitoring" || list[0].Role != auth.RoleViewer || list[0].Owner != "admin" {
		t.Fatalf("list = %+v", list)
	}
	// The secret is never handed out again.
	if strings.Contains(string(raw), strings.SplitN(secret, "_", 3)[2]) {
		t.Error("the secret came back from the list endpoint")
	}
	if list[0].Hash != "" {
		t.Error("the hash came back from the list endpoint")
	}

	resp, raw = do(t, srv, http.MethodDelete, "/api/v1/tokens/"+list[0].ID, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, raw)
	}
	if got := tokens.List(); len(got) != 0 {
		t.Errorf("after delete = %+v", got)
	}
	// And it stops working straight away.
	resp, _ = withToken(t, srv, http.MethodGet, "/api/v1/status", secret)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("deleted token = %d, want 401", resp.StatusCode)
	}
}

func TestRolesAreEnforcedForSessionsToo(t *testing.T) {
	t.Parallel()
	srv, as, _ := roleServer(t)
	if err := as.SetPassword("watcher", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := as.SetRole("watcher", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}

	// Signing in as the viewer replaces the admin's cookie in the jar.
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login",
		credentials{Username: "watcher", Password: testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	resp, _ = do(t, srv, http.MethodGet, "/api/v1/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("viewer reading status = %d", resp.StatusCode)
	}
	resp, _ = do(t, srv, http.MethodPost, "/api/v1/apply/confirm", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer confirming = %d, want 403", resp.StatusCode)
	}
}

// A viewer reads the configuration and its history without the secrets in
// them. An operator, who saves the configuration back whole, reads it as
// it is.
func TestViewerReadsTheConfigurationWithoutSecrets(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	cfg := starter()
	cfg.Backup.Remote = model.RemoteBackup{
		Enabled: true, Endpoint: "https://s3.us-west-004.backblazeb2.com", Bucket: "router-backups",
		KeyID: "key-id-1", Secret: "s3-secret-1", Passphrase: "correct horse battery",
	}
	// Twice, so the first is in the history too.
	for range 2 {
		if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
			t.Fatalf("apply: %d %s", resp.StatusCode, raw)
		}
	}
	viewer := mintToken(t, srv, "dashboard", "viewer")
	operator := mintToken(t, srv, "automation", "operator")

	_, raw := withToken(t, srv, http.MethodGet, "/api/v1/config/revisions", viewer)
	var revs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &revs); err != nil || len(revs) == 0 {
		t.Fatalf("revisions: %v %s", err, raw)
	}
	for _, path := range []string{"/api/v1/config", "/api/v1/config/revisions/" + revs[0].ID} {
		resp, raw := withToken(t, srv, http.MethodGet, path, viewer)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("viewer %s: %d %s", path, resp.StatusCode, raw)
		}
		for _, secret := range []string{"key-id-1", "s3-secret-1", "correct horse battery"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("a viewer reads %q in %s", secret, path)
			}
		}
		if !strings.Contains(string(raw), "router-backups") {
			t.Errorf("%s lost more than its secrets: %s", path, raw)
		}
		if _, raw := withToken(t, srv, http.MethodGet, path, operator); !strings.Contains(string(raw), "s3-secret-1") {
			t.Errorf("an operator reads %s without its secrets", path)
		}
	}
}

// An operator applies the configuration, but not the parts that would make
// them root, hand them the accounts or decide who gets in. Running a
// command cron now is the administrator's call too.
func TestOperatorCannotTakeWhatIsTheAdministrators(t *testing.T) {
	t.Parallel()
	tokens, err := auth.NewTokens(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, operator, err := tokens.Create("automation", auth.RoleOperator, 0, "admin", "", auth.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServerWith(t, func(d *Deps) { d.Tokens = tokens; d.Crons = &fakeCrons{} })
	clone := func(c *model.Config) *model.Config {
		raw, _ := json.Marshal(c)
		var out model.Config
		_ = json.Unmarshal(raw, &out)
		return &out
	}
	cfg := starter()
	cfg.Crons = []model.Cron{{ID: "hook", Enabled: true, Schedule: "0 5 * * *", Kind: model.CronCommand, Command: "/usr/local/bin/hook"}}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin apply: %d %s", resp.StatusCode, raw)
	}

	mine := clone(cfg)
	mine.Rules = append(mine.Rules, model.Rule{ID: "extra", Enabled: true, Zone: "lan", Action: model.ActionAccept, Protocol: model.ProtocolAny})
	if resp, raw := sendAs(t, srv, "/api/v1/apply", operator, applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(mine)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator's own change: %d %s", resp.StatusCode, raw)
	}
	for name, change := range map[string]func(*model.Config){
		"a command": func(c *model.Config) {
			c.Crons = append(c.Crons, model.Cron{ID: "shell", Enabled: true, Schedule: "* * * * *",
				Kind: model.CronCommand, Command: "/bin/sh", Args: []string{"-c", "id"}})
		},
		"ssh passwords": func(c *model.Config) { c.System.Management.SSHPasswords = !c.System.Management.SSHPasswords },
		"the remote backup": func(c *model.Config) {
			c.Backup.Remote = model.RemoteBackup{Enabled: true, Endpoint: "https://s3.example.net", Bucket: "theirs",
				KeyID: "k", Secret: "s", Passphrase: "their passphrase"}
		},
	} {
		next := clone(mine)
		change(next)
		for path, body := range map[string]any{
			"/api/v1/check": configRequest{Config: (*draftConfig)(next)},
			"/api/v1/apply": applyRequest{BaseRevision: revision(t, srv), Config: (*draftConfig)(next)},
		} {
			resp, raw := sendAs(t, srv, path, operator, body)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "only an administrator") {
				t.Errorf("operator %s via %s: %d %s", name, path, resp.StatusCode, raw)
			}
		}
	}

	if resp, raw := sendAs(t, srv, "/api/v1/crons/hook/run", operator, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator ran the command cron: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/crons/hook/run", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("admin run: %d %s", resp.StatusCode, raw)
	}
}

// sendAs posts a JSON body with a bearer token and nothing else.
func sendAs(t *testing.T, srv *httptest.Server, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// signedInAs makes an account with the role and answers its session cookie.
func signedInAs(t *testing.T, as *auth.Service, name string, role auth.Role) *http.Cookie {
	t.Helper()
	if err := as.SetPassword(name, testPassword); err != nil {
		t.Fatal(err)
	}
	if err := as.SetRole(name, role); err != nil {
		t.Fatal(err)
	}
	sess, err := as.SignIn(name)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: SessionCookie, Value: sess.ID}
}

// doAs sends a request with the cookie alone, as the browser would.
func doAs(t *testing.T, srv *httptest.Server, cookie *http.Cookie, method, path string, body any) (*http.Response, []byte) {
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
	req.AddCookie(cookie)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func TestTokenCannotOutrankItsMaker(t *testing.T) {
	t.Parallel()
	srv, as, _ := roleServer(t)
	operator := signedInAs(t, as, "quill", auth.RoleOperator)

	resp, raw := doAs(t, srv, operator, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "too-much", "role": "admin"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "more than your role") {
		t.Errorf("operator minting admin: %d %s", resp.StatusCode, raw)
	}
	resp, raw = doAs(t, srv, operator, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "fetch", "role": "viewer", "certificates": []string{"web"}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "only an admin") {
		t.Errorf("operator minting a certificate token: %d %s", resp.StatusCode, raw)
	}
	resp, raw = doAs(t, srv, operator, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "scrape", "metrics": true})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("operator minting a metrics token: %d %s", resp.StatusCode, raw)
	}
	resp, raw = doAs(t, srv, operator, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "deploy", "role": "operator"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("operator minting operator: %d %s", resp.StatusCode, raw)
	}
	var tok auth.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatal(err)
	}
	if tok.Owner != "quill" || tok.CreatedBy != "quill" || tok.Role != auth.RoleOperator {
		t.Errorf("token = %+v", tok)
	}

	resp, raw = do(t, srv, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "fetch", "role": "viewer", "certificates": []string{"web"}})
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("admin minting a certificate token: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "old-way"}); resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /api/v1/tokens = %d, want it gone", resp.StatusCode)
	}
}

func TestOwnTokensAreListedAndDeleted(t *testing.T) {
	t.Parallel()
	srv, as, tokens := roleServer(t)
	adminSecret := mintToken(t, srv, "admin-panel", "admin")
	viewer := signedInAs(t, as, "wren", auth.RoleViewer)
	resp, raw := doAs(t, srv, viewer, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "wren-display"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("viewer mint: %d %s", resp.StatusCode, raw)
	}

	resp, raw = doAs(t, srv, viewer, http.MethodGet, "/api/v1/auth/tokens", nil)
	var mine []auth.Token
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &mine) != nil {
		t.Fatalf("own list: %d %s", resp.StatusCode, raw)
	}
	if len(mine) != 1 || mine[0].Name != "wren-display" || mine[0].Owner != "wren" {
		t.Errorf("own list = %+v", mine)
	}
	if resp, _ := doAs(t, srv, viewer, http.MethodGet, "/api/v1/tokens", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("viewer listing every token = %d, want 403", resp.StatusCode)
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/tokens", nil)
	var all []auth.Token
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &all) != nil {
		t.Fatalf("admin list: %d %s", resp.StatusCode, raw)
	}
	owners := map[string]string{}
	for _, tok := range all {
		owners[tok.Name] = tok.Owner
	}
	if len(all) != 2 || owners["admin-panel"] != "admin" || owners["wren-display"] != "wren" {
		t.Errorf("admin list = %+v", all)
	}

	if resp, _ := doAs(t, srv, viewer, http.MethodDelete, "/api/v1/auth/tokens/admin-panel", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("viewer deleting the admin's token = %d, want 404", resp.StatusCode)
	}
	if resp, raw := doAs(t, srv, viewer, http.MethodDelete, "/api/v1/auth/tokens/WREN-DISPLAY", nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("viewer deleting own: %d %s", resp.StatusCode, raw)
	}
	if got := tokens.List(); len(got) != 1 || got[0].Name != "admin-panel" {
		t.Errorf("left = %+v", got)
	}

	// A token owns nothing, so it reaches none of the account's token routes.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if resp, _ := withToken(t, srv, method, "/api/v1/auth/tokens", adminSecret); resp.StatusCode < 400 {
			t.Errorf("token %s own tokens = %d", method, resp.StatusCode)
		}
	}
	if resp, _ := withToken(t, srv, http.MethodGet, "/api/v1/tokens", adminSecret); resp.StatusCode != http.StatusOK {
		t.Errorf("admin token listing every token = %d", resp.StatusCode)
	}
}
