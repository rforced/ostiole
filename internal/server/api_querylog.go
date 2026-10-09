package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ostiole/internal/dnslog"
	"ostiole/internal/logsearch"
)

func (a *api) registerQueryLog(mux *router) {
	mux.HandleFunc("GET /api/v1/dns/queries", a.readNoEngine(a.queryLogList))
	mux.HandleFunc("GET /api/v1/dns/queries/stream", a.readNoEngine(a.queryLogStream))
	mux.HandleFunc("GET /api/v1/dns/queries/summary", a.readNoEngine(a.queryLogSummary))
	mux.HandleFunc("DELETE /api/v1/dns/queries", a.admin(a.clearOne(dnslog.FileName)))
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

// queryLogPage is a page of the log, and whether it is on at all.
type queryLogPage struct {
	Enabled bool `json:"enabled"`
	logPage[queryRow]
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

// storedRow is an answer read back from the files on the wire, its lists
// already by name.
func storedRow(n *namer, s dnslog.Stored) queryRow {
	r := queryRow{
		Seq: s.Seq, Time: s.Time, Name: s.Name,
		Type: dnslog.TypeName(s.Type), Status: s.Status.String(), Reason: s.Reason.String(),
		Lists: s.ListNames,
	}
	if s.Client.IsValid() {
		r.Client, r.Device = s.Client.String(), n.Name(s.Client)
	}
	if s.Answer.IsValid() {
		r.Answer = s.Answer.String()
	}
	return r
}

// queryLogList serves a page of answers, newest first, searched and
// narrowed by the filters. name and client stay for scripts; the page
// sends the words it searches for as q.
func (a *api) queryLogList(w http.ResponseWriter, r *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	page := queryLogPage{Enabled: a.querylog.Enabled(), Entries: []queryRow{}}
	if !page.Enabled {
		writeJSON(w, http.StatusOK, page)
		return nil
	}
	q, before, limit, err := pageParams(r)
	if err != nil {
		return err
	}
	f, matchable, err := a.queryFilter(r)
	if err != nil {
		return err
	}
	held, oldest := a.querylog.Held()
	if !matchable {
		page.logPage = newLogPage(logsearch.Page[dnslog.Entry]{}, []queryRow{}, held, oldest)
		writeJSON(w, http.StatusOK, page)
		return nil
	}
	n := a.namer()
	started := time.Now()
	found, err := a.querylog.Query(r.Context(), q, before, limit, f, n.Name)
	if err != nil {
		return err
	}
	older, found, err := carryOn(r.Context(), a, a.querylog.Files(), dnslog.FileVersion, found, before, limit, started,
		dnslog.ParseLine, func(s *dnslog.Stored) uint64 { return s.Seq }, dnslog.FileMatcher(q, f, n.Name))
	if err != nil {
		return err
	}
	rows := make([]queryRow, 0, len(found.Entries)+len(older))
	for _, e := range found.Entries {
		rows = append(rows, a.row(n, e))
	}
	for _, s := range older {
		rows = append(rows, storedRow(n, s))
	}
	page.logPage = newLogPage(found, rows, held, oldest)
	writeJSON(w, http.StatusOK, page)
	return nil
}

// queryFilter reads the filters off the query string, refusing anything it
// does not understand rather than quietly ignoring it. The second result
// is false when the filter cannot match anything at all, which is what a
// device name nothing on this router answers to means.
func (a *api) queryFilter(r *http.Request) (dnslog.Filter, bool, error) {
	q := r.URL.Query()
	f := dnslog.Filter{Name: strings.TrimSpace(q.Get("name")), List: strings.TrimSpace(q.Get("list"))}
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
	return f, true, nil
}

// queryLogStream sends answers as they arrive, the way the firewall log
// does. The device names come from a namer refreshed every ten seconds,
// so a long-lived stream picks up a lease that was taken while it ran.
func (a *api) queryLogStream(w http.ResponseWriter, r *http.Request) error {
	if a.querylog == nil {
		return &unavailable{errors.New("query log not available (daemon not running as root?)")}
	}
	ch, cancel := a.querylog.Subscribe(256)
	defer cancel()
	return streamEvents(a, w, r, ch, func(e dnslog.Entry) any { return a.row(a.namer(), e) }, nil)
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
