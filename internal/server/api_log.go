package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (a *api) registerLog(mux *router) {
	mux.HandleFunc("GET /api/v1/log/recent", a.readNoEngine(a.logRecent))
	mux.HandleFunc("GET /api/v1/log/stream", a.readNoEngine(a.logStream))
}

func (a *api) logRecent(w http.ResponseWriter, r *http.Request) error {
	if a.fwlog == nil {
		return &unavailable{errors.New("firewall log not available (daemon not running as root?)")}
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5000 {
			return &badRequest{errors.New("limit must be 1-5000")}
		}
		limit = n
	}
	writeJSON(w, http.StatusOK, a.fwlog.Recent(limit))
	return nil
}

// logStream sends new entries as server-sent events until the client
// goes away. A comment line every 15s keeps proxies from timing out.
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

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			if err := rc.Flush(); err != nil {
				return nil
			}
		case e := <-ch:
			raw, err := json.Marshal(e)
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
