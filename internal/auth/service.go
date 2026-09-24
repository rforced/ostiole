package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"
)

// UsersFile is the account database inside the config directory. It is
// deliberately separate from config.json so credentials never end up in
// revisions or exports.
const UsersFile = "users.json"

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)

// Errors returned by Service.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many failed logins; try again later")
	ErrSetupDone          = errors.New("an account already exists")
	ErrInvalidUsername    = errors.New("username must be 1-32 lowercase letters, digits, '_', '.', or '-' and start with a letter")
	ErrUserExists         = errors.New("an account with that name already exists")
	ErrNoSuchUser         = errors.New("no such account")
	ErrLastAdmin          = errors.New("this is the only administrator; promote another account first")
	ErrLastAccount        = errors.New("cannot delete the last account")
	ErrUnknownRole        = errors.New("unknown role")
	ErrBusy               = errors.New("too many sign-ins at once; try again in a moment")
)

// hashSlots is how many passwords are hashed or checked at once. Each
// takes 64 MiB, and the limiter only counts a failure once it is done, so
// a burst of logins from one client would otherwise ask for as much
// memory as it liked.
const hashSlots = 2

// hashWait is how long a sign-in waits for a slot before it is refused.
const hashWait = 5 * time.Second

// User is a local administrator.
type User struct {
	Username string `json:"username"`
	Hash     string `json:"hash"`
	// Role decides what this account may do. An account written before
	// roles existed has none, and is treated as an administrator: the router
	// had exactly one kind of user then.
	Role      Role      `json:"role,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// RoleOf returns the account's role, defaulting to administrator for an
// account that predates roles.
func (u User) RoleOf() Role {
	if u.Role.Valid() {
		return u.Role
	}
	return RoleAdmin
}

type usersFile struct {
	Version int    `json:"version"`
	Users   []User `json:"users"`
}

// Service is the authentication facade used by the API and CLI.
type Service struct {
	path     string
	now      func() time.Time
	sessions *sessionStore
	limiter  *limiter
	// hashing holds a token for each argon2 computation running.
	hashing  chan struct{}
	hashWait time.Duration

	// SessionLoadError says why the stored sessions could not be read, if
	// they could not. Everyone logs in again; nothing else breaks.
	SessionLoadError error

	mu       sync.RWMutex
	users    map[string]User
	loadedAt time.Time // mtime of the file when last read
	loadedSz int64
}

// NewService loads (or lazily creates) the users file in dir.
func NewService(dir string) (*Service, error) {
	s := &Service{
		path: filepath.Join(dir, UsersFile), now: time.Now, users: map[string]User{},
		hashing: make(chan struct{}, hashSlots), hashWait: hashWait,
	}
	s.sessions = newSessionStore(dir, func() time.Time { return s.now() })
	s.limiter = newLimiter(func() time.Time { return s.now() })
	if err := s.load(); err != nil {
		return nil, err
	}
	// Sessions outlive a restart so an update does not log everybody out.
	// A file that will not load is not fatal: the worst it costs is a
	// round of logins, which is better than a daemon that will not start.
	if err := s.sessions.load(); err != nil {
		s.SessionLoadError = err
	}
	// Anyone whose account went away while the daemon was down loses their
	// session with it.
	s.mu.RLock()
	known := make(map[string]bool, len(s.users))
	for name := range s.users {
		known[name] = true
	}
	s.mu.RUnlock()
	s.sessions.keepOnly(known)
	return s, nil
}

// load reads the users file. It is a no-op when the file is unchanged
// since the last read; when users changed on disk (e.g. `ostiole
// reset-password` while the daemon runs) their sessions are dropped.
func (s *Service) load() error {
	info, err := os.Stat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for name := range s.users {
			s.sessions.deleteUser(name)
		}
		s.users = map[string]User{}
		s.loadedAt, s.loadedSz = time.Time{}, 0
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.RLock()
	fresh := info.ModTime().Equal(s.loadedAt) && info.Size() == s.loadedSz && !s.loadedAt.IsZero()
	s.mu.RUnlock()
	if fresh {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var f usersFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := map[string]User{}
	for _, u := range f.Users {
		next[u.Username] = u
	}
	for name, old := range s.users {
		if nu, ok := next[name]; !ok || nu.Hash != old.Hash {
			s.sessions.deleteUser(name)
		}
	}
	s.users = next
	s.loadedAt, s.loadedSz = info.ModTime(), info.Size()
	return nil
}

// refresh reloads the file if it changed, logging nothing on failure so a
// transient error never locks admins out of a working in-memory state.
func (s *Service) refresh() {
	_ = s.load()
}

func (s *Service) save() error {
	f := usersFile{Version: 1}
	for _, u := range s.users {
		f.Users = append(f.Users, u)
	}
	sort.Slice(f.Users, func(i, j int) bool { return f.Users[i].Username < f.Users[j].Username })
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".users.*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		s.loadedAt, s.loadedSz = info.ModTime(), info.Size()
	}
	return nil
}

// NeedsSetup reports whether no account exists yet.
func (s *Service) NeedsSetup() bool {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) == 0
}

// Role reports what an account may do; an unknown account gets the
// weakest role rather than an error, so a caller cannot be surprised into
// granting more than it meant to.
func (s *Service) Role(username string) Role {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return RoleViewer
	}
	return u.RoleOf()
}

// Account looks one account up, without its password hash. The second
// result is false when there is no such account, which is how a caller
// tells a rename or a password reset for a name that does not exist from
// one that does.
func (s *Service) Account(username string) (User, bool) {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[username]
	if !ok {
		return User{}, false
	}
	u.Hash = ""
	u.Role = u.RoleOf()
	return u, true
}

// Accounts lists accounts with their roles, for the UI.
func (s *Service) Accounts() []User {
	out := s.Users()
	for i := range out {
		out[i].Hash = ""
		out[i].Role = out[i].RoleOf()
	}
	return out
}

// Usernames lists accounts.
func (s *Service) Usernames() []string {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.users))
	for n := range s.users {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Users returns every account, password hashes included, sorted by name.
// It exists for backups; nothing that answers a request should use it.
func (s *Service) Users() []User {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out
}

// Restore replaces every account with the given set, which is how a
// backup brings its administrators back. An empty list is refused: that
// would lock everyone out with no way in but the CLI.
func (s *Service) Restore(users []User) error {
	if len(users) == 0 {
		return errors.New("a restore needs at least one account")
	}
	for _, u := range users {
		if !usernameRe.MatchString(u.Username) {
			return ErrInvalidUsername
		}
		if u.Hash == "" {
			return fmt.Errorf("account %q has no password", u.Username)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = make(map[string]User, len(users))
	for _, u := range users {
		s.users[u.Username] = u
	}
	return s.save()
}

// turn waits for a hashing slot; the caller gives it back.
func (s *Service) turn() (func(), error) {
	t := time.NewTimer(s.hashWait)
	defer t.Stop()
	select {
	case s.hashing <- struct{}{}:
		return func() { <-s.hashing }, nil
	case <-t.C:
		return nil, ErrBusy
	}
}

// hash is HashPassword in its turn.
func (s *Service) hash(password string) (string, error) {
	if err := checkPassword(password); err != nil {
		return "", err
	}
	done, err := s.turn()
	if err != nil {
		return "", err
	}
	defer done()
	return HashPassword(password)
}

// Setup creates the first account, as an administrator: the operator
// running first-run setup has to be able to manage everything afterwards.
// It fails once any account exists. The check is made again under the
// lock the write takes, so two setups racing each other, or one racing
// `ostiole reset-password`, leave one account rather than two.
func (s *Service) Setup(username, password string) error {
	// Cheap first, so a request after setup never costs a hash.
	if !s.NeedsSetup() {
		return ErrSetupDone
	}
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	return s.exclusive(func() error {
		if len(s.users) > 0 {
			return ErrSetupDone
		}
		now := s.now()
		s.users[username] = User{Username: username, Hash: hash, Role: RoleAdmin, CreatedAt: now, UpdatedAt: now}
		return s.save()
	})
}

// exclusive runs fn with the users file locked against other processes and
// read fresh, and the accounts locked against this one, so what fn checks
// is still true when it writes.
func (s *Service) exclusive(fn func() error) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".users.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", s.path, err)
	}
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn()
}

// CreateUser adds an account. It is separate from SetPassword so that
// creating an account is never something a typo does: a name that is
// already taken is an error here, not a silent password reset.
func (s *Service) CreateUser(username, password string, role Role) error {
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	if !role.Valid() {
		return fmt.Errorf("%w %q", ErrUnknownRole, role)
	}
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	return s.exclusive(func() error {
		if _, taken := s.users[username]; taken {
			return ErrUserExists
		}
		now := s.now()
		s.users[username] = User{Username: username, Hash: hash, Role: role, CreatedAt: now, UpdatedAt: now}
		return s.save()
	})
}

// SetPassword creates or updates an account and invalidates its sessions.
// An account it has to create becomes an administrator, because the one
// caller that reaches this with an unknown name is `ostiole
// reset-password` at the console, recovering a router nobody can log in to.
func (s *Service) SetPassword(username, password string) error {
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	hash, err := s.hash(password)
	if err != nil {
		return err
	}
	return s.exclusive(func() error {
		now := s.now()
		u, ok := s.users[username]
		if !ok {
			u = User{Username: username, Role: RoleAdmin, CreatedAt: now}
		}
		u.Hash = hash
		u.UpdatedAt = now
		s.users[username] = u
		if err := s.save(); err != nil {
			return err
		}
		s.sessions.deleteUser(username)
		return nil
	})
}

// Rename changes an account's username, keeping its password, its role,
// and its sessions. Sessions follow the name because renaming yourself is
// the one change an administrator may make to their own account, and it
// would be a poor one if it logged them out.
func (s *Service) Rename(username, next string) error {
	if !usernameRe.MatchString(next) {
		return ErrInvalidUsername
	}
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	if next == username {
		return nil
	}
	if _, taken := s.users[next]; taken {
		return ErrUserExists
	}
	delete(s.users, username)
	u.Username = next
	u.UpdatedAt = s.now()
	s.users[next] = u
	if err := s.save(); err != nil {
		return err
	}
	s.sessions.rename(username, next)
	return nil
}

// SetRole changes what an account may do. The last administrator keeps
// the role: a router with nobody who can manage accounts is a router you have
// to rebuild.
func (s *Service) SetRole(username string, role Role) error {
	if !role.Valid() {
		return fmt.Errorf("%w %q", ErrUnknownRole, role)
	}
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[username]
	if !ok {
		return fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	if u.RoleOf() == RoleAdmin && role != RoleAdmin && s.countAdmins() == 1 {
		return ErrLastAdmin
	}
	u.Role = role
	u.UpdatedAt = s.now()
	s.users[username] = u
	return s.save()
}

// countAdmins is called with the lock held.
func (s *Service) countAdmins() int {
	n := 0
	for _, u := range s.users {
		if u.RoleOf() == RoleAdmin {
			n++
		}
	}
	return n
}

// DeleteUser removes an account and its sessions. The last account cannot
// be removed.
func (s *Service) DeleteUser(username string) error {
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	if len(s.users) == 1 {
		return ErrLastAccount
	}
	if s.users[username].RoleOf() == RoleAdmin && s.countAdmins() == 1 {
		return ErrLastAdmin
	}
	delete(s.users, username)
	if err := s.save(); err != nil {
		return err
	}
	s.sessions.deleteUser(username)
	return nil
}

// dummyHash keeps unknown-user logins as slow as wrong-password logins.
var dummyHash = func() string {
	h, _ := HashPassword("not-a-real-password-just-for-timing")
	return h
}()

// Login checks credentials, applying per-address rate limiting, and
// returns a new session on success. remote should be the client IP.
func (s *Service) Login(username, password, remote string) (*Session, error) {
	if s.limiter.blocked(remote) {
		return nil, ErrRateLimited
	}
	done, err := s.turn()
	if err != nil {
		return nil, err
	}
	// The slot goes back once this one is counted, so the next sign-in from
	// the same address to get it sees the failure.
	defer done()
	// Failures from the same address may have landed while this waited,
	// and a burst that all got past the first look must not all be tried.
	if s.limiter.blocked(remote) {
		return nil, ErrRateLimited
	}
	s.refresh()
	s.mu.RLock()
	u, ok := s.users[username]
	s.mu.RUnlock()
	hash := dummyHash
	if ok {
		hash = u.Hash
	}
	match, err := VerifyPassword(hash, password)
	if err != nil || !match || !ok {
		s.limiter.failure(remote)
		return nil, ErrInvalidCredentials
	}
	s.limiter.success(remote)
	return s.sessions.create(username), nil
}

// Session resolves a session ID, refreshing its idle timer.
func (s *Service) Session(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.refresh()
	return s.sessions.get(id)
}

// SignIn starts a session for an account the caller has just created or
// given a new password. Checking the password again would cost a second
// hash, and could be refused for want of a slot after the change was made.
func (s *Service) SignIn(username string) (*Session, error) {
	s.refresh()
	s.mu.RLock()
	_, ok := s.users[username]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	return s.sessions.create(username), nil
}

// Peek resolves a session ID without counting as use of it: a response
// that stays open, like a log stream, checks it is still wanted this way,
// and must not keep the session from idling out.
func (s *Service) Peek(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}
	s.refresh()
	return s.sessions.peek(id)
}

// Logout ends a session.
func (s *Service) Logout(id string) {
	s.sessions.delete(id)
}
