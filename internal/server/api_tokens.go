package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
)

func (a *api) registerTokens(mux *router) {
	mux.HandleFunc("GET /api/v1/auth/tokens", a.protect(a.listOwnTokens))
	mux.HandleFunc("POST /api/v1/auth/tokens", a.protect(a.createToken))
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", a.protect(a.deleteOwnToken))
	mux.HandleFunc("GET /api/v1/tokens", a.admin(a.listTokens))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", a.admin(a.deleteToken))
}

func (a *api) listTokens(w http.ResponseWriter, _ *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	out := a.tokens.List()
	if out == nil {
		out = []auth.Token{}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// createdToken is the one and only time the secret leaves the server.
type createdToken struct {
	auth.Token
	// Secret is the value to put in an Authorization header. It is not
	// stored and cannot be shown again.
	Secret string `json:"secret"`
}

// listOwnTokens answers the signed-in account's tokens.
func (a *api) listOwnTokens(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a.tokens.ListOwned(sess.Username))
	return nil
}

// createToken mints a token for the signed-in account, with no more than
// its role.
func (a *api) createToken(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
		// ExpiresInDays ends the token automatically; 0 means never.
		ExpiresInDays int `json:"expiresInDays,omitempty"`
		// Certificates limits the token to fetching those, and nothing
		// else on this API.
		Certificates []string `json:"certificates,omitempty"`
		// Metrics limits it to /metrics.
		Metrics bool `json:"metrics,omitempty"`
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
	mine := a.auth.Role(sess.Username)
	if !mine.Allows(role) {
		return &badRequest{errors.New("a token cannot have more than your role")}
	}
	if len(req.Certificates) > 0 && !mine.Allows(auth.RoleAdmin) {
		return &badRequest{errors.New("only an admin can make a certificate token")}
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		return &badRequest{errors.New("expiresInDays must be 0-3650")}
	}
	var ttl time.Duration
	if req.ExpiresInDays > 0 {
		ttl = time.Duration(req.ExpiresInDays) * 24 * time.Hour
	}
	tok, secret, err := a.tokens.Create(req.Name, role, ttl, sess.Username, sess.Username, auth.Limits{Certificates: req.Certificates, Metrics: req.Metrics})
	if err != nil {
		return &badRequest{err}
	}
	detail := string(tok.Role)
	switch {
	case len(tok.Certificates) > 0:
		detail = "certificates"
	case tok.Metrics:
		detail = "metrics"
	}
	a.record(r, audit.TokenCreate, tok.Name, detail)
	writeJSON(w, http.StatusCreated, createdToken{Token: tok, Secret: secret})
	return nil
}

// deleteOwnToken deletes one of the signed-in account's tokens.
func (a *api) deleteOwnToken(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	sess, err := a.current(r)
	if err != nil {
		return err
	}
	ref := r.PathValue("id")
	name := ref
	for _, tok := range a.tokens.ListOwned(sess.Username) {
		if tok.ID == ref || strings.EqualFold(tok.Name, ref) {
			name = tok.Name
			break
		}
	}
	if err := a.tokens.DeleteOf(sess.Username, ref); err != nil {
		return err
	}
	a.record(r, audit.TokenDelete, name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *api) deleteToken(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	ref := r.PathValue("id")
	name := ref
	for _, tok := range a.tokens.List() {
		if tok.ID == ref || strings.EqualFold(tok.Name, ref) {
			name = tok.Name
			break
		}
	}
	if err := a.tokens.Delete(ref); err != nil {
		return err
	}
	a.record(r, audit.TokenDelete, name, "")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
