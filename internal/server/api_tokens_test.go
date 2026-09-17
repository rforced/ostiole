package server

import (
	"encoding/json"
	"io"
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
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Tokens: tokens}))
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
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/tokens",
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
	resp, _ = withToken(t, srv, http.MethodGet, "/api/v1/config/backup", secret)
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
	if len(list) != 1 || list[0].Name != "monitoring" || list[0].Role != auth.RoleViewer {
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

// The only administrator cannot be demoted, or nobody could manage the
// router again without the CLI.
func TestTheLastAdministratorStays(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/users/admin/role", map[string]string{"role": "viewer"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("demoting the only admin: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "only administrator") {
		t.Errorf("error = %s", raw)
	}
}
