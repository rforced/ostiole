package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodPassword = "correct horse battery"

func newService(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	s, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	h, err := HashPassword(goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(h, goodPassword); err != nil || !ok {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, goodPassword+"x"); ok {
		t.Fatal("wrong password verified")
	}
	if _, err := HashPassword("short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("short password: %v", err)
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=18$m=1,t=1,p=1$YQ$YQ", "$argon2id$v=19$m=x$YQ$YQ", "$argon2id$v=19$m=1,t=1,p=1$!!$YQ"} {
		if ok, err := VerifyPassword(bad, goodPassword); ok || err == nil {
			t.Errorf("VerifyPassword(%q) = %v, %v; want false, error", bad, ok, err)
		}
	}
}

func TestSetupLoginAndSessions(t *testing.T) {
	t.Parallel()
	s, now := newService(t)
	if !s.NeedsSetup() {
		t.Fatal("fresh service should need setup")
	}
	if _, err := s.Login("admin", goodPassword, "1.2.3.4"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("login before setup: %v", err)
	}
	for _, bad := range []string{"", "Admin", "1abc", "a b", "toolongtoolongtoolongtoolongtoolongx"} {
		if err := s.Setup(bad, goodPassword); !errors.Is(err, ErrInvalidUsername) {
			t.Errorf("Setup(%q) = %v", bad, err)
		}
	}
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.Setup("other", goodPassword); !errors.Is(err, ErrSetupDone) {
		t.Errorf("second setup: %v", err)
	}
	if s.NeedsSetup() {
		t.Fatal("setup not recorded")
	}
	info, _ := os.Stat(filepath.Join(filepath.Dir(s.path), UsersFile))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("users file mode %o", info.Mode().Perm())
	}

	sess, err := s.Login("admin", goodPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Session(sess.ID); !ok || got.Username != "admin" {
		t.Fatalf("Session = %+v, %v", got, ok)
	}
	if _, ok := s.Session("nope"); ok {
		t.Error("bogus session resolved")
	}

	// Idle timeout.
	*now = now.Add(SessionIdleTimeout + time.Minute)
	if _, ok := s.Session(sess.ID); ok {
		t.Error("idle session still valid")
	}

	// Absolute lifetime even when active.
	sess, _ = s.Login("admin", goodPassword, "1.2.3.4")
	for range 30 {
		*now = now.Add(time.Hour)
		s.Session(sess.ID)
	}
	if _, ok := s.Session(sess.ID); ok {
		t.Error("session outlived max lifetime")
	}

	// Password change kills sessions.
	sess, _ = s.Login("admin", goodPassword, "1.2.3.4")
	if err := s.SetPassword("admin", "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Session(sess.ID); ok {
		t.Error("session survived password change")
	}
	if _, err := s.Login("admin", goodPassword, "1.2.3.4"); err == nil {
		t.Error("old password still works")
	}
	if _, err := s.Login("admin", "a brand new passphrase", "1.2.3.4"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}

	// Logout.
	sess, _ = s.Login("admin", "a brand new passphrase", "1.2.3.4")
	s.Logout(sess.ID)
	if _, ok := s.Session(sess.ID); ok {
		t.Error("session survived logout")
	}

	// Reload from disk.
	s2, err := NewService(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	if s2.NeedsSetup() || len(s2.Usernames()) != 1 {
		t.Errorf("reload lost users: %v", s2.Usernames())
	}
}

// An update restarts the daemon, so a session that only lived in memory
// logged everybody out every time.
func TestSessionsSurviveARestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login("admin", goodPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}

	restarted, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.SessionLoadError != nil {
		t.Fatalf("sessions did not load: %v", restarted.SessionLoadError)
	}
	got, ok := restarted.Session(sess.ID)
	if !ok {
		t.Fatal("session did not survive the restart")
	}
	if got.Username != "admin" || got.ID != sess.ID {
		t.Errorf("session = %+v", got)
	}

	// The file must not be usable as a cookie: it holds hashes only.
	raw, err := os.ReadFile(filepath.Join(dir, SessionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sess.ID) {
		t.Error("the session file contains the session ID itself")
	}
	if info, err := os.Stat(filepath.Join(dir, SessionsFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("sessions file mode = %v, %v", info.Mode().Perm(), err)
	}

	// Logging out on one instance clears it for the next one too.
	restarted.Logout(sess.ID)
	again, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Session(sess.ID); ok {
		t.Error("a logged-out session came back after a restart")
	}
}

// A session whose account is gone must not come back with the file.
func TestRestartDropsSessionsOfDeletedAccounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword("temp", goodPassword); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login("temp", goodPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser("temp"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := restarted.Session(sess.ID); ok {
		t.Error("session of a deleted account survived the restart")
	}
}

func TestRateLimit(t *testing.T) {
	t.Parallel()
	s, now := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range MaxFailures {
		if _, err := s.Login("admin", "wrong password here", "10.0.0.9"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("err = %v", err)
		}
	}
	if _, err := s.Login("admin", goodPassword, "10.0.0.9"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
	if _, err := s.Login("admin", goodPassword, "10.0.0.10"); err != nil {
		t.Errorf("other address blocked: %v", err)
	}
	*now = now.Add(FailureWindow + time.Second)
	if _, err := s.Login("admin", goodPassword, "10.0.0.9"); err != nil {
		t.Errorf("still blocked after window: %v", err)
	}
}

func TestDeleteUser(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	_ = s.Setup("admin", goodPassword)
	if err := s.DeleteUser("admin"); err == nil {
		t.Error("deleted last account")
	}
	_ = s.SetPassword("second", goodPassword)
	if err := s.DeleteUser("admin"); err != nil {
		t.Fatal(err)
	}
	if got := s.Usernames(); len(got) != 1 || got[0] != "second" {
		t.Errorf("users = %v", got)
	}
	if err := s.DeleteUser("ghost"); err == nil {
		t.Error("deleted unknown user")
	}
}

func TestReloadsWhenFileChangesOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	daemon, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	sess, err := daemon.Login("admin", goodPassword, "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}

	// A second process (the CLI) rewrites the password.
	cli, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure a distinguishable mtime even on coarse filesystems.
	time.Sleep(20 * time.Millisecond)
	if err := cli.SetPassword("admin", "replaced by the cli!"); err != nil {
		t.Fatal(err)
	}

	// The change must be seen on session lookup, not only on login.
	if _, ok := daemon.Session(sess.ID); ok {
		t.Error("session lookup did not notice the external password change")
	}
	if _, err := daemon.Login("admin", goodPassword, "1.1.1.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("old password still accepted by the daemon: %v", err)
	}
	if _, err := daemon.Login("admin", "replaced by the cli!", "1.1.1.1"); err != nil {
		t.Errorf("new password rejected by the daemon: %v", err)
	}
	if _, ok := daemon.Session(sess.ID); ok {
		t.Error("session survived an external password change")
	}

	// File removed: everyone is logged out and setup is needed again.
	if err := os.Remove(filepath.Join(dir, UsersFile)); err != nil {
		t.Fatal(err)
	}
	if !daemon.NeedsSetup() {
		t.Error("NeedsSetup false after file removal")
	}
}
