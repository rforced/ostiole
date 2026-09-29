package server

import (
	"context"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/shaping"
)

// Shaper reports the live state of the traffic queues. It is an interface
// so the daemon can hand over the backend it already applies through, and
// so a build without one simply has no live figures to show.
type Shaper interface {
	// Status samples the queues for the configuration the kernel is
	// running.
	Status(ctx context.Context, cfg *model.Config) (*shaping.Report, error)
	// Missing reports whether the command that installs the queues is
	// absent, and the package that carries it here.
	Missing() (pkg string, missing bool)
}

// shapingStatus is the report with the tier names alongside, so the page
// labels its rows from the router rather than from a list of its own that
// could drift out of step.
type shapingStatus struct {
	*shaping.Report
	Tiers []model.Tier `json:"tiers"`
}

// shapingStatus reads the queues the kernel is running. Like the policy
// endpoint it goes over the effective configuration, so what is shown
// during a confirmation window is what is actually installed rather than
// the draft in the browser.
func (a *api) shapingStatus(w http.ResponseWriter, r *http.Request) error {
	out := shapingStatus{
		Report: &shaping.Report{SampledAt: time.Now().UTC(), Interfaces: []shaping.InterfaceStatus{}},
		Tiers:  model.Tiers,
	}
	if a.shaping == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	rep, err := a.shaping.Status(r.Context(), a.engine.Effective())
	if err != nil {
		return err
	}
	if rep.Interfaces == nil {
		rep.Interfaces = []shaping.InterfaceStatus{}
	}
	out.Report = rep
	writeJSON(w, http.StatusOK, out)
	return nil
}
