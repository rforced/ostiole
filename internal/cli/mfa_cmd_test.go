package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
)

func runSecondFactor(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := newSecondFactorCmd(&globals{configDir: dir})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func mustSecondFactor(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runSecondFactor(t, dir, args...)
	if err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out
}

// mfaAccounts makes alice, with an authenticator app and two passkeys, bob
// with the app alone, and carol with neither.
func mfaAccounts(t *testing.T, dir string) {
	t.Helper()
	svc, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if err := svc.CreateUser(name, "correct horse battery", auth.RoleViewer); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alice", "bob"} {
		if err := svc.EnableTOTP(name, "GEZDGNBVGY3TQOJQ", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"c3Rhcg", "bW9vbg"} {
		key := auth.Passkey{ID: id, Name: "desk key", PublicKey: "cHVibGlj", RPID: "gateway.example.net"}
		if err := svc.AddPasskey("alice", key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBypassLastsAsLongAsAsked(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		span time.Duration
		said string
	}{
		{nil, 15 * time.Minute, "15m"},
		{[]string{"--for", "10m"}, 10 * time.Minute, "10m"},
		{[]string{"--for", "90m"}, 90 * time.Minute, "1h30m"},
		{[]string{"--for", "24h"}, 24 * time.Hour, "24h"},
	} {
		t.Run(tc.said, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			before := time.Now()
			got := mustSecondFactor(t, dir, append([]string{"bypass"}, tc.args...)...)
			b, ok := auth.ReadBypass(dir)
			if !ok {
				t.Fatalf("no bypass is running after\n%s", got)
			}
			if b.Until.Before(before.Add(tc.span-time.Second)) || b.Until.After(time.Now().Add(tc.span)) {
				t.Errorf("bypass runs until %s, want about %s from %s", b.Until, tc.span, before)
			}
			if want := audit.ShellActor().Name; b.By != want {
				t.Errorf("bypass started by %q, want %q", b.By, want)
			}
			if want := "second factor bypassed until " + b.Until.Local().Format(time.RFC3339) + " (" + tc.said + ")\n"; got != want {
				t.Errorf("printed %q, want %q", got, want)
			}
		})
	}
}

func TestBypassRefusesWhatIsOutOfRange(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--for", "0"},
		{"--for=-5m"},
		{"--for", "24h1s"},
		{"--for", "72h"},
		{"--for", "5m", "--end"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if _, err := runSecondFactor(t, dir, append([]string{"bypass"}, args...)...); err == nil {
				t.Fatal("was accepted")
			}
			if _, ok := auth.ReadBypass(dir); ok {
				t.Error("a bypass is running")
			}
			if events := auditEvents(t, dir); len(events) != 0 {
				t.Errorf("a refused bypass recorded %+v", events)
			}
		})
	}
}

func TestBypassEndStopsIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustSecondFactor(t, dir, "bypass", "--for", "1h")
	if got, want := mustSecondFactor(t, dir, "bypass", "--end"), "second factor bypass ended\n"; got != want {
		t.Errorf("--end printed %q, want %q", got, want)
	}
	if _, ok := auth.ReadBypass(dir); ok {
		t.Error("a bypass is still running")
	}
	if _, err := os.Stat(filepath.Join(dir, auth.BypassFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the bypass file is still there: %v", err)
	}

	// With nothing running, and with one that ran out, there is nothing to end.
	if got, want := mustSecondFactor(t, dir, "bypass", "--end"), "no bypass was active\n"; got != want {
		t.Errorf("--end with none printed %q, want %q", got, want)
	}
	if err := auth.WriteBypass(dir, auth.Bypass{Until: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if got, want := mustSecondFactor(t, dir, "bypass", "--end"), "no bypass was active\n"; got != want {
		t.Errorf("--end after it ran out printed %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, auth.BypassFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the spent bypass file is still there: %v", err)
	}
	auditWant(t, auditEvents(t, dir),
		audit.Event{Action: audit.SecondFactorBypass, Detail: "1h"},
		audit.Event{Action: audit.SecondFactorBypassEnd},
	)
}

func TestStatusShowsTheBypassAndEachAccount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mfaAccounts(t, dir)
	accounts := "alice  totp: yes  passkeys: 2\nbob  totp: yes  passkeys: 0\ncarol  totp: no  passkeys: 0\n"

	want := "second factor not bypassed\n\n" + accounts
	for _, args := range [][]string{{"status"}, nil} {
		if got := mustSecondFactor(t, dir, args...); got != want {
			t.Errorf("%v printed\n%s\nwant\n%s", args, got, want)
		}
	}

	until := time.Now().Add(time.Hour)
	if err := auth.WriteBypass(dir, auth.Bypass{Until: until, By: "dana"}); err != nil {
		t.Fatal(err)
	}
	want = "second factor bypassed until " + until.Format(time.RFC3339) + ", started by dana\n\n" + accounts
	if got := mustSecondFactor(t, dir, "status"); got != want {
		t.Errorf("status with a bypass printed\n%s\nwant\n%s", got, want)
	}
}

func TestResetRemovesTheSecondFactor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mfaAccounts(t, dir)
	if got, want := mustSecondFactor(t, dir, "reset", "alice"), "second factor removed from alice\n"; got != want {
		t.Errorf("reset printed %q, want %q", got, want)
	}
	svc, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if totp, keys := svc.SecondFactorOf("alice"); totp || len(keys) != 0 {
		t.Errorf("alice still has totp %v and %d passkeys", totp, len(keys))
	}
	if !svc.HasSecondFactor("bob") {
		t.Error("bob lost his second factor too")
	}
	if u, ok := svc.Account("alice"); !ok || u.Role != auth.RoleViewer {
		t.Errorf("alice is %+v, %v after the reset", u, ok)
	}
	auditWant(t, auditEvents(t, dir),
		audit.Event{Action: audit.AccountSecondFactorClear, Target: "alice"},
	)
}

func TestResetRefusesWhatHasNothingToRemove(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mfaAccounts(t, dir)
	if _, err := runSecondFactor(t, dir, "reset", "carol"); !errors.Is(err, auth.ErrNoSecondFactor) {
		t.Errorf("reset of an account without one gave %v, want %v", err, auth.ErrNoSecondFactor)
	}
	if _, err := runSecondFactor(t, dir, "reset", "nobody"); !errors.Is(err, auth.ErrNoSuchUser) {
		t.Errorf("reset of an unknown account gave %v, want %v", err, auth.ErrNoSuchUser)
	}
	if events := auditEvents(t, dir); len(events) != 0 {
		t.Errorf("refused resets recorded %+v", events)
	}
}

func TestSecondFactorIsOnTheRootCommandAsMFA(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"second-factor status": "ostiole second-factor status",
		"mfa status":           "ostiole second-factor status",
		"mfa bypass":           "ostiole second-factor bypass",
		"mfa reset alice":      "ostiole second-factor reset",
	} {
		cmd, _, err := newRootCmd().Find(strings.Fields(path))
		if err != nil || cmd.CommandPath() != want {
			t.Errorf("%q finds %v, %v; want %q", path, cmd.CommandPath(), err, want)
		}
	}
}
