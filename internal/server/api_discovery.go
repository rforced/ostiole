package server

import (
	"errors"
	"net/http"
	"time"

	"ostiole/internal/discovery"
	"ostiole/internal/discoverylog"
	"ostiole/internal/logring"
)

// DiscoveryRelay is what the API reads of the mDNS and SSDP relay.
type DiscoveryRelay interface {
	Status() discovery.Status
	Announcements() []discovery.Announcement
}

func (a *api) registerDiscovery(mux *router) {
	mux.HandleFunc("GET /api/v1/discovery/log", a.readNoEngine(a.discoveryLogList))
	mux.HandleFunc("GET /api/v1/discovery/log/stream", a.readNoEngine(a.discoveryLogStream))
	mux.HandleFunc("DELETE /api/v1/discovery/log", a.admin(a.clearOne(discoverylog.FileName)))
	mux.HandleFunc("GET /api/v1/discovery/announcements", a.readNoEngine(a.discoveryAnnouncements))
}

// discoveryLogPage is a page of the discovery log, and whether the router
// keeps it at all.
type discoveryLogPage struct {
	Kept bool `json:"kept"`
	logPage[discoverylog.Event]
}

var (
	errNoDiscoveryLog   = errors.New("the discovery log is not kept by this daemon")
	errNoDiscoveryRelay = errors.New("the discovery relay does not run in this daemon")
)

// discoveryLogList serves a page of the discovery log, newest first and
// searched, from memory and on into the files.
func (a *api) discoveryLogList(w http.ResponseWriter, r *http.Request) error {
	l := a.discoverylog
	if l == nil {
		return &unavailable{errNoDiscoveryLog}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	started := time.Now()
	match := discoverylog.Matcher(q)
	page, err := l.Query(r.Context(), before, limit, match)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, discoverylog.Files(l), discoverylog.FileVersion, page, before, limit, started,
		discoverylog.ParseLine, logring.Seq[discoverylog.Event, *discoverylog.Event], match)
	if err != nil {
		return err
	}
	held, oldest := l.Held()
	out := discoveryLogPage{logPage: newLogPage(page, append(page.Entries, older...), held, oldest)}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			out.Kept = discoverylog.Kept(cfg)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// discoveryLogStream sends the packets the relay sees as it sees them.
func (a *api) discoveryLogStream(w http.ResponseWriter, r *http.Request) error {
	if a.discoverylog == nil {
		return &unavailable{errNoDiscoveryLog}
	}
	ch, cancel := a.discoverylog.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e discoverylog.Event) any { return e }, nil)
}

// discoveryAnnouncements lists what the relay has heard announced.
func (a *api) discoveryAnnouncements(w http.ResponseWriter, _ *http.Request) error {
	if a.discovery == nil {
		return &unavailable{errNoDiscoveryRelay}
	}
	list := a.discovery.Announcements()
	if list == nil {
		list = []discovery.Announcement{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": list})
	return nil
}

// discoveryWarnings are the networks where more packets arrive than the
// relay will carry.
func (a *api) discoveryWarnings() []Warning {
	if a.discovery == nil {
		return nil
	}
	var out []Warning
	for _, name := range a.discovery.Status().Overrun {
		out = append(out, Warning{
			Kind: "discovery-overrun", Level: "warn", Key: name,
			Title:  "Discovery is dropping packets on " + name,
			Detail: "Over 1000 packets a second arrived here. Another relay on this network?",
		})
	}
	return out
}
