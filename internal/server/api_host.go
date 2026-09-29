package server

import (
	"net/http"

	"github.com/rforced/ostiole/internal/host"
)

func (a *api) registerHost(mux *router) {
	mux.HandleFunc("GET /api/v1/host", a.readNoEngine(a.hostStatus))
	mux.HandleFunc("POST /api/v1/host/legacy/flush", a.admin(a.hostFlushLegacy))
}

// hostResult is what the flush returns: what it said, and the state of the
// router afterwards, so the page never has to ask twice.
type hostResult struct {
	Output string      `json:"output,omitempty"`
	Status host.Report `json:"status"`
}

func (a *api) hostStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, host.Status(r.Context(), a.host))
	return nil
}

// hostFlushLegacy clears what an older firewall left in the kernel.
func (a *api) hostFlushLegacy(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Tables []string `json:"tables"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			return err
		}
	}
	out, err := host.FlushLegacy(r.Context(), a.host, body.Tables)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, hostResult{Output: out, Status: host.Status(r.Context(), a.host)})
	return nil
}
