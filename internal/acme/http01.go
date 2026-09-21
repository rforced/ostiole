package acme

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// challengePath is what a CA fetches, and the only path the solver
// answers.
const challengePath = "/.well-known/acme-challenge/"

// HTTP01 answers http-01 challenges. The listener is bound while a
// challenge is pending and closed after the last one, so nothing is on
// port 80 the rest of the time.
type HTTP01 struct {
	// Addr is where to listen, read when the listener is bound. Nil is
	// port 80, which is where a CA looks; behind the proxy it is a
	// loopback port and the proxy forwards the path.
	Addr func() string

	mu     sync.Mutex
	tokens map[string]string
	srv    *http.Server
	done   chan struct{}
}

// Present puts one key authorisation up and binds the listener if this is
// the first challenge.
func (h *HTTP01) Present(_, token, keyAuth string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tokens == nil {
		h.tokens = map[string]string{}
	}
	h.tokens[token] = keyAuth
	if h.srv != nil {
		return nil
	}
	addr := ":80"
	if h.Addr != nil {
		if a := h.Addr(); a != "" {
			addr = a
		}
	}
	// A CA reaches port 80 from outside; the firewall rule is what
	// bounds who gets there.
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		delete(h.tokens, token)
		return fmt.Errorf("port %s: %w", strings.TrimPrefix(addr, ":"), err)
	}
	h.srv = &http.Server{Handler: http.HandlerFunc(h.serve), ReadHeaderTimeout: 10 * time.Second}
	h.done = make(chan struct{})
	srv, done := h.srv, h.done
	go func() {
		defer close(done)
		_ = srv.Serve(l)
	}()
	return nil
}

// CleanUp forgets a token and takes the listener down with the last one.
func (h *HTTP01) CleanUp(_, token, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.tokens, token)
	if len(h.tokens) > 0 || h.srv == nil {
		return nil
	}
	// Close rather than Shutdown: an open connection to a challenge
	// server has nothing left to say.
	err := h.srv.Close()
	<-h.done
	h.srv, h.done = nil, nil
	return err
}

// serve answers the challenge path and nothing else: no redirect, no
// index, nothing that would make this look like a web server.
func (h *HTTP01) serve(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.URL.Path, challengePath)
	if !ok || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	h.mu.Lock()
	keyAuth, known := h.tokens[token]
	h.mu.Unlock()
	if !known {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(keyAuth))
}
