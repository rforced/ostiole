package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
)

// login swaps the test client's session for another account's.
func login(t *testing.T, srv *httptest.Server, username string) {
	t.Helper()
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: username, Password: testPassword})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login as %s: %d %s", username, resp.StatusCode, raw)
	}
}

// accounts decodes an account list response, which is what every change to
// an account answers with.
func accounts(t *testing.T, raw []byte) map[string]auth.Role {
	t.Helper()
	var list []auth.User
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("decode accounts: %v: %s", err, raw)
	}
	out := make(map[string]auth.Role, len(list))
	for _, u := range list {
		if u.Hash != "" {
			t.Errorf("account %s came back with a password hash", u.Username)
		}
		out[u.Username] = u.Role
	}
	return out
}

func TestCreateAndDeleteAccounts(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)

	body := map[string]string{"username": "watcher", "password": testPassword, "role": "viewer"}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/users", body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, raw)
	}
	if got := accounts(t, raw); got["watcher"] != auth.RoleViewer || got["admin"] != auth.RoleAdmin {
		t.Fatalf("accounts = %v", got)
	}

	// The new account is real: it can sign in, and only as far as its role.
	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users", body); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate name: %d %s", resp.StatusCode, raw)
	}
	for _, bad := range []map[string]string{
		{"username": "1nope", "password": testPassword, "role": "viewer"},
		{"username": "weak", "password": "short", "role": "viewer"},
	} {
		if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users", bad); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("create %v: %d %s", bad, resp.StatusCode, raw)
		}
	}
	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users",
		map[string]string{"username": "wizard", "password": testPassword, "role": "wizard"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown role: %d %s", resp.StatusCode, raw)
	}

	resp, raw = do(t, srv, http.MethodDelete, "/api/v1/users/watcher", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, raw)
	}
	if _, ok := accounts(t, raw)["watcher"]; ok {
		t.Error("deleted account still listed")
	}
	if resp, raw = do(t, srv, http.MethodDelete, "/api/v1/users/ghost", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("delete unknown: %d %s", resp.StatusCode, raw)
	}
}

// An administrator cannot demote or delete their own account. Two
// administrators could otherwise strip each other and then themselves, and
// the last-administrator rule would never once have been broken.
func TestAdminsCannotLockThemselvesOut(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/users",
		map[string]string{"username": "second", "password": testPassword, "role": "admin"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second admin: %d %s", resp.StatusCode, raw)
	}

	for _, tc := range []struct {
		what   string
		method string
		path   string
		body   any
	}{
		{"demote", http.MethodPost, "/api/v1/users/admin/role", map[string]string{"role": "viewer"}},
		{"delete", http.MethodDelete, "/api/v1/users/admin", nil},
		{"password", http.MethodPost, "/api/v1/users/admin/password", map[string]string{"password": testPassword + "!"}},
	} {
		resp, raw := do(t, srv, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("self %s: %d %s, want 403", tc.what, resp.StatusCode, raw)
		}
	}
	// Still an admin, and still signed in.
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/users", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	if got := accounts(t, raw)["admin"]; got != auth.RoleAdmin {
		t.Errorf("own role = %q", got)
	}
	// Somebody else's account is fair game.
	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users/second/role", map[string]string{"role": "operator"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("demote the other admin: %d %s", resp.StatusCode, raw)
	}
	if got := accounts(t, raw)["second"]; got != auth.RoleOperator {
		t.Errorf("second = %q", got)
	}
}

// The last administrator stays an administrator even for a caller the
// self-account rule does not cover, which is any API token.
func TestTheLastAdministratorStays(t *testing.T) {
	t.Parallel()
	srv, as, tokens := roleServer(t)
	if err := as.CreateUser("watcher", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	_, secret, err := tokens.Create("robot", auth.RoleAdmin, 0, "admin", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, raw := withToken(t, srv, http.MethodDelete, "/api/v1/users/admin", secret)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("token deleting the only admin: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "only administrator") {
		t.Errorf("error = %s", raw)
	}
}

// Renaming yourself is allowed: it cannot cost you the administrator role,
// and the session has to follow the name or it would sign you out.
func TestRenameKeepsTheCallerSignedIn(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/users/admin/username", map[string]string{"username": "josh"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename: %d %s", resp.StatusCode, raw)
	}
	if got := accounts(t, raw); got["josh"] != auth.RoleAdmin || len(got) != 1 {
		t.Fatalf("accounts = %v", got)
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/auth/me", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me after rename: %d %s", resp.StatusCode, raw)
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		t.Fatal(err)
	}
	if me.Username != "josh" {
		t.Errorf("me = %q, want josh", me.Username)
	}
	// The rename did not quietly hand the session a lesser role.
	if resp, raw = do(t, srv, http.MethodGet, "/api/v1/users", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("still an admin: %d %s", resp.StatusCode, raw)
	}
	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users/ghost/username", map[string]string{"username": "spook"}); resp.StatusCode != http.StatusNotFound {
		t.Errorf("rename unknown: %d %s", resp.StatusCode, raw)
	}
}

// Setting somebody's password must never be the thing that creates them:
// a typo would otherwise add an account nobody asked for.
func TestSetPasswordNeedsAnExistingAccount(t *testing.T) {
	t.Parallel()
	srv, as, _ := roleServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/users",
		map[string]string{"username": "watcher", "password": testPassword, "role": "viewer"}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, raw)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/users/watcher-typo/password", map[string]string{"password": testPassword + "typo"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("password for a name nobody has: %d %s", resp.StatusCode, raw)
	}
	if names := as.Usernames(); len(names) != 2 {
		t.Fatalf("accounts = %v", names)
	}

	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users/watcher/password", map[string]string{"password": "short"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("weak password: %d %s", resp.StatusCode, raw)
	}
	if resp, raw = do(t, srv, http.MethodPost, "/api/v1/users/watcher/password", map[string]string{"password": testPassword + "new"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set password: %d %s", resp.StatusCode, raw)
	}
	if _, err := as.Login("watcher", testPassword+"new", "1.2.3.4"); err != nil {
		t.Errorf("login with the new password: %v", err)
	}
	if got := as.Role("watcher"); got != auth.RoleViewer {
		t.Errorf("role after a password reset = %q", got)
	}
}

// Everything under /users is an administrator's to do.
func TestAccountRoutesAreAdminOnly(t *testing.T) {
	t.Parallel()
	srv, as, _ := roleServer(t)
	if err := as.CreateUser("watcher", testPassword, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}
	if err := as.SetRole("admin", auth.RoleOperator); err == nil {
		t.Fatal("expected the last-administrator rule to hold")
	}
	login(t, srv, "watcher")

	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/v1/users", nil},
		{http.MethodPost, "/api/v1/users", map[string]string{"username": "x", "password": testPassword, "role": "admin"}},
		{http.MethodDelete, "/api/v1/users/admin", nil},
		{http.MethodPost, "/api/v1/users/admin/role", map[string]string{"role": "viewer"}},
		{http.MethodPost, "/api/v1/users/admin/password", map[string]string{"password": testPassword}},
		{http.MethodPost, "/api/v1/users/watcher/username", map[string]string{"username": "sneaky"}},
	} {
		resp, raw := do(t, srv, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("operator %s %s: %d %s, want 403", tc.method, tc.path, resp.StatusCode, raw)
		}
	}
}

// The session says which role it acts with, so the UI can grey out what
// the account may not change rather than let the apply refuse it.
func TestSessionsSayTheirRole(t *testing.T) {
	t.Parallel()
	srv, as, _ := roleServer(t)
	role := func(raw []byte) auth.Role {
		t.Helper()
		var s struct {
			Role auth.Role `json:"role"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		return s.Role
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK || role(raw) != auth.RoleAdmin {
		t.Errorf("me as the admin: %d %s", resp.StatusCode, raw)
	}
	if err := as.CreateUser("hand", testPassword, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "hand", Password: testPassword})
	if resp.StatusCode != http.StatusOK || role(raw) != auth.RoleOperator {
		t.Errorf("login as an operator: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/auth/me", nil); resp.StatusCode != http.StatusOK || role(raw) != auth.RoleOperator {
		t.Errorf("me as an operator: %d %s", resp.StatusCode, raw)
	}
}
