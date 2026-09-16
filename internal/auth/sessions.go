package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session lifetimes.
const (
	SessionIdleTimeout = 2 * time.Hour
	SessionMaxLifetime = 24 * time.Hour
)

// SessionsFile holds the sessions across a restart, so an update does not
// log everybody out.
const SessionsFile = "sessions.json"

// persistIdle is how far LastSeen may drift before the file is rewritten.
// Writing on every request would put a disk write in front of every API
// call; a minute of drift only ever shortens the idle window, never
// lengthens it.
const persistIdle = time.Minute

// Session is a logged-in browser or API client.
type Session struct {
	ID       string    `json:"-"`
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
}

// storedSession is a session on disk. The session ID itself is never
// written: only its hash, so the file cannot be replayed as a set of live
// cookies by anyone who gets a copy of it.
type storedSession struct {
	Hash     string    `json:"hash"`
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
}

type sessionsFile struct {
	Sessions []storedSession `json:"sessions"`
}

// sessionStore keys sessions by the hash of their ID, which is what the
// file holds too, so a session survives a restart without the store ever
// keeping the secret it was created with.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
	path     string
	// persisted is the LastSeen written for each session, so a refresh
	// only reaches the disk once it has drifted by persistIdle.
	persisted map[string]time.Time
}

func newSessionStore(dir string, now func() time.Time) *sessionStore {
	s := &sessionStore{
		sessions:  map[string]*Session{},
		persisted: map[string]time.Time{},
		now:       now,
	}
	if dir != "" {
		s.path = filepath.Join(dir, SessionsFile)
	}
	return s
}

func sessionKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func (s *sessionStore) create(username string) *Session {
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	now := s.now()
	sess := &Session{
		ID:       base64.RawURLEncoding.EncodeToString(id),
		Username: username,
		Created:  now,
		LastSeen: now,
		Expires:  now.Add(SessionMaxLifetime),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	key := sessionKey(sess.ID)
	stored := *sess
	stored.ID = ""
	s.sessions[key] = &stored
	s.persisted[key] = now
	s.persist()
	return sess
}

// get returns a live session and refreshes its idle timer.
func (s *sessionStore) get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(id)
	sess, ok := s.sessions[key]
	if !ok {
		return nil, false
	}
	now := s.now()
	if now.After(sess.Expires) || now.Sub(sess.LastSeen) > SessionIdleTimeout {
		delete(s.sessions, key)
		delete(s.persisted, key)
		s.persist()
		return nil, false
	}
	sess.LastSeen = now
	if now.Sub(s.persisted[key]) >= persistIdle {
		s.persisted[key] = now
		s.persist()
	}
	cp := *sess
	cp.ID = id
	return &cp, true
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(id)
	delete(s.sessions, key)
	delete(s.persisted, key)
	s.persist()
}

func (s *sessionStore) deleteUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, sess := range s.sessions {
		if sess.Username == username {
			delete(s.sessions, key)
			delete(s.persisted, key)
		}
	}
	s.persist()
}

// keepOnly drops sessions belonging to accounts that no longer exist,
// which is how a restored or hand-edited users file is honoured on start.
func (s *sessionStore) keepOnly(usernames map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for key, sess := range s.sessions {
		if !usernames[sess.Username] {
			delete(s.sessions, key)
			delete(s.persisted, key)
			changed = true
		}
	}
	if changed {
		s.persist()
	}
}

// sweep drops expired sessions; called with the lock held.
func (s *sessionStore) sweep(now time.Time) {
	for key, sess := range s.sessions {
		if now.After(sess.Expires) || now.Sub(sess.LastSeen) > SessionIdleTimeout {
			delete(s.sessions, key)
			delete(s.persisted, key)
		}
	}
}

// load reads the sessions file, dropping anything that has already run
// out. A file that cannot be read or parsed leaves the store empty and is
// reported: the daemon still starts, everyone simply logs in again.
func (s *sessionStore) load() error {
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f sessionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range f.Sessions {
		if st.Hash == "" || now.After(st.Expires) || now.Sub(st.LastSeen) > SessionIdleTimeout {
			continue
		}
		s.sessions[st.Hash] = &Session{
			Username: st.Username,
			Created:  st.Created,
			LastSeen: st.LastSeen,
			Expires:  st.Expires,
		}
		s.persisted[st.Hash] = st.LastSeen
	}
	return nil
}

// persist writes the store out. Called with the lock held, and best
// effort: a login must not fail because the disk is full or read-only, and
// the only consequence is that the session does not outlive a restart.
func (s *sessionStore) persist() {
	if s.path == "" {
		return
	}
	f := sessionsFile{Sessions: make([]storedSession, 0, len(s.sessions))}
	for key, sess := range s.sessions {
		f.Sessions = append(f.Sessions, storedSession{
			Hash:     key,
			Username: sess.Username,
			Created:  sess.Created,
			LastSeen: sess.LastSeen,
			Expires:  sess.Expires,
		})
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".sessions.*.tmp")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if err := os.Rename(name, s.path); err != nil {
		_ = os.Remove(name)
	}
}
