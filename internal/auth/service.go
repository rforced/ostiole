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
)

// User is a local administrator.
type User struct {
	Username  string    `json:"username"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
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

	mu       sync.RWMutex
	users    map[string]User
	loadedAt time.Time // mtime of the file when last read
	loadedSz int64
}

// NewService loads (or lazily creates) the users file in dir.
func NewService(dir string) (*Service, error) {
	s := &Service{path: filepath.Join(dir, UsersFile), now: time.Now, users: map[string]User{}}
	s.sessions = newSessionStore(func() time.Time { return s.now() })
	s.limiter = newLimiter(func() time.Time { return s.now() })
	if err := s.load(); err != nil {
		return nil, err
	}
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

// Setup creates the first account. It fails once any account exists.
func (s *Service) Setup(username, password string) error {
	if !s.NeedsSetup() {
		return ErrSetupDone
	}
	return s.SetPassword(username, password)
}

// SetPassword creates or updates an account and invalidates its sessions.
func (s *Service) SetPassword(username, password string) error {
	if !usernameRe.MatchString(username) {
		return ErrInvalidUsername
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	u, ok := s.users[username]
	if !ok {
		u = User{Username: username, CreatedAt: now}
	}
	u.Hash = hash
	u.UpdatedAt = now
	s.users[username] = u
	if err := s.save(); err != nil {
		return err
	}
	s.sessions.deleteUser(username)
	return nil
}

// DeleteUser removes an account and its sessions. The last account cannot
// be removed.
func (s *Service) DeleteUser(username string) error {
	s.refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return fmt.Errorf("no such user %q", username)
	}
	if len(s.users) == 1 {
		return errors.New("cannot delete the last account")
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
	if blocked, _ := s.limiter.blocked(remote); blocked {
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

// Logout ends a session.
func (s *Service) Logout(id string) {
	s.sessions.delete(id)
}
