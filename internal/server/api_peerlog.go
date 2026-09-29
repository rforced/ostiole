package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/peerlog"
)

func (a *api) registerPeerLogs(mux *router) {
	mux.HandleFunc("GET /api/v1/wireguard/log", a.readNoEngine(a.peerLogList(peerlog.WireGuard, func() *peerlog.Log { return a.wireguardLog })))
	mux.HandleFunc("GET /api/v1/wireguard/log/stream", a.readNoEngine(a.peerLogStream(func() *peerlog.Log { return a.wireguardLog })))
	mux.HandleFunc("DELETE /api/v1/wireguard/log", a.admin(a.clearOne(peerlog.WireGuard.Name)))
	mux.HandleFunc("GET /api/v1/tailscale/log", a.readNoEngine(a.peerLogList(peerlog.Tailscale, func() *peerlog.Log { return a.tailscaleLog })))
	mux.HandleFunc("GET /api/v1/tailscale/log/stream", a.readNoEngine(a.peerLogStream(func() *peerlog.Log { return a.tailscaleLog })))
	mux.HandleFunc("DELETE /api/v1/tailscale/log", a.admin(a.clearOne(peerlog.Tailscale.Name)))
}

// peerLogPage is a page of a VPN's peer log, and whether the level the
// router runs at keeps it at all.
type peerLogPage struct {
	Kept bool `json:"kept"`
	logPage[peerlog.Event]
}

var errNoPeerLog = errors.New("the peers' log is not kept by this daemon")

// peerLogList serves a page of one kind of peer's coming and going, newest
// first and searched, from memory and on into the files.
func (a *api) peerLogList(kind peerlog.Kind, logOf func() *peerlog.Log) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		l := logOf()
		if l == nil {
			return &unavailable{errNoPeerLog}
		}
		q, before, limit, err := pageParams(r)
		if err != nil {
			return err
		}
		started := time.Now()
		match := peerlog.Matcher(q)
		page, err := l.Query(r.Context(), before, limit, match)
		if err != nil {
			return err
		}
		older, page, err := carryOn(r.Context(), a, kind.Files(l), peerlog.FileVersion, page, before, limit, started,
			peerlog.ParseLine, logring.Seq[peerlog.Event, *peerlog.Event], match)
		if err != nil {
			return err
		}
		held, oldest := l.Held()
		out := peerLogPage{logPage: newLogPage(page, append(page.Entries, older...), held, oldest)}
		if a.engine != nil {
			if cfg := a.engine.Effective(); cfg != nil {
				out.Kept = peerlog.Kept(cfg)
			}
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	}
}

// peerLogStream sends one kind of peer's coming and going as it happens.
func (a *api) peerLogStream(logOf func() *peerlog.Log) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		l := logOf()
		if l == nil {
			return &unavailable{errNoPeerLog}
		}
		ch, cancel := l.Subscribe(256)
		defer cancel()
		return streamEvents(a, w, r, ch, func(e peerlog.Event) any { return e }, nil)
	}
}
