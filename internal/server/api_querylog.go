package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/dnslog"
)

func (a *api) registerQueryLog(mux *router) {
	mux.HandleFunc("GET /api/v1/dns/queries", a.readNoEngine(a.queryLogList))
	mux.HandleFunc("GET /api/v1/dns/queries/stream", a.readNoEngine(a.queryLogStream))
	mux.HandleFunc("GET /api/v1/dns/queries/summary", a.readNoEngine(a.queryLogSummary))
	mux.HandleFunc("DELETE /api/v1/dns/queries", a.write(a.queryLogClear))
}

// queryRow is one answer on the wire. dnslog.Entry is not marshalled
// directly: the device name and the list names are put on here, from the
// tables that hold them, at the moment the row is read.
type queryRow struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Client string    `json:"client"`
	Device string    `json:"device,omitempty"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	Lists  []string  `json:"lists,omitempty"`
	Answer string    `json:"answer,omitempty"`
}

type queryLogPage struct {
	Enabled bool `json:"enabled"`
	// Since is when the log was switched on or last cleared.
	Since *time.Time `json:"since,omitempty"`
	// Total is how many answers match the filter, which is more than the
	// page holds whenever there is another page.
	Total   int        `json:"total"`
	Dropped uint64     `json:"dropped"`
	Entries []queryRow `json:"entries"`
}

func (a *api) row(n *namer, e dnslog.Entry) queryRow {
	r := queryRow{
		Seq: e.Seq, Time: e.Time, Name: e.Name,
		Type: dnslog.TypeName(e.Type), Status: e.Status.String(), Reason: e.Reason.String(),
		Lists: a.querylog.ListNames(e.Lists),
	}
	if e.Client.IsValid() {
		r.Client, r.Device = e.Client.String(), n.Name(e.Client)
	}
	if e.Answer.IsValid() {
		r.Answer = e.Answer.String()
	}
	return r
}

// queryLogList serves a page of answers, newest first.
func (a *api) queryLogList(w http.ResponseWriter, r *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	page := queryLogPage{Enabled: a.querylog.Enabled(), Entries: []queryRow{}}
	if !page.Enabled {
		writeJSON(w, http.StatusOK, page)
		return nil
	}
	f, matchable, err := a.queryFilter(r)
	if err != nil {
		return err
	}
	if matchable {
		entries, total := a.querylog.Query(f)
		n := a.namer()
		for _, e := range entries {
			page.Entries = append(page.Entries, a.row(n, e))
		}
		page.Total = total
	}
	page.Dropped = a.querylog.Dropped()
	if since := a.querylog.Since(); !since.IsZero() {
		page.Since = &since
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

// queryFilter reads the filter off the query string, refusing anything it
// does not understand rather than quietly ignoring it. The second result
// is false when the filter cannot match anything at all, which is what a
// device name nothing on this router answers to means.
func (a *api) queryFilter(r *http.Request) (dnslog.Filter, bool, error) {
	q := r.URL.Query()
	f := dnslog.Filter{Name: strings.TrimSpace(q.Get("name")), List: strings.TrimSpace(q.Get("list")), Limit: 200}
	status, ok := dnslog.ParseStatus(strings.TrimSpace(q.Get("status")))
	if !ok {
		return f, false, &badRequest{fmt.Errorf("unknown status %q", q.Get("status"))}
	}
	f.Status = status
	rtype, ok := dnslog.ParseType(q.Get("type"))
	if !ok {
		return f, false, &badRequest{fmt.Errorf("unknown record type %q", q.Get("type"))}
	}
	f.Type = rtype
	if v := strings.TrimSpace(q.Get("client")); v != "" {
		// An address matches itself; anything else is a device name.
		if f.Clients = a.namer().Match(v); len(f.Clients) == 0 {
			return f, false, nil
		}
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, false, &badRequest{fmt.Errorf("since %q is not an RFC 3339 time", v)}
		}
		f.Since = t
	}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil || n == 0 {
			return f, false, &badRequest{fmt.Errorf("before %q is not an entry number", v)}
		}
		f.Before = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 5000 {
			return f, false, &badRequest{errors.New("limit must be 1-5000")}
		}
		f.Limit = n
	}
	return f, true, nil
}

// queryLogStream sends answers as they arrive, the way the firewall log
// does. The device names come from a namer refreshed every ten seconds,
// so a long-lived stream picks up a lease that was taken while it ran.
func (a *api) queryLogStream(w http.ResponseWriter, r *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	rc := http.NewResponseController(w)
	ch, cancel := a.querylog.Subscribe(256)
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
			raw, err := json.Marshal(a.row(a.namer(), e))
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

// querySummary is the line above the table: what is held, and who asked
// for it.
type querySummary struct {
	Enabled    bool               `json:"enabled"`
	Since      *time.Time         `json:"since,omitempty"`
	Total      int                `json:"total"`
	Blocked    int                `json:"blocked"`
	Clients    int                `json:"clients"`
	Dropped    uint64             `json:"dropped"`
	TopNames   []dnslog.NameCount `json:"topNames"`
	TopBlocked []dnslog.NameCount `json:"topBlocked"`
	TopClients []clientSummary    `json:"topClients"`
}

type clientSummary struct {
	Client  string `json:"client"`
	Device  string `json:"device,omitempty"`
	Total   int    `json:"total"`
	Blocked int    `json:"blocked"`
}

func (a *api) queryLogSummary(w http.ResponseWriter, _ *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	out := querySummary{
		Enabled:  a.querylog.Enabled(),
		TopNames: []dnslog.NameCount{}, TopBlocked: []dnslog.NameCount{}, TopClients: []clientSummary{},
	}
	if !out.Enabled {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	s := a.querylog.Summary(10)
	out.Total, out.Blocked, out.Clients, out.Dropped = s.Total, s.Blocked, s.Clients, s.Dropped
	out.TopNames, out.TopBlocked = s.TopNames, s.TopBlocked
	if !s.Since.IsZero() {
		out.Since = &s.Since
	}
	n := a.namer()
	for _, c := range s.TopClients {
		out.TopClients = append(out.TopClients, clientSummary{
			Client: c.Client.String(), Device: n.Name(c.Client), Total: c.Total, Blocked: c.Blocked,
		})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *api) queryLogClear(w http.ResponseWriter, _ *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	a.querylog.Clear()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
	return nil
}
