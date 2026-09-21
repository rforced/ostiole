package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/modem"
)

// Diagnostics are root-only: they need raw sockets and the journal.
func (a *api) registerDiag(mux *router) {
	mux.HandleFunc("POST /api/v1/diagnostics/ping", a.write(a.diagPing))
	mux.HandleFunc("POST /api/v1/diagnostics/traceroute", a.write(a.diagTraceroute))
	mux.HandleFunc("POST /api/v1/diagnostics/capture", a.write(a.diagCapture))
	mux.HandleFunc("GET /api/v1/diagnostics/journal", a.readNoEngine(a.diagJournal))
	mux.HandleFunc("GET /api/v1/diagnostics/states", a.readNoEngine(a.diagStates))
	mux.HandleFunc("GET /api/v1/diagnostics/neighbours", a.readNoEngine(a.diagNeighbours))
	mux.HandleFunc("GET /api/v1/diagnostics/modem", a.readNoEngine(a.diagModem))
}

// diagModem reads the cable modem's status pages: provisioning, the
// channels and their levels. The address defaults to where a DOCSIS modem
// answers; a status is kept for a minute unless refresh is asked for.
func (a *api) diagModem(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	address := strings.TrimSpace(q.Get("address"))
	if address == "" {
		address = modem.DefaultAddress
	}
	refresh := q.Get("refresh") == "1" || q.Get("refresh") == "true"
	st, err := a.modems.Get(r.Context(), address, refresh)
	switch {
	case errors.Is(err, modem.ErrBadAddress):
		return &badRequest{err}
	case err != nil:
		return &upstream{err}
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// diagStates reports the connections the kernel is tracking: what the
// firewall's established rules are matching, and where NAT is happening.
func (a *api) diagStates(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	opts := diag.StatesOptions{
		Address:  strings.TrimSpace(q.Get("address")),
		Protocol: strings.TrimSpace(q.Get("protocol")),
	}
	switch opts.Protocol {
	case "", "tcp", "udp", "icmp", "icmpv6":
	default:
		return &badRequest{fmt.Errorf("unknown protocol %q", opts.Protocol)}
	}
	if v := q.Get("port"); v != "" {
		port, err := strconv.ParseUint(v, 10, 16)
		if err != nil || port == 0 {
			return &badRequest{fmt.Errorf("port %q is not a port", v)}
		}
		opts.Port = uint16(port)
	}
	if v := q.Get("limit"); v != "" {
		limit, err := strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 5000 {
			return &badRequest{errors.New("limit must be 1-5000")}
		}
		opts.Limit = limit
	}
	res, err := diag.States(opts)
	if err != nil {
		return &unavailable{err}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// diagNeighbours reports the ARP and NDP tables: which address is behind
// which hardware address, and on which interface.
func (a *api) diagNeighbours(w http.ResponseWriter, _ *http.Request) error {
	out, err := diag.Neighbours()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type pingRequest struct {
	Target    string `json:"target"`
	Interface string `json:"interface,omitempty"`
	Count     int    `json:"count,omitempty"`
}

func (a *api) diagPing(w http.ResponseWriter, r *http.Request) error {
	var req pingRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Count < 0 || req.Count > 20 {
		return &badRequest{errors.New("count must be 1-20")}
	}
	res, err := diag.Ping(r.Context(), nil, diag.PingOptions{
		Target:    req.Target,
		Interface: req.Interface,
		Count:     req.Count,
	})
	if err != nil {
		return diagError(err)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

type traceRequest struct {
	Target  string `json:"target"`
	MaxHops int    `json:"maxHops,omitempty"`
	Resolve bool   `json:"resolve,omitempty"`
}

func (a *api) diagTraceroute(w http.ResponseWriter, r *http.Request) error {
	var req traceRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.MaxHops < 0 || req.MaxHops > 64 {
		return &badRequest{errors.New("maxHops must be 1-64")}
	}
	// A full sweep of unanswered hops must not outlive the request.
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	res, err := diag.Traceroute(ctx, diag.TraceOptions{
		Target:  req.Target,
		MaxHops: req.MaxHops,
		Resolve: req.Resolve,
	})
	if err != nil {
		return diagError(err)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

type captureRequest struct {
	Interface string `json:"interface"`
	Count     int    `json:"count,omitempty"`
	Seconds   int    `json:"seconds,omitempty"`
	Address   string `json:"address,omitempty"`
	Port      int    `json:"port,omitempty"`
	Snaplen   int    `json:"snaplen,omitempty"`
}

// diagCapture streams a pcap file back, so the browser can hand it
// straight to Wireshark.
func (a *api) diagCapture(w http.ResponseWriter, r *http.Request) error {
	var req captureRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Interface == "" {
		return &badRequest{errors.New("interface is required")}
	}
	if req.Port < 0 || req.Port > 65535 {
		return &badRequest{errors.New("port must be 0-65535")}
	}
	seconds := req.Seconds
	if seconds <= 0 || seconds > diag.MaxCaptureSeconds {
		seconds = 10
	}
	ctx, cancel := contextWithTimeout(r, time.Duration(seconds+5)*time.Second)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "application/vnd.tcpdump.pcap")
	h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		"ostiole-"+req.Interface+"-"+time.Now().UTC().Format("20060102-150405")+".pcap"))
	h.Set("Cache-Control", "no-store")
	// The body is written as packets arrive, so the client sees progress
	// rather than waiting on a buffer.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(time.Duration(seconds+30) * time.Second))
	n, err := diag.Capture(ctx, flushWriter{w: w, rc: rc}, diag.CaptureOptions{
		Interface: req.Interface,
		Count:     req.Count,
		Duration:  time.Duration(seconds) * time.Second,
		Address:   req.Address,
		Port:      req.Port,
		Snaplen:   req.Snaplen,
	})
	if err != nil && n == 0 {
		// Nothing has been written yet, so a JSON error is still possible.
		return diagError(err)
	}
	return nil
}

type flushWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (f flushWriter) Write(p []byte) (int, error) { return f.w.Write(p) }
func (f flushWriter) Flush() error                { return f.rc.Flush() }

func (a *api) diagJournal(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	lines := 200
	if v := q.Get("lines"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > diag.MaxJournalLines {
			return &badRequest{fmt.Errorf("lines must be 1-%d", diag.MaxJournalLines)}
		}
		lines = n
	}
	priority := 0
	if v := q.Get("priority"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 7 {
			return &badRequest{errors.New("priority must be 0-7")}
		}
		priority = n
	}
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()
	entries, err := diag.Journal(ctx, diag.JournalOptions{
		Unit:     q.Get("unit"),
		Lines:    lines,
		Since:    q.Get("since"),
		Priority: priority,
	})
	if err != nil {
		return diagError(err)
	}
	writeJSON(w, http.StatusOK, entries)
	return nil
}

// diagError turns the mistakes a user can make into 400s, and a missing
// privilege into a 503 that says so.
func diagError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "operation not permitted"), strings.Contains(msg, "not permitted"):
		return &unavailable{fmt.Errorf("%w (diagnostics need the daemon to run as root)", err)}
	case strings.Contains(msg, "cannot resolve"),
		strings.Contains(msg, "required"),
		strings.Contains(msg, "invalid"),
		strings.Contains(msg, "interface "):
		return &badRequest{err}
	}
	return err
}

// contextWithTimeout bounds a diagnostic so a slow one cannot hold a
// request open forever.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
