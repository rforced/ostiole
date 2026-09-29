package waflog

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/wafevent"
)

func event(id string, at time.Time) wafevent.Event {
	return wafevent.Event{Time: at, ID: id, Verdict: wafevent.VerdictMatched, Rules: []wafevent.Hit{}}
}

func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func TestRecentIsNewestFirstWithRisingSequence(t *testing.T) {
	t.Parallel()
	l := New()
	now := time.Now()
	for i := range 3 {
		l.Add(now, event(fmt.Sprint(i), now))
	}
	got := l.Recent(0)
	if fmt.Sprint(ids(got)) != "[2 1 0]" {
		t.Fatalf("recent = %v", ids(got))
	}
	if got[0].Seq != 3 || got[2].Seq != 1 {
		t.Errorf("sequence = %d..%d, want 3..1", got[0].Seq, got[2].Seq)
	}
	if got := l.Recent(2); fmt.Sprint(ids(got)) != "[2 1]" {
		t.Errorf("recent(2) = %v", ids(got))
	}
}

// A back-fill arrives newest first and goes in oldest first, so the ring
// reads the same as if it had followed the journal all along. What it
// brings was logged after anything a page has, so a subscriber hears of it
// in that order.
func TestFillPutsTheNewestLast(t *testing.T) {
	t.Parallel()
	l := New()
	ch, cancel := l.Subscribe(4)
	defer cancel()
	now := time.Now()
	entry := func(id string) Entry { return Entry{Logged: now, Event: event(id, now)} }
	l.fill([]Entry{entry("c"), entry("b"), entry("a")})
	l.Add(now, event("d", now))
	if got := ids(l.Recent(0)); fmt.Sprint(got) != "[d c b a]" {
		t.Fatalf("recent = %v", got)
	}
	var heard []string
	for range 4 {
		heard = append(heard, (<-ch).ID)
	}
	if fmt.Sprint(heard) != "[a b c d]" {
		t.Errorf("subscriber heard %v", heard)
	}
}

func TestConfigureKeepsTheNewestThatFit(t *testing.T) {
	t.Parallel()
	l := New()
	now := time.Now()
	for i := range 600 {
		l.Add(now, event(fmt.Sprint(i), now))
	}
	l.Configure(100, time.Hour)
	got := l.Recent(0)
	if len(got) != 100 || got[0].ID != "599" || got[99].ID != "500" {
		t.Fatalf("after shrinking: %d held, %s..%s", len(got), got[0].ID, got[len(got)-1].ID)
	}
	l.Add(now, event("600", now))
	if got := l.Recent(0); len(got) != 100 || got[0].ID != "600" || got[99].ID != "501" {
		t.Errorf("a full ring did not turn over: %d held, %s..%s", len(got), got[0].ID, got[len(got)-1].ID)
	}
	// Growing again leaves what is held alone and grows into the room.
	l.Configure(1000, time.Hour)
	l.Add(now, event("601", now))
	if n, _ := l.Held(); n != 101 {
		t.Errorf("held %d after growing, want 101", n)
	}
}

// An event ages from when it was logged: a WebSocket's comes when it
// closes, however long ago it opened.
func TestEventsAgeOutByDays(t *testing.T) {
	t.Parallel()
	l := New()
	l.Configure(100, 24*time.Hour)
	now := time.Now()
	l.Add(now.Add(-25*time.Hour), event("old", now.Add(-25*time.Hour)))
	l.Add(now.Add(-2*time.Hour), event("socket", now.Add(-30*time.Hour)))
	l.Add(now.Add(-time.Hour), event("new", now.Add(-time.Hour)))
	n, oldest := l.Held()
	if n != 2 || !oldest.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("held %d back to %v, want two back to when the socket was logged", n, oldest)
	}
	if got := ids(l.Recent(0)); fmt.Sprint(got) != "[new socket]" {
		t.Errorf("recent = %v", got)
	}
}

// What comes back from the files keeps its numbers and the order it was
// logged in, keeps what the log's size and days allow, and says what it
// let go of. Numbering goes on after the highest number in the files. Once
// an event has been added nothing more comes back.
func TestRestoreKeepsTheNumbers(t *testing.T) {
	t.Parallel()
	l := New()
	l.Configure(3, 24*time.Hour)
	now := time.Now()
	var entries []Entry
	for i, age := range []time.Duration{30 * time.Hour, 4 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour} {
		entries = append(entries, Entry{Seq: uint64(20 + i), Logged: now.Add(-age), Event: event(fmt.Sprint(i), now.Add(-age))})
	}
	if err := l.Restore(entries, 30); err != nil {
		t.Fatal(err)
	}
	got := l.Recent(0)
	if fmt.Sprint(ids(got)) != "[4 3 2]" || got[0].Seq != 24 || got[2].Seq != 22 {
		t.Fatalf("restored %v numbered %d..%d", ids(got), got[2].Seq, got[0].Seq)
	}
	if l.Newest() != 30 {
		t.Errorf("newest %d", l.Newest())
	}
	if l.Between(now.Add(-4*time.Hour), now, func(*Entry) {}) {
		t.Error("the count reaches back past what the restore let go of")
	}
	l.Add(now, event("5", now))
	if got := l.Recent(1); got[0].Seq != 31 {
		t.Errorf("next = %d", got[0].Seq)
	}
	if err := l.Restore(entries, 0); err == nil {
		t.Error("restored after an event was added")
	}
}

// The writer reads what came after the last entry it wrote, oldest first,
// a buffer at a time.
func TestAfterReadsOnFromASequence(t *testing.T) {
	t.Parallel()
	l := New()
	now := time.Now()
	for i := range 5 {
		l.Add(now, event(fmt.Sprint(i), now))
	}
	buf := make([]Entry, 2)
	n, more := l.After(1, buf)
	if n != 2 || !more || buf[0].ID != "1" || buf[1].ID != "2" {
		t.Fatalf("after 1: %d %v %v", n, more, ids(buf[:n]))
	}
	n, more = l.After(3, buf)
	if n != 2 || more || buf[1].ID != "4" {
		t.Errorf("after 3: %d %v %v", n, more, ids(buf[:n]))
	}
	if n, more := l.After(5, buf); n != 0 || more {
		t.Errorf("after the newest: %d %v", n, more)
	}
}

// A line in the files is the entry with its number, read back by when it
// was logged.
func TestALineKeepsWhenItWasLogged(t *testing.T) {
	t.Parallel()
	logged := time.Date(2026, 9, 28, 10, 0, 0, 123456000, time.UTC)
	e := Entry{Seq: 7, Logged: logged, Event: event("socket", logged.Add(-time.Hour))}
	line := appendLine(nil, &e)
	if !strings.Contains(string(line), `"seq":7,`) {
		t.Errorf("the line has no number: %s", line)
	}
	got, at, err := ParseLine(line)
	if err != nil || !at.Equal(logged) || !got.Time.Equal(logged.Add(-time.Hour)) || got.Seq != 7 || got.ID != "socket" {
		t.Errorf("read back %+v at %v (%v)", got, at, err)
	}
}

// The daily notice counts from the log. A count reaching back past an
// event the log let go of, for its ceiling or its age, is short.
func TestBetweenSaysWhenTheLogLetGo(t *testing.T) {
	t.Parallel()
	l := New()
	l.Configure(3, 0)
	start := time.Now().Add(-time.Hour)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	count := func(from, until int) (string, bool) {
		var got []string
		whole := l.Between(at(from), at(until), func(e *Entry) { got = append(got, e.ID) })
		return fmt.Sprint(got), whole
	}
	// Each counts from when it was logged, not from when it opened.
	for i := range 3 {
		l.Add(at(i), event(fmt.Sprint(i), at(i-30)))
	}
	if got, whole := count(1, 2); got != "[1]" || !whole {
		t.Errorf("from 1 until 2: %s, whole %v", got, whole)
	}
	// A fourth pushes the first out.
	l.Add(at(3), event("3", at(3)))
	if got, whole := count(0, 4); got != "[1 2 3]" || whole {
		t.Errorf("from 0: %s, whole %v", got, whole)
	}
	if _, whole := count(1, 4); !whole {
		t.Error("a count from after what the log let go of is short")
	}
	// So does age.
	l.Configure(3, 30*time.Minute+time.Minute)
	if got, whole := count(1, 4); got != "[]" || whole {
		t.Errorf("aged out: %s, whole %v", got, whole)
	}
}

// A Clear lets go of everything the log held: a count reaching back over
// it is short, and one from after it is whole.
func TestBetweenSaysWhenALogWasCleared(t *testing.T) {
	t.Parallel()
	l := New()
	now := time.Now()
	l.Add(now.Add(-2*time.Minute), event("1", now))
	l.Add(now.Add(-time.Minute), event("2", now))
	l.Clear()
	if l.Between(now.Add(-time.Hour), now, func(*Entry) {}) {
		t.Error("a count over what a Clear took is whole")
	}
	if !l.Between(now.Add(-30*time.Second), now, func(*Entry) {}) {
		t.Error("a count from after the Clear is short")
	}
}
