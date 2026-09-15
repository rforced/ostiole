package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
)

const maxBodyBytes = 1 << 20

type api struct {
	engine *engine.Engine
}

func (a *api) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/status", a.guard(a.status))
	mux.HandleFunc("GET /api/v1/config", a.guard(a.getConfig))
	mux.HandleFunc("GET /api/v1/config/revisions", a.guard(a.revisions))
	mux.HandleFunc("GET /api/v1/config/revisions/{id}", a.guard(a.revision))
	mux.HandleFunc("GET /api/v1/ruleset", a.guard(a.ruleset))
	mux.HandleFunc("GET /api/v1/counters", a.guard(a.counters))
	mux.HandleFunc("POST /api/v1/check", a.guard(a.check))
	mux.HandleFunc("POST /api/v1/apply", a.guard(a.apply))
	mux.HandleFunc("POST /api/v1/apply/confirm", a.guard(a.confirm))
	mux.HandleFunc("POST /api/v1/apply/revert", a.guard(a.revert))
}

// guard turns handler errors into JSON responses and refuses requests
// when the engine is not wired (tests of the static handler).
func (a *api) guard(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.engine == nil {
			writeError(w, http.StatusServiceUnavailable, errors.New("engine not available"))
			return
		}
		if err := h(w, r); err != nil {
			writeError(w, statusFor(err), err)
		}
	}
}

type errorResponse struct {
	Error  string        `json:"error"`
	Issues []model.Issue `json:"issues,omitempty"`
}

func writeError(w http.ResponseWriter, status int, err error) {
	resp := errorResponse{Error: err.Error()}
	var ve *model.ValidationError
	if errors.As(err, &ve) {
		resp.Error = "invalid configuration"
		resp.Issues = ve.Issues
	}
	writeJSON(w, status, resp)
}

func statusFor(err error) int {
	var ve *model.ValidationError
	var ne *nft.Error
	var be *badRequest
	switch {
	case errors.As(err, &be):
		return http.StatusBadRequest
	case errors.As(err, &ve), errors.As(err, &ne):
		return http.StatusUnprocessableEntity
	case errors.Is(err, engine.ErrPending), errors.Is(err, engine.ErrNothingPending):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotFound), errors.Is(err, nft.ErrNoTable):
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

type badRequest struct{ err error }

func (b *badRequest) Error() string { return b.err.Error() }
func (b *badRequest) Unwrap() error { return b.err }

func decodeJSON(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &badRequest{fmt.Errorf("invalid JSON body: %w", err)}
	}
	if dec.More() {
		return &badRequest{errors.New("invalid JSON body: trailing data")}
	}
	return nil
}

func (a *api) status(w http.ResponseWriter, r *http.Request) error {
	st, err := a.engine.Status(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

func (a *api) getConfig(w http.ResponseWriter, _ *http.Request) error {
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
}

func (a *api) revisions(w http.ResponseWriter, _ *http.Request) error {
	revs, err := a.engine.Store().Revisions()
	if err != nil {
		return err
	}
	if revs == nil {
		revs = []store.Revision{}
	}
	writeJSON(w, http.StatusOK, revs)
	return nil
}

func (a *api) revision(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.engine.Store().LoadRevision(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
}

func (a *api) ruleset(w http.ResponseWriter, _ *http.Request) error {
	rs, err := a.engine.Store().LoadRuleset()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err = io.WriteString(w, rs)
	return err
}

func (a *api) counters(w http.ResponseWriter, r *http.Request) error {
	c, err := a.engine.Counters(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

type configRequest struct {
	Config *model.Config `json:"config"`
}

type applyRequest struct {
	Config *model.Config `json:"config"`
	// ConfirmTimeoutSeconds of zero commits immediately.
	ConfirmTimeoutSeconds int `json:"confirmTimeoutSeconds"`
}

func (a *api) check(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	ruleset, err := a.engine.Check(r.Context(), req.Config)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"ruleset": ruleset})
	return nil
}

func (a *api) apply(w http.ResponseWriter, r *http.Request) error {
	var req applyRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	if req.ConfirmTimeoutSeconds < 0 || req.ConfirmTimeoutSeconds > 3600 {
		return &badRequest{errors.New("confirmTimeoutSeconds must be 0-3600")}
	}
	res, err := a.engine.Apply(r.Context(), req.Config, engine.ApplyOptions{
		ConfirmTimeout: time.Duration(req.ConfirmTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (a *api) confirm(w http.ResponseWriter, r *http.Request) error {
	archived, err := a.engine.Confirm(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": archived})
	return nil
}

func (a *api) revert(w http.ResponseWriter, r *http.Request) error {
	if err := a.engine.Revert(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
