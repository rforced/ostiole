package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/rforced/ostiole/internal/auth"
)

func (a *api) registerUsers(mux *router) {
	mux.HandleFunc("GET /api/v1/users", a.admin(a.listUsers))
	mux.HandleFunc("POST /api/v1/users", a.admin(a.createUser))
	mux.HandleFunc("DELETE /api/v1/users/{name}", a.admin(a.deleteUser))
	mux.HandleFunc("POST /api/v1/users/{name}/role", a.admin(a.setUserRole))
	mux.HandleFunc("POST /api/v1/users/{name}/password", a.admin(a.setUserPassword))
	mux.HandleFunc("POST /api/v1/users/{name}/username", a.admin(a.renameUser))
}

// errSelfAccount refuses the two account changes that can leave a router
// with nobody able to manage it: an administrator demoting or deleting
// themselves. The last-administrator rules alone are not enough, because
// with two administrators each can strip the other and then themselves.
var errSelfAccount = errors.New("you cannot do that to your own account; ask another administrator")

// self reports whether the caller is signed in as this account. An API
// token is not an account, so it is never "self": what protects the router
// from a token is the last-administrator rule, not this one.
func (a *api) self(r *http.Request, username string) bool {
	p, ok := a.authenticate(r)
	return ok && !p.Token && p.Name == username
}

// users answers with the whole account list, which is what every change
// here returns: the table the UI redraws is never a guess about what the
// server did.
func (a *api) users(w http.ResponseWriter, status int) error {
	writeJSON(w, status, a.auth.Accounts())
	return nil
}

func (a *api) requireAuth() error {
	if a.auth == nil {
		return &unavailable{errors.New("authentication not available")}
	}
	return nil
}

func (a *api) listUsers(w http.ResponseWriter, _ *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	return a.users(w, http.StatusOK)
}

func (a *api) createUser(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	role := auth.Role(req.Role)
	if req.Role == "" {
		role = auth.RoleViewer
	}
	if !role.Valid() {
		return &badRequest{fmt.Errorf("unknown role %q", req.Role)}
	}
	if err := a.auth.CreateUser(req.Username, req.Password, role); err != nil {
		return err
	}
	return a.users(w, http.StatusCreated)
}

func (a *api) deleteUser(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	name := r.PathValue("name")
	if a.self(r, name) {
		return fmt.Errorf("%w: %w", errForbidden, errSelfAccount)
	}
	if err := a.auth.DeleteUser(name); err != nil {
		return err
	}
	return a.users(w, http.StatusOK)
}

func (a *api) setUserRole(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	role := auth.Role(req.Role)
	if !role.Valid() {
		return &badRequest{fmt.Errorf("unknown role %q", req.Role)}
	}
	name := r.PathValue("name")
	if a.self(r, name) {
		return fmt.Errorf("%w: %w", errForbidden, errSelfAccount)
	}
	if err := a.auth.SetRole(name, role); err != nil {
		return err
	}
	return a.users(w, http.StatusOK)
}

// setUserPassword is an administrator setting somebody else's password,
// which ends every session that account had. Your own password goes
// through POST /api/v1/auth/password instead, because that one checks the
// current password and hands you a fresh session rather than signing you
// out mid-change.
func (a *api) setUserPassword(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	name := r.PathValue("name")
	if a.self(r, name) {
		return fmt.Errorf("%w: change your own password under Change password, which keeps you signed in", errForbidden)
	}
	if _, ok := a.auth.Account(name); !ok {
		return fmt.Errorf("%w %q", auth.ErrNoSuchUser, name)
	}
	if err := a.auth.SetPassword(name, req.Password); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// renameUser is allowed on your own account: a new name cannot cost you
// the administrator role, and the session follows the name.
func (a *api) renameUser(w http.ResponseWriter, r *http.Request) error {
	if err := a.requireAuth(); err != nil {
		return err
	}
	var req struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if err := a.auth.Rename(r.PathValue("name"), req.Username); err != nil {
		return err
	}
	return a.users(w, http.StatusOK)
}
