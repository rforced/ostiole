package server

import (
	"errors"
	"net/http"

	"ostiole/internal/audit"
	"ostiole/internal/store"
)

func (a *api) registerAudit(mux *router) {
	mux.HandleFunc("GET /api/v1/audit", a.admin(a.auditLog))
	mux.HandleFunc("GET /api/v1/audit/stream", a.admin(a.auditStream))
	mux.HandleFunc("DELETE /api/v1/audit", a.admin(a.auditClear))
	mux.HandleFunc("GET /api/v1/config/applied", a.admin(a.needEngine(a.configApplied)))
}

var errNoAudit = errors.New("the audit log is not kept by this daemon")

// auditLog serves a page of who did what, newest first, searched.
func (a *api) auditLog(w http.ResponseWriter, r *http.Request) error {
	if a.audit == nil {
		return &unavailable{errNoAudit}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	page, held, oldest, err := a.audit.Query(r.Context(), q, before, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, newLogPage(page, page.Entries, held, oldest))
	return nil
}

func (a *api) auditStream(w http.ResponseWriter, r *http.Request) error {
	if a.audit == nil {
		return &unavailable{errNoAudit}
	}
	ch, cancel := a.audit.Subscribe(64)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e audit.Event) any { return e }, nil)
}

func (a *api) auditClear(w http.ResponseWriter, r *http.Request) error {
	if a.audit == nil {
		return &unavailable{errNoAudit}
	}
	return a.clearOne(audit.Name)(w, r)
}

// configApplied serves who applied the configuration in force, or null
// when nobody was recorded.
func (a *api) configApplied(w http.ResponseWriter, _ *http.Request) error {
	rec, err := a.engine.Store().Applied()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rec)
	return nil
}

// withoutAuthor hands back rev without who applied it unless the caller is
// an admin: who and from where are theirs only.
func (a *api) withoutAuthor(r *http.Request, rev *store.Revision) *store.Revision {
	if rev == nil || rev.Applied == nil || a.isAdmin(r) {
		return rev
	}
	out := *rev
	out.Applied = nil
	return &out
}
