// Package journalfeed keeps a log in memory from what a unit writes to the
// journal: each line of its kind as the unit writes it, and at start what
// it wrote after the newest entry the log got back from its files, which
// is what it wrote while the daemon was down. The log is the store; the
// journal only feeds it.
package journalfeed

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/panics"
)

// DefaultWatch is how often the feed is put back in step with the
// configuration the router is running.
const DefaultWatch = 5 * time.Second

// The waits between tries when the journal cannot be read: short at first,
// since a restart of the journal is over in a moment, and long once it
// keeps failing.
const (
	firstBackoff = time.Second
	lastBackoff  = time.Minute
)

// Sink is the log a feed keeps.
type Sink[E any] interface {
	// Configure sets the most entries it holds and how long it keeps one.
	Configure(size int, keep time.Duration)
	// Size is the most entries it holds.
	Size() int
	// NewestAt is when the newest entry it holds was logged, zero while it
	// holds none.
	NewestAt() time.Time
	// AddAt stores an entry the unit logged at at.
	AddAt(at time.Time, e E)
	// FillAt stores what the journal held after NewestAt, given newest
	// first. As many as it holds may mean the journal had more.
	FillAt(items []Item[E])
	// Clear empties it.
	Clear()
}

// Item is an entry and when the unit logged it.
type Item[E any] struct {
	At time.Time
	E  E
}

// Feed keeps Log from a unit's journal while the configuration says so.
type Feed[E any] struct {
	Log     Sink[E]
	Journal Journal
	// Parse reads one line the unit wrote; false passes it over.
	Parse func(message string) (E, bool)
	// Source is the configuration the kernel is actually running.
	Source func() *model.Config
	// Settings sizes the log in a configuration.
	Settings func(*model.Config) (size int, keep time.Duration)
	// On says whether the unit is read in a configuration.
	On func(*model.Config) bool
	// Kept says whether the log is kept at all in a configuration; one that
	// is not is emptied. Nil is always.
	Kept func(*model.Config) bool
	// Installed reports whether the unit is on this router; nil is never,
	// which is what a daemon that is not root can say.
	Installed func(context.Context) bool
	// Name is what the daemon's own log calls what is read.
	Name string
	Slog *slog.Logger
	// Interval is how often to look; zero means DefaultWatch.
	Interval time.Duration
	// Backoff is the first wait after the reader fails; zero is a second.
	Backoff time.Duration

	reader reader
	// emptied says Log was cleared for a configuration that does not keep
	// it, and has taken nothing since.
	emptied bool
	// cursor is the last entry read that ended a line, of any kind, so a
	// reader started again carries on after it. Only the reader touches
	// it, and a new one starts once the old one has gone.
	cursor string
}

// Run follows the configuration until ctx is done. The first pass happens
// immediately, so the journal is read at boot rather than at the next
// apply.
func (f *Feed[E]) Run(ctx context.Context) {
	interval := f.Interval
	if interval <= 0 {
		interval = DefaultWatch
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	defer f.reader.halt()
	for {
		f.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (f *Feed[E]) tick(ctx context.Context) {
	cfg := f.Source()
	if cfg == nil {
		cfg = &model.Config{}
	}
	f.Log.Configure(f.Settings(cfg))
	on := f.On(cfg)
	switch {
	case on && !f.reader.running() && f.Installed != nil && f.Installed(ctx):
		f.emptied = false
		f.reader.start(ctx, f.follow)
	case !on:
		f.reader.halt()
	}
	if f.Kept != nil && !f.Kept(cfg) && !f.emptied {
		f.reader.halt()
		f.Log.Clear()
		f.cursor, f.emptied = "", true
	}
}

// reader is the goroutine reading the journal, while there is one.
type reader struct {
	stop context.CancelFunc
	done chan struct{}
}

func (r *reader) running() bool { return r.stop != nil }

// start runs read until halt stops it.
func (r *reader) start(ctx context.Context, read func(context.Context, chan struct{})) {
	rctx, cancel := context.WithCancel(ctx)
	r.stop, r.done = cancel, make(chan struct{})
	go read(rctx, r.done)
}

// halt stops the reader and waits for it to go.
func (r *reader) halt() {
	if r.stop == nil {
		return
	}
	r.stop()
	<-r.done
	r.stop, r.done = nil, nil
}

func (f *Feed[E]) log() *slog.Logger {
	if f.Slog != nil {
		return f.Slog
	}
	return slog.Default()
}

// follow reads the journal until ctx is done, starting again after a wait
// whenever journalctl fails or exits.
func (f *Feed[E]) follow(ctx context.Context, done chan struct{}) {
	defer close(done)
	first := f.Backoff
	if first <= 0 {
		first = firstBackoff
	}
	wait := first
	failed := ""
	for {
		started := time.Now()
		err := f.read(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("the journal reader stopped")
		}
		// A reader that ran a good while failed afresh, not again.
		if time.Since(started) > lastBackoff {
			wait, failed = first, ""
		}
		// Once, not every minute it goes on failing the same way.
		if msg := err.Error(); msg != failed {
			failed = msg
			f.log().Warn("stopped reading "+f.Name+" from the journal; trying again", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, lastBackoff)
	}
}

// read carries on after the last entry read when the journal still has it.
// Otherwise it reads what the journal holds after the newest entry the log
// has, which after a restart is what the unit logged while the daemon was
// down, before following; a log with none follows from now, so a cleared
// one stays cleared. What it parses came from the internet, so a panic
// fails the read rather than the daemon.
func (f *Feed[E]) read(ctx context.Context) (err error) {
	defer panics.Into(&err, f.log(), f.Name+" reader")
	if f.cursor != "" {
		// An error reads as gone: reading again from the log's newest
		// costs a moment, and following a cursor that is not there would
		// skip an entry.
		if held, err := f.Journal.Holds(ctx, f.cursor); err != nil || !held {
			f.cursor = ""
		}
	}
	var since time.Time
	if f.cursor == "" {
		// Following from the moment the back-fill began, when it found
		// nothing, is what leaves no gap between the two.
		since = time.Now()
		if after := f.Log.NewestAt(); !after.IsZero() {
			cursor, err := f.backfill(ctx, after)
			if err != nil {
				return err
			}
			f.cursor = cursor
		}
	}
	var lines lines
	return f.Journal.Follow(ctx, f.cursor, since, func(r Record) {
		line, whole := lines.add(r)
		if !whole {
			return
		}
		f.cursor = line.Cursor
		if e, ok := f.Parse(line.Message); ok {
			f.Log.AddAt(line.Time, e)
		}
	})
}

// backfill reads the journal newest first back to after, the newest entry
// the log holds, or until the log is full, and hands back the cursor of the
// newest entry that ends a line, empty when the journal had none. Following
// from a piece would read the rest of its line without the start.
func (f *Feed[E]) backfill(ctx context.Context, after time.Time) (string, error) {
	size := f.Log.Size()
	var cursor string
	var items []Item[E]
	var lines backLines
	read := func(line Record) {
		// The journal is read from the second after falls in, so what the
		// log has already comes round again.
		if !line.Time.After(after) {
			return
		}
		if e, ok := f.Parse(line.Message); ok {
			items = append(items, Item[E]{At: line.Time, E: e})
		}
	}
	err := f.Journal.Back(ctx, after, func(r Record) bool {
		if cursor == "" && !r.Cut {
			cursor = r.Cursor
		}
		if line, whole := lines.add(r); whole {
			read(line)
		}
		return len(items) < size
	})
	if err != nil {
		return "", err
	}
	// Cut short, it is read again in full next time.
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	for _, line := range lines.rest() {
		read(line)
	}
	// A cut line is known whole only once the entry before it is read, so
	// entries come out of order where lines were cut: put them back in
	// the order the follower would have added them.
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	f.Log.FillAt(items[:min(len(items), size)])
	return cursor, nil
}
