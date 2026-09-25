package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// streamEvents sends what arrives on ch as server-sent events, each made
// by event, until the client goes away or may no longer read them. A
// comment line every keepalive keeps proxies from timing out, and is when
// the caller is checked again and refresh, if there is one, runs.
func streamEvents[T any](a *api, w http.ResponseWriter, r *http.Request, ch <-chan T, event func(T) any, refresh func()) error {
	// The logging middleware wraps the writer; the response controller
	// reaches through it to flush.
	rc := http.NewResponseController(w)
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
			if refresh != nil {
				refresh()
			}
			fmt.Fprint(w, ": keepalive\n\n")
			if err := rc.Flush(); err != nil {
				return nil
			}
		case v := <-ch:
			raw, err := json.Marshal(event(v))
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
