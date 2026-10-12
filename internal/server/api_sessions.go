package server

import (
	"errors"
	"net/http"
	"time"

	"ostiole/internal/auth"
)

func (a *api) registerSessions(mux *router) {
	mux.HandleFunc("GET /api/v1/auth/sessions", a.protect(a.listSessions))
	mux.HandleFunc("DELETE /api/v1/auth/sessions/{id}", a.protect(a.endSession))
	mux.HandleFunc("POST /api/v1/auth/sessions/end-others", a.protect(a.endOtherSessions))
}

// sessionRow is one of the account's sessions as its own page lists it. ID
// names the session without being one.
type sessionRow struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
	Address  string    `json:"address"`
	Agent    string    `json:"agent"`
	Current  bool      `json:"current"`
}

// listSessions lists the signed-in account's sessions, most recently used
// first.
func (a *api) listSessions(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	current := a.auth.RefOf(sess.ID)
	list := a.auth.Sessions(sess.Username)
	out := make([]sessionRow, len(list))
	for i, s := range list {
		out[i] = sessionRow{
			ID: s.Ref, Created: s.Created, LastSeen: s.LastSeen, Expires: s.Expires,
			Address: s.Address, Agent: s.Agent, Current: s.Ref == current,
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// endSession signs out one of the account's sessions, this one included.
func (a *api) endSession(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	ref := r.PathValue("id")
	if err := a.auth.EndSession(sess.Username, ref); err != nil {
		if errors.Is(err, auth.ErrNoSuchSession) {
			return &notFound{err}
		}
		return err
	}
	if ref == a.auth.RefOf(sess.ID) {
		clearSessionCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// endOtherSessions signs out every session of the account but this one.
func (a *api) endOtherSessions(w http.ResponseWriter, r *http.Request) error {
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	a.auth.EndOtherSessions(sess.Username, sess.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
