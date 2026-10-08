// Package requestlog keeps what the reverse proxy served: the line per
// request it writes at the Info and Debug levels, read from its journal.
// The proxy cuts each line down before writing it: no query string, no
// headers but the user agent.
package requestlog

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/logsearch"
	"github.com/rforced/ostiole/internal/model"
)

// Request is one request the proxy answered.
type Request struct {
	logring.Stamp
	// Site is the site that answered, empty for a name no site claims.
	Site   string `json:"site,omitempty"`
	Client string `json:"client"`
	Method string `json:"method"`
	Host   string `json:"host"`
	// Path is what was asked for, without its query string.
	Path  string `json:"path"`
	Proto string `json:"proto,omitempty"`
	// Status is what the client got, Bytes how much, and Duration how long
	// it took, in seconds.
	Status   int     `json:"status"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Agent    string  `json:"agent,omitempty"`
	// By is who answered: the site's server, the WAF, or the proxy itself
	// (a site's address check, a name no site claims, a server it could not
	// reach). Empty on a line an older proxy wrote.
	By string `json:"by,omitempty"`
}

// Who answered a request.
const (
	BySite  = "site"
	ByWAF   = "waf"
	ByProxy = "proxy"
)

// byLabels are the answers the Requests tab marks, as it marks them.
var byLabels = map[string]string{ByWAF: "WAF", ByProxy: "proxy"}

// Log is the proxy's requests in memory.
type Log = logring.Ring[Request, *Request]

// New returns an empty log of the default size.
func New() *Log {
	var k model.LogKeep
	return logring.New[Request, *Request](k.Size(model.DefaultRequestEntries), k.Retention())
}

// Settings sizes the log in a configuration.
func Settings(c *model.Config) (int, time.Duration) {
	r := c.Services.Proxy.Requests
	return r.Size(model.DefaultRequestEntries), r.Retention()
}

// Kept says whether the configuration keeps the requests at all: only the
// levels that keep what the daemons say about their clients do.
func Kept(c *model.Config) bool { return c.System.Logging.Records() }

// The log's directory under logfile.Dir, and the format of its lines: a
// request as the API serves it.
const (
	FileName    = "requests"
	FileVersion = 1
)

// Files describes the log to the writer that keeps it in files.
func Files(l *Log) logfile.Log {
	return l.Files(FileName, FileVersion, Kept, func(c *model.Config) int {
		return int(c.Services.Proxy.Requests.Retention() / (24 * time.Hour))
	})
}

// ParseLine reads a line of the log's files.
func ParseLine(line []byte) (Request, time.Time, error) {
	return logring.Parse[Request, *Request](line)
}

// Parse reads a line the proxy wrote, keeping only a request's.
func Parse(message string) (Request, bool) {
	if !strings.HasPrefix(message, "{") || !strings.Contains(message, `"http.log.access`) {
		return Request{}, false
	}
	var l struct {
		Logger  string `json:"logger"`
		Request struct {
			RemoteIP string `json:"remote_ip"`
			ClientIP string `json:"client_ip"`
			Proto    string `json:"proto"`
			Method   string `json:"method"`
			Host     string `json:"host"`
			URI      string `json:"uri"`
		} `json:"request"`
		Duration float64 `json:"duration"`
		Size     int64   `json:"size"`
		Status   int     `json:"status"`
		Site     string  `json:"site"`
		Agent    string  `json:"user_agent"`
		// Upstream is null when no server behind the proxy answered, and
		// missing from an older proxy's line.
		Upstream json.RawMessage `json:"upstream_status"`
		WAF      string          `json:"waf"`
	}
	if json.Unmarshal([]byte(message), &l) != nil || !strings.HasPrefix(l.Logger, "http.log.access") {
		return Request{}, false
	}
	client := l.Request.ClientIP
	if client == "" {
		client = l.Request.RemoteIP
	}
	// The proxy takes the query off; a line an older proxy wrote still has
	// it.
	path, _, _ := strings.Cut(l.Request.URI, "?")
	return Request{
		Site: l.Site, Client: client, Method: l.Request.Method, Host: l.Request.Host, Path: path,
		Proto: l.Request.Proto, Status: l.Status, Bytes: l.Size, Duration: l.Duration, Agent: l.Agent,
		By: answeredBy(l.Upstream, l.WAF, l.Status),
	}, true
}

// answeredBy reads who answered from what the proxy noted. The WAF notes
// the error it made; when it refuses what the server sent instead, the
// client gets another status than the server's, which nothing else on
// the path changes.
func answeredBy(upstream json.RawMessage, waf string, status int) string {
	switch {
	case waf != "":
		return ByWAF
	case len(upstream) == 0:
		return ""
	case string(upstream) == "null":
		return ByProxy
	}
	if s, err := strconv.Atoi(string(upstream)); err == nil && s != status {
		return ByWAF
	}
	return BySite
}

// Search hands a the values the Requests tab shows for the request, as it
// shows them. buf is scratch, handed back to be used again.
func (r *Request) Search(a logsearch.Adder, buf []byte) []byte {
	a.Add(r.Site)
	a.Add(r.Client)
	a.Add(r.Method)
	a.Add(r.Host)
	a.Add(r.Path)
	buf = strconv.AppendInt(buf[:0], int64(r.Status), 10)
	a.AddBytes(buf)
	a.Add(byLabels[r.By])
	a.Add(r.Agent)
	return buf
}

// Matcher is a search's test of a request, the same for the log's files
// as for its memory.
func Matcher(q logsearch.Query) func(*Request) bool {
	row := q.Row()
	var buf []byte
	return func(r *Request) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		buf = r.Search(&row, buf)
		return row.Match()
	}
}
