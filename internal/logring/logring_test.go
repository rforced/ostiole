package logring

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ostiole/internal/journalfeed"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
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

// A restorer keeps the entries whose numbers rise, no more than the ring's
// size however many the files counted, and numbering goes on after the
// last of them when that is higher than the files' newest.
func TestARestorerFillsTheRingAsTheFilesStream(t *testing.T) {
	t.Parallel()
	now := time.Now()
	back := func(seq uint64, s string) word { return word{Stamp{Seq: seq, Time: now}, s} }
	r := newRing(3, 0)
	s, err := r.Restorer(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []word{back(2, "a"), back(1, "early"), back(2, "again"), back(0, "none"), back(3, "b"), back(5, "c"), back(6, "d")} {
		s.Push(e)
	}
	if n, err := s.Done(4); err != nil || n != 3 {
		t.Fatalf("kept %d (%v)", n, err)
	}
	if got := words(r.Recent(0)); got != "[6d 5c 3b]" || r.Newest() != 6 {
		t.Errorf("restored %s, newest %d", got, r.Newest())
	}
	r.Add(w("new", now))
	if r.Newest() != 7 {
		t.Errorf("numbered %d after the restore", r.Newest())
	}

	aged := newRing(5, 24*time.Hour)
	s, _ = aged.Restorer(3)
	s.Push(word{Stamp{Seq: 1, Time: now.Add(-25 * time.Hour)}, "old"})
	s.Push(back(2, "a"))
	s.Push(back(3, "b"))
	if n, _ := s.Done(0); n != 2 || words(aged.Recent(0)) != "[3b 2a]" {
		t.Errorf("kept %d past its days: %s", n, words(aged.Recent(0)))
	}

	shrunk := newRing(5, 0)
	s, _ = shrunk.Restorer(5)
	for _, e := range []word{back(1, "a"), back(2, "b"), back(3, "c")} {
		s.Push(e)
	}
	shrunk.Configure(2, 0)
	if n, _ := s.Done(0); n != 2 || words(shrunk.Recent(0)) != "[3c 2b]" {
		t.Errorf("kept %d of a ring made smaller: %s", n, words(shrunk.Recent(0)))
	}
	shrunk.Add(w("d", now))
	if got := words(shrunk.Recent(0)); got != "[4d 3c]" {
		t.Errorf("the smaller ring holds %s", got)
	}
}

// A count short of what the files hold, or none at all, grows the
// restorer's places up to the ring's size, then the oldest go.
func TestARestorerGrowsPastAShortCount(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, count := range []int{0, 2} {
		r := newRing(1000, 0)
		s, err := r.Restorer(count)
		if err != nil {
			t.Fatal(err)
		}
		for seq := range uint64(1200) {
			s.Push(word{Stamp{Seq: seq + 1, Time: now}, "x"})
		}
		n, err := s.Done(0)
		got := r.Recent(0)
		if err != nil || n != 1000 || len(got) != 1000 || got[0].Seq != 1200 || got[999].Seq != 201 {
			t.Errorf("count %d: kept %d, %d from %d to %d (%v)", count, n, len(got), got[len(got)-1].Seq, got[0].Seq, err)
		}
	}
}

// Once an entry has been added, a restorer neither starts nor finishes:
// the numbers would go backwards.
func TestARestorerRefusesOnceAnEntryCame(t *testing.T) {
	t.Parallel()
	now := time.Now()
	r := newRing(5, 0)
	s, err := r.Restorer(1)
	if err != nil {
		t.Fatal(err)
	}
	s.Push(word{Stamp{Seq: 8, Time: now}, "old"})
	r.Add(w("live", now))
	if _, err := s.Done(9); err == nil {
		t.Error("finished after an entry came")
	}
	if _, err := r.Restorer(0); err == nil {
		t.Error("started after an entry came")
	}
	if got := words(r.Recent(0)); got != "[1live]" || r.Newest() != 1 {
		t.Errorf("holds %s, newest %d", got, r.Newest())
	}
}
