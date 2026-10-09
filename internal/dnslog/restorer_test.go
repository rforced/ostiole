package dnslog

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"testing"
	"time"

	"ostiole/internal/model"
)

func stored(seq uint64, when time.Time, name string, status Status, lists ...string) Stored {
	return Stored{Seq: seq, Time: when, Name: name, Type: 1, Status: status,
		Client: netip.MustParseAddr("192.0.2.10"), ListNames: lists}
}

// week is what memory keeps by default.
const week = 7 * 24 * time.Hour

func restorer(t *testing.T, q model.QueryLog, keep time.Duration, count int) (*Log, *Restorer) {
	t.Helper()
	l := New()
	l.Slog = slog.New(slog.DiscardHandler)
	r, err := l.Restorer(q, keep, count)
	if err != nil {
		t.Fatal(err)
	}
	return l, r
}

func done(t *testing.T, r *Restorer, newest uint64) int {
	t.Helper()
	n, err := r.Done(newest)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func rowNames(entries []Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// An answer whose number does not rise above the last is dropped, and one
// older than the log keeps still moves the numbering on.
func TestRestorerDropsWhatDoesNotRiseOrHasAgedOut(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l, r := restorer(t, model.QueryLog{Enabled: true}, 24*time.Hour, 6)
	r.Push(stored(2, now.Add(-time.Hour), "a.example.test", StatusOK))
	r.Push(stored(2, now.Add(-time.Hour), "again.example.test", StatusOK))
	r.Push(stored(1, now.Add(-time.Hour), "back.example.test", StatusOK))
	r.Push(stored(4, now.Add(-48*time.Hour), "old.example.test", StatusOK))
	r.Push(stored(3, now.Add(-time.Minute), "after-old.example.test", StatusOK))
	r.Push(stored(5, now.Add(-time.Minute), "b.example.test", StatusOK))
	if n := done(t, r, 0); n != 2 {
		t.Fatalf("kept %d, want 2", n)
	}
	got, _ := query(t, l, Filter{})
	if want := []string{"b.example.test", "a.example.test"}; !slices.Equal(rowNames(got), want) {
		t.Errorf("kept %v, want %v", rowNames(got), want)
	}
	if l.Newest() != 5 {
		t.Errorf("newest %d, want 5", l.Newest())
	}
}

// Past the size the oldest make way, and the totals and the counts per
// list are those of what is kept.
func TestRestorerKeepsTheNewestOfMoreThanItsSize(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l, r := restorer(t, model.QueryLog{Enabled: true, Entries: 3}, week, 3)
	for i := range 5 {
		r.Push(stored(uint64(i+1), now.Add(time.Duration(i-5)*time.Minute), fmt.Sprintf("n%d.example.test", i),
			StatusBlocked, "made-up-list-1"))
	}
	if n := done(t, r, 9); n != 3 || len(l.ring) != 3 {
		t.Fatalf("kept %d in %d places, want 3 in 3", n, len(l.ring))
	}
	got, _ := query(t, l, Filter{})
	if want := []string{"n4.example.test", "n3.example.test", "n2.example.test"}; !slices.Equal(rowNames(got), want) {
		t.Errorf("kept %v, want %v", rowNames(got), want)
	}
	total, blocked, oldest := l.Totals()
	counts, since := l.ListCounts()
	if total != 3 || blocked != 3 || !oldest.Equal(now.Add(-3*time.Minute)) || !since.Equal(oldest) ||
		counts["made-up-list-1"] != (ListCount{Blocked: 3, Alone: 3}) {
		t.Errorf("totals %d/%d from %v, counts %v since %v", total, blocked, oldest, counts, since)
	}
	if l.Newest() != 9 {
		t.Errorf("newest %d, want 9", l.Newest())
	}
	// Numbering goes on, and the ring takes new answers as it would have.
	at(l, now, "new.example.test", StatusOK, "192.0.2.11")
	got, _ = query(t, l, Filter{})
	if got[0].Seq != 10 || len(got) != 3 || got[2].Name != "n3.example.test" {
		t.Errorf("after an answer %+v", got)
	}
}

// A count that falls short grows the ring up to the size; none starts it
// at the first answer.
func TestRestorerGrowsPastAShortCount(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l, r := restorer(t, model.QueryLog{Enabled: true, Entries: 2500}, week, 2)
	for i := range 3000 {
		r.Push(stored(uint64(i+1), now.Add(time.Duration(i-3000)*time.Millisecond), fmt.Sprintf("n%d.example.test", i), StatusOK))
	}
	if n := done(t, r, 0); n != 2500 || len(l.ring) != 2500 {
		t.Fatalf("kept %d in %d places, want 2500 in 2500", n, len(l.ring))
	}
	got, _ := query(t, l, Filter{})
	for i, e := range got {
		if want := fmt.Sprintf("n%d.example.test", 2999-i); e.Name != want {
			t.Fatalf("row %d = %s, want %s", i, e.Name, want)
		}
	}

	_, r = restorer(t, model.QueryLog{Enabled: true, Entries: 5000}, week, 0)
	if r.ring != nil {
		t.Fatalf("no count gave %d places before an answer", len(r.ring))
	}
	r.Push(stored(1, now, "one.example.test", StatusOK))
	if len(r.ring) != initialRing {
		t.Errorf("the first answer gave %d places, want %d", len(r.ring), initialRing)
	}
}

// The lists come back by name: interned, attributed and counted.
func TestRestorerInternsAndCountsLists(t *testing.T) {
	t.Parallel()
	now := time.Now().Add(-time.Hour)
	l, r := restorer(t, model.QueryLog{Enabled: true}, week, 4)
	r.Push(stored(1, now, "a.example.test", StatusBlocked, "made-up-list-1"))
	r.Push(stored(2, now.Add(time.Second), "b.example.test", StatusBlocked, "made-up-list-1", "made-up-list-2"))
	r.Push(stored(3, now.Add(2*time.Second), "c.example.test", StatusOK))
	r.Push(stored(4, now.Add(3*time.Second), "d.example.test", StatusBlocked, "made-up-list-2"))
	if n := done(t, r, 0); n != 4 {
		t.Fatalf("kept %d, want 4", n)
	}
	if want := []string{"made-up-list-1", "made-up-list-2"}; !slices.Equal(l.Lists(), want) {
		t.Errorf("lists %v, want %v", l.Lists(), want)
	}
	got, _ := query(t, l, Filter{List: "made-up-list-2"})
	if want := []string{"d.example.test", "b.example.test"}; !slices.Equal(rowNames(got), want) ||
		!slices.Equal(l.ListNames(got[1].Lists), []string{"made-up-list-1", "made-up-list-2"}) {
		t.Errorf("made-up-list-2 blocked %v", got)
	}
	counts, since := l.ListCounts()
	total, blocked, _ := l.Totals()
	if counts["made-up-list-1"] != (ListCount{Blocked: 2, Alone: 1}) ||
		counts["made-up-list-2"] != (ListCount{Blocked: 2, Alone: 1}) ||
		!since.Equal(now) || total != 4 || blocked != 3 {
		t.Errorf("counts %v since %v, totals %d/%d", counts, since, total, blocked)
	}
}

// An answer added while the files are read numbers from one, so what was
// read is not put back; a clear meanwhile keeps nothing either, and the
// numbering still goes on after the files.
func TestRestorerRefusesOnceTheLogMovedOn(t *testing.T) {
	t.Parallel()
	now := time.Now()
	l, r := restorer(t, model.QueryLog{Enabled: true}, week, 1)
	r.Push(stored(7, now.Add(-time.Minute), "a.example.test", StatusOK))
	at(l, now, "new.example.test", StatusOK, "192.0.2.11")
	if n, err := r.Done(7); err == nil || n != 0 {
		t.Errorf("done after an answer: %d, %v", n, err)
	}
	if _, err := l.Restorer(model.QueryLog{Enabled: true}, week, 0); err == nil {
		t.Error("a restorer after an answer")
	}

	l, r = restorer(t, model.QueryLog{Enabled: true}, week, 1)
	r.Push(stored(7, now.Add(-time.Minute), "a.example.test", StatusOK, "made-up-list-1"))
	l.Clear()
	if n, err := r.Done(5); err == nil || n != 0 {
		t.Errorf("done after a clear: %d, %v", n, err)
	}
	if total, _, _ := l.Totals(); total != 0 || l.Newest() != 7 || !l.Enabled() {
		t.Errorf("after a clear: %d held, newest %d, on %v", total, l.Newest(), l.Enabled())
	}
}
