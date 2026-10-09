package journalfeed

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ostiole/internal/model"
)

// sink is a log in memory as a test keeps it.
type sink struct {
	mu         sync.Mutex
	entries    []Item[string]
	cleared    int
	configured int
}

func (s *sink) Configure(int, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured++
}

func (s *sink) Size() int { return 100 }

func (s *sink) NewestAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return time.Time{}
	}
	return s.entries[len(s.entries)-1].At
}

func (s *sink) AddAt(at time.Time, e string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, Item[string]{At: at, E: e})
}

func (s *sink) FillAt(items []Item[string]) {
	for _, item := range slices.Backward(items) {
		s.AddAt(item.At, item.E)
	}
}

func (s *sink) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.cleared++
}

func (s *sink) held() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries), s.cleared
}

// words is what the log holds, oldest first.
func (s *sink) words() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := make([]string, len(s.entries))
	for i, e := range s.entries {
		w[i] = e.E
	}
	return strings.Join(w, " ")
}

func (s *sink) ticks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configured
}

// restored is a log that came back from its files holding word, logged at.
func restored(word string, at time.Time) *sink {
	s := &sink{}
	s.AddAt(at, word)
	return s
}

// quiet is a journal with one line in it, whose follower waits to be
// stopped.
type quiet struct{ follows atomic.Int32 }

func (q *quiet) Back(context.Context, time.Time, func(Record) bool) error { return nil }
func (q *quiet) Holds(context.Context, string) (bool, error)              { return true, nil }
func (q *quiet) Follow(ctx context.Context, _ string, _ time.Time, fn func(Record)) error {
	if q.follows.Add(1) == 1 {
		fn(Record{Cursor: "c1", Time: time.Now(), Message: "keep: one"})
	}
	<-ctx.Done()
	return nil
}

// book is a journal of lines in the order they were written, whose
// follower is woken as more arrive.
type book struct {
	mu      sync.Mutex
	records []Record
	wake    chan struct{}
	backs   int
	follows int
	active  int
	// after is the cursor each follower started after.
	after []string
	// during runs inside the next back-fill, after its first record.
	during func()
}

func newBook() *book { return &book{wake: make(chan struct{}, 1)} }

func (b *book) write(msg string, at time.Time) {
	b.mu.Lock()
	b.records = append(b.records, Record{Cursor: fmt.Sprintf("c%d", len(b.records)+1), Time: at, Message: msg})
	b.mu.Unlock()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *book) Back(ctx context.Context, since time.Time, fn func(Record) bool) error {
	b.mu.Lock()
	held := slices.Clone(b.records)
	during := b.during
	b.during = nil
	b.backs++
	b.mu.Unlock()
	for i := len(held) - 1; i >= 0 && ctx.Err() == nil; i-- {
		if held[i].Time.Before(since) || !fn(held[i]) {
			return nil
		}
		if during != nil {
			during()
			during = nil
		}
	}
	return nil
}

func (b *book) Follow(ctx context.Context, cursor string, since time.Time, fn func(Record)) error {
	b.mu.Lock()
	b.follows++
	b.active++
	b.after = append(b.after, cursor)
	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()
	next := 0
	if cursor != "" {
		next = slices.IndexFunc(b.records, func(r Record) bool { return r.Cursor == cursor }) + 1
	} else {
		for from := time.Unix(since.Unix(), 0); next < len(b.records) && b.records[next].Time.Before(from); {
			next++
		}
	}
	b.mu.Unlock()
	for {
		b.mu.Lock()
		for next < len(b.records) {
			r := b.records[next]
			next++
			b.mu.Unlock()
			fn(r)
			b.mu.Lock()
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-b.wake:
		}
	}
}

func (b *book) Holds(_ context.Context, cursor string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.ContainsFunc(b.records, func(r Record) bool { return r.Cursor == cursor }), nil
}

func (b *book) counts() (backs, follows int, after []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.backs, b.follows, slices.Clone(b.after)
}

func (b *book) following() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active > 0
}

// tap feeds s the lines that start "keep: " while on says so and kept,
// when given, says it is kept.
func tap(s *sink, on, kept *atomic.Bool) *Tap[string] {
	t := &Tap[string]{
		Log: s, Name: "test lines",
		Parse: func(m string) (string, bool) {
			v, ok := strings.CutPrefix(m, "keep: ")
			return v, ok
		},
		Settings: func(*model.Config) (int, time.Duration) { return 100, time.Hour },
		On:       func(*model.Config) bool { return on == nil || on.Load() },
	}
	if kept != nil {
		t.Kept = func(*model.Config) bool { return kept.Load() }
	}
	return t
}

func yes() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}

// run runs a feed of taps over j until the test ends.
func run(t *testing.T, j Journal, taps ...AnyTap) {
	t.Helper()
	f := &Feed{
		Journal: j, Taps: taps, Slog: slog.New(slog.DiscardHandler),
		Source:    func() *model.Config { return &model.Config{} },
		Installed: func(context.Context) bool { return true },
		Interval:  time.Millisecond, Backoff: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func wait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
	}
}

// holds waits for s to hold want, oldest first.
func holds(t *testing.T, s *sink, want string) {
	t.Helper()
	wait(t, fmt.Sprintf("%q, holding %q", want, s.words()), func() bool { return s.words() == want })
}

// aTick waits for s's tap to go round twice, so a whole tick has seen
// what changed before.
func aTick(t *testing.T, s *sink) {
	t.Helper()
	n := s.ticks()
	wait(t, "a tick", func() bool { return s.ticks() >= n+2 })
}

// A log the configuration no longer keeps is emptied once, and read from
// now when it is kept again; one that is only not read keeps what it has.
func TestALogNoLongerKeptIsEmptied(t *testing.T) {
	t.Parallel()
	var state atomic.Int32 // 0 read, 1 not read but kept, 2 not kept
	s := &sink{}
	j := &quiet{}
	f := &Feed{
		Journal: j, Slog: slog.New(slog.DiscardHandler),
		Taps: []AnyTap{&Tap[string]{
			Log: s, Name: "test lines",
			Parse: func(m string) (string, bool) {
				v, ok := strings.CutPrefix(m, "keep: ")
				return v, ok
			},
			Settings: func(*model.Config) (int, time.Duration) { return 100, time.Hour },
			On:       func(*model.Config) bool { return state.Load() == 0 },
			Kept:     func(*model.Config) bool { return state.Load() != 2 },
		}},
		Source:    func() *model.Config { return &model.Config{} },
		Installed: func(context.Context) bool { return true },
		Interval:  time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	wait(t, "the line", func() bool { n, _ := s.held(); return n == 1 })
	state.Store(1)
	time.Sleep(20 * time.Millisecond)
	if n, cleared := s.held(); n != 1 || cleared != 0 {
		t.Fatalf("off but kept: %d held, cleared %d times", n, cleared)
	}
	state.Store(2)
	wait(t, "the log to empty", func() bool { _, c := s.held(); return c == 1 })
	time.Sleep(20 * time.Millisecond)
	if _, cleared := s.held(); cleared != 1 {
		t.Errorf("cleared %d times", cleared)
	}
	state.Store(0)
	wait(t, "a second reader", func() bool { return j.follows.Load() == 2 })
}

// Two logs of one unit are fed by one reader.
func TestTwoLogsShareOneReader(t *testing.T) {
	t.Parallel()
	j := newBook()
	x, y := &sink{}, &sink{}
	run(t, j, tap(x, nil, nil), tap(y, nil, nil))
	wait(t, "a follower", j.following)
	j.write("keep: one", time.Now())
	holds(t, x, "one")
	holds(t, y, "one")
	if backs, follows, _ := j.counts(); backs != 0 || follows != 1 {
		t.Errorf("%d back-fills and %d followers, want one follower", backs, follows)
	}
}

// Each log reads back what was logged after its own newest entry, the one
// furthest behind first, and the reader follows on from where that first
// back-fill ended. A line logged while the logs are read back reaches the
// first log through the follower, and the second, which read it back,
// passes over it there.
func TestEachLogReadsBackAndTheReaderFollowsFromTheOldest(t *testing.T) {
	t.Parallel()
	j := newBook()
	now := time.Now()
	j.write("keep: old", now.Add(-3*time.Hour))
	j.write("keep: mid", now.Add(-2*time.Hour))
	j.write("keep: late", now.Add(-30*time.Minute))
	j.during = func() { j.write("keep: during", time.Now()) }
	behind := restored("zero", now.Add(-150*time.Minute))
	ahead := restored("zero", now.Add(-time.Hour))
	run(t, j, tap(ahead, nil, nil), tap(behind, nil, nil))
	holds(t, behind, "zero mid late during")
	holds(t, ahead, "zero late during")
	j.write("keep: next", time.Now())
	holds(t, behind, "zero mid late during next")
	holds(t, ahead, "zero late during next")
	if backs, follows, after := j.counts(); backs != 2 || follows != 1 || after[0] != "c3" {
		t.Errorf("%d back-fills and %d followers after %v, want two, one, after c3", backs, follows, after)
	}
}

// A log that held nothing takes what is logged from when the reader
// started, while the other takes what it had not read back.
func TestALogPassesOverWhatWasLoggedBeforeItsStart(t *testing.T) {
	t.Parallel()
	j := newBook()
	now := time.Now()
	j.write("keep: late", now.Add(-30*time.Minute))
	j.during = func() { j.write("keep: earlier", now.Add(-10*time.Minute)) }
	held, empty := restored("zero", now.Add(-time.Hour)), &sink{}
	run(t, j, tap(empty, nil, nil), tap(held, nil, nil))
	holds(t, held, "zero late earlier")
	j.write("keep: next", time.Now())
	holds(t, held, "zero late earlier next")
	holds(t, empty, "next")
}

// A log no longer kept is emptied and takes nothing, while the reader goes
// on feeding the other. Kept again, it takes what is logged from then.
func TestALogNotKeptIsSkippedWhileTheOtherIsFed(t *testing.T) {
	t.Parallel()
	j := newBook()
	x, y := &sink{}, &sink{}
	kept := yes()
	run(t, j, tap(x, nil, nil), tap(y, nil, kept))
	wait(t, "a follower", j.following)
	j.write("keep: one", time.Now())
	holds(t, y, "one")
	kept.Store(false)
	wait(t, "the log to empty", func() bool { _, c := y.held(); return c == 1 })
	j.write("keep: two", time.Now())
	holds(t, x, "one two")
	kept.Store(true)
	aTick(t, y)
	j.write("keep: three", time.Now())
	holds(t, x, "one two three")
	holds(t, y, "three")
	if _, follows, _ := j.counts(); follows != 1 {
		t.Errorf("%d followers, want the one", follows)
	}
	if _, cleared := y.held(); cleared != 1 {
		t.Errorf("cleared %d times", cleared)
	}
}

// The reader stops only once neither log is fed.
func TestTheReaderStopsOnlyWhenNoLogIsFed(t *testing.T) {
	t.Parallel()
	j := newBook()
	x, y := &sink{}, &sink{}
	xOn, yOn := yes(), yes()
	run(t, j, tap(x, xOn, nil), tap(y, yOn, nil))
	wait(t, "a follower", j.following)
	xOn.Store(false)
	aTick(t, x)
	if !j.following() {
		t.Fatal("the reader stopped while a log was fed")
	}
	j.write("keep: one", time.Now())
	holds(t, y, "one")
	if n, _ := x.held(); n != 0 {
		t.Errorf("a log not fed took %d", n)
	}
	yOn.Store(false)
	wait(t, "the reader to stop", func() bool { return !j.following() })
	xOn.Store(true)
	wait(t, "a second follower", j.following)
	if _, follows, _ := j.counts(); follows != 2 {
		t.Errorf("%d followers, want two", follows)
	}
}
