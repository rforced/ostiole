package logsearch

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// entry is a log line: its place, when it was logged, and a word.
type entry struct {
	seq  uint64
	at   time.Time
	word string
}

// sliceLog holds entries oldest first, the way a ring hands them out.
type sliceLog struct {
	entries []entry
	reads   int
}

func (l *sliceLog) Before(seq uint64, buf []entry) (int, bool) {
	l.reads++
	i := len(l.entries) - 1
	if seq != 0 {
		i = sort.Search(len(l.entries), func(k int) bool { return l.entries[k].seq >= seq }) - 1
	}
	n := 0
	for ; i >= 0 && n < len(buf); i-- {
		buf[n] = l.entries[i]
		n++
	}
	return n, i >= 0
}

func newLog(n int, word func(i int) string) *sliceLog {
	l := &sliceLog{}
	start := time.Unix(1_790_000_000, 0)
	for i := range n {
		l.entries = append(l.entries, entry{seq: uint64(i + 1), at: start.Add(time.Duration(i) * time.Second), word: word(i)})
	}
	return l
}

func walk(t *testing.T, l *sliceLog, before uint64, limit int, b Budget, want string) Page[entry] {
	t.Helper()
	page, err := Walk(context.Background(), Log[entry](l), before, limit, b,
		func(e *entry) uint64 { return e.seq },
		func(e *entry) time.Time { return e.at },
		func(e *entry) bool { return want == "" || e.word == want })
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func seqs(p Page[entry]) []uint64 {
	out := make([]uint64, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.seq
	}
	return out
}

func TestWalkPagesNewestFirst(t *testing.T) {
	t.Parallel()
	l := newLog(10, func(int) string { return "x" })
	first := walk(t, l, 0, 4, DefaultBudget, "")
	if got := seqs(first); len(got) != 4 || got[0] != 10 || got[3] != 7 || first.Next != 7 || !first.More {
		t.Fatalf("first page %v, next %d, more %v", got, first.Next, first.More)
	}
	second := walk(t, l, first.Next, 4, DefaultBudget, "")
	if got := seqs(second); got[0] != 6 || got[3] != 3 || !second.More {
		t.Fatalf("second page %v", got)
	}
	last := walk(t, l, second.Next, 4, DefaultBudget, "")
	if got := seqs(last); len(got) != 2 || got[1] != 1 || last.More || !last.SearchedTo.IsZero() {
		t.Fatalf("last page %v, more %v", got, last.More)
	}
	// A page that fills on the oldest entry has nothing after it.
	exact := walk(t, l, 5, 4, DefaultBudget, "")
	if got := seqs(exact); len(got) != 4 || exact.More {
		t.Errorf("exact page %v, more %v", got, exact.More)
	}
}

// A search reads the log in chunks until it has a page, however far back
// the matches are.
func TestWalkSearchesAcrossChunks(t *testing.T) {
	t.Parallel()
	l := newLog(3*Chunk, func(i int) string {
		if i == 10 || i == 20 {
			return "rare"
		}
		return "common"
	})
	page := walk(t, l, 0, 200, DefaultBudget, "rare")
	if got := seqs(page); len(got) != 2 || got[0] != 21 || got[1] != 11 || page.More {
		t.Errorf("found %v, more %v", got, page.More)
	}
	if l.reads != 3 {
		t.Errorf("%d reads of the log, want one a chunk", l.reads)
	}
}

// Out of budget, the walk says how far back it looked, and the next read
// carries on from there.
func TestWalkStopsOnItsBudget(t *testing.T) {
	t.Parallel()
	l := newLog(1000, func(i int) string {
		if i == 5 {
			return "rare"
		}
		return "common"
	})
	page := walk(t, l, 0, 200, Budget{Entries: 300, Time: time.Hour}, "rare")
	if len(page.Entries) != 0 || !page.More || page.Next != 701 {
		t.Fatalf("page %v, next %d, more %v", seqs(page), page.Next, page.More)
	}
	if want := l.entries[700].at; !page.SearchedTo.Equal(want) {
		t.Errorf("searched to %v, want %v", page.SearchedTo, want)
	}
	next := walk(t, l, page.Next, 200, Budget{Entries: 1000, Time: time.Hour}, "rare")
	if got := seqs(next); len(got) != 1 || got[0] != 6 || next.More {
		t.Errorf("carried on to %v, more %v", got, next.More)
	}
	// The clock is a budget too.
	timed := walk(t, l, 0, 200, Budget{Entries: 1_000_000, Time: -1}, "rare")
	if !timed.More || timed.SearchedTo.IsZero() {
		t.Errorf("a spent clock read the whole log: %+v", timed)
	}
}

func TestWalkStopsWhenAsked(t *testing.T) {
	t.Parallel()
	l := newLog(10, func(int) string { return "x" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Walk(ctx, Log[entry](l), 0, 5, DefaultBudget,
		func(e *entry) uint64 { return e.seq }, func(e *entry) time.Time { return e.at },
		func(*entry) bool { return true })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
