package server

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/auth"
)

// SessionCookie is the session cookie name.
const SessionCookie = "ostiole_session"

func (a *api) registerAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/setup", a.public(a.setupStatus))
	mux.HandleFunc("POST /api/v1/setup", a.public(a.setup))
	mux.HandleFunc("POST /api/v1/auth/login", a.public(a.login))
	mux.HandleFunc("POST /api/v1/auth/logout", a.protect(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.protect(a.me))
}

// session resolves the request's session cookie.
func (a *api) session(r *http.Request) (*auth.Session, bool) {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil, false
	}
	return a.auth.Session(c.Value)
}

// Secure follows the connection: the UI is served over TLS in production,
// while plain HTTP on localhost is allowed for development.
func setSessionCookie(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is set whenever the request arrived over TLS
		Name:     SessionCookie,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(time.Until(sess.Expires).Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // see setSessionCookie
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	Username string    `json:"username"`
	Expires  time.Time `json:"expires"`
}

func (a *api) setupStatus(w http.ResponseWriter, _ *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"needed": a.auth.NeedsSetup()})
	return nil
}

// setup creates the first account and logs it in.
func (a *api) setup(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		return err
	}
	if err := a.auth.Setup(c.Username, c.Password); err != nil {
		return err
	}
	sess, err := a.auth.Login(c.Username, c.Password, remoteIP(r))
	if err != nil {
		return err
	}
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusCreated, sessionResponse{Username: sess.Username, Expires: sess.Expires})
	return nil
}

func (a *api) login(w http.ResponseWriter, r *http.Request) error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	var c credentials
	if err := decodeJSON(r, &c); err != nil {
		return err
	}
	sess, err := a.auth.Login(c.Username, c.Password, remoteIP(r))
	if err != nil {
		return err
	}
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusOK, sessionResponse{Username: sess.Username, Expires: sess.Expires})
	return nil
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(SessionCookie); err == nil {
		a.auth.Logout(c.Value)
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *api) me(w http.ResponseWriter, r *http.Request) error {
	sess, _ := a.session(r)
	writeJSON(w, http.StatusOK, sess)
	return nil
}
