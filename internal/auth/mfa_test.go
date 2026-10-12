package auth

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"

// withAccounts makes administrators without hashing a password for each, so
// none of them can sign in with one.
func withAccounts(t *testing.T, names ...string) (*Service, *time.Time) {
	t.Helper()
	s, now := newService(t)
	users := make([]User, len(names))
	for i, name := range names {
		users[i] = User{Username: name, Hash: "no password", Role: RoleAdmin, CreatedAt: *now, UpdatedAt: *now}
	}
	if err := s.Restore(users); err != nil {
		t.Fatal(err)
	}
	return s, now
}

func testPasskey(name string) Passkey {
	return Passkey{
		ID: "id-" + name, Name: name, PublicKey: "key-" + name,
		RPID: "router.example.test", Transports: []string{"internal"},
	}
}

// A code on the screen works once, or whoever watched it typed could sign in
// with it a moment later.
func TestTOTPCodesWorkOnce(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin", "watcher")
	if s.HasMFA("admin") {
		t.Fatal("a new account has MFA")
	}
	if _, ok := s.TOTPSecret("admin"); ok {
		t.Error("a secret for an account that never set one up")
	}
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if !s.HasMFA("admin") || s.HasMFA("watcher") || s.HasMFA("ghost") {
		t.Errorf("HasMFA: admin %v, watcher %v, ghost %v",
			s.HasMFA("admin"), s.HasMFA("watcher"), s.HasMFA("ghost"))
	}
	if secret, ok := s.TOTPSecret("admin"); !ok || secret != testSecret {
		t.Errorf("TOTPSecret = %q, %v", secret, ok)
	}
	if totp, keys, alone := s.MFAOf("admin"); totp == nil || totp.Secret != "" || !totp.EnabledAt.Equal(*now) || len(keys) != 0 || alone {
		t.Errorf("MFAOf = %+v, %v, %v", totp, keys, alone)
	}

	if err := s.UseTOTPStep("admin", 100); err != nil {
		t.Fatal(err)
	}
	for _, step := range []int64{100, 99} {
		if err := s.UseTOTPStep("admin", step); !errors.Is(err, ErrCodeUsed) {
			t.Errorf("step %d after 100: %v, want ErrCodeUsed", step, err)
		}
	}
	if err := s.UseTOTPStep("admin", 101); err != nil {
		t.Errorf("the next step: %v", err)
	}
	if totp, _, _ := s.MFAOf("admin"); totp == nil || totp.LastStep != 101 {
		t.Errorf("MFAOf after step 101 = %+v", totp)
	}
	if err := s.UseTOTPStep("watcher", 1); !errors.Is(err, ErrNoMFA) {
		t.Errorf("step for an account with no app: %v, want ErrNoMFA", err)
	}
	if err := s.UseTOTPStep("ghost", 1); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("step for an unknown account: %v, want ErrNoSuchUser", err)
	}

	// A restart does not hand the used codes back.
	restarted, err := NewService(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.UseTOTPStep("admin", 101); !errors.Is(err, ErrCodeUsed) {
		t.Errorf("a used step after a restart: %v, want ErrCodeUsed", err)
	}
}

// Setting the app up again replaces the old one outright: its codes stop
// working and the new one starts with a clean slate. A secret that is no
// secret is refused and leaves the old one in place.
func TestSettingUpTheAppAgain(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.UseTOTPStep("admin", 5000); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{"", "====", "not base32 at all", "0189"} {
		if err := s.EnableTOTP("admin", bad, *now); err == nil {
			t.Errorf("EnableTOTP(%q) took a secret that cannot be decoded", bad)
		}
	}
	if secret, _ := s.TOTPSecret("admin"); secret != testSecret {
		t.Errorf("a refused secret replaced the old one with %q", secret)
	}
	if err := s.EnableTOTP("ghost", testSecret, *now); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("EnableTOTP for an unknown account: %v", err)
	}

	// Lower case and padding are what some apps and people write.
	fresh := "gezdgnbvgy3tqojqgezdgnbvgy3tqojq======"
	if err := s.EnableTOTP("admin", fresh, *now); err != nil {
		t.Fatal(err)
	}
	if secret, _ := s.TOTPSecret("admin"); secret != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Errorf("stored secret = %q", secret)
	}
	if err := s.UseTOTPStep("admin", 4000); err != nil {
		t.Errorf("the new app inherited the old app's last step: %v", err)
	}
}

// A passkey is bound to the name it was made on and a router is often opened
// by address, so the authenticator app, which works anywhere, comes first.
func TestAPasskeyNeedsTheAuthenticatorAppFirst(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	if err := s.AddPasskey("admin", testPasskey("desk")); !errors.Is(err, ErrTOTPFirst) {
		t.Fatalf("passkey before the app: %v, want ErrTOTPFirst", err)
	}
	if s.HasMFA("admin") {
		t.Error("a refused passkey was kept")
	}
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey("admin", testPasskey("desk")); err != nil {
		t.Errorf("passkey after the app: %v", err)
	}
}

func TestPasskeysAreAddedListedUsedAndRemoved(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	desk, phone := testPasskey("desk"), testPasskey("phone")
	for _, k := range []Passkey{desk, phone} {
		if err := s.AddPasskey("admin", k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddPasskey("admin", desk); !errors.Is(err, ErrPasskeyExists) {
		t.Errorf("the same passkey twice: %v, want ErrPasskeyExists", err)
	}
	if err := s.AddPasskey("ghost", phone); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("passkey for an unknown account: %v, want ErrNoSuchUser", err)
	}

	totp, listed, _ := s.MFAOf("admin")
	if totp == nil || len(listed) != 2 || listed[0].Name != "desk" || listed[1].Name != "phone" {
		t.Fatalf("MFAOf = %+v, %+v", totp, listed)
	}
	for _, k := range listed {
		if k.PublicKey != "" {
			t.Errorf("MFAOf handed out the public key of %s", k.Name)
		}
	}
	kept := s.Passkeys("admin")
	if len(kept) != 2 || kept[0].PublicKey != desk.PublicKey || kept[1].PublicKey != phone.PublicKey {
		t.Fatalf("Passkeys = %+v", kept)
	}
	if !kept[0].CreatedAt.Equal(*now) {
		t.Errorf("CreatedAt = %v, want the time it was added, %v", kept[0].CreatedAt, *now)
	}

	// What the lists hand out is a copy.
	listed[0].Name, kept[0].Name, kept[0].Transports[0] = "changed", "changed", "changed"
	if got := s.Passkeys("admin"); got[0].Name != "desk" || got[0].Transports[0] != "internal" {
		t.Errorf("editing a list changed the account: %+v", got[0])
	}

	*now = now.Add(time.Hour)
	if err := s.PasskeyUsed("admin", desk.ID, 7, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.PasskeyUsed("admin", "no-such", 1, *now); !errors.Is(err, ErrNoSuchPasskey) {
		t.Errorf("use of an unknown passkey: %v, want ErrNoSuchPasskey", err)
	}
	got := s.Passkeys("admin")
	if got[0].SignCount != 7 || !got[0].LastUsed.Equal(*now) || got[1].SignCount != 0 || !got[1].LastUsed.IsZero() {
		t.Errorf("after use: %+v, %+v", got[0], got[1])
	}

	restarted, err := NewService(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	again := restarted.Passkeys("admin")
	if len(again) != 2 || again[0].ID != desk.ID || again[0].PublicKey != desk.PublicKey || again[0].SignCount != 7 ||
		!again[0].LastUsed.Equal(*now) || again[0].RPID != desk.RPID || again[0].Transports[0] != "internal" {
		t.Errorf("after a restart: %+v", again)
	}

	if err := s.RemovePasskey("admin", desk.ID); err != nil {
		t.Fatal(err)
	}
	if _, left, _ := s.MFAOf("admin"); len(left) != 1 || left[0].ID != phone.ID {
		t.Errorf("after removing the desk key: %+v", left)
	}
	if err := s.RemovePasskey("admin", desk.ID); !errors.Is(err, ErrNoSuchPasskey) {
		t.Errorf("removing it twice: %v, want ErrNoSuchPasskey", err)
	}
	if err := s.RemovePasskey("admin", phone.ID); err != nil {
		t.Fatal(err)
	}
	if totp, left, _ := s.MFAOf("admin"); totp == nil || len(left) != 0 {
		t.Errorf("with every passkey gone: %v, %+v", totp, left)
	}
}

func TestClearingMFA(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin", "watcher")
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey("admin", testPasskey("desk")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasskeyLogin("admin", true); err != nil {
		t.Fatal(err)
	}
	handle, err := s.UserID("admin")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.SignIn("admin")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.ClearMFA("admin"); err != nil {
		t.Fatal(err)
	}
	if s.HasMFA("admin") {
		t.Error("the account still has MFA")
	}
	if _, ok := s.TOTPSecret("admin"); ok {
		t.Error("the secret survived")
	}
	if keys := s.Passkeys("admin"); len(keys) != 0 {
		t.Errorf("passkeys survived: %+v", keys)
	}
	if _, _, alone := s.MFAOf("admin"); alone {
		t.Error("passkey sign-in stayed on")
	}
	if raw, err := os.ReadFile(filepath.Join(s.Dir(), UsersFile)); err != nil || strings.Contains(string(raw), testSecret) {
		t.Errorf("the secret is still on disk (read error %v)", err)
	}
	if got, err := s.UserID("admin"); err != nil || got != handle {
		t.Errorf("user handle after clearing = %q, %v; want %q", got, err, handle)
	}
	if _, ok := s.Session(sess.ID); !ok {
		t.Error("clearing MFA ended a session that was open")
	}

	if err := s.ClearMFA("admin"); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	if err := s.ClearMFA("watcher"); err != nil {
		t.Errorf("clearing an account that has none: %v", err)
	}
	if err := s.ClearMFA("ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("clearing an unknown account: %v, want ErrNoSuchUser", err)
	}
}

// The command line resets an account while the daemon runs; the daemon must
// not go on asking for MFA the file no longer has.
func TestTheDaemonSeesAResetFromTheCommandLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	daemon, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.Restore([]User{{Username: "admin", Hash: "no password", Role: RoleAdmin}}); err != nil {
		t.Fatal(err)
	}
	if err := daemon.EnableTOTP("admin", testSecret, time.Now()); err != nil {
		t.Fatal(err)
	}
	cli, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cli.HasMFA("admin") {
		t.Fatal("the command line does not see the app the daemon saved")
	}
	if err := cli.ClearMFA("admin"); err != nil {
		t.Fatal(err)
	}
	if daemon.HasMFA("admin") {
		t.Error("the daemon still asks for MFA after a reset")
	}
	if _, ok := daemon.TOTPSecret("admin"); ok {
		t.Error("the daemon still holds the secret after a reset")
	}
}

// Lists the page shows must not carry what signs people in, and building
// them must not blank what the account keeps.
func TestAccountsCarryNoSecrets(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	desk := testPasskey("desk")
	desk.PublicKey = "c2VjcmV0LWtleS1ieXRlcw"
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey("admin", desk); err != nil {
		t.Fatal(err)
	}

	list := s.Accounts()
	one, _ := s.Account("admin")
	for what, shown := range map[string]any{"Accounts": list, "Account": one} {
		raw, err := json.Marshal(shown)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{testSecret, desk.PublicKey, "no password"} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s shows %q: %s", what, secret, raw)
			}
		}
	}
	if len(list) != 1 || list[0].TOTP == nil || list[0].TOTP.Secret != "" ||
		len(list[0].Passkeys) != 1 || list[0].Passkeys[0].Name != "desk" || list[0].Passkeys[0].PublicKey != "" {
		t.Errorf("Accounts()[0] = %+v", list[0])
	}

	if secret, ok := s.TOTPSecret("admin"); !ok || secret != testSecret {
		t.Errorf("listing the accounts changed the secret to %q", secret)
	}
	if keys := s.Passkeys("admin"); len(keys) != 1 || keys[0].PublicKey != desk.PublicKey {
		t.Errorf("listing the accounts changed the passkeys to %+v", keys)
	}
}

// A backup has to bring MFA back with the accounts, or
// restoring one locks people out of an account they could not reset.
func TestBackupKeepsMFA(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	desk := testPasskey("desk")
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey("admin", desk); err != nil {
		t.Fatal(err)
	}
	handle, err := s.UserID("admin")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(s.Users())
	if err != nil {
		t.Fatal(err)
	}
	var archived []User
	if err := json.Unmarshal(raw, &archived); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := newService(t)
	if err := rebuilt.Restore(archived); err != nil {
		t.Fatal(err)
	}
	if secret, ok := rebuilt.TOTPSecret("admin"); !ok || secret != testSecret {
		t.Errorf("restored secret = %q, %v", secret, ok)
	}
	if keys := rebuilt.Passkeys("admin"); len(keys) != 1 || keys[0].PublicKey != desk.PublicKey {
		t.Errorf("restored passkeys = %+v", keys)
	}
	if got, err := rebuilt.UserID("admin"); err != nil || got != handle {
		t.Errorf("restored user handle = %q, %v; want %q", got, err, handle)
	}
}

func TestUserIDIsAssignedOnceAndKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	users := []User{{Username: "admin", Hash: "no password"}, {Username: "watcher", Hash: "no password"}}
	if err := s.Restore(users); err != nil {
		t.Fatal(err)
	}
	if got := s.Users()[0].ID; got != "" {
		t.Errorf("an account got a handle before it needed one: %q", got)
	}

	id, err := s.UserID("admin")
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := hex.DecodeString(id); err != nil || len(raw) != 16 {
		t.Fatalf("handle %q is not 16 bytes of hex (%v)", id, err)
	}
	if again, _ := s.UserID("admin"); again != id {
		t.Errorf("a second call made a new handle: %q, then %q", id, again)
	}
	if other, _ := s.UserID("watcher"); other == id {
		t.Error("two accounts share a handle")
	}

	restarted, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.UserID("admin"); got != id {
		t.Errorf("handle after a restart = %q, want %q", got, id)
	}
	if err := s.Rename("admin", "root"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserID("root"); got != id {
		t.Errorf("handle after a rename = %q, want %q", got, id)
	}
	if _, err := s.UserID("ghost"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("handle of an unknown account: %v, want ErrNoSuchUser", err)
	}
}

// The daemon and the command line may both need an account's handle for the
// first time at once; they must agree on one.
func TestTwoProcessesAgreeOnTheUserID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Restore([]User{{Username: "admin", Hash: "no password"}}); err != nil {
		t.Fatal(err)
	}
	second, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	services := []*Service{first, second}
	ids := make(chan string, 8)
	start := make(chan struct{})
	for i := range cap(ids) {
		go func() {
			<-start
			id, err := services[i%2].UserID("admin")
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	close(start)
	want := <-ids
	for range cap(ids) - 1 {
		if got := <-ids; got != want {
			t.Errorf("handles %q and %q for one account", want, got)
		}
	}
	if got, _ := first.UserID("admin"); got != want {
		t.Errorf("saved handle %q differs from %q", got, want)
	}
}

func TestPendingSignInResolvesOnce(t *testing.T) {
	t.Parallel()
	s, _ := withAccounts(t, "admin", "temp")
	token := s.PendingLogin("admin")
	if raw, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(raw) != 32 {
		t.Fatalf("token %q is not 32 bytes of base64url (%v)", token, err)
	}
	if other := s.PendingLogin("admin"); other == token {
		t.Error("two sign-ins share a token")
	}
	if user, checked, ok := s.PendingUser(token); !ok || user != "admin" || !checked {
		t.Errorf("PendingUser = %q, %v, %v", user, checked, ok)
	}
	for _, bogus := range []string{"", "nope"} {
		if _, _, ok := s.PendingUser(bogus); ok {
			t.Errorf("PendingUser(%q) found a sign-in", bogus)
		}
	}

	sess, err := s.ResolvePending(token, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Session(sess.ID); !ok || got.Username != "admin" {
		t.Errorf("the session ResolvePending made: %+v, %v", got, ok)
	}
	if _, _, ok := s.PendingUser(token); ok {
		t.Error("the sign-in outlived its session")
	}
	if _, err := s.ResolvePending(token, "192.0.2.1"); !errors.Is(err, ErrPendingGone) {
		t.Errorf("resolving twice: %v, want ErrPendingGone", err)
	}
	if _, err := s.ResolvePending("", "192.0.2.1"); !errors.Is(err, ErrPendingGone) {
		t.Errorf("resolving no token: %v, want ErrPendingGone", err)
	}

	// An account deleted while its owner was typing the code gets no session.
	gone := s.PendingLogin("temp")
	if err := s.DeleteUser("temp"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolvePending(gone, "192.0.2.1"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("resolving for a deleted account: %v, want ErrNoSuchUser", err)
	}
}

func TestPendingSignInsExpire(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	late, early := s.PendingLogin("admin"), ""
	*now = now.Add(time.Minute)
	early = s.PendingLogin("admin")

	*now = now.Add(pendingLife - time.Minute - time.Second)
	if _, _, ok := s.PendingUser(late); !ok {
		t.Fatal("a sign-in ended before its five minutes were up")
	}
	*now = now.Add(2 * time.Second)
	if _, _, ok := s.PendingUser(late); ok {
		t.Error("a sign-in outlived its five minutes")
	}
	if _, err := s.ResolvePending(late, "192.0.2.1"); !errors.Is(err, ErrPendingGone) {
		t.Errorf("resolving an expired sign-in: %v, want ErrPendingGone", err)
	}
	if _, _, ok := s.PendingUser(early); !ok {
		t.Error("expiry of one sign-in ended another")
	}
}

// Five wrong codes end a sign-in however many addresses they come from, and
// from one address they also count against it like bad passwords.
func TestFiveWrongCodesEndASignIn(t *testing.T) {
	t.Parallel()
	s, _ := withAccounts(t, "admin")

	many := s.PendingLogin("admin")
	for i := range pendingFailures {
		if _, _, ok := s.PendingUser(many); !ok {
			t.Fatalf("sign-in ended after %d wrong codes", i)
		}
		s.FailPending(many, fmt.Sprintf("198.51.100.%d", i+1))
	}
	if _, _, ok := s.PendingUser(many); ok {
		t.Error("sign-in still open after five wrong codes")
	}
	if _, err := s.ResolvePending(many, "192.0.2.1"); !errors.Is(err, ErrPendingGone) {
		t.Errorf("resolving it: %v, want ErrPendingGone", err)
	}
	if s.limiter.blocked("198.51.100.1") {
		t.Error("one wrong code shut an address out")
	}

	const addr = "192.0.2.5"
	one := s.PendingLogin("admin")
	for i := range pendingFailures {
		if _, _, ok := s.PendingUser(one); !ok {
			t.Fatalf("sign-in ended after %d wrong codes", i)
		}
		s.FailPending(one, addr)
	}
	if _, _, ok := s.PendingUser(one); ok {
		t.Error("sign-in still open after five wrong codes from one address")
	}
	if err := s.Authenticate("admin", goodPassword, addr); !errors.Is(err, ErrRateLimited) {
		t.Errorf("password from the address that guessed: %v, want ErrRateLimited", err)
	}
	if s.limiter.blocked("192.0.2.6") {
		t.Error("another address was shut out with it")
	}
}

// A stolen password can open as many sign-ins as it likes before the first
// wrong code. Once the address is shut out they all go, or each would give
// five more tries.
func TestAShutOutAddressEndsTheAccountsSignIns(t *testing.T) {
	t.Parallel()
	s, _ := withAccounts(t, "admin", "watcher")
	const addr = "192.0.2.5"
	open := []string{s.PendingLogin("admin"), s.PendingLogin("admin"), s.PendingLogin("admin")}
	bystander := s.PendingLogin("watcher")

	for i := range MaxFailures - 1 {
		s.FailPending(open[i%len(open)], addr)
	}
	for i, token := range open {
		if _, _, ok := s.PendingUser(token); !ok {
			t.Fatalf("sign-in %d ended with tries left", i)
		}
	}
	s.FailPending(open[0], addr)
	for i, token := range open {
		if _, _, ok := s.PendingUser(token); ok {
			t.Errorf("sign-in %d outlived the shut-out address", i)
		}
	}
	if _, _, ok := s.PendingUser(bystander); !ok {
		t.Error("another account's sign-in ended with it")
	}
}

// MFA has to hold against someone who knows the password.
// Were each password check to forgive the address its wrong codes, that
// someone would get fresh tries every time, for ever.
func TestAPasswordCheckDoesNotForgiveWrongCodes(t *testing.T) {
	t.Parallel()
	s, now := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	const addr = "192.0.2.7"
	for range MaxFailures {
		if err := s.Authenticate("admin", goodPassword, addr); err != nil {
			t.Fatal(err)
		}
		s.FailPending(s.PendingLogin("admin"), addr)
	}
	if err := s.Authenticate("admin", goodPassword, addr); !errors.Is(err, ErrRateLimited) {
		t.Errorf("password after %d wrong codes: %v, want ErrRateLimited", MaxFailures, err)
	}
}

func TestAuthenticateChecksThePasswordAndMakesNoSession(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	const addr = "192.0.2.8"
	if err := s.Authenticate("admin", goodPassword, addr); err != nil {
		t.Fatal(err)
	}
	if n := len(s.sessions.sessions); n != 0 {
		t.Errorf("%d sessions after Authenticate", n)
	}
	for range MaxFailures {
		if err := s.Authenticate("admin", "wrong password here", addr); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password: %v, want ErrInvalidCredentials", err)
		}
	}
	if err := s.Authenticate("admin", goodPassword, addr); !errors.Is(err, ErrRateLimited) {
		t.Errorf("after %d wrong passwords: %v, want ErrRateLimited", MaxFailures, err)
	}
}

func TestChallengesAreTakenOnce(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	s.SetChallenge("pending-token", []byte("first"))
	s.SetChallenge("pending-token", []byte("second"))
	s.SetChallenge("session-id", []byte("other"))
	if got, ok := s.TakeChallenge("pending-token"); !ok || string(got) != "second" {
		t.Errorf("TakeChallenge = %q, %v; want the latest, second", got, ok)
	}
	if _, ok := s.TakeChallenge("pending-token"); ok {
		t.Error("a challenge was handed out twice")
	}
	if got, ok := s.TakeChallenge("session-id"); !ok || string(got) != "other" {
		t.Errorf("taking one challenge disturbed another: %q, %v", got, ok)
	}
	if _, ok := s.TakeChallenge("never-set"); ok {
		t.Error("a challenge nobody set")
	}

	s.SetChallenge("early", []byte("x"))
	*now = now.Add(challengeLife - time.Second)
	s.SetChallenge("late", []byte("y"))
	*now = now.Add(2 * time.Second)
	if _, ok := s.TakeChallenge("early"); ok {
		t.Error("a challenge outlived its two minutes")
	}
	if _, ok := s.TakeChallenge("late"); !ok {
		t.Error("a challenge ended before its two minutes")
	}
}

// Scanning a QR code takes longer than answering a prompt: installing the
// app first can take minutes.
func TestTheSecretBehindAQRCodeIsKeptLonger(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	s.SetChallenge("totp:session-id", []byte("secret"))
	*now = now.Add(challengeLife + time.Minute)
	if got, ok := s.TakeChallenge("totp:session-id"); !ok || string(got) != "secret" {
		t.Fatalf("after %v: %q, %v", challengeLife+time.Minute, got, ok)
	}
	s.SetChallenge("totp:session-id", []byte("secret"))
	*now = now.Add(totpSetupLife + time.Second)
	if _, ok := s.TakeChallenge("totp:session-id"); ok {
		t.Error("the secret was kept for ever")
	}
}

func TestBypassFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, BypassFile)
	if _, ok := ReadBypass(dir); ok {
		t.Error("a bypass in an empty directory")
	}

	until := time.Now().Add(time.Hour)
	if err := WriteBypass(dir, Bypass{Until: until, By: "root"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("bypass file: %v, %v", info, err)
	}
	if got, ok := ReadBypass(dir); !ok || got.By != "root" || !got.Until.Equal(until) {
		t.Errorf("ReadBypass = %+v, %v", got, ok)
	}

	if err := WriteBypass(dir, Bypass{Until: time.Now().Add(-time.Second), By: "root"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadBypass(dir); ok {
		t.Error("a bypass that has ended still counts")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadBypass(dir); ok {
		t.Error("a bypass file nobody can read still counts")
	}

	if err := WriteBypass(dir, Bypass{Until: until}); err != nil {
		t.Fatal(err)
	}
	if err := ClearBypass(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadBypass(dir); ok {
		t.Error("a bypass that was cleared still counts")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bypass file after clearing: %v", err)
	}
	if err := ClearBypass(dir); err != nil {
		t.Errorf("clearing none: %v", err)
	}
}

// The command line starts and ends a bypass while the daemon runs, and the
// daemon's own clock decides when one has ended.
func TestTheServiceFollowsTheBypassFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if s.Dir() != dir {
		t.Errorf("Dir = %q, want %q", s.Dir(), dir)
	}
	if _, ok := s.Bypass(); ok {
		t.Fatal("a bypass nobody started")
	}

	if err := WriteBypass(dir, Bypass{Until: now.Add(10 * time.Minute), By: "root"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Bypass(); !ok || got.By != "root" {
		t.Errorf("Bypass = %+v, %v", got, ok)
	}
	now = now.Add(11 * time.Minute)
	if _, ok := s.Bypass(); ok {
		t.Error("the bypass outlived its end")
	}

	if err := WriteBypass(dir, Bypass{Until: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Bypass(); !ok {
		t.Error("a bypass started later is not seen")
	}
	if err := ClearBypass(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Bypass(); ok {
		t.Error("a bypass ended from the command line is still seen")
	}
}

// A credential belongs to one account: were two to hold it, one device
// would sign in as either.
func TestAPasskeyBelongsToOneAccount(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin", "watcher")
	for _, name := range []string{"admin", "watcher"} {
		if err := s.EnableTOTP(name, testSecret, *now); err != nil {
			t.Fatal(err)
		}
	}
	desk := testPasskey("desk")
	if err := s.AddPasskey("admin", desk); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey("watcher", desk); !errors.Is(err, ErrPasskeyExists) {
		t.Errorf("another account's passkey: %v, want ErrPasskeyExists", err)
	}
	if keys := s.Passkeys("watcher"); len(keys) != 0 {
		t.Errorf("the refused passkey was kept: %+v", keys)
	}
}

func TestRenamingAPasskey(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin", "watcher")
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	desk, phone := testPasskey("desk"), testPasskey("phone")
	for _, k := range []Passkey{desk, phone} {
		if err := s.AddPasskey("admin", k); err != nil {
			t.Fatal(err)
		}
	}
	held := s.Passkeys("admin")

	*now = now.Add(time.Hour)
	if err := s.RenamePasskey("admin", desk.ID, "office"); err != nil {
		t.Fatal(err)
	}
	got := s.Passkeys("admin")
	if got[0].Name != "office" || got[1].Name != "phone" || got[0].PublicKey != desk.PublicKey {
		t.Errorf("after the rename: %+v", got)
	}
	if held[0].Name != "desk" {
		t.Error("the rename changed a list handed out before it")
	}
	if u, _ := s.Account("admin"); !u.UpdatedAt.Equal(*now) {
		t.Errorf("UpdatedAt = %v, want %v", u.UpdatedAt, *now)
	}
	if err := s.RenamePasskey("admin", desk.ID, "office"); err != nil {
		t.Errorf("the same name again: %v", err)
	}
	if err := s.RenamePasskey("admin", "no-such", "x"); !errors.Is(err, ErrNoSuchPasskey) {
		t.Errorf("an unknown passkey: %v, want ErrNoSuchPasskey", err)
	}
	if err := s.RenamePasskey("watcher", desk.ID, "x"); !errors.Is(err, ErrNoSuchPasskey) {
		t.Errorf("another account's passkey: %v, want ErrNoSuchPasskey", err)
	}
	if err := s.RenamePasskey("ghost", desk.ID, "x"); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("an unknown account: %v, want ErrNoSuchUser", err)
	}

	restarted, err := NewService(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if again := restarted.Passkeys("admin"); again[0].Name != "office" {
		t.Errorf("after a restart: %+v", again)
	}
}

func TestPasskeySignInSwitch(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	if err := s.SetPasskeyLogin("admin", true); !errors.Is(err, ErrNoPasskeys) {
		t.Fatalf("on with no passkey: %v, want ErrNoPasskeys", err)
	}
	if err := s.SetPasskeyLogin("admin", false); err != nil {
		t.Errorf("off when off: %v", err)
	}
	if err := s.SetPasskeyLogin("ghost", false); !errors.Is(err, ErrNoSuchUser) {
		t.Errorf("an unknown account: %v, want ErrNoSuchUser", err)
	}
	if err := s.EnableTOTP("admin", testSecret, *now); err != nil {
		t.Fatal(err)
	}
	desk, phone := testPasskey("desk"), testPasskey("phone")
	for _, k := range []Passkey{desk, phone} {
		if err := s.AddPasskey("admin", k); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetPasskeyLogin("admin", true); err != nil {
		t.Fatal(err)
	}
	if _, _, alone := s.MFAOf("admin"); !alone {
		t.Error("MFAOf says the switch is off")
	}
	if u, _ := s.Account("admin"); !u.PasskeyLogin {
		t.Error("Account says the switch is off")
	}
	restarted, err := NewService(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, alone := restarted.MFAOf("admin"); !alone {
		t.Error("the switch did not survive a restart")
	}

	if err := s.RemovePasskey("admin", desk.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, alone := s.MFAOf("admin"); !alone {
		t.Error("removing one of two passkeys turned the switch off")
	}
	if err := s.RemovePasskey("admin", phone.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, alone := s.MFAOf("admin"); alone {
		t.Error("the switch stayed on with no passkey left")
	}

	if err := s.AddPasskey("admin", desk); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasskeyLogin("admin", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasskeyLogin("admin", false); err != nil {
		t.Fatal(err)
	}
	if _, _, alone := s.MFAOf("admin"); alone {
		t.Error("the switch did not turn off")
	}
}

// A sign-in opened by a name alone has not passed the password, and the
// routes that finish one need to tell.
func TestPasswordlessSignIns(t *testing.T) {
	t.Parallel()
	s, _ := withAccounts(t, "admin")
	token := s.StartPasskeyLogin("admin")
	if raw, err := base64.RawURLEncoding.DecodeString(token); err != nil || len(raw) != 32 {
		t.Fatalf("token %q is not 32 bytes of base64url (%v)", token, err)
	}
	if user, checked, ok := s.PendingUser(token); !ok || user != "admin" || checked {
		t.Errorf("PendingUser = %q, %v, %v; want admin, not checked", user, checked, ok)
	}
	sess, err := s.ResolvePendingFrom(token, Client{Address: "192.0.2.9", Agent: "Testbrowser/1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != "192.0.2.9" || sess.Agent != "Testbrowser/1.0" {
		t.Errorf("the session keeps %q, %q", sess.Address, sess.Agent)
	}
	if _, err := s.ResolvePendingFrom(token, Client{}); !errors.Is(err, ErrPendingGone) {
		t.Errorf("resolving twice: %v, want ErrPendingGone", err)
	}
}

// Anyone can open a passwordless sign-in with a name, so how many are held
// has a ceiling, and reaching it costs no sign-in after the password.
func TestPasswordlessSignInsAreCapped(t *testing.T) {
	t.Parallel()
	s, now := withAccounts(t, "admin")
	afterPassword := s.PendingLogin("admin")
	first := s.StartPasskeyLogin("admin")
	*now = now.Add(time.Second)
	second := s.StartPasskeyLogin("admin")
	*now = now.Add(time.Second)
	for range maxPasswordless - 2 {
		s.StartPasskeyLogin("admin")
	}
	if _, _, ok := s.PendingUser(first); !ok {
		t.Fatal("a sign-in went before the ceiling was reached")
	}
	s.StartPasskeyLogin("admin")
	if _, _, ok := s.PendingUser(first); ok {
		t.Error("the oldest sign-in outlived the ceiling")
	}
	if _, _, ok := s.PendingUser(second); !ok {
		t.Error("more than the oldest went")
	}
	if _, _, ok := s.PendingUser(afterPassword); !ok {
		t.Error("a sign-in past the password went to make room")
	}
	held := 0
	for _, p := range s.mfa.pending {
		if p.passwordless {
			held++
		}
	}
	if held != maxPasswordless {
		t.Errorf("%d passwordless sign-ins held, want %d", held, maxPasswordless)
	}
}

// Finishing a sign-in forgives the address as a right password does.
func TestResolvingASignInForgivesTheAddress(t *testing.T) {
	t.Parallel()
	s, _ := withAccounts(t, "admin")
	const addr = "192.0.2.10"
	for range MaxFailures - 1 {
		s.limiter.failure(addr)
	}
	if _, err := s.ResolvePending(s.PendingLogin("admin"), addr); err != nil {
		t.Fatal(err)
	}
	for range MaxFailures - 1 {
		s.limiter.failure(addr)
	}
	if s.Blocked(addr) {
		t.Error("failures from before the sign-in still count")
	}
	s.limiter.failure(addr)
	if !s.Blocked(addr) || s.Blocked("192.0.2.11") {
		t.Errorf("Blocked: %v for the address, %v for another", s.Blocked(addr), s.Blocked("192.0.2.11"))
	}
}

func TestLoginKeepsTheClient(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)
	if err := s.Setup("admin", goodPassword); err != nil {
		t.Fatal(err)
	}
	sess, err := s.LoginFrom("admin", goodPassword, Client{Address: "198.51.100.4", Agent: "Testbrowser/2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != "198.51.100.4" || sess.Agent != "Testbrowser/2.0" {
		t.Errorf("LoginFrom kept %q, %q", sess.Address, sess.Agent)
	}
	sess, err = s.Login("admin", goodPassword, "198.51.100.5")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != "198.51.100.5" || sess.Agent != "" {
		t.Errorf("Login kept %q, %q", sess.Address, sess.Agent)
	}
	if _, err := s.LoginFrom("admin", "wrong password here", Client{Address: "198.51.100.6"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong password: %v, want ErrInvalidCredentials", err)
	}
}
