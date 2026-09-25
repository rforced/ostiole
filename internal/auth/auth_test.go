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

	// A tab left in the background overnight is still signed in.
	*now = now.Add(23 * time.Hour)
	if _, ok := s.Session(sess.ID); !ok {
		t.Error("session idle for less than a day was dropped")
	}

	// Idle timeout.
	*now = now.Add(SessionIdleTimeout + time.Minute)
	if _, ok := s.Session(sess.ID); ok {
		t.Error("idle session still valid")
	}

	// Absolute lifetime even when active.
	sess, _ = s.Login("admin", goodPassword, "1.2.3.4")
	for range SessionMaxLifetime/(SessionIdleTimeout/2) + 1 {
		*now = now.Add(SessionIdleTimeout / 2)
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

// A log stream checks its session with Peek every few seconds. That is
// not somebody using the router, so it must not keep the session from
// idling out, and it sees a logout like any request does.
func TestPeekDoesNotKeepASessionAlive(t *testing.T) {
	t.Parallel()
	s, now := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	sess, err := s.Login("admin", goodPassword, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		*now = now.Add(SessionIdleTimeout / 4)
		if _, ok := s.Peek(sess.ID); !ok {
			t.Fatal("a live session did not peek")
		}
	}
	*now = now.Add(SessionIdleTimeout / 2)
	if _, ok := s.Peek(sess.ID); ok {
		t.Error("peeking kept an idle session alive")
	}

	sess, _ = s.Login("admin", goodPassword, "192.0.2.1")
	if got, ok := s.Peek(sess.ID); !ok || got.Username != "admin" {
		t.Fatalf("Peek = %+v, %v", got, ok)
	}
	s.Logout(sess.ID)
	if _, ok := s.Peek(sess.ID); ok {
		t.Error("a logged out session still peeks")
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
	info, err := os.Stat(filepath.Join(dir, SessionsFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sessions file mode = %v", info.Mode().Perm())
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

// Each check of a password takes 64 MiB, so a burst of sign-ins waits its
// turn for one of two, gives up rather than queue for ever, and is looked
// at by the limiter again once it has waited.
func TestSignInsTakeTurnsToHash(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	s.hashWait = 20 * time.Millisecond
	for range cap(s.hashing) {
		s.hashing <- struct{}{}
	}
	if _, err := s.Login("admin", goodPassword, "192.0.2.1"); !errors.Is(err, ErrBusy) {
		t.Errorf("login with every slot taken: %v, want ErrBusy", err)
	}
	if err := s.SetPassword("admin", goodPassword+"!"); !errors.Is(err, ErrBusy) {
		t.Errorf("password change with every slot taken: %v, want ErrBusy", err)
	}

	// The failures that landed while this one waited count against it.
	s.hashWait = time.Minute
	result := make(chan error, 1)
	go func() {
		_, err := s.Login("admin", goodPassword, "192.0.2.2")
		result <- err
	}()
	// Time to get past the first look and start waiting. Were it slower,
	// the first look would refuse it, and the answer would be the same.
	time.Sleep(20 * time.Millisecond)
	for range MaxFailures {
		s.limiter.failure("192.0.2.2")
	}
	<-s.hashing
	if err := <-result; !errors.Is(err, ErrRateLimited) {
		t.Errorf("login after its address used up its tries: %v, want ErrRateLimited", err)
	}
	if _, err := s.Login("admin", goodPassword, "192.0.2.1"); err != nil {
		t.Errorf("login with a slot free: %v", err)
	}
}

// Setup and a password change end in a session for the account without a
// second hash: with every slot taken, the change itself still signs in.
func TestSignInNeedsNoHash(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	for range cap(s.hashing) {
		s.hashing <- struct{}{}
	}
	sess, err := s.SignIn("admin")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Session(sess.ID); !ok || got.Username != "admin" {
		t.Errorf("Session = %+v, %v", got, ok)
	}
	if _, err := s.SignIn("nobody"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("SignIn(nobody) = %v, want ErrNoSuchUser", err)
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
	if err := s.DeleteUser("ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("delete unknown user: %v", err)
	}
}

// The account first-run setup creates has to be an administrator on disk,
// not merely one by the default a role-less record falls back to.
func TestSetupCreatesAnAdministrator(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if got := s.Users()[0].Role; got != RoleAdmin {
		t.Errorf("stored role = %q, want %q", got, RoleAdmin)
	}
}

func TestCreateUser(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	_ = s.Setup("admin", goodPassword)

	if err := s.CreateUser("watcher", goodPassword, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if got := s.Role("watcher"); got != RoleViewer {
		t.Errorf("role = %q, want %q", got, RoleViewer)
	}
	// Creating is not a password reset in disguise.
	if err := s.CreateUser("watcher", goodPassword+"x", RoleAdmin); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate name: %v", err)
	}
	if got := s.Role("watcher"); got != RoleViewer {
		t.Errorf("refused create changed the role to %q", got)
	}
	if _, err := s.Login("watcher", goodPassword, "1.2.3.4"); err != nil {
		t.Errorf("refused create changed the password: %v", err)
	}

	if err := s.CreateUser("1bad", goodPassword, RoleViewer); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("bad name: %v", err)
	}
	if err := s.CreateUser("weak", "short", RoleViewer); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak password: %v", err)
	}
	if err := s.CreateUser("wizard", goodPassword, Role("wizard")); !errors.Is(err, ErrUnknownRole) {
		t.Errorf("bad role: %v", err)
	}
	if got := s.Usernames(); len(got) != 2 {
		t.Errorf("refused creates left accounts behind: %v", got)
	}
}

// Renaming is the one change an administrator may make to their own
// account, so it must keep everything that makes it theirs.
func TestRenameKeepsRoleAndSession(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	_ = s.Setup("admin", goodPassword)
	_ = s.CreateUser("watcher", goodPassword, RoleViewer)
	sess, err := s.Login("admin", goodPassword, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Rename("admin", "josh"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Session(sess.ID)
	if !ok {
		t.Fatal("rename signed the account out")
	}
	if got.Username != "josh" {
		t.Errorf("session username = %q, want josh", got.Username)
	}
	if role := s.Role("josh"); role != RoleAdmin {
		t.Errorf("role after rename = %q", role)
	}
	if role := s.Role("admin"); role != RoleViewer {
		t.Error("the old name still resolves to an account")
	}
	if _, err := s.Login("josh", goodPassword, "1.2.3.4"); err != nil {
		t.Errorf("login under the new name: %v", err)
	}

	if err := s.Rename("josh", "watcher"); !errors.Is(err, ErrUserExists) {
		t.Errorf("rename onto a taken name: %v", err)
	}
	if err := s.Rename("josh", "Nope"); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("rename to a bad name: %v", err)
	}
	if err := s.Rename("ghost", "spook"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("rename an unknown account: %v", err)
	}
	if err := s.Rename("josh", "josh"); err != nil {
		t.Errorf("rename to the same name: %v", err)
	}
}

// Whatever else the service allows, it never reaches a state with no
// administrator in it.
func TestAlwaysLeavesAnAdministrator(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	_ = s.Setup("admin", goodPassword)
	_ = s.CreateUser("watcher", goodPassword, RoleViewer)

	if err := s.SetRole("admin", RoleViewer); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demoted the only administrator: %v", err)
	}
	if err := s.DeleteUser("admin"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleted the only administrator: %v", err)
	}
	if err := s.SetRole("watcher", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	// With a second administrator in place, the first may step down.
	if err := s.SetRole("admin", RoleOperator); err != nil {
		t.Errorf("demote with a spare administrator: %v", err)
	}
	if err := s.SetRole("watcher", Role("wizard")); !errors.Is(err, ErrUnknownRole) {
		t.Errorf("unknown role: %v", err)
	}
	if err := s.SetRole("ghost", RoleAdmin); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("role of an unknown account: %v", err)
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

// First-run setup is open to whoever gets there first, so two at once must
// leave one account, not one each. The two services stand for the daemon
// and `ostiole reset-password`: they share only the directory.
func TestConcurrentSetupsLeaveOneAccount(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var services [2]*Service
	for i := range services {
		s, err := NewService(dir)
		if err != nil {
			t.Fatal(err)
		}
		services[i] = s
	}
	names := []string{"alice", "bob", "carol", "dave"}
	errs := make(chan error, len(names))
	start := make(chan struct{})
	for i, name := range names {
		go func() {
			<-start
			errs <- services[i%2].Setup(name, goodPassword)
		}()
	}
	close(start)
	created := 0
	for range names {
		switch err := <-errs; {
		case err == nil:
			created++
		case !errors.Is(err, ErrSetupDone):
			t.Errorf("setup: %v", err)
		}
	}
	if created != 1 {
		t.Errorf("%d setups succeeded, want 1", created)
	}
	fresh, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fresh.Users()); n != 1 {
		t.Errorf("%d accounts on disk, want 1", n)
	}
}
