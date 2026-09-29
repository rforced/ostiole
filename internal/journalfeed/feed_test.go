package journalfeed

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// sink is a log in memory as a test keeps it.
type sink struct {
	mu      sync.Mutex
	entries []Item[string]
	cleared int
}

func (s *sink) Configure(int, time.Duration) {}
func (s *sink) Size() int                    { return 100 }

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
	for i := len(items) - 1; i >= 0; i-- {
		s.AddAt(items[i].At, items[i].E)
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

// A log the configuration no longer keeps is emptied once, and read from
// now when it is kept again; one that is only not read keeps what it has.
func TestALogNoLongerKeptIsEmptied(t *testing.T) {
	t.Parallel()
	var state atomic.Int32 // 0 read, 1 not read but kept, 2 not kept
	s := &sink{}
	j := &quiet{}
	f := &Feed[string]{
		Log: s, Journal: j, Name: "test lines", Slog: slog.New(slog.DiscardHandler),
		Parse: func(m string) (string, bool) {
			v, ok := strings.CutPrefix(m, "keep: ")
			return v, ok
		},
		Source:    func() *model.Config { return &model.Config{} },
		Settings:  func(*model.Config) (int, time.Duration) { return 100, time.Hour },
		On:        func(*model.Config) bool { return state.Load() == 0 },
		Kept:      func(*model.Config) bool { return state.Load() != 2 },
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
	wait := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("gave up waiting for %s", what)
			}
		}
	}
	wait("the line", func() bool { n, _ := s.held(); return n == 1 })
	state.Store(1)
	time.Sleep(20 * time.Millisecond)
	if n, cleared := s.held(); n != 1 || cleared != 0 {
		t.Fatalf("off but kept: %d held, cleared %d times", n, cleared)
	}
	state.Store(2)
	wait("the log to empty", func() bool { _, c := s.held(); return c == 1 })
	time.Sleep(20 * time.Millisecond)
	if _, cleared := s.held(); cleared != 1 {
		t.Errorf("cleared %d times", cleared)
	}
	state.Store(0)
	wait("a second reader", func() bool { return j.follows.Load() == 2 })
}
