package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logsearch"
)

// The rows one read of a log returns: 200 unless asked, 1000 at most.
const (
	logPageRows    = 200
	maxLogPageRows = 1000
)

// logPage is one read of a log: a page of rows newest first, where the
// next page starts, and what the log holds. A search that runs out of its
// budget says how far back it looked in SearchedTo; the next read carries
// on from Next. A match count would take a walk of the whole log, so there
// is none.
type logPage[T any] struct {
	Entries    []T        `json:"entries"`
	Next       uint64     `json:"next,omitempty"`
	More       bool       `json:"more"`
	SearchedTo *time.Time `json:"searchedTo,omitempty"`
	Held       int        `json:"held"`
	Oldest     *time.Time `json:"oldest,omitempty"`
}

// newLogPage fills in the page from a walk and what the log holds.
func newLogPage[T, E any](p logsearch.Page[E], rows []T, held int, oldest time.Time) logPage[T] {
	out := logPage[T]{Entries: rows, More: p.More, Held: held}
	if p.More {
		out.Next = p.Next
	}
	if !p.SearchedTo.IsZero() {
		out.SearchedTo = &p.SearchedTo
	}
	if !oldest.IsZero() {
		out.Oldest = &oldest
	}
	return out
}

// pageParams reads what every log's read takes: the words to look for,
// the entry to read before, and how many rows.
func pageParams(r *http.Request) (logsearch.Query, uint64, int, error) {
	v := r.URL.Query()
	q := logsearch.Parse(v.Get("q"))
	var before uint64
	if s := v.Get("before"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil || n == 0 {
			return q, 0, 0, &badRequest{fmt.Errorf("before %q is not an entry number", s)}
		}
		before = n
	}
	limit := logPageRows
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLogPageRows {
			return q, 0, 0, &badRequest{fmt.Errorf("limit must be 1-%d", maxLogPageRows)}
		}
		limit = n
	}
	return q, before, limit, nil
}

// carryOn reads on into a log's files where a read of its memory ran out,
// while the running configuration writes them: from the entry the walk
// stopped at, as many rows as the page has room for, with what is left of
// the budget. It hands back the rows the files gave and the page the read
// ends on, so paging and search go from memory into the files unbroken.
func carryOn[M, F any](ctx context.Context, a *api, log logfile.Log, version int, mem logsearch.Page[M],
	before uint64, limit int, started time.Time,
	parse func([]byte) (F, time.Time, error), seq func(*F) uint64, keep func(*F) bool,
) ([]F, logsearch.Page[M], error) {
	if mem.More || a.logFiles == nil || a.engine == nil {
		return nil, mem, nil
	}
	cfg := a.engine.Effective()
	if cfg == nil || !cfg.System.Logging.Files.Enabled || !log.On(cfg) {
		return nil, mem, nil
	}
	from := before
	if mem.Last != 0 {
		from = mem.Last
	}
	room := limit - len(mem.Entries)
	budget := logsearch.Budget{
		Entries: logsearch.DefaultBudget.Entries - mem.Walked,
		Time:    logsearch.DefaultBudget.Time - time.Since(started),
	}
	if room <= 0 || budget.Entries <= 0 || budget.Time <= 0 {
		// The next read starts in the files.
		mem.Next, mem.More = from, true
		if room > 0 {
			mem.SearchedTo = mem.LastAt
		}
		return nil, mem, nil
	}
	older, err := logfile.Page(ctx, a.logFiles, log.Name, version, from, time.Now().Add(-log.Kept(cfg)),
		room, budget, parse, seq, keep)
	if err != nil {
		return nil, mem, err
	}
	mem.Next, mem.More, mem.SearchedTo = older.Next, older.More, older.SearchedTo
	return older.Entries, mem, nil
}
