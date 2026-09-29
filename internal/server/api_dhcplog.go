package server

import (
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/rforced/ostiole/internal/dhcplog"
	"github.com/rforced/ostiole/internal/logring"
)

func (a *api) registerDHCPLog(mux *router) {
	mux.HandleFunc("GET /api/v1/dhcp/log", a.readNoEngine(a.dhcpLogList))
	mux.HandleFunc("GET /api/v1/dhcp/log/stream", a.readNoEngine(a.dhcpLogStream))
	mux.HandleFunc("DELETE /api/v1/dhcp/log", a.admin(a.clearOne(dhcplog.FileName)))
}

// dhcpRow is one of the DHCP server's messages on the wire, with the name
// this router knows the client by.
type dhcpRow struct {
	dhcplog.Event
	Device string `json:"device,omitempty"`
}

// dhcpLogPage is a page of the DHCP log, and whether the level the router
// runs at keeps it at all.
type dhcpLogPage struct {
	Kept bool `json:"kept"`
	logPage[dhcpRow]
}

// dhcpDevice is the name this router knows a client by: by its hardware
// address, which a DHCPv6 client of the link-layer kinds carries too, or
// by the address it was given.
func dhcpDevice(n *namer, e *dhcplog.Event) string {
	if mac := e.ClientMAC(); mac != "" {
		if name := n.ByMAC(mac); name != "" {
			return name
		}
	}
	if addr, err := netip.ParseAddr(e.Address); err == nil {
		return n.Name(addr)
	}
	return ""
}

// dhcpLogList serves a page of the DHCP log, newest first and searched,
// from memory and on into the files.
func (a *api) dhcpLogList(w http.ResponseWriter, r *http.Request) error {
	if a.dhcplog == nil {
		return &unavailable{errors.New("the DHCP log is not kept by this daemon")}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	started := time.Now()
	n := a.namer()
	match := dhcplog.Matcher(q, func(e *dhcplog.Event) string { return dhcpDevice(n, e) })
	page, err := a.dhcplog.Query(r.Context(), before, limit, match)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, dhcplog.Files(a.dhcplog), dhcplog.FileVersion, page, before, limit, started,
		dhcplog.ParseLine, logring.Seq[dhcplog.Event, *dhcplog.Event], match)
	if err != nil {
		return err
	}
	rows := make([]dhcpRow, 0, len(page.Entries)+len(older))
	for _, e := range append(page.Entries, older...) {
		rows = append(rows, dhcpRow{Event: e, Device: dhcpDevice(n, &e)})
	}
	held, oldest := a.dhcplog.Held()
	out := dhcpLogPage{logPage: newLogPage(page, rows, held, oldest)}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			out.Kept = dhcplog.Kept(cfg)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// dhcpLogStream sends the DHCP server's messages as it logs them.
func (a *api) dhcpLogStream(w http.ResponseWriter, r *http.Request) error {
	if a.dhcplog == nil {
		return &unavailable{errors.New("the DHCP log is not kept by this daemon")}
	}
	ch, cancel := a.dhcplog.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e dhcplog.Event) any { return dhcpRow{Event: e, Device: dhcpDevice(a.namer(), &e)} }, nil)
}
