package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/wirelesslog"
)

func (a *api) registerWirelessLog(mux *router) {
	mux.HandleFunc("GET /api/v1/wireless/log", a.readNoEngine(a.wirelessLogList))
	mux.HandleFunc("GET /api/v1/wireless/log/stream", a.readNoEngine(a.wirelessLogStream))
	mux.HandleFunc("DELETE /api/v1/wireless/log", a.admin(a.clearOne(wirelesslog.FileName)))
}

// wirelessRow is a client's coming or going on the wire, with the network
// it was and the name this router knows the client by.
type wirelessRow struct {
	wirelesslog.Event
	Network string `json:"network,omitempty"`
	Device  string `json:"device,omitempty"`
}

// wirelessLogPage is a page of the wireless log, and whether the level the
// router runs at keeps it at all.
type wirelessLogPage struct {
	Kept bool `json:"kept"`
	logPage[wirelessRow]
}

// networks names each wireless interface by the network it serves.
func networks(cfg *model.Config) map[string]string {
	out := map[string]string{}
	if cfg == nil {
		return out
	}
	for _, in := range cfg.Interfaces {
		if in.Wireless != nil {
			out[in.Name] = in.Wireless.SSID
		}
	}
	return out
}

// wirelessNames is how a row is named: its network and its device.
func (a *api) wirelessNames() func(*wirelesslog.Event) (string, string) {
	var cfg *model.Config
	if a.engine != nil {
		cfg = a.engine.Effective()
	}
	ssids, n := networks(cfg), a.namer()
	return func(e *wirelesslog.Event) (string, string) { return ssids[e.Interface], n.ByMAC(e.MAC) }
}

func wirelessRowOf(e wirelesslog.Event, names func(*wirelesslog.Event) (string, string)) wirelessRow {
	network, device := names(&e)
	return wirelessRow{Event: e, Network: network, Device: device}
}

// wirelessLogList serves a page of the wireless log, newest first and
// searched, from memory and on into the files.
func (a *api) wirelessLogList(w http.ResponseWriter, r *http.Request) error {
	if a.wirelesslog == nil {
		return &unavailable{errors.New("the wireless log is not kept by this daemon")}
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	started := time.Now()
	names := a.wirelessNames()
	match := wirelesslog.Matcher(q, names)
	page, err := a.wirelesslog.Query(r.Context(), before, limit, match)
	if err != nil {
		return err
	}
	older, page, err := carryOn(r.Context(), a, wirelesslog.Files(a.wirelesslog), wirelesslog.FileVersion, page, before, limit,
		started, wirelesslog.ParseLine, logring.Seq[wirelesslog.Event, *wirelesslog.Event], match)
	if err != nil {
		return err
	}
	rows := make([]wirelessRow, 0, len(page.Entries)+len(older))
	for _, e := range append(page.Entries, older...) {
		rows = append(rows, wirelessRowOf(e, names))
	}
	held, oldest := a.wirelesslog.Held()
	out := wirelessLogPage{logPage: newLogPage(page, rows, held, oldest)}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil {
			out.Kept = wirelesslog.Kept(cfg)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// wirelessLogStream sends the clients' coming and going as it happens.
func (a *api) wirelessLogStream(w http.ResponseWriter, r *http.Request) error {
	if a.wirelesslog == nil {
		return &unavailable{errors.New("the wireless log is not kept by this daemon")}
	}
	ch, cancel := a.wirelesslog.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e wirelesslog.Event) any { return wirelessRowOf(e, a.wirelessNames()) }, nil)
}
