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
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"ostiole/internal/atomicfile"
)

// Session lifetimes. A tab in the background stops polling, so a day idle
// keeps a browser left open overnight signed in; the cap ends even a
// session in daily use once a week.
const (
	SessionIdleTimeout = 24 * time.Hour
	SessionMaxLifetime = 7 * 24 * time.Hour
)

// SessionsFile holds the sessions across a restart, so an update does not
// log everybody out.
const SessionsFile = "sessions.json"

// persistIdle is how far LastSeen may drift before the file is rewritten.
// Writing on every request would put a disk write in front of every API
// call; a minute of drift only ever shortens the idle window, never
// lengthens it.
const persistIdle = time.Minute

// maxAgent is how much of a user agent a session keeps.
const maxAgent = 200

// refLen is how many hex characters of a session key name it in a listing.
const refLen = 16

// Client is where a sign-in came from.
type Client struct {
	Address string
	Agent   string
}

// Session is a logged-in browser or API client.
type Session struct {
	ID       string    `json:"-"`
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
	Address  string    `json:"address,omitempty"`
	Agent    string    `json:"agent,omitempty"`
}

// SessionInfo describes a session without its ID. Ref names it safely.
type SessionInfo struct {
	Ref      string
	Created  time.Time
	LastSeen time.Time
	Expires  time.Time
	Address  string
	Agent    string
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
	Address  string    `json:"address,omitempty"`
	Agent    string    `json:"agent,omitempty"`
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

func sessionRef(key string) string {
	if len(key) < refLen {
		return key
	}
	return key[:refLen]
}

func cleanAgent(agent string) string {
	agent = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(agent, "")))
	if len(agent) <= maxAgent {
		return agent
	}
	cut := maxAgent
	for cut > 0 && !utf8.RuneStart(agent[cut]) {
		cut--
	}
	return strings.TrimSpace(agent[:cut])
}

func (s *sessionStore) create(username string) *Session {
	return s.createFrom(username, Client{})
}

func (s *sessionStore) createFrom(username string, from Client) *Session {
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	now := s.now()
	sess := &Session{
		ID:       base64.RawURLEncoding.EncodeToString(id),
		Username: username,
		Created:  now,
		LastSeen: now,
		Expires:  now.Add(SessionMaxLifetime),
		Address:  from.Address,
		Agent:    cleanAgent(from.Agent),
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

// peek returns a live session without refreshing its idle timer.
func (s *sessionStore) peek(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionKey(id)]
	now := s.now()
	if !ok || now.After(sess.Expires) || now.Sub(sess.LastSeen) > SessionIdleTimeout {
		return nil, false
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

func sessionLive(sess *Session, now time.Time) bool {
	return !now.After(sess.Expires) && now.Sub(sess.LastSeen) <= SessionIdleTimeout
}

// list returns the user's live sessions, most recently used first.
func (s *sessionStore) list(username string) []SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var out []SessionInfo
	for key, sess := range s.sessions {
		if sess.Username != username || !sessionLive(sess, now) {
			continue
		}
		out = append(out, SessionInfo{
			Ref:      sessionRef(key),
			Created:  sess.Created,
			LastSeen: sess.LastSeen,
			Expires:  sess.Expires,
			Address:  sess.Address,
			Agent:    sess.Agent,
		})
	}
	slices.SortFunc(out, func(a, b SessionInfo) int {
		if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
			return c
		}
		if c := b.Created.Compare(a.Created); c != 0 {
			return c
		}
		return strings.Compare(a.Ref, b.Ref)
	})
	return out
}

// deleteRef ends the user's live session named by ref, reporting whether
// there was one.
func (s *sessionStore) deleteRef(username, ref string) bool {
	if len(ref) != refLen {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, sess := range s.sessions {
		if sess.Username == username && sessionLive(sess, now) && sessionRef(key) == ref {
			delete(s.sessions, key)
			delete(s.persisted, key)
			s.persist()
			return true
		}
	}
	return false
}

// deleteOthers ends every session of the user except the one with keepID.
func (s *sessionStore) deleteOthers(username, keepID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := sessionKey(keepID)
	changed := false
	for key, sess := range s.sessions {
		if sess.Username == username && key != keep {
			delete(s.sessions, key)
			delete(s.persisted, key)
			changed = true
		}
	}
	if changed {
		s.persist()
	}
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

// rename moves an account's sessions to its new name, so renaming an
// account does not sign out whoever is using it at the time.
func (s *sessionStore) rename(username, next string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, sess := range s.sessions {
		if sess.Username == username {
			sess.Username = next
			changed = true
		}
	}
	if changed {
		s.persist()
	}
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
			Address:  st.Address,
			Agent:    st.Agent,
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
			Address:  sess.Address,
			Agent:    sess.Agent,
		})
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return
	}
	_ = atomicfile.Write(s.path, raw, 0o600)
}
