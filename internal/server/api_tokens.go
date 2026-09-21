package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/auth"
)

func (a *api) registerTokens(mux *router) {
	mux.HandleFunc("GET /api/v1/tokens", a.admin(a.listTokens))
	mux.HandleFunc("POST /api/v1/tokens", a.admin(a.createToken))
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

func (a *api) createToken(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	var req struct {
		Name string `json:"name"`
		Role string `json:"role"`
		// ExpiresInDays ends the token automatically; 0 means never.
		ExpiresInDays int `json:"expiresInDays,omitempty"`
		// Certificates limits the token to fetching those, and nothing
		// else on this API.
		Certificates []string `json:"certificates,omitempty"`
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
	if req.ExpiresInDays < 0 || req.ExpiresInDays > 3650 {
		return &badRequest{errors.New("expiresInDays must be 0-3650")}
	}
	var ttl time.Duration
	if req.ExpiresInDays > 0 {
		ttl = time.Duration(req.ExpiresInDays) * 24 * time.Hour
	}
	by := ""
	if p, ok := a.authenticate(r); ok {
		by = p.Name
	}
	tok, secret, err := a.tokens.Create(req.Name, role, ttl, by, req.Certificates)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusCreated, createdToken{Token: tok, Secret: secret})
	return nil
}

func (a *api) deleteToken(w http.ResponseWriter, r *http.Request) error {
	if a.tokens == nil {
		return &unavailable{errors.New("API tokens are not available")}
	}
	if err := a.tokens.Delete(r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
