package auth

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func tokens(t *testing.T) *Tokens {
	t.Helper()
	tk, err := NewTokens(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestTokenRoundTrip(t *testing.T) {
	t.Parallel()
	tk := tokens(t)

	tok, secret, err := tk.Create("monitoring", RoleViewer, 0, "admin", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Hash != "" {
		t.Error("the hash was handed back to the caller")
	}
	if !strings.HasPrefix(secret, TokenPrefix) || len(secret) < 40 {
		t.Fatalf("secret = %q", secret)
	}

	got, err := tk.Authenticate(secret)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got.Name != "monitoring" || got.Role != RoleViewer {
		t.Errorf("token = %+v", got)
	}
	// Using it is recorded, which is how an unused token is spotted.
	if list := tk.List(); len(list) != 1 || list[0].LastUsedAt == nil {
		t.Errorf("list = %+v", list)
	}

	// The secret is not recoverable from the file.
	raw, err := os.ReadFile(tk.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), strings.SplitN(secret, "_", 3)[2]) {
		t.Error("the secret itself was written to disk")
	}
}

func TestAuthenticateRejects(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	_, secret, err := tk.Create("real", RoleAdmin, 0, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"empty":         "",
		"no prefix":     "just-a-string",
		"unknown id":    TokenPrefix + "00000000_" + strings.Repeat("a", 43),
		"wrong secret":  secret[:len(secret)-4] + "zzzz",
		"prefix only":   TokenPrefix,
		"no underscore": TokenPrefix + "abcdef",
	}
	for name, presented := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := tk.Authenticate(presented); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestTokenExpiry(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	now := time.Now()
	tk.now = func() time.Time { return now }

	_, secret, err := tk.Create("short", RoleViewer, 24*time.Hour, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Authenticate(secret); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	now = now.Add(25 * time.Hour)
	if _, err := tk.Authenticate(secret); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("after expiry = %v, want ErrTokenExpired", err)
	}
	// Refused, but kept a while: a clock ahead at boot must not prune it.
	if _, _, err := tk.Create("later", RoleViewer, 0, "", Limits{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(tk.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.List()) != 2 || !strings.Contains(string(raw), `"short"`) {
		t.Errorf("a token expired an hour ago was pruned:\n%s", raw)
	}
}

// A token expired for longer than pruneAfter goes from the list at once
// and from the file at the next save, whether it ran out while the store
// was open or before it was read. Reading never writes the file.
func TestExpiredTokensArePruned(t *testing.T) {
	t.Parallel()
	names := func(list []Token) []string {
		var out []string
		for _, tok := range list {
			out = append(out, tok.Name)
		}
		return out
	}
	read := func(t *testing.T, path string) string {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	open := func(t *testing.T, dir string, at time.Time) *Tokens {
		t.Helper()
		tk, err := NewTokens(dir)
		if err != nil {
			t.Fatal(err)
		}
		tk.now = func() time.Time { return at }
		if _, _, err := tk.Create("nightly-report", RoleViewer, 24*time.Hour, "", Limits{}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := tk.Create("wall-display", RoleViewer, 0, "", Limits{}); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	pruned := func(t *testing.T, tk *Tokens) {
		t.Helper()
		before := read(t, tk.path)
		if got := names(tk.List()); !slices.Equal(got, []string{"wall-display"}) {
			t.Errorf("listed %v, want only the live token", got)
		}
		if read(t, tk.path) != before {
			t.Error("listing wrote the file")
		}
		if _, _, err := tk.Create("door-sensor", RoleViewer, 0, "", Limits{}); err != nil {
			t.Fatal(err)
		}
		if raw := read(t, tk.path); strings.Contains(raw, `"nightly-report"`) || !strings.Contains(raw, `"wall-display"`) {
			t.Errorf("the save kept the expired token, or lost the live one:\n%s", raw)
		}
	}

	t.Run("while open", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		tk := open(t, t.TempDir(), now)
		tk.now = func() time.Time { return now.Add(60 * 24 * time.Hour) }
		pruned(t, tk)
	})

	t.Run("when read", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		before := read(t, open(t, dir, time.Now().Add(-61*24*time.Hour)).path)
		tk, err := NewTokens(dir)
		if err != nil {
			t.Fatal(err)
		}
		if read(t, tk.path) != before {
			t.Error("reading the file wrote it")
		}
		pruned(t, tk)
	})
}

func TestTokenNamesAreUniqueAndChecked(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	if _, _, err := tk.Create("taken", RoleViewer, 0, "", Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tk.Create("TAKEN", RoleViewer, 0, "", Limits{}); err == nil {
		t.Error("a duplicate name was accepted")
	}
	if _, _, err := tk.Create("", RoleViewer, 0, "", Limits{}); err == nil {
		t.Error("an empty name was accepted")
	}
	if _, _, err := tk.Create("fine", "wizard", 0, "", Limits{}); err == nil {
		t.Error("an unknown role was accepted")
	}
}

func TestRestrictedTokenCarriesItsCertificates(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	tok, secret, err := tk.Create("proxy", RoleViewer, 0, "", Limits{Certificates: []string{"router", "mail"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tok.Certificates) != 2 {
		t.Errorf("created token = %+v, want two certificates", tok)
	}
	back, err := tk.Authenticate(secret)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Certificates, tok.Certificates) {
		t.Errorf("authenticated = %v, want %v", back.Certificates, tok.Certificates)
	}
	if _, _, err := tk.Create("bad", RoleViewer, 0, "", Limits{Certificates: []string{"Not A Name"}}); err == nil {
		t.Error("a name no certificate could have was accepted")
	}
	too := make([]string, MaxTokenCertificates+1)
	for i := range too {
		too[i] = fmt.Sprintf("c%d", i)
	}
	if _, _, err := tk.Create("many", RoleViewer, 0, "", Limits{Certificates: too}); err == nil {
		t.Error("an unbounded list was accepted")
	}
}

// A token for a scraper reads the metrics and nothing else. It is a
// viewer, and it does not fetch certificates too.
func TestMetricsTokenIsAViewerAndNothingElse(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	tok, secret, err := tk.Create("scraper", RoleViewer, 0, "", Limits{Metrics: true})
	if err != nil {
		t.Fatal(err)
	}
	if back, err := tk.Authenticate(secret); err != nil || !back.Metrics || !tok.Metrics {
		t.Errorf("authenticated = %+v, %v", back, err)
	}
	if _, _, err := tk.Create("loud", RoleAdmin, 0, "", Limits{Metrics: true}); err == nil {
		t.Error("an admin limited to the metrics was accepted")
	}
	if _, _, err := tk.Create("both", RoleViewer, 0, "", Limits{Metrics: true, Certificates: []string{"router"}}); err == nil {
		t.Error("metrics and certificates together were accepted")
	}
}

func TestDeleteTokenByNameOrID(t *testing.T) {
	t.Parallel()
	tk := tokens(t)
	byName, _, _ := tk.Create("by-name", RoleViewer, 0, "", Limits{})
	byID, _, _ := tk.Create("by-id", RoleViewer, 0, "", Limits{})

	if err := tk.Delete("BY-NAME"); err != nil {
		t.Errorf("delete by name: %v", err)
	}
	if err := tk.Delete(byID.ID); err != nil {
		t.Errorf("delete by id: %v", err)
	}
	if err := tk.Delete(byName.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("deleting twice = %v", err)
	}
	if got := tk.List(); len(got) != 0 {
		t.Errorf("list = %+v", got)
	}
}

// Another process writing the file is picked up, which is what lets the
// CLI and the daemon share it.
func TestTokensFollowTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := first.Create("shared", RoleOperator, 0, "", Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Authenticate(secret); err != nil {
		t.Errorf("the second reader does not know the token: %v", err)
	}
}

func TestRolesRank(t *testing.T) {
	t.Parallel()
	if !RoleAdmin.Allows(RoleOperator) || !RoleAdmin.Allows(RoleViewer) || !RoleAdmin.Allows(RoleAdmin) {
		t.Error("an administrator should be allowed everything")
	}
	if RoleOperator.Allows(RoleAdmin) {
		t.Error("an operator is not an administrator")
	}
	if RoleViewer.Allows(RoleOperator) {
		t.Error("a viewer cannot operate")
	}
	if Role("wizard").Valid() || Role("wizard").Allows(RoleViewer) {
		t.Error("an unknown role should allow nothing")
	}
}
