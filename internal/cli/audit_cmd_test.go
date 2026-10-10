package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/model"
	"ostiole/internal/store"
)

func auditRun(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

// auditStdin hands the commands a password on stdin. Tests that use it
// cannot run in parallel.
func auditStdin(t *testing.T, text string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		_ = f.Close()
	})
}

func auditEvents(t *testing.T, dir string) []audit.Event {
	t.Helper()
	events, err := audit.Open(dir).Events()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.By.Kind != audit.Shell || e.By.Name == "" {
			t.Errorf("%s recorded as %+v, want a named shell user", e.Action, e.By)
		}
	}
	return events
}

func auditWant(t *testing.T, got []audit.Event, want ...audit.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("recorded %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.Action != w.Action || g.Target != w.Target || g.Detail != w.Detail {
			t.Errorf("event %d is %s %q %q, want %s %q %q", i, g.Action, g.Target, g.Detail, w.Action, w.Target, w.Detail)
		}
	}
}

func TestUsersCommandsRecordTheShellUser(t *testing.T) {
	dir := t.TempDir()
	g := &globals{configDir: dir}
	auditStdin(t, "correct horse battery\n")
	auditRun(t, newUsersCmd(g), "create", "alice", "--role", "operator", "--password-stdin")
	auditStdin(t, "correct horse battery\n")
	auditRun(t, newResetPasswordCmd(g), "-u", "alice", "--password-stdin")
	auditStdin(t, "correct horse battery\n")
	auditRun(t, newResetPasswordCmd(g), "-u", "carol", "--password-stdin")
	auditRun(t, newUsersCmd(g), "role", "alice", "admin")
	auditRun(t, newUsersCmd(g), "rename", "alice", "bob")
	auditRun(t, newUsersCmd(g), "delete", "bob")
	auditWant(t, auditEvents(t, dir),
		audit.Event{Action: audit.AccountCreate, Target: "alice", Detail: "operator"},
		audit.Event{Action: audit.AccountPassword, Target: "alice"},
		audit.Event{Action: audit.AccountCreate, Target: "carol", Detail: "admin"},
		audit.Event{Action: audit.AccountRole, Target: "alice", Detail: "admin"},
		audit.Event{Action: audit.AccountRename, Target: "alice", Detail: "bob"},
		audit.Event{Action: audit.AccountDelete, Target: "bob"},
	)
}

func TestUsersCommandRecordsNothingWhenRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := newUsersCmd(&globals{configDir: dir})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"delete", "nobody"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("deleting an account that does not exist succeeded")
	}
	if events := auditEvents(t, dir); len(events) != 0 {
		t.Errorf("a refused delete recorded %+v", events)
	}
}

func TestTokensCommandsRecordTheShellUser(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	g := &globals{configDir: dir}
	auditRun(t, newTokensCmd(g), "create", "deploy", "--role", "operator")
	auditRun(t, newTokensCmd(g), "create", "scraper", "--metrics")
	auditRun(t, newTokensCmd(g), "create", "certbot", "--certificates", "web")
	tk, err := auth.NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, tok := range tk.List() {
		if tok.Name == "scraper" {
			id = tok.ID
		}
	}
	auditRun(t, newTokensCmd(g), "delete", id)
	auditRun(t, newTokensCmd(g), "delete", "DEPLOY")
	auditWant(t, auditEvents(t, dir),
		audit.Event{Action: audit.TokenCreate, Target: "deploy", Detail: "operator"},
		audit.Event{Action: audit.TokenCreate, Target: "scraper", Detail: "metrics"},
		audit.Event{Action: audit.TokenCreate, Target: "certbot", Detail: "certificates"},
		audit.Event{Action: audit.TokenDelete, Target: "scraper"},
		audit.Event{Action: audit.TokenDelete, Target: "deploy"},
	)
}

func TestRevisionsNameWhoApplied(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := store.New(dir)
	cfg := model.Starter(model.StarterOptions{Hostname: "gateway", LAN: "eth1", LANAddress: "192.0.2.1/24", WAN: "eth0"})
	alice := audit.Actor{Name: "alice", Kind: audit.Account, Role: "admin", Address: "192.0.2.10"}
	bob := audit.Actor{Name: "bob", Kind: audit.Shell, Address: "198.51.100.7"}
	deploy := audit.Actor{Name: "deploy", Kind: audit.Token, Role: "operator"}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, store.ConfigFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.System.Hostname = "gateway-2"
	if _, err := st.Save(cfg, "", store.Author{By: alice, ConfirmedBy: &deploy}); err != nil {
		t.Fatal(err)
	}
	cfg.System.Hostname = "gateway-3"
	if _, err := st.Save(cfg, "", store.Author{By: bob}); err != nil {
		t.Fatal(err)
	}
	got := auditRun(t, newRevisionsCmd(&globals{configDir: dir}))
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 5 {
		t.Fatalf("revisions printed\n%s", got)
	}
	if !strings.HasPrefix(lines[0], "in force since ") || !strings.HasSuffix(lines[0], ", applied by bob (shell) from 198.51.100.7") {
		t.Errorf("first line is %q", lines[0])
	}
	if !strings.HasSuffix(lines[2], "APPLIED BY") {
		t.Errorf("header is %q", lines[2])
	}
	if !strings.HasSuffix(lines[3], "  alice from 192.0.2.10, confirmed by deploy (token)") {
		t.Errorf("newest revision reads %q", lines[3])
	}
	if !strings.HasSuffix(lines[4], "  -") {
		t.Errorf("oldest revision reads %q", lines[4])
	}
}
