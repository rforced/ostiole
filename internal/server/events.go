package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// endStreams ends every stream, as the server shuts down.
func (a *api) endStreams() {
	a.stopOnce.Do(func() { close(a.stopping) })
}

// streamEvents sends what arrives on ch as server-sent events, each made
// by event, until the client goes away, may no longer read them, or the
// server shuts down. A comment line every keepalive keeps proxies from
// timing out, and is when the caller is checked again and refresh, if
// there is one, runs.
func streamEvents[T any](a *api, w http.ResponseWriter, r *http.Request, ch <-chan T, event func(T) any, refresh func()) error {
	release, ok := a.holdStream(r)
	if !ok {
		return errTooManyStreams
	}
	defer release()
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
		case <-a.stopping:
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

// maxStreams is how many streams one account or token holds at once. A
// page open live on every log, in a few tabs, is a dozen; each stream is
// a goroutine and a buffer for as long as it lasts.
const maxStreams = 32

var errTooManyStreams = errors.New("this account has too many pages open live; close some and reload")

// holdStream counts a stream against its caller, and says no past
// maxStreams. The release gives the place back.
func (a *api) holdStream(r *http.Request) (func(), bool) {
	p, ok := a.principal(r, false)
	if !ok {
		// The route let the caller in; the keepalive's check ends the
		// stream if they have gone since.
		return func() {}, true
	}
	key := "user:" + p.Name
	if p.Token {
		key = "token:" + p.Name
	}
	s := &a.streams
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]int{}
	}
	if s.by[key] >= maxStreams {
		return nil, false
	}
	s.by[key]++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.by[key]--; s.by[key] <= 0 {
			delete(s.by, key)
		}
	}, true
}
