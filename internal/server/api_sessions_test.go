package server

import (
	"net/http"
	"testing"
	"time"

	"ostiole/internal/auth"
)

const browserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"

// sessionListRow is a row of GET /api/v1/auth/sessions.
type sessionListRow struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
	Address  string    `json:"address"`
	Agent    string    `json:"agent"`
	Current  bool      `json:"current"`
}

func (s *factorSite) sessions(t *testing.T) []sessionListRow {
	t.Helper()
	return jsonOf[[]sessionListRow](t, s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/auth/sessions", nil))
}

// another signs admin in afresh, beside the session the site holds, and
// gives the new session's cookie.
func (s *factorSite) another(t *testing.T) string {
	t.Helper()
	kept := s.session
	s.session = ""
	if resp, raw := s.signIn(t, "", "admin"); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in again: %d %s", resp.StatusCode, raw)
	}
	made := s.session
	s.session = kept
	return made
}

func TestSessionsListThisOneWithWhereItCameFrom(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.agent = browserAgent
	s.session = s.another(t)
	other := s.another(t)

	list := s.sessions(t)
	if len(list) != 3 {
		t.Fatalf("sessions = %+v, want three", list)
	}
	current := 0
	for _, row := range list {
		if len(row.ID) != 16 || row.ID == s.session || row.ID == other {
			t.Errorf("session id %q is not a ref", row.ID)
		}
		if row.Address != "127.0.0.1" || row.Created.IsZero() || row.Expires.Before(row.LastSeen) {
			t.Errorf("session = %+v", row)
		}
		if row.Current {
			current++
			if row.ID != s.as.RefOf(s.session) || row.Agent != browserAgent {
				t.Errorf("the current session = %+v", row)
			}
		}
	}
	if current != 1 {
		t.Errorf("%d sessions marked current, want one", current)
	}
	if !list[0].Current {
		t.Errorf("the session just used is not first: %+v", list)
	}
	for i := 1; i < len(list); i++ {
		if list[i].LastSeen.After(list[i-1].LastSeen) {
			t.Errorf("sessions out of order: %+v", list)
		}
	}
}

func TestEndingAnotherSession(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	other := s.another(t)

	resp, raw := s.call(t, "", http.MethodDelete, "/api/v1/auth/sessions/"+s.as.RefOf(other), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end another: %d %s", resp.StatusCode, raw)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("ending another session touched this one's cookie: %v", resp.Cookies())
	}
	if _, ok := s.as.Session(other); ok {
		t.Error("the other session is still open")
	}
	if list := s.sessions(t); len(list) != 1 || !list[0].Current {
		t.Errorf("sessions = %+v, want this one alone", list)
	}
	s.refused(t, http.StatusNotFound, auth.ErrNoSuchSession.Error(), "", http.MethodDelete, "/api/v1/auth/sessions/"+s.as.RefOf(other), nil)
	s.refused(t, http.StatusNotFound, auth.ErrNoSuchSession.Error(), "", http.MethodDelete, "/api/v1/auth/sessions/nothing", nil)

	// Another account's session is not one of yours.
	if err := s.as.CreateUser("watcher", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	theirs, err := s.as.SignIn("watcher")
	if err != nil {
		t.Fatal(err)
	}
	s.refused(t, http.StatusNotFound, auth.ErrNoSuchSession.Error(), "", http.MethodDelete, "/api/v1/auth/sessions/"+s.as.RefOf(theirs.ID), nil)
	if _, ok := s.as.Session(theirs.ID); !ok {
		t.Error("another account's session was ended")
	}
}

func TestEndingThisSessionClearsTheCookie(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	this := s.session
	resp, raw := s.call(t, "", http.MethodDelete, "/api/v1/auth/sessions/"+s.as.RefOf(this), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("end this one: %d %s", resp.StatusCode, raw)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		cleared = cleared || c.Name == SessionCookie && c.Value == "" && c.MaxAge < 0
	}
	if !cleared {
		t.Errorf("the cookie was left: %v", resp.Cookies())
	}
	s.session = this
	s.refused(t, http.StatusUnauthorized, "authentication required", "", http.MethodGet, "/api/v1/auth/me", nil)
}

func TestEndingTheOtherSessionsKeepsThisOne(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	first, second := s.another(t), s.another(t)
	s.expect(t, http.StatusNoContent, "", http.MethodPost, "/api/v1/auth/sessions/end-others", nil)
	for _, id := range []string{first, second} {
		if _, ok := s.as.Session(id); ok {
			t.Error("another session is still open")
		}
	}
	if list := s.sessions(t); len(list) != 1 || !list[0].Current {
		t.Errorf("sessions = %+v, want this one alone", list)
	}
	s.expect(t, http.StatusOK, "", http.MethodGet, "/api/v1/auth/me", nil)
}

func TestSessionRoutesNeedASession(t *testing.T) {
	t.Parallel()
	s := newFactorSite(t)
	s.session = ""
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/auth/sessions"},
		{http.MethodDelete, "/api/v1/auth/sessions/0123456789abcdef"},
		{http.MethodPost, "/api/v1/auth/sessions/end-others"},
	} {
		s.refused(t, http.StatusUnauthorized, "authentication required", "", route.method, route.path, nil)
	}
}
