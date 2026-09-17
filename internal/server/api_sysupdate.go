package server

import (
	"errors"
	"net/http"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/sysupdate"
)

func (a *api) registerSysUpdate(mux *router) {
	mux.HandleFunc("GET /api/v1/system/updates", a.readNoEngine(a.systemUpdates))
	mux.HandleFunc("POST /api/v1/system/updates/check", a.write(a.systemUpdateCheck))
	mux.HandleFunc("POST /api/v1/system/updates/apply", a.admin(a.systemUpdateApply))
	mux.HandleFunc("POST /api/v1/system/reboot", a.admin(a.systemReboot))
}

// updates is the update settings in force, which are the defaults on a
// router that has never been told otherwise.
func (a *api) updates() model.Updates {
	if a.engine == nil {
		return model.Updates{}
	}
	cfg := a.engine.Effective()
	if cfg == nil {
		return model.Updates{}
	}
	return cfg.Updates
}

// systemUpdateStatus is the status with the configured mode and schedule
// filled in, which is what the page needs to draw itself.
func (a *api) systemUpdateStatus() sysupdate.Status {
	updates := a.updates()
	mode := updates.SystemMode()
	st := a.packages.Status(mode == model.UpdateSecurity)
	st.Mode = string(mode)
	st.Schedule = updates.SystemSchedule()
	return st
}

func (a *api) systemUpdates(w http.ResponseWriter, _ *http.Request) error {
	if a.packages == nil {
		return &unavailable{errors.New("nothing on this router drives a package manager")}
	}
	writeJSON(w, http.StatusOK, a.systemUpdateStatus())
	return nil
}

// systemUpdateCheck asks the package manager what is waiting. It blocks,
// because refreshing metadata is what makes the answer worth having.
func (a *api) systemUpdateCheck(w http.ResponseWriter, r *http.Request) error {
	if a.packages == nil {
		return &unavailable{errors.New("nothing on this router drives a package manager")}
	}
	if _, err := a.packages.Check(r.Context()); err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, a.systemUpdateStatus())
	return nil
}

// systemUpdateApply starts an update in the background. The body may ask
// for security fixes alone; without one the configured mode decides, and
// a router on manual gets everything, because pressing the button is asking
// for it.
func (a *api) systemUpdateApply(w http.ResponseWriter, r *http.Request) error {
	if a.packages == nil {
		return &unavailable{errors.New("nothing on this router drives a package manager")}
	}
	var body struct {
		Security *bool `json:"security"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
	}
	updates := a.updates()
	security := updates.SystemMode() == model.UpdateSecurity
	if body.Security != nil {
		security = *body.Security
	}
	if err := a.packages.Start(security, updates.System.Exclude); err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, a.systemUpdateStatus())
	return nil
}

// systemReboot restarts the router, which is the only way to finish a
// kernel update.
func (a *api) systemReboot(w http.ResponseWriter, r *http.Request) error {
	if a.packages == nil {
		return &unavailable{errors.New("this router cannot reboot itself from here")}
	}
	if err := a.packages.Reboot(r.Context()); err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rebooting": true})
	return nil
}
