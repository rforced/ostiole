package waflog

import (
	"context"
	"strconv"
	"time"

	"ostiole/internal/logsearch"
	"ostiole/internal/wafevent"
)

// verdictLabels are what the Events tab calls each verdict.
var verdictLabels = map[string]string{
	wafevent.VerdictBlocked:    "blocked",
	wafevent.VerdictWouldBlock: "would block",
	wafevent.VerdictMatched:    "matched",
}

// VerdictLabel is a verdict as the Events tab writes it.
func VerdictLabel(v string) string {
	if label, ok := verdictLabels[v]; ok {
		return label
	}
	return v
}

// Search hands a the values the Events tab shows for the event, as it
// shows them: the site, who asked for what, the verdict, and each rule
// that matched. buf is scratch, handed back to be used again.
func (e *Entry) Search(a logsearch.Adder, buf []byte) []byte {
	a.Add(e.Site)
	a.Add(e.Client)
	a.Add(e.Method)
	a.Add(e.URI)
	if e.Status != 0 {
		buf = strconv.AppendInt(buf[:0], int64(e.Status), 10)
		a.AddBytes(buf)
	}
	a.Add(VerdictLabel(e.Verdict))
	for _, r := range e.Rules {
		buf = strconv.AppendInt(buf[:0], int64(r.ID), 10)
		a.AddBytes(buf)
		a.Add(r.Message)
	}
	return buf
}

// Query reads the log newest first from the event before: those keep
// accepts whose row holds every word of q, a page of limit at most, within
// the search budget.
func (l *Log) Query(ctx context.Context, q logsearch.Query, before uint64, limit int,
	keep func(*Entry) bool,
) (logsearch.Page[Entry], error) {
	return logsearch.Walk(ctx, l, before, limit, logsearch.DefaultBudget,
		func(e *Entry) uint64 { return e.Seq },
		func(e *Entry) time.Time { return e.Logged },
		Matcher(q, keep))
}

// Matcher is Query's test of an event, the same for the log's files as for
// its memory.
func Matcher(q logsearch.Query, keep func(*Entry) bool) func(*Entry) bool {
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
		buf = e.Search(&row, buf)
		return row.Match()
	}
}
