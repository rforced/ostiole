package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/model"
)

func (a *api) registerLog(mux *router) {
	mux.HandleFunc("GET /api/v1/log/recent", a.readNoEngine(a.logRecent))
	mux.HandleFunc("GET /api/v1/log/stream", a.readNoEngine(a.logStream))
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
	if a.engine == nil {
		return nil
	}
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return nil
	}
	return ruleActions(cfg)
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
