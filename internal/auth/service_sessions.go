package auth

import (
	"errors"
	"fmt"
)

// ErrNoSuchSession is returned when a session to end is not one of the account's.
var ErrNoSuchSession = errors.New("no such session")

func (s *Service) accountExists(username string) bool {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.users[username]
	return ok
}

// SignInFrom is SignIn for a client whose address and user agent the
// session keeps.
func (s *Service) SignInFrom(username string, from Client) (*Session, error) {
	if !s.accountExists(username) {
		return nil, fmt.Errorf("%w %q", ErrNoSuchUser, username)
	}
	return s.sessions.createFrom(username, from), nil
}

// Sessions lists the account's live sessions, most recently used first.
func (s *Service) Sessions(username string) []SessionInfo {
	if !s.accountExists(username) {
		return nil
	}
	return s.sessions.list(username)
}

// EndSession ends the account's session named by ref.
func (s *Service) EndSession(username, ref string) error {
	if !s.accountExists(username) || !s.sessions.deleteRef(username, ref) {
		return ErrNoSuchSession
	}
	return nil
}

// EndOtherSessions ends every session of the account but the one with keepID.
func (s *Service) EndOtherSessions(username, keepID string) {
	if !s.accountExists(username) {
		return
	}
	s.sessions.deleteOthers(username, keepID)
}

// RefOf returns the ref a listing gives the session with this ID.
func (s *Service) RefOf(id string) string {
	if id == "" {
		return ""
	}
	return sessionRef(sessionKey(id))
}
