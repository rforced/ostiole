package logfile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ostiole/internal/logsearch"
)

// words is what a page holds, newest first.
func words(entries []entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Word
	}
	return out
}

func seqOf(e *entry) uint64 { return e.Seq }

func all(*entry) bool { return true }

func (g *rig) page(t *testing.T, before uint64, limit int, budget logsearch.Budget, keep func(*entry) bool) logsearch.Page[entry] {
	t.Helper()
	p, err := Page(t.Context(), g.w, "test", 1, before, time.Time{}, limit, budget, parse, seqOf, keep)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A page is read newest first from the entry before, as memory is, across
// members, and says where the next one starts.
func TestPageReadsNewestFirstFromBefore(t *testing.T) {
	t.Parallel()
	g := written(t, 10, 4)
	p := g.page(t, 0, 3, logsearch.DefaultBudget, all)
	if fmt.Sprint(words(p.Entries)) != "[9 8 7]" || p.Next != 8 || !p.More {
		t.Fatalf("first page: %v next %d more %v", words(p.Entries), p.Next, p.More)
	}
	p = g.page(t, p.Next, 100, logsearch.DefaultBudget, all)
	if fmt.Sprint(words(p.Entries)) != "[6 5 4 3 2 1 0]" || p.More || p.Last != 1 || p.Walked != 7 {
		t.Errorf("rest: %v more %v last %d walked %d", words(p.Entries), p.More, p.Last, p.Walked)
	}
	// Reading on from a page that ended where a member did.
	p = g.page(t, 5, 4, logsearch.DefaultBudget, all)
	if fmt.Sprint(words(p.Entries)) != "[3 2 1 0]" || p.More {
		t.Errorf("from 5: %v more %v", words(p.Entries), p.More)
	}
}

// Days are read newest first, and a search keeps only what it matches.
func TestPageCarriesOnIntoOlderDays(t *testing.T) {
	t.Parallel()
	g := newRig(t, 1000)
	for i, at := range []time.Time{
		time.Date(2026, 9, 25, 23, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 26, 23, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC),
	} {
		g.r.add(at, fmt.Sprint(i))
		g.w.writeDue(true)
	}
	even := func(e *entry) bool { return e.Seq%2 == 0 }
	if p := g.page(t, 0, 10, logsearch.DefaultBudget, even); fmt.Sprint(words(p.Entries)) != "[3 1]" || p.More {
		t.Errorf("searched: %v more %v", words(p.Entries), p.More)
	}
	if p := g.page(t, 0, 10, logsearch.DefaultBudget, all); fmt.Sprint(words(p.Entries)) != "[3 2 1 0]" {
		t.Errorf("all: %v", words(p.Entries))
	}
}

// A search that finds nothing stops on its budget and says how far back it
// read, so the next read carries on from there.
func TestPageStopsOnItsBudget(t *testing.T) {
	t.Parallel()
	g := written(t, 10, 4)
	none := func(*entry) bool { return false }
	p := g.page(t, 0, 10, logsearch.Budget{Entries: 3, Time: time.Minute}, none)
	if len(p.Entries) != 0 || !p.More || p.Next != 8 || p.Walked != 3 ||
		!p.SearchedTo.Equal(g.clock.t.Add(7*time.Minute)) {
		t.Fatalf("stopped: next %d more %v walked %d searched to %v", p.Next, p.More, p.Walked, p.SearchedTo)
	}
	p = g.page(t, p.Next, 10, logsearch.Budget{Entries: 100, Time: time.Minute}, none)
	if p.More || !p.SearchedTo.IsZero() || p.Walked != 7 {
		t.Errorf("carried on: more %v walked %d searched to %v", p.More, p.Walked, p.SearchedTo)
	}
}

// What is older than the files keep, not yet pruned, is not read, nor a
// member in a format this build does not write.
func TestPageReadsOnlySinceAndItsOwnFormat(t *testing.T) {
	t.Parallel()
	g := written(t, 6, 3)
	since := g.clock.t.Add(2 * time.Minute)
	p, err := Page(t.Context(), g.w, "test", 1, 0, since, 10, logsearch.DefaultBudget, parse, seqOf, all)
	if err != nil || fmt.Sprint(words(p.Entries)) != "[5 4 3 2]" || p.More {
		t.Errorf("since: %v more %v (%v)", words(p.Entries), p.More, err)
	}
	p, err = Page(t.Context(), g.w, "test", 2, 0, time.Time{}, 10, logsearch.DefaultBudget, parse, seqOf, all)
	if err != nil || len(p.Entries) != 0 {
		t.Errorf("another format: %v (%v)", words(p.Entries), err)
	}
	if p, err := Page(t.Context(), g.w, "none", 1, 0, time.Time{}, 10, logsearch.DefaultBudget, parse, seqOf, all); err != nil || len(p.Entries) != 0 {
		t.Errorf("no files: %v (%v)", p.Entries, err)
	}
}

func TestPageStopsWhenAsked(t *testing.T) {
	t.Parallel()
	g := written(t, 6, 3)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Page(ctx, g.w, "test", 1, 0, time.Time{}, 10, logsearch.DefaultBudget, parse, seqOf, all); err == nil {
		t.Error("read on after the request went")
	}
}
