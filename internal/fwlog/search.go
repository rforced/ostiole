package fwlog

import (
	"context"
	"strconv"
	"time"

	"github.com/rforced/ostiole/internal/logsearch"
)

// kindLabels are what the Logs page calls what matched, for the kinds that
// are not a rule. matchedLabel in web/src/lib/fwlog.js is the other copy.
var kindLabels = map[string]string{
	"default-drop":      "default drop",
	"block-private":     "private source",
	"block-bogons":      "bogon source",
	"block-dot":         "DNS over TLS",
	"block-doh":         "DNS over HTTPS",
	"protect-scanner":   "port scan",
	"protect-synflood":  "connection flood",
	"protect-icmpflood": "ping flood",
}

// Search hands a the values the Logs page shows for the entry, as it shows
// them: its verdict, what matched, the links, the protocol, both ends and
// the detail. access names a proxy access rule by its ID. buf is scratch
// for the values that are put together, handed back to be used again.
func (e *Entry) Search(a logsearch.Adder, buf []byte, access func(id string) string) []byte {
	if e.Action == "" {
		a.Add("unknown")
	} else {
		a.Add(e.Action)
	}
	switch label, ok := kindLabels[e.Kind]; {
	case ok && e.Zone != "" && SystemKinds[e.Kind]:
		buf = append(append(append(buf[:0], e.Zone...), ' '), label...)
		a.AddBytes(buf)
	case ok:
		a.Add(label)
	case e.Kind == "rule":
		a.Add(orDash(e.RuleID))
	case e.Kind == "zone-drop":
		buf = append(append(buf[:0], e.Zone...), " default"...)
		a.AddBytes(buf)
	case e.Kind == "proxy":
		name := e.RuleID
		if access != nil {
			if n := access(e.RuleID); n != "" {
				name = n
			}
		}
		buf = append(append(buf[:0], "proxy: "...), name...)
		a.AddBytes(buf)
	default:
		a.Add(orDash(e.Prefix))
	}
	a.Add(e.InIface)
	a.Add(e.OutIface)
	a.Add(e.Proto)
	buf = endpoint(buf[:0], e.Src, e.SrcPort)
	a.AddBytes(buf)
	buf = endpoint(buf[:0], e.Dst, e.DstPort)
	a.AddBytes(buf)
	switch {
	case e.TCPFlags != "":
		a.Add(e.TCPFlags)
	case len(e.Proto) >= 4 && e.Proto[:4] == "icmp":
		if e.ICMPType != nil {
			buf = strconv.AppendUint(append(buf[:0], "type "...), uint64(*e.ICMPType), 10)
			a.AddBytes(buf)
		}
	default:
		buf = append(strconv.AppendInt(buf[:0], int64(e.Length), 10), " B"...)
		a.AddBytes(buf)
	}
	return buf
}

// endpoint is an address with its port, as the page writes it.
func endpoint(buf []byte, addr string, port uint16) []byte {
	if addr == "" {
		return buf
	}
	buf = append(buf, addr...)
	if port != 0 {
		buf = strconv.AppendUint(append(buf, ':'), uint64(port), 10)
	}
	return buf
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Query reads the ring newest first from the packet before: those keep
// accepts whose row holds every word of q, a page of limit at most, within
// the search budget. keep may fill in what the row shows, the verdict of a
// packet whose prefix did not record it, before the words are looked for.
func (r *Ring) Query(ctx context.Context, q logsearch.Query, before uint64, limit int,
	keep func(*Entry) bool, access func(id string) string,
) (logsearch.Page[Entry], error) {
	return logsearch.Walk(ctx, r, before, limit, logsearch.DefaultBudget,
		func(e *Entry) uint64 { return e.Seq },
		func(e *Entry) time.Time { return e.Time },
		Matcher(q, keep, access))
}

// Matcher is Query's test of an entry, the same for the log's files as for
// its memory.
func Matcher(q logsearch.Query, keep func(*Entry) bool, access func(id string) string) func(*Entry) bool {
	row := q.Row()
	var buf []byte
	return func(e *Entry) bool {
		if keep != nil && !keep(e) {
			return false
		}
		if q.Empty() {
			return true
		}
		row.Reset()
		buf = e.Search(&row, buf, access)
		return row.Match()
	}
}
