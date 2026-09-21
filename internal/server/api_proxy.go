package server

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/services"
)

// proxyStatus is what the page's strip shows. A router with no sidecar,
// or one that is stopped, answers with the flags false: "not set up" is a
// state of the page, not an error.
type proxyStatus struct {
	SetUp   bool   `json:"setUp"`
	Running bool   `json:"running"`
	Release string `json:"release,omitempty"`
	Ports   struct {
		HTTP  uint16 `json:"http"`
		HTTPS uint16 `json:"https"`
		HTTP3 bool   `json:"http3"`
	} `json:"ports"`
	Upstreams []services.Upstream `json:"upstreams"`
}

func (a *api) registerProxy(mux *router) {
	mux.HandleFunc("GET /api/v1/proxy/status", a.readNoEngine(a.proxyStatus))
	mux.HandleFunc("GET /api/v1/proxy/events", a.readNoEngine(a.proxyEvents))
}

func (a *api) proxyStatus(w http.ResponseWriter, r *http.Request) error {
	out := proxyStatus{Upstreams: []services.Upstream{}}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			p := cfg.Services.Proxy
			out.Ports.HTTP, out.Ports.HTTPS, out.Ports.HTTP3 = p.HTTPPortOr(), p.HTTPSPortOr(), p.HTTP3
		}
	}
	ctx := r.Context()
	if a.proxy == nil || !a.proxy.Installed(ctx) {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	out.SetUp = true
	out.Release = a.proxy.Release(ctx)
	if out.Running = a.proxy.Active(ctx); out.Running {
		if ups, err := a.proxy.Upstreams(ctx); err == nil && ups != nil {
			out.Upstreams = ups
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// maxProxyEvents caps one query. An audit entry is one line among the
// proxy's own logging, so more lines are read than events are wanted —
// but not a fixed 2000, because one entry is tens of kilobytes.
const (
	maxProxyEvents  = 1000
	proxyReadFactor = 3
)

func (a *api) proxyEvents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxProxyEvents {
			return &badRequest{fmt.Errorf("limit must be 1-%d", maxProxyEvents)}
		}
		limit = n
	}
	since := q.Get("since")
	if since == "" {
		since = "-1h"
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	read := a.journal
	if read == nil {
		read = diag.Journal
	}
	entries, err := read(ctx, diag.JournalOptions{
		Unit:  services.ProxyUnit,
		Lines: min(limit*proxyReadFactor, diag.MaxJournalLines),
		Since: since,
	})
	if err != nil {
		return diagError(err)
	}
	events := []services.ProxyEvent{}
	for _, e := range entries {
		ev, ok := services.ParseProxyEvent(e.Message, e.Time)
		if !ok {
			continue
		}
		if events = append(events, ev); len(events) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, events)
	return nil
}
