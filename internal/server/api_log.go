package server

import (
	"encoding/json"
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

// logStream sends new entries as server-sent events until the client
// goes away or may no longer read them. A comment line every 15s keeps
// proxies from timing out, and is when the caller is checked again.
func (a *api) logStream(w http.ResponseWriter, r *http.Request) error {
	if a.fwlog == nil {
		return &unavailable{errors.New("firewall log not available (daemon not running as root?)")}
	}
	// The logging middleware wraps the writer; the response controller
	// reaches through it to flush.
	rc := http.NewResponseController(w)
	ch, cancel := a.fwlog.Subscribe(256)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	if err := rc.Flush(); err != nil {
		return nil
	}
	_ = rc.SetWriteDeadline(time.Time{})

	keepalive := time.NewTicker(a.keepalive)
	defer keepalive.Stop()
	// Read once here rather than per packet: a stream stays open for hours
	// and the store is a file. The keepalive picks up an apply since.
	actions := a.ruleActions()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			// The session or token that opened the stream can end while
			// it runs, and the stream goes with it.
			if !a.stillAllowed(r) {
				return nil
			}
			actions = a.ruleActions()
			fmt.Fprint(w, ": keepalive\n\n")
			if err := rc.Flush(); err != nil {
				return nil
			}
		case e := <-ch:
			raw, err := json.Marshal(withAction(e, actions))
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", raw)
			if err := rc.Flush(); err != nil {
				return nil
			}
		}
	}
}
