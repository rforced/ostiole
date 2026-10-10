package server

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/logging"
)

// SessionCookie is the session cookie name.
const SessionCookie = "ostiole_session"

func (a *api) registerAuth(mux *router) {
	mux.HandleFunc("GET /api/v1/setup", a.public(a.setupStatus))
	mux.HandleFunc("POST /api/v1/setup", a.public(a.setup))
	mux.HandleFunc("GET /api/v1/auth/login", a.public(a.loginPage))
	mux.HandleFunc("POST /api/v1/auth/login", a.public(a.login))
	mux.HandleFunc("POST /api/v1/auth/logout", a.protect(a.logout))
	mux.HandleFunc("GET /api/v1/auth/me", a.protect(a.me))
	mux.HandleFunc("POST /api/v1/auth/password", a.protect(a.changePassword))
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

// sessionResponse describes the caller's session and the role it acts
// with, which the UI reads to grey out what the account may not change.
type sessionResponse struct {
	Username string    `json:"username"`
	Role     auth.Role `json:"role"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	Expires  time.Time `json:"expires"`
}

func (a *api) describe(sess *auth.Session) sessionResponse {
	return sessionResponse{
		Username: sess.Username, Role: a.auth.Role(sess.Username),
		Created: sess.Created, LastSeen: sess.LastSeen, Expires: sess.Expires,
	}
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
	sess, err := a.auth.SignIn(c.Username)
	if err != nil {
		return err
	}
	a.recordAs(r, sess, audit.Setup)
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusCreated, a.describe(sess))
	return nil
}

// loginPageResponse is what the sign-in page shows before anyone signs in.
type loginPageResponse struct {
	Hostname string `json:"hostname,omitempty"`
}

// loginPage names the router on the sign-in page by the saved
// configuration's hostname, the one the sidebar shows.
func (a *api) loginPage(w http.ResponseWriter, _ *http.Request) error {
	var res loginPageResponse
	// A configuration that cannot be read is reported after sign-in, not
	// to whoever opens this page.
	if a.engine != nil {
		if cfg, err := a.engine.Store().Load(); err == nil {
			res.Hostname = cfg.System.Hostname
		}
	}
	writeJSON(w, http.StatusOK, res)
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
		// A name that is no account is left out of the log: it is as often
		// a password typed into the wrong box.
		user := "(no such account)"
		if _, ok := a.auth.Account(c.Username); ok {
			user = c.Username
		}
		// Sign-ins are kept at every level, as sshd's are.
		slog.WarnContext(logging.Always(r.Context()), "sign-in refused", "user", user, "address", remoteIP(r), "reason", err)
		return err
	}
	slog.InfoContext(logging.Always(r.Context()), "signed in", "user", sess.Username, "address", remoteIP(r))
	if old, err := r.Cookie(SessionCookie); err == nil {
		a.auth.Logout(old.Value)
	}
	a.recordAs(r, sess, audit.SignIn)
	setSessionCookie(w, r, sess)
	writeJSON(w, http.StatusOK, a.describe(sess))
	return nil
}

// recordAs records what the account sess belongs to just did, for a
// request that arrived with no session or one the action ended.
func (a *api) recordAs(r *http.Request, sess *auth.Session, action string) {
	p := Principal{Name: sess.Username, Role: a.auth.Role(sess.Username)}
	a.audit.Add(audit.Event{Action: action, By: p.actor(remoteIP(r))})
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
	// The session can end between protect's look and this one.
	sess, ok := a.session(r)
	if !ok {
		return errUnauthorized
	}
	writeJSON(w, http.StatusOK, a.describe(sess))
	return nil
}

type passwordChange struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

// changePassword verifies the current password (rate limited like a
// login), sets the new one, and re-issues the caller's session since the
// change ends every session for the account.
func (a *api) changePassword(w http.ResponseWriter, r *http.Request) error {
	sess, ok := a.session(r)
	if !ok {
		return errUnauthorized
	}
	var req passwordChange
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	check, err := a.auth.Login(sess.Username, req.Current, remoteIP(r))
	if err != nil {
		return err
	}
	a.auth.Logout(check.ID)
	if err := a.auth.SetPassword(sess.Username, req.New); err != nil {
		return err
	}
	fresh, err := a.auth.SignIn(sess.Username)
	if err != nil {
		return err
	}
	a.recordAs(r, fresh, audit.Password)
	setSessionCookie(w, r, fresh)
	writeJSON(w, http.StatusOK, a.describe(fresh))
	return nil
}
