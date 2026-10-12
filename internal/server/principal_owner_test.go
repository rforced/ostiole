package server

import (
	"net/http"
	"testing"

	"ostiole/internal/auth"
)

func TestBuildAdoptsTokensForTheirMakers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := as.SetPassword("mira", testPassword); err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, by := range map[string]string{"made-by-mira": "mira", "made-by-otto": "otto", "made-at-shell": "cli"} {
		if _, _, err := tokens.Create(name, auth.RoleViewer, 0, by, "", auth.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	Handler(Deps{Auth: as, Tokens: tokens})
	for _, tok := range tokens.List() {
		want := ""
		if tok.Name == "made-by-mira" {
			want = "mira"
		}
		if tok.Owner != want {
			t.Errorf("%s owned by %q, want %q", tok.Name, tok.Owner, want)
		}
	}
}

func TestOrphanedTokenIsRefused(t *testing.T) {
	t.Parallel()
	srv, as, tokens := roleServer(t)
	signedInAs(t, as, "otto", auth.RoleOperator)
	_, owned, err := tokens.Create("otto-deploy", auth.RoleOperator, 0, "otto", "otto", auth.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, router, err := tokens.Create("router-wide", auth.RoleViewer, 0, "cli", "", auth.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/status", owned); resp.StatusCode != http.StatusOK {
		t.Fatalf("owned token: %d %s", resp.StatusCode, raw)
	}
	// The account goes behind the API's back, leaving the token.
	if err := as.DeleteUser("otto"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := withToken(t, srv, http.MethodGet, "/api/v1/status", owned); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("orphaned token = %d, want 401", resp.StatusCode)
	}
	if resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/status", router); resp.StatusCode != http.StatusOK {
		t.Errorf("router token: %d %s", resp.StatusCode, raw)
	}
}

func TestLoweredOwnerLowersTheToken(t *testing.T) {
	t.Parallel()
	srv, as, tokens := roleServer(t)
	signedInAs(t, as, "quill", auth.RoleOperator)
	_, secret, err := tokens.Create("quill-deploy", auth.RoleOperator, 0, "quill", "quill", auth.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if resp, raw := withToken(t, srv, http.MethodPost, "/api/v1/apply/revert", secret); resp.StatusCode != http.StatusConflict {
		t.Fatalf("operator token reverting: %d %s", resp.StatusCode, raw)
	}
	if err := as.SetRole("quill", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if resp, raw := withToken(t, srv, http.MethodPost, "/api/v1/apply/revert", secret); resp.StatusCode != http.StatusForbidden {
		t.Errorf("token of a lowered owner reverting: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := withToken(t, srv, http.MethodGet, "/api/v1/status", secret); resp.StatusCode != http.StatusOK {
		t.Errorf("token of a lowered owner reading: %d %s", resp.StatusCode, raw)
	}
	// Raising the owner again gives the token back no more than it had.
	if err := as.SetRole("quill", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if resp, _ := withToken(t, srv, http.MethodGet, "/api/v1/tokens", secret); resp.StatusCode != http.StatusForbidden {
		t.Errorf("operator token of an admin listing tokens = %d, want 403", resp.StatusCode)
	}
}
