package logring

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/journalfeed"
	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/model"
)

// word is an entry as a test logs it.
type word struct {
	Stamp
	Word string `json:"word"`
}

func w(s string, at time.Time) word { return word{Stamp: Stamp{Time: at}, Word: s} }

func words(entries []word) string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = fmt.Sprintf("%d%s", e.Seq, e.Word)
	}
	return fmt.Sprint(out)
}

func newRing(size int, keep time.Duration) *Ring[word, *word] { return New[word, *word](size, keep) }

// Entries are numbered as they come and read newest first; a full ring
// turns over; the oldest goes past its days.
func TestTheRingNumbersKeepsAndAges(t *testing.T) {
	t.Parallel()
	r := newRing(3, 24*time.Hour)
	now := time.Now()
	r.Add(w("old", now.Add(-25*time.Hour)))
	for _, s := range []string{"a", "b", "c", "d"} {
		r.Add(w(s, now))
	}
	if got := words(r.Recent(0)); got != "[5d 4c 3b]" {
		t.Errorf("recent %s", got)
	}
	if n, oldest := r.Held(); n != 3 || !oldest.Equal(now) || r.Newest() != 5 {
		t.Errorf("held %d from %v, newest %d", n, oldest, r.Newest())
	}
	r.Add(word{Word: "now"})
	if e := r.Recent(1)[0]; e.Time.IsZero() || e.Seq != 6 {
		t.Errorf("an entry with no time is logged now: %+v", e)
	}
	r.Configure(2, 0)
	if got := words(r.Recent(0)); got != "[6now 5d]" {
		t.Errorf("shrunk %s", got)
	}
}

// A page walks back from a number; the writer reads on from one.
func TestBeforeAndAfterReadAroundANumber(t *testing.T) {
	t.Parallel()
	r := newRing(10, 0)
	for i := range 5 {
		r.Add(w(fmt.Sprint(i), time.Now()))
	}
	buf := make([]word, 2)
	n, more := r.Before(4, buf)
	if n != 2 || !more || words(buf[:n]) != "[32 21]" {
		t.Errorf("before 4: %s %v", words(buf[:n]), more)
	}
	n, more = r.After(3, buf)
	if n != 2 || more || words(buf[:n]) != "[43 54]" {
		t.Errorf("after 3: %s %v", words(buf[:n]), more)
	}
	p, err := r.Query(t.Context(), 0, 2, func(e *word) bool { return e.Word != "3" })
	if err != nil || words(p.Entries) != "[54 32]" || !p.More {
		t.Errorf("query %s %v (%v)", words(p.Entries), p.More, err)
	}
}

// What comes back from the files keeps its numbers, and numbering goes on
// after the highest number in them; once an entry has come, nothing more
// comes back. Clearing keeps the numbers going.
func TestRestoreKeepsTheNumbers(t *testing.T) {
	t.Parallel()
	r := newRing(2, 0)
	now := time.Now()
	back := []word{{Stamp{Seq: 7, Time: now}, "a"}, {Stamp{Seq: 7, Time: now}, "again"}, {Stamp{Seq: 8, Time: now}, "b"}, {Stamp{Seq: 9, Time: now}, "c"}}
	if err := r.Restore(back, 12); err != nil {
		t.Fatal(err)
	}
	if got := words(r.Recent(0)); got != "[9c 8b]" {
		t.Errorf("restored %s", got)
	}
	r.Add(w("new", now))
	if r.Newest() != 13 || r.Restore(back, 0) == nil {
		t.Errorf("newest %d, or restored again", r.Newest())
	}
	r.Clear()
	r.Add(w("after", now))
	if got := words(r.Recent(0)); got != "[14after]" {
		t.Errorf("after clearing %s", got)
	}
}

// What the journal held comes newest first and goes in oldest first, each
// logged when the journal says, and subscribers hear of it in that order.
func TestTheJournalFillsTheRing(t *testing.T) {
	t.Parallel()
	r := newRing(10, 0)
	ch, cancel := r.Subscribe(4)
	defer cancel()
	at := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	r.FillAt([]journalfeed.Item[word]{{At: at.Add(time.Minute), E: word{Word: "b"}}, {At: at, E: word{Word: "a"}}})
	r.AddAt(at.Add(2*time.Minute), word{Word: "c"})
	if got := words(r.Recent(0)); got != "[3c 2b 1a]" || !r.NewestAt().Equal(at.Add(2*time.Minute)) {
		t.Errorf("filled %s, newest %v", got, r.NewestAt())
	}
	var heard []string
	for range 3 {
		heard = append(heard, (<-ch).Word)
	}
	if fmt.Sprint(heard) != "[a b c]" {
		t.Errorf("heard %v", heard)
	}
}

// Written by the writer and read back, an entry is what it was.
func TestEntriesGoThroughTheFiles(t *testing.T) {
	t.Parallel()
	r := newRing(10, 0)
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	files := &logfile.Writer{Dir: t.TempDir(), Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	files.Add(r.Files("words", 1, func(*model.Config) bool { return true }, func(*model.Config) int { return 7 }), logfile.ReadStats{})
	at := time.Now().UTC().Truncate(time.Second)
	r.Add(w("a", at))
	r.Add(w("b", at))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	files.Run(ctx)
	got, _, err := logfile.Read(files.Dir, "words", 1, 10, time.Time{}, Parse[word, *word])
	if err != nil || words(got) != "[1a 2b]" || !got[0].Time.Equal(at) {
		t.Fatalf("read %s (%v)", words(got), err)
	}
	if _, _, err := Parse[word, *word]([]byte("not json")); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("parsed a bad line: %v", err)
	}
}
