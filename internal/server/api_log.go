package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/model"
)

func (a *api) registerLog(mux *router) {
	mux.HandleFunc("GET /api/v1/log/recent", a.readNoEngine(a.logRecent))
	mux.HandleFunc("GET /api/v1/log/entries", a.readNoEngine(a.logEntries))
	mux.HandleFunc("GET /api/v1/log/stream", a.readNoEngine(a.logStream))
	mux.HandleFunc("DELETE /api/v1/log", a.admin(a.clearOne(fwlog.FileName)))
}

// logEntries serves a page of the firewall log, newest first, searched and
// narrowed to what was blocked or allowed as asked.
func (a *api) logEntries(w http.ResponseWriter, r *http.Request) error {
	if a.fwlog == nil {
		return &unavailable{errors.New("firewall log not available (daemon not running as root?)")}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	show := r.URL.Query().Get("show")
	switch show {
	case "", "all", "blocked", "allowed":
	default:
		return &badRequest{fmt.Errorf("show %q must be all, blocked or allowed", show)}
	}
	started := time.Now()
	cfg := a.savedConfig()
	actions := ruleActions(cfg)
	names := accessNames(cfg)
	keep := func(e *fwlog.Entry) bool {
		*e = withAction(*e, actions)
		return shows(show, e.Action)
	}
	access := func(id string) string { return names[id] }
	page, err := a.fwlog.Query(r.Context(), q, before, limit, keep, access)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, a.fwlog.Files(), fwlog.FileVersion, page, before, limit, started,
		fwlog.ParseLine, func(e *fwlog.Entry) uint64 { return e.Seq }, fwlog.Matcher(q, keep, access))
	if err != nil {
		return err
	}
	held, oldest := a.fwlog.Held()
	writeJSON(w, http.StatusOK, newLogPage(page, append(page.Entries, older...), held, oldest))
	return nil
}

// shows reports whether a packet with this verdict is in the view: blocked
// is a drop or a reject. A packet whose prefix never recorded its verdict
// shows either way rather than being hidden on a guess.
func shows(show, action string) bool {
	switch {
	case show == "" || show == "all" || action == "":
		return true
	case show == "blocked":
		return action != string(model.ActionAccept)
	}
	return action == string(model.ActionAccept)
}

// savedConfig is the configuration saved on this router, nil when there is
// none or it cannot be read: the log reads it for names, and does without.
func (a *api) savedConfig() *model.Config {
	if a.engine == nil {
		return nil
	}
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return nil
	}
	return cfg
}

// accessNames is what the Logs page calls each line of the proxy's access
// list: its description, or its ID.
func accessNames(cfg *model.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	out := make(map[string]string, len(cfg.Services.Proxy.Access))
	for _, a := range cfg.Services.Proxy.Access {
		out[a.ID] = a.ID
		if a.Description != "" {
			out[a.ID] = a.Description
		}
	}
	return out
}

// logRecent returns what the ring still holds, newest first, the same way
// the journal and the overview's recent lists are ordered.
func (a *api) logRecent(w http.ResponseWriter, r *http.Request) error {
	if a.fwlog == nil {
		return &unavailable{errors.New("firewall log not available (daemon not running as root?)")}
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		// The ceiling is what the ring can hold, so a caller can ask for
		// everything a router was told to keep.
		if err != nil || n < 1 || n > model.MaxFirewallLogEntries {
			return &badRequest{fmt.Errorf("limit must be 1-%d", model.MaxFirewallLogEntries)}
		}
		limit = n
	}
	entries := a.fwlog.Recent(limit)
	actions := a.ruleActions()
	for i := range entries {
		entries[i] = withAction(entries[i], actions)
	}
	writeJSON(w, http.StatusOK, entries)
	return nil
}

// ruleActions reads what each rule does today, for entries whose prefix
// did not record it. A nil map is fine: it fills nothing in.
func (a *api) ruleActions() map[string]string {
	return ruleActions(a.savedConfig())
}

func ruleActions(cfg *model.Config) map[string]string {
	if cfg == nil {
		return nil
	}
	out := make(map[string]string, len(cfg.Rules))
	for _, r := range cfg.Rules {
		out[r.ID] = string(r.Action)
	}
	return out
}

// withAction fills in a verdict the log prefix did not carry. A router
// that has not applied since the upgrade is still running the stored
// ruleset, which wrote rule prefixes without one; the rule's action today
// is the only answer there is, and it is wrong only for a rule edited
// since the packet arrived. Everything else already logs its own verdict.
func withAction(e fwlog.Entry, actions map[string]string) fwlog.Entry {
	if e.Action == "" && e.Kind == "rule" {
		e.Action = actions[e.RuleID]
	}
	return e
}

// logStream sends new entries as they are logged.
func (a *api) logStream(w http.ResponseWriter, r *http.Request) error {
	if a.fwlog == nil {
		return &unavailable{errors.New("firewall log not available (daemon not running as root?)")}
	}
	ch, cancel := a.fwlog.Subscribe(256)
	defer cancel()
	// Read once here rather than per packet: a stream stays open for hours
	// and the store is a file. The keepalive picks up an apply since.
	actions := a.ruleActions()
	return streamEvents(a, w, r, ch,
		func(e fwlog.Entry) any { return withAction(e, actions) },
		func() { actions = a.ruleActions() })
}
