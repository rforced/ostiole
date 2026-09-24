package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rforced/ostiole/internal/auth"
)

// Principal is whoever made a request: an operator with a browser
// session, or something holding an API token.
type Principal struct {
	// Name is the username, or the token's name.
	Name string
	Role auth.Role
	// Token is true when the credential was an API token, which is how
	// the CSRF check knows it has nothing to protect: a token is never
	// sent automatically by a browser.
	Token bool
	// Certificates restricts the caller to fetching those certificates.
	// A principal with any is refused everywhere else, whatever its role.
	Certificates []string
}

// errForbidden is returned when the caller is authenticated but not
// allowed to do this.
var errForbidden = errors.New("this account is not allowed to do that")

// authenticate resolves the caller from the Authorization header, or
// from the session cookie when there is no header. A bearer that does not
// authenticate is refused outright, cookie or no cookie: the CSRF guard
// stands down for a bearer request, so the bearer has to be the credential.
func (a *api) authenticate(r *http.Request) (Principal, bool) {
	return a.principal(r, true)
}

// principal resolves the caller the way authenticate says. touch counts
// the request as use of the session, which a request somebody made is and
// a stream checking on itself is not.
func (a *api) principal(r *http.Request, touch bool) (Principal, bool) {
	if raw := bearer(r); raw != "" {
		if a.tokens == nil {
			return Principal{}, false
		}
		tok, err := a.tokens.Authenticate(raw)
		if err != nil {
			return Principal{}, false
		}
		return Principal{Name: tok.Name, Role: tok.Role, Token: true, Certificates: tok.Certificates}, true
	}
	if a.auth == nil {
		return Principal{}, false
	}
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return Principal{}, false
	}
	lookup := a.auth.Session
	if !touch {
		lookup = a.auth.Peek
	}
	sess, ok := lookup(c.Value)
	if !ok {
		return Principal{}, false
	}
	return Principal{Name: sess.Username, Role: a.auth.Role(sess.Username)}, true
}

// routeRole is the context key for the role a route was let in with.
type routeRole struct{}

// stillAllowed checks, partway through a response that stays open, that
// the caller could make the request now. A log stream runs for hours, and
// meanwhile the session can end, the account go, the token be revoked.
func (a *api) stillAllowed(r *http.Request) bool {
	role, _ := r.Context().Value(routeRole{}).(auth.Role)
	p, ok := a.principal(r, false)
	return ok && len(p.Certificates) == 0 && p.Role.Allows(role)
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// requires wraps a handler so only a caller with at least the given role
// reaches it. It is the one place authorisation is decided.
func (a *api) requires(role auth.Role, h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.public(func(w http.ResponseWriter, r *http.Request) error {
		if a.auth == nil {
			return &unavailable{errors.New("authentication not available")}
		}
		p, ok := a.authenticate(r)
		if !ok {
			return errUnauthorized
		}
		// A token restricted to certificates reaches nothing that goes
		// through here, so the restriction does not depend on its role.
		if len(p.Certificates) > 0 || !p.Role.Allows(role) {
			return errForbidden
		}
		return h(w, r.WithContext(context.WithValue(r.Context(), routeRole{}, role)))
	})
}

// read, write, and admin name the three levels the API uses, so a route's
// line says who may call it.
func (a *api) read(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.requires(auth.RoleViewer, a.needEngine(h))
}

func (a *api) write(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.requires(auth.RoleOperator, a.needEngine(h))
}

func (a *api) admin(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.requires(auth.RoleAdmin, h)
}

// readNoEngine is for endpoints that report on the router rather than the
// configuration, and work before anything is configured.
func (a *api) readNoEngine(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.requires(auth.RoleViewer, h)
}

func (a *api) needEngine(h func(w http.ResponseWriter, r *http.Request) error) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		if a.engine == nil {
			return &unavailable{errors.New("engine not available")}
		}
		return h(w, r)
	}
}
