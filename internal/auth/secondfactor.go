package auth

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ostiole/internal/atomicfile"
)

// TOTP is an authenticator app an account has set up.
type TOTP struct {
	// Secret is base32, upper case, without padding.
	Secret    string    `json:"secret"`
	EnabledAt time.Time `json:"enabledAt"`
	// LastStep is the last 30 second step accepted, so a code works once.
	LastStep int64 `json:"lastStep,omitempty"`
}

// Passkey is a WebAuthn credential an account can sign in with.
type Passkey struct {
	// ID is the credential id, base64url without padding.
	ID   string `json:"id"`
	Name string `json:"name"`
	// PublicKey is the COSE key, base64url without padding.
	PublicKey string `json:"publicKey"`
	SignCount uint32 `json:"signCount"`
	// RPID is the host name the passkey was made on, and the only one it works on.
	RPID       string    `json:"rpId"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsed   time.Time `json:"lastUsed,omitzero"`
	Transports []string  `json:"transports,omitempty"`
}

// Errors the second factor can produce.
var (
	ErrTOTPFirst      = errors.New("set up an authenticator app before adding a passkey")
	ErrCodeUsed       = errors.New("that code was used already")
	ErrNoSuchPasskey  = errors.New("no such passkey")
	ErrNoSecondFactor = errors.New("this account has no second factor")
	ErrPasskeyExists  = errors.New("that passkey is already registered")
	ErrPendingGone    = errors.New("that sign-in timed out; enter the password again")
)

var (
	errUnchanged = errors.New("nothing to change")
	errBadSecret = errors.New("the authenticator secret is not base32")
)

// How long a sign-in waits for its second factor, how many wrong codes it
// allows, and how long a challenge is kept. The secret behind a QR code is
// kept under a "totp:" token and outlasts a challenge: installing an
// authenticator app takes minutes.
const (
	pendingLife     = 5 * time.Minute
	pendingFailures = 5
	challengeLife   = 2 * time.Minute
	totpSetupLife   = 10 * time.Minute
	totpSetupPrefix = "totp:"
)

// secondFactor is what the second step of a sign-in keeps in memory: the
// sign-ins waiting for it and the challenges handed out. A restart loses
// them, which costs a sign-in its second step and nothing more.
type secondFactor struct {
	mu         sync.Mutex
	pending    map[string]*pendingSignIn
	challenges map[string]heldChallenge
}

type pendingSignIn struct {
	username string
	expires  time.Time
	failures int
}

type heldChallenge struct {
	data    []byte
	expires time.Time
}

func newSecondFactor() *secondFactor {
	return &secondFactor{pending: map[string]*pendingSignIn{}, challenges: map[string]heldChallenge{}}
}

// sweep drops what has run out; called with the lock held.
func (f *secondFactor) sweep(now time.Time) {
	for token, p := range f.pending {
		if now.After(p.expires) {
			delete(f.pending, token)
		}
	}
	for token, c := range f.challenges {
		if now.After(c.expires) {
			delete(f.challenges, token)
		}
	}
}

func hasSecondFactor(u User) bool { return u.TOTP != nil || len(u.Passkeys) > 0 }

// withoutSecrets is the account as a list shows it: no password hash, no
// authenticator secret, no passkey public keys. Copies from Users share the
// stored TOTP and Passkeys, so these are replaced and never blanked in place.
func (u User) withoutSecrets() User {
	u.Hash = ""
	if u.TOTP != nil {
		t := *u.TOTP
		t.Secret = ""
		u.TOTP = &t
	}
	u.Passkeys = copyPasskeys(u.Passkeys)
	for i := range u.Passkeys {
		u.Passkeys[i].PublicKey = ""
	}
	return u
}

// copyPasskeys copies the list and each passkey's transports, so what a
// caller gets back can be changed without touching the account.
func copyPasskeys(keys []Passkey) []Passkey {
	out := slices.Clone(keys)
	for i := range out {
		out[i].Transports = slices.Clone(out[i].Transports)
	}
	return out
}

// lookup returns the stored account, secrets included.
func (s *Service) lookup(username string) (User, bool) {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	return u, ok
}

// edit changes one account under the file lock and saves it. fn must replace
// u.TOTP and u.Passkeys rather than edit them, because copies handed out by
// Users share them. It may return errUnchanged to save nothing.
func (s *Service) edit(username string, fn func(u *User) error) error {
	return s.exclusive(func() error {
		u, ok := s.users[username]
		if !ok {
			return fmt.Errorf("%w %q", ErrNoSuchUser, username)
		}
		switch err := fn(&u); {
		case errors.Is(err, errUnchanged):
			return nil
		case err != nil:
			return err
		}
		s.users[username] = u
		return s.save()
	})
}

// secondFactorDue reports whether signing in as u still takes a second
// factor: it has one and no bypass is running.
func (s *Service) secondFactorDue(u User) bool {
	if !hasSecondFactor(u) {
		return false
	}
	_, bypassed := s.Bypass()
	return !bypassed
}

// HasSecondFactor reports whether the account has an authenticator app or
// a passkey.
func (s *Service) HasSecondFactor(username string) bool {
	u, ok := s.lookup(username)
	return ok && hasSecondFactor(u)
}

// SecondFactorOf lists what an account has, never the secrets.
func (s *Service) SecondFactorOf(username string) (totp bool, passkeys []Passkey) {
	u, ok := s.lookup(username)
	if !ok {
		return false, nil
	}
	return u.TOTP != nil, u.withoutSecrets().Passkeys
}

// UserID returns the account's WebAuthn user handle, making and saving
// one the first time.
func (s *Service) UserID(username string) (string, error) {
	known, ok := s.lookup(username)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	if known.ID != "" {
		return known.ID, nil
	}
	var id string
	err := s.edit(username, func(u *User) error {
		if u.ID != "" {
			id = u.ID
			return errUnchanged
		}
		made, err := randomHex(16)
		if err != nil {
			return err
		}
		u.ID, id = made, made
		return nil
	})
	return id, err
}

// EnableTOTP sets up an authenticator app for the account, replacing any
// it had.
func (s *Service) EnableTOTP(username, secret string, at time.Time) error {
	secret = strings.ToUpper(strings.TrimRight(secret, "="))
	if raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret); err != nil || len(raw) == 0 {
		return errBadSecret
	}
	if at.IsZero() {
		at = s.now()
	}
	return s.edit(username, func(u *User) error {
		u.TOTP = &TOTP{Secret: secret, EnabledAt: at}
		u.UpdatedAt = s.now()
		return nil
	})
}

// UseTOTPStep records an accepted step and refuses one at or before the
// last (replay).
func (s *Service) UseTOTPStep(username string, step int64) error {
	return s.edit(username, func(u *User) error {
		if u.TOTP == nil {
			return ErrNoSecondFactor
		}
		if step <= u.TOTP.LastStep {
			return ErrCodeUsed
		}
		t := *u.TOTP
		t.LastStep = step
		u.TOTP = &t
		return nil
	})
}

// TOTPSecret returns the authenticator secret, for checking a code and
// nothing else.
func (s *Service) TOTPSecret(username string) (string, bool) {
	u, ok := s.lookup(username)
	if !ok || u.TOTP == nil {
		return "", false
	}
	return u.TOTP.Secret, true
}

// AddPasskey adds a passkey; the account needs an authenticator app first.
func (s *Service) AddPasskey(username string, p Passkey) error {
	return s.edit(username, func(u *User) error {
		if u.TOTP == nil {
			return ErrTOTPFirst
		}
		if slices.ContainsFunc(u.Passkeys, func(k Passkey) bool { return k.ID == p.ID }) {
			return ErrPasskeyExists
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = s.now()
		}
		p.Transports = slices.Clone(p.Transports)
		u.Passkeys = append(slices.Clone(u.Passkeys), p)
		u.UpdatedAt = s.now()
		return nil
	})
}

// RemovePasskey removes one of the account's passkeys.
func (s *Service) RemovePasskey(username, id string) error {
	return s.edit(username, func(u *User) error {
		i := slices.IndexFunc(u.Passkeys, func(k Passkey) bool { return k.ID == id })
		if i < 0 {
			return ErrNoSuchPasskey
		}
		u.Passkeys = slices.Delete(slices.Clone(u.Passkeys), i, i+1)
		u.UpdatedAt = s.now()
		return nil
	})
}

// Passkeys lists the account's passkeys with their public keys, for
// checking a sign-in.
func (s *Service) Passkeys(username string) []Passkey {
	u, _ := s.lookup(username)
	return copyPasskeys(u.Passkeys)
}

// PasskeyUsed records a sign-in with a passkey: its new sign count and when.
func (s *Service) PasskeyUsed(username, id string, signCount uint32, at time.Time) error {
	return s.edit(username, func(u *User) error {
		i := slices.IndexFunc(u.Passkeys, func(k Passkey) bool { return k.ID == id })
		if i < 0 {
			return ErrNoSuchPasskey
		}
		keys := slices.Clone(u.Passkeys)
		keys[i].SignCount, keys[i].LastUsed = signCount, at
		u.Passkeys = keys
		return nil
	})
}

// ClearSecondFactor removes the authenticator app and every passkey. Open
// sessions stay; an account with nothing to clear is not an error.
func (s *Service) ClearSecondFactor(username string) error {
	return s.edit(username, func(u *User) error {
		if !hasSecondFactor(*u) {
			return errUnchanged
		}
		u.TOTP, u.Passkeys, u.UpdatedAt = nil, nil, s.now()
		return nil
	})
}

// PendingLogin opens a pending sign-in for an account and returns its
// token (32 random bytes, base64url). It lasts 5 minutes.
func (s *Service) PendingLogin(username string) string {
	token, err := randomSecret()
	if err != nil {
		return ""
	}
	now := s.now()
	f := s.second
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(now)
	f.pending[token] = &pendingSignIn{username: username, expires: now.Add(pendingLife)}
	return token
}

// PendingUser names the account behind a live token.
func (s *Service) PendingUser(token string) (string, bool) {
	f := s.second
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(s.now())
	p, ok := f.pending[token]
	if !ok {
		return "", false
	}
	return p.username, true
}

// ResolvePending ends the pending sign-in with a session. The token works once.
func (s *Service) ResolvePending(token string) (*Session, error) {
	f := s.second
	f.mu.Lock()
	f.sweep(s.now())
	p, ok := f.pending[token]
	delete(f.pending, token)
	f.mu.Unlock()
	if !ok {
		return nil, ErrPendingGone
	}
	return s.SignIn(p.username)
}

// FailPending counts a failed second-factor attempt against the remote
// address like a bad password, and after 5 ends the pending sign-in. Once
// the address is shut out the account's other pending sign-ins end too, so
// a stolen password cannot be turned into many sign-ins, each with tries of
// its own.
func (s *Service) FailPending(token, remote string) {
	s.limiter.failure(remote)
	shut := s.limiter.blocked(remote)
	f := s.second
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(s.now())
	p, ok := f.pending[token]
	if !ok {
		return
	}
	p.failures++
	switch {
	case shut:
		for other, q := range f.pending {
			if q.username == p.username {
				delete(f.pending, other)
			}
		}
	case p.failures >= pendingFailures:
		delete(f.pending, token)
	}
}

// SetChallenge keeps a WebAuthn challenge against a token (a pending
// sign-in or a session id) for 2 minutes, or for 10 when the token starts
// with "totp:". It replaces any kept before.
func (s *Service) SetChallenge(token string, challenge []byte) {
	now := s.now()
	life := challengeLife
	if strings.HasPrefix(token, totpSetupPrefix) {
		life = totpSetupLife
	}
	f := s.second
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(now)
	f.challenges[token] = heldChallenge{data: slices.Clone(challenge), expires: now.Add(life)}
}

// TakeChallenge returns the challenge kept against the token and forgets it.
func (s *Service) TakeChallenge(token string) ([]byte, bool) {
	f := s.second
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweep(s.now())
	c, ok := f.challenges[token]
	delete(f.challenges, token)
	return c.data, ok
}

// BypassFile sits beside the users file. The command line writes it and the
// daemon reads it at every sign-in.
const BypassFile = "second-factor-bypass.json"

// Bypass is a stretch of time in which a sign-in skips the second factor,
// started from the command line to get back into a router.
type Bypass struct {
	Until time.Time `json:"until"`
	// By is who started it, for the UI.
	By string `json:"by,omitempty"`
}

func readBypass(dir string, now time.Time) (Bypass, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, BypassFile))
	if err != nil {
		return Bypass{}, false
	}
	var b Bypass
	if json.Unmarshal(raw, &b) != nil || !b.Until.After(now) {
		return Bypass{}, false
	}
	return b, true
}

// ReadBypass returns the bypass in dir; false when there is none, it
// cannot be read or it has ended.
func ReadBypass(dir string) (Bypass, bool) { return readBypass(dir, time.Now()) }

// WriteBypass starts a bypass in dir, replacing any other.
func WriteBypass(dir string, b Bypass) error {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, BypassFile), append(raw, '\n'), 0o600)
}

// ClearBypass ends the bypass in dir; none running is fine.
func ClearBypass(dir string) error {
	if err := os.Remove(filepath.Join(dir, BypassFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Bypass returns the bypass running now. It reads the file every time: the
// command line writes it while the daemon runs.
func (s *Service) Bypass() (Bypass, bool) { return readBypass(s.Dir(), s.now()) }

// Dir is the directory holding the users file and the bypass.
func (s *Service) Dir() string { return filepath.Dir(s.path) }
