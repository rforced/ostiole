package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/cron"
	"github.com/rforced/ostiole/internal/model"
)

// CronRunner reports and runs the scheduled work.
type CronRunner interface {
	Statuses() []cron.Status
	RunNow(ctx context.Context, id string) error
}

func (a *api) registerCrons(mux *router) {
	mux.HandleFunc("GET /api/v1/crons", a.readNoEngine(a.cronStatus))
	mux.HandleFunc("POST /api/v1/crons/{id}/run", a.write(a.runCron))
}

// cronStatus lists the operator's crons and the work Ostiole does on its
// own account, together.
func (a *api) cronStatus(w http.ResponseWriter, _ *http.Request) error {
	out := []cron.Status{}
	if a.crons != nil {
		out = a.crons.Statuses()
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *api) runCron(w http.ResponseWriter, r *http.Request) error {
	if a.crons == nil {
		return &unavailable{errors.New("nothing is running scheduled crons on this router")}
	}
	id := r.PathValue("id")
	if err := a.mayRun(r, id); err != nil {
		return err
	}
	if err := a.crons.RunNow(r.Context(), id); err != nil {
		// The result is recorded either way, so the page shows what
		// happened rather than only that it failed.
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "error": err.Error()})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

// mayRun keeps the crons that run a command, or that install updates, to
// administrators: an operator may run the rest now rather than wait.
func (a *api) mayRun(r *http.Request, id string) error {
	cfg := a.engine.Effective()
	if cfg == nil {
		return nil
	}
	c, ok := cfg.Cron(id)
	if !ok || (c.Kind != model.CronCommand && c.Kind != model.CronSystemUpdate && c.Kind != model.CronOstioleUpdate) {
		return nil
	}
	if p, ok := a.authenticate(r); ok && p.Role.Allows(auth.RoleAdmin) {
		return nil
	}
	return fmt.Errorf("%w: only an administrator can run %s now", errForbidden, id)
}
