package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Session lifetimes.
const (
	SessionIdleTimeout = 2 * time.Hour
	SessionMaxLifetime = 24 * time.Hour
)

// Session is a logged-in browser or API client.
type Session struct {
	ID       string    `json:"-"`
	Username string    `json:"username"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
}

func newSessionStore(now func() time.Time) *sessionStore {
	return &sessionStore{sessions: map[string]*Session{}, now: now}
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
	s.sessions[sess.ID] = sess
	return sess
}

// get returns a live session and refreshes its idle timer.
func (s *sessionStore) get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false
	}
	now := s.now()
	if now.After(sess.Expires) || now.Sub(sess.LastSeen) > SessionIdleTimeout {
		delete(s.sessions, id)
		return nil, false
	}
	sess.LastSeen = now
	cp := *sess
	return &cp, true
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *sessionStore) deleteUser(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		if sess.Username == username {
			delete(s.sessions, id)
		}
	}
}

// sweep drops expired sessions; called with the lock held.
func (s *sessionStore) sweep(now time.Time) {
	for id, sess := range s.sessions {
		if now.After(sess.Expires) || now.Sub(sess.LastSeen) > SessionIdleTimeout {
			delete(s.sessions, id)
		}
	}
}
