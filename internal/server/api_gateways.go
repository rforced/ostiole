package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"ostiole/internal/gateway"
	"ostiole/internal/model"
)

func (a *api) registerGateways(mux *router) {
	mux.HandleFunc("GET /api/v1/gateways/{name}/history", a.readNoEngine(a.gatewayHistoryRead))
	mux.HandleFunc("GET /api/v1/gateways/stream", a.readNoEngine(a.gatewayStream))
	mux.HandleFunc("GET /api/v1/gateways/strips", a.readNoEngine(a.gatewayStrips))
	mux.HandleFunc("GET /api/v1/gateways/events", a.readNoEngine(a.gatewayEvents))
	mux.HandleFunc("GET /api/v1/gateways/events/stream", a.readNoEngine(a.gatewayEventStream))
	mux.HandleFunc("DELETE /api/v1/gateways/history", a.admin(a.clearOne(gateway.HistoryFileName)))
	mux.HandleFunc("DELETE /api/v1/gateways/events", a.admin(a.clearOne(gateway.EventsFileName)))
}

var errNoGatewayHistory = errors.New("the gateways' history is not kept by this daemon (not running as root?)")

// windowSpans are how far back each window reaches.
var windowSpans = map[string]time.Duration{
	gateway.Window5m:  5 * time.Minute,
	gateway.Window24h: 24 * time.Hour,
	gateway.Window31d: 31 * 24 * time.Hour,
}

// gatewayEventsShown bounds the events a history read carries.
const gatewayEventsShown = 200

type gatewayHistory struct {
	gateway.Report
	// Monitor is the address the gateway is probed at, empty for its next
	// hop.
	Monitor string          `json:"monitor,omitempty"`
	Events  []gateway.Event `json:"events"`
}

// gatewayHistoryRead serves a gateway over a window, with its events in
// the window, newest first.
func (a *api) gatewayHistoryRead(w http.ResponseWriter, r *http.Request) error {
	if a.gatewayHistory == nil {
		return &unavailable{errNoGatewayHistory}
	}
	window := r.URL.Query().Get("window")
	if window == "" {
		window = gateway.Window5m
	}
	if !slices.Contains(gateway.Windows, window) {
		return &badRequest{fmt.Errorf("window %q must be one of %s", window, strings.Join(gateway.Windows, ", "))}
	}
	name := r.PathValue("name")
	out := gatewayHistory{Events: []gateway.Event{}}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			g, ok := cfg.Gateway(name)
			if !ok {
				return &notFound{fmt.Errorf("no gateway is called %q", name)}
			}
			out.Monitor = g.Monitor
		}
	}
	now := time.Now()
	out.Report = a.gatewayHistory.Read(name, window, now)
	from := now.Add(-windowSpans[window])
	for _, e := range a.gatewayHistory.Events.Recent(0) {
		if !e.Time.After(from) {
			break
		}
		if e.Gateway == name {
			out.Events = append(out.Events, e)
			if len(out.Events) == gatewayEventsShown {
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type gatewayStrip struct {
	Name string `json:"name"`
	gateway.Strip
	// Monitor is what the gateway is probed at, empty for its next hops,
	// and Families how many it is probed in.
	Monitor  string `json:"monitor,omitempty"`
	Families int    `json:"families"`
}

// gatewayStrips serves each watched gateway's last day as the dashboard
// draws it, judged against its thresholds.
func (a *api) gatewayStrips(w http.ResponseWriter, _ *http.Request) error {
	if a.gatewayHistory == nil {
		return &unavailable{errNoGatewayHistory}
	}
	var cfg *model.Config
	if a.engine != nil {
		cfg = a.engine.Effective()
	}
	now := time.Now()
	out := []gatewayStrip{}
	for _, s := range a.gatewayStatuses() {
		var g model.Gateway
		if cfg != nil {
			if c, ok := cfg.Gateway(s.Name); ok {
				g = *c
			}
		}
		out = append(out, gatewayStrip{
			Name: s.Name, Strip: a.gatewayHistory.Strip(s.Name, now, g.SlowAbove(), g.LossyAbove()),
			Monitor: g.Monitor, Families: len(s.Families),
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// gatewayStream sends every probe as it lands.
func (a *api) gatewayStream(w http.ResponseWriter, r *http.Request) error {
	if a.gatewayHistory == nil {
		return &unavailable{errNoGatewayHistory}
	}
	ch, cancel := a.gatewayHistory.Subscribe(64)
	defer cancel()
	return streamEvents(a, w, r, ch, func(p gateway.Probe) any { return p }, nil)
}

// gatewayEvents serves a page of the gateways' events, newest first,
// searched, one gateway's when it is named.
func (a *api) gatewayEvents(w http.ResponseWriter, r *http.Request) error {
	if a.gatewayHistory == nil {
		return &unavailable{errNoGatewayHistory}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	name := r.URL.Query().Get("gateway")
	match := gateway.EventMatcher(q)
	page, err := a.gatewayHistory.Events.Query(r.Context(), before, limit, func(e *gateway.Event) bool {
		return (name == "" || e.Gateway == name) && match(e)
	})
	if err != nil {
		return err
	}
	held, oldest := a.gatewayHistory.Events.Held()
	writeJSON(w, http.StatusOK, newLogPage(page, page.Entries, held, oldest))
	return nil
}

// gatewayEventStream sends each event as it happens.
func (a *api) gatewayEventStream(w http.ResponseWriter, r *http.Request) error {
	if a.gatewayHistory == nil {
		return &unavailable{errNoGatewayHistory}
	}
	ch, cancel := a.gatewayHistory.Events.Subscribe(64)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e gateway.Event) any { return e }, nil)
}
