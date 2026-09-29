package dnslog

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/rforced/ostiole/internal/logsearch"
)

// Search hands a the values the Queries tab shows for the entry, as it
// shows them: who asked by name and address, what for, what came of it,
// the lists that blocked it and the answer. device is the client's name
// and lists the log's list names, one bit of Lists each. buf is scratch,
// handed back to be used again.
func (e *Entry) Search(a logsearch.Adder, buf []byte, device string, lists []string) []byte {
	a.Add(device)
	if e.Client.IsValid() {
		buf = e.Client.AppendTo(buf[:0])
		a.AddBytes(buf)
	}
	a.Add(e.Name)
	a.Add(TypeName(e.Type))
	a.Add(e.Status.String())
	for i, name := range lists {
		if e.Lists&(1<<uint(i)) != 0 {
			a.Add(name)
		}
	}
	if e.Answer.IsValid() {
		buf = e.Answer.AppendTo(buf[:0])
		a.AddBytes(buf)
	}
	return buf
}

// FileMatcher is Query's test for an answer read back from the files,
// whose lists are named rather than bits of the table in memory: the same
// filter and the same words.
func FileMatcher(q logsearch.Query, f Filter, device func(netip.Addr) string) func(*Stored) bool {
	name := strings.ToLower(strings.TrimSpace(f.Name))
	row := q.Row()
	var buf []byte
	return func(s *Stored) bool {
		e := s.Entry
		if !matches(e, f, name, 0) {
			return false
		}
		if f.List != "" && (e.Status != StatusBlocked || !slices.Contains(s.ListNames, f.List)) {
			return false
		}
		if q.Empty() {
			return true
		}
		row.Reset()
		var who string
		if device != nil {
			who = device(e.Client)
		}
		// Each of its own names, as a bit of a table of just those.
		e.Lists = 1<<uint(min(len(s.ListNames), MaxLists)) - 1
		buf = e.Search(&row, buf, who, s.ListNames)
		return row.Match()
	}
}
