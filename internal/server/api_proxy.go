package server

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/requestlog"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/wafevent"
	"github.com/rforced/ostiole/internal/waflog"
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
	mux.HandleFunc("GET /api/v1/proxy/events/stream", a.readNoEngine(a.proxyEventsStream))
	mux.HandleFunc("DELETE /api/v1/proxy/events", a.admin(a.clearOne(waflog.FileName)))
	mux.HandleFunc("GET /api/v1/proxy/requests", a.readNoEngine(a.proxyRequests))
	mux.HandleFunc("GET /api/v1/proxy/requests/stream", a.readNoEngine(a.proxyRequestsStream))
	mux.HandleFunc("DELETE /api/v1/proxy/requests", a.admin(a.clearOne(requestlog.FileName)))
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

// proxyEvents serves a page of what the WAF matched, newest first,
// searched and narrowed to one verdict when asked, from the log the daemon
// keeps of the proxy's journal.
func (a *api) proxyEvents(w http.ResponseWriter, r *http.Request) error {
	if a.waflog == nil {
		return &unavailable{errors.New("WAF events are not kept by this daemon")}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	verdict := r.URL.Query().Get("verdict")
	switch verdict {
	case "", wafevent.VerdictBlocked, wafevent.VerdictWouldBlock, wafevent.VerdictMatched:
	default:
		return &badRequest{fmt.Errorf("verdict %q must be %s, %s or %s", verdict,
			wafevent.VerdictBlocked, wafevent.VerdictWouldBlock, wafevent.VerdictMatched)}
	}
	started := time.Now()
	keep := func(e *waflog.Entry) bool { return verdict == "" || e.Verdict == verdict }
	page, err := a.waflog.Query(r.Context(), q, before, limit, keep)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, a.waflog.Files(), waflog.FileVersion, page, before, limit, started,
		waflog.ParseLine, func(e *waflog.Entry) uint64 { return e.Seq }, waflog.Matcher(q, keep))
	if err != nil {
		return err
	}
	held, oldest := a.waflog.Held()
	writeJSON(w, http.StatusOK, newLogPage(page, append(page.Entries, older...), held, oldest))
	return nil
}

// requestsPage is a page of the proxy's requests, and whether the level the
// router runs at keeps them at all.
type requestsPage struct {
	Kept bool `json:"kept"`
	logPage[requestlog.Request]
}

// proxyRequests serves a page of what the proxy answered, newest first and
// searched, from memory and on into the files.
func (a *api) proxyRequests(w http.ResponseWriter, r *http.Request) error {
	if a.requests == nil {
		return &unavailable{errors.New("the proxy's requests are not kept by this daemon")}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	started := time.Now()
	match := requestlog.Matcher(q)
	page, err := a.requests.Query(r.Context(), before, limit, match)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, requestlog.Files(a.requests), requestlog.FileVersion, page, before, limit, started,
		requestlog.ParseLine, logring.Seq[requestlog.Request, *requestlog.Request], match)
	if err != nil {
		return err
	}
	held, oldest := a.requests.Held()
	out := requestsPage{logPage: newLogPage(page, append(page.Entries, older...), held, oldest)}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			out.Kept = requestlog.Kept(cfg)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// proxyRequestsStream sends requests as the proxy answers them.
func (a *api) proxyRequestsStream(w http.ResponseWriter, r *http.Request) error {
	if a.requests == nil {
		return &unavailable{errors.New("the proxy's requests are not kept by this daemon")}
	}
	ch, cancel := a.requests.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e requestlog.Request) any { return e }, nil)
}

// proxyEventsStream sends events as the proxy writes them.
func (a *api) proxyEventsStream(w http.ResponseWriter, r *http.Request) error {
	if a.waflog == nil {
		return &unavailable{errors.New("WAF events are not kept by this daemon")}
	}
	ch, cancel := a.waflog.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e waflog.Entry) any { return e }, nil)
}
