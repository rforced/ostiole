package waflog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ostiole/internal/journalfeed"
	"ostiole/internal/model"
	"ostiole/internal/wafevent"
)

// fakeJournal is the proxy's journal: records in the order they were
// written, a follower woken as more arrive, and a switch that makes the
// follower fail the way a journalctl that exits does.
type fakeJournal struct {
	mu      sync.Mutex
	records []journalfeed.Record
	seq     int
	wake    chan struct{}
	failing bool
	backs   int
	follows int
	// active is how many followers are running now.
	active int
	// during runs inside the next back-fill, after its first record: an
	// entry written while the journal is being read back.
	during func()
}

func newFakeJournal() *fakeJournal { return &fakeJournal{wake: make(chan struct{}, 1)} }

func (j *fakeJournal) signal() {
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

func (j *fakeJournal) write(msg string, at time.Time) {
	j.writePiece("", msg, false, at)
}

// writePiece writes one entry of a stream, cut when journald cut the line
// it belongs to and more of it follows.
func (j *fakeJournal) writePiece(stream, msg string, cut bool, at time.Time) {
	j.mu.Lock()
	j.seq++
	j.records = append(j.records, journalfeed.Record{
		Cursor: fmt.Sprintf("c%d", j.seq), Time: at, Message: msg, Stream: stream, Cut: cut,
	})
	j.mu.Unlock()
	j.signal()
}

// writeCut writes msg as journald writes a line past its LineMax: in n
// pieces of one stream.
func (j *fakeJournal) writeCut(stream, msg string, n int, at time.Time) {
	size := (len(msg) + n - 1) / n
	for i := 0; i < len(msg); i += size {
		end := min(i+size, len(msg))
		j.writePiece(stream, msg[i:end], end < len(msg), at)
	}
}

// fail ends the follower that is running, as journalctl exiting would.
func (j *fakeJournal) fail() {
	j.mu.Lock()
	j.failing = true
	j.mu.Unlock()
	j.signal()
}

// vacuum drops the oldest n entries.
func (j *fakeJournal) vacuum(n int) {
	j.mu.Lock()
	j.records = slices.Clone(j.records[n:])
	j.mu.Unlock()
}

func (j *fakeJournal) counts() (backs, follows int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.backs, j.follows
}

func (j *fakeJournal) following() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.active > 0
}

func (j *fakeJournal) Back(ctx context.Context, since time.Time, fn func(journalfeed.Record) bool) error {
	j.mu.Lock()
	held := slices.Clone(j.records)
	during := j.during
	j.during = nil
	j.backs++
	j.mu.Unlock()
	for i := len(held) - 1; i >= 0 && ctx.Err() == nil; i-- {
		if held[i].Time.Before(since) {
			return nil
		}
		if !fn(held[i]) {
			return nil
		}
		if during != nil {
			during()
			during = nil
		}
	}
	return nil
}

func (j *fakeJournal) Follow(ctx context.Context, cursor string, since time.Time, fn func(journalfeed.Record)) error {
	j.mu.Lock()
	j.follows++
	j.active++
	j.failing = false
	defer func() {
		j.mu.Lock()
		j.active--
		j.mu.Unlock()
	}()
	next := 0
	if cursor != "" {
		i := slices.IndexFunc(j.records, func(r journalfeed.Record) bool { return r.Cursor == cursor })
		if i < 0 {
			j.mu.Unlock()
			return fmt.Errorf("cursor %s is gone, and a real journal would skip an entry", cursor)
		}
		next = i + 1
	} else {
		from := time.Unix(since.Unix(), 0)
		for next < len(j.records) && j.records[next].Time.Before(from) {
			next++
		}
	}
	j.mu.Unlock()
	for {
		j.mu.Lock()
		for next < len(j.records) && !j.failing {
			r := j.records[next]
			next++
			j.mu.Unlock()
			fn(r)
			j.mu.Lock()
		}
		failing := j.failing
		j.mu.Unlock()
		if failing {
			return errors.New("journalctl exited")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-j.wake:
		}
	}
}

func (j *fakeJournal) Holds(_ context.Context, cursor string) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.ContainsFunc(j.records, func(r journalfeed.Record) bool { return r.Cursor == cursor }), nil
}

// audit is an event line as the proxy writes it into its journal.
func audit(id string, at time.Time) string {
	line, err := wafevent.Line(wafevent.Event{
		Time: at, ID: id, Site: "shop", Client: "192.0.2.7", Method: "GET", URI: "/login",
		Verdict: wafevent.VerdictBlocked, Engine: "On",
		Rules: []wafevent.Hit{{
			ID: 941100, Message: "XSS Attack Detected via libinjection",
			Data: "Matched Data: <script>", Severity: "critical",
		}},
	})
	if err != nil {
		panic(err)
	}
	return string(line)
}

// access is what the proxy writes for every request at the info level.
const access = `{"level":"info","logger":"http.log.access","msg":"handled request"}`

func proxyOn() *model.Config {
	return &model.Config{Services: model.Services{Proxy: model.Proxy{
		Enabled: true,
		Sites:   []model.ProxySite{{ID: "shop", Enabled: true}},
	}}}
}

// watch runs a watcher over j until the test ends, with the configuration
// cfg holds, feeding an empty log: one that had no files to come back from.
func watch(t *testing.T, j journalfeed.Journal, cfg *atomic.Pointer[model.Config], installed bool) *Log {
	t.Helper()
	return watchLog(t, New(), j, cfg, installed)
}

// restored is a log that came back from its files holding one event per
// id, in the order given, logged at when.
func restored(t *testing.T, when time.Time, ids ...string) *Log {
	t.Helper()
	entries := make([]Entry, len(ids))
	for i, id := range ids {
		entries[i] = Entry{Seq: uint64(i + 1), Logged: when, Event: event(id, when)}
	}
	l := New()
	if err := l.Restore(entries, 0); err != nil {
		t.Fatal(err)
	}
	return l
}

// watchLog runs a watcher feeding l, set as each of set says.
func watchLog(t *testing.T, l *Log, j journalfeed.Journal, cfg *atomic.Pointer[model.Config], installed bool) *Log {
	t.Helper()
	w := &journalfeed.Feed{
		Journal: j, Source: cfg.Load, Slog: slog.New(slog.DiscardHandler),
		Taps: []journalfeed.AnyTap{&journalfeed.Tap[wafevent.Event]{
			Log: l, Parse: wafevent.Parse, Name: "the WAF events",
			Settings: func(c *model.Config) (int, time.Duration) {
				return c.Services.Proxy.Events.Size(), c.System.Logging.MemoryKeep()
			},
			On: func(c *model.Config) bool { return c.ProxyEnabled() },
		}},
		Installed: func(context.Context) bool { return installed },
		Interval:  5 * time.Millisecond, Backoff: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return l
}

func on(cfg *model.Config) *atomic.Pointer[model.Config] {
	var p atomic.Pointer[model.Config]
	p.Store(cfg)
	return &p
}

// waitFor waits for ok to hold.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// eventually waits for the log to hold want, newest first.
func eventually(t *testing.T, l *Log, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := fmt.Sprint(ids(l.Recent(0)))
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("log holds %s, want %s", got, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A log that came back from its files takes what the proxy logged after
// its newest event, while the daemon was down. An entry written while the
// journal is being read back lands once: the follower starts after the
// newest entry the back-fill saw, not at the moment it finished.
func TestBackFillThenFollowLeavesNoGapAndNoDuplicate(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	now := time.Now()
	j.write(audit("a", now.Add(-3*time.Hour)), now.Add(-3*time.Hour))
	j.write(access, now.Add(-2*time.Hour))
	j.write(audit("b", now.Add(-time.Hour)), now.Add(-time.Hour))
	j.write(access, now.Add(-time.Minute))
	j.during = func() { j.write(audit("during", time.Now()), time.Now()) }

	l := watchLog(t, restored(t, now.Add(-3*time.Hour), "a"), j, on(proxyOn()), true)
	eventually(t, l, "[during b a]")
	j.write(access, time.Now())
	j.write(audit("after", time.Now()), time.Now())
	eventually(t, l, "[after during b a]")
	if backs, follows := j.counts(); backs != 1 || follows != 1 {
		t.Errorf("%d back-fills and %d followers, want one of each", backs, follows)
	}
}

// An empty log, as the files being off or a Clear leave one after a
// restart, reads nothing back from the journal: it follows from now.
func TestAnEmptyLogFollowsFromNow(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.write(audit("before", time.Now().Add(-time.Hour)), time.Now().Add(-time.Hour))
	l := watch(t, j, on(proxyOn()), true)
	waitFor(t, "a follower", j.following)
	j.write(audit("first", time.Now()), time.Now())
	eventually(t, l, "[first]")
	if backs, _ := j.counts(); backs != 0 {
		t.Errorf("%d back-fills of an empty log", backs)
	}
}

// With nothing in the journal after the log's newest event there is no
// cursor to follow from, so the follower starts where the back-fill did.
func TestAnEmptyJournalIsFollowedFromWhenTheBackFillBegan(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	l := watchLog(t, restored(t, time.Now().Add(-time.Hour), "old"), j, on(proxyOn()), true)
	waitFor(t, "a follower", j.following)
	j.write(audit("first", time.Now()), time.Now())
	eventually(t, l, "[first old]")
}

// The back-fill stops once the log is full, rather than reading a week of
// a busy proxy's journal it has no room for.
func TestBackFillStopsWhenTheLogIsFull(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	now := time.Now()
	for i := range 5 {
		at := now.Add(time.Duration(i-5) * time.Minute)
		j.write(audit(fmt.Sprint(i), at), at)
	}
	cfg := proxyOn()
	cfg.Services.Proxy.Events.Entries = 3
	l := watchLog(t, restored(t, now.Add(-time.Hour), "old"), j, on(cfg), true)
	eventually(t, l, "[4 3 2]")
}

// What the log holds is not read again, nor anything logged before it.
func TestBackFillReadsOnlyAfterTheNewestEvent(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	now := time.Now()
	j.write(audit("older", now.Add(-3*time.Hour)), now.Add(-3*time.Hour))
	j.write(audit("a", now.Add(-2*time.Hour)), now.Add(-2*time.Hour))
	j.write(audit("b", now.Add(-time.Hour)), now.Add(-time.Hour))
	l := watchLog(t, restored(t, now.Add(-2*time.Hour), "a"), j, on(proxyOn()), true)
	eventually(t, l, "[b a]")
}

// The proxy logs a request when it ends, so a WebSocket's event comes long
// after the socket opened. It takes the place it was logged at, whether
// the log followed the journal or came back from its files, so a restart
// leaves the order alone.
func TestAnEventTakesThePlaceItWasLogged(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	followed := watch(t, j, on(proxyOn()), true)
	waitFor(t, "a follower", j.following)
	now := time.Now()
	opened := now.Add(-time.Hour)
	j.write(audit("a", now), now)
	j.write(audit("socket", opened), now.Add(time.Second))
	j.write(audit("b", now.Add(2*time.Second)), now.Add(2*time.Second))
	eventually(t, followed, "[b socket a]")
	// Through the files: each entry's line, read back in the order written.
	var lines []Entry
	recent := followed.Recent(0)
	for i := len(recent) - 1; i >= 0; i-- {
		e, _, err := ParseLine(appendLine(nil, &recent[i]))
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, e)
	}
	readBack := New()
	if err := readBack.Restore(lines, 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, readBack, "[b socket a]")
	for _, l := range []*Log{followed, readBack} {
		if socket := l.Recent(0)[1]; !socket.Logged.Equal(now.Add(time.Second)) || !socket.Time.Equal(opened) {
			t.Errorf("the socket was logged %v and opened %v", socket.Logged, socket.Time)
		}
	}
}

// journalctl exiting costs nothing: the next one carries on after the
// last entry read.
func TestAFollowerThatStopsCarriesOnWhereItWas(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	l := watch(t, j, on(proxyOn()), true)
	waitFor(t, "a follower", j.following)
	j.write(audit("a", time.Now()), time.Now())
	eventually(t, l, "[a]")
	j.write(audit("b", time.Now()), time.Now())
	eventually(t, l, "[b a]")
	j.fail()
	j.write(audit("c", time.Now()), time.Now())
	eventually(t, l, "[c b a]")
	backs, follows := j.counts()
	if backs != 0 || follows < 2 {
		t.Errorf("%d back-fills and %d followers, want none and a second follower", backs, follows)
	}
}

// A cursor into files the journal has since vacuumed cannot be followed
// from, so the reader carries on from the newest event the log holds, and
// the log keeps what it had.
func TestAVacuumedCursorCarriesOnFromTheNewestEvent(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	cfg := on(proxyOn())
	l := watch(t, j, cfg, true)
	waitFor(t, "a follower", j.following)
	j.write(audit("a", time.Now()), time.Now())
	j.write(audit("b", time.Now()), time.Now())
	eventually(t, l, "[b a]")

	// The proxy goes off, the journal is vacuumed while it is, and the
	// proxy comes back. The fake's follower only returns when it is
	// stopped, so its going is the watcher having seen the proxy off.
	cfg.Store(&model.Config{})
	waitFor(t, "the follower to stop", func() bool { return !j.following() })
	j.write(audit("c", time.Now()), time.Now())
	j.vacuum(3)
	j.write(audit("d", time.Now()), time.Now())
	cfg.Store(proxyOn())
	eventually(t, l, "[d b a]")
	if backs, _ := j.counts(); backs != 1 {
		t.Errorf("%d back-fills, want one after the vacuum", backs)
	}
}

// journald cuts a line past its LineMax into pieces. The event is read
// whole, by the back-fill and by the follower, with another stream's
// entries between its pieces, and was logged when its last piece was.
func TestACutLineIsReadWhole(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	at := time.Now().Add(-time.Hour)
	old := audit("old", at)
	j.writePiece("stderr", old[:10], true, at)
	j.writePiece("stdout", access, false, at)
	j.writePiece("stderr", old[10:40], true, at)
	j.writePiece("stdout", access, false, at)
	j.writePiece("stderr", old[40:], false, at.Add(time.Second))
	l := watchLog(t, restored(t, at.Add(-time.Hour), "first"), j, on(proxyOn()), true)
	eventually(t, l, "[old first]")
	waitFor(t, "a follower", j.following)
	now := time.Now()
	line := audit("new", now)
	j.writePiece("stderr", line[:25], true, now)
	j.writePiece("stdout", access, false, now)
	j.writePiece("stderr", line[25:], false, now.Add(time.Second))
	eventually(t, l, "[new old first]")
	for _, e := range l.Recent(0)[:2] {
		if want := e.Time.Add(time.Second); !e.Logged.Equal(want) {
			t.Errorf("%s was logged %v, want %v", e.ID, e.Logged, want)
		}
	}
}

// A reader stopped part way through a line reads all of it again: the
// back-fill does not follow on from a piece, and a follower's place stays
// before a line until its last piece.
func TestAReaderStoppedMidLineReadsItAgain(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	now := time.Now()
	j.write(audit("a", now), now)
	b := audit("b", now)
	j.writePiece("stderr", b[:20], true, now)
	l := watchLog(t, restored(t, now.Add(-time.Hour), "zero"), j, on(proxyOn()), true)
	eventually(t, l, "[a zero]")
	waitFor(t, "a follower", j.following)
	j.writePiece("stderr", b[20:40], true, now)
	j.fail()
	j.writePiece("stderr", b[40:], false, now)
	eventually(t, l, "[b a zero]")
	if _, follows := j.counts(); follows < 2 {
		t.Errorf("%d followers, want a second", follows)
	}
	j.writeCut("stderr", audit("c", now), 4, now)
	eventually(t, l, "[c b a zero]")
}

// Nothing is read while the proxy is off or not on this router.
func TestTheReaderIdlesWhileTheProxyCannotLog(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		cfg       *model.Config
		installed bool
	}{
		"off":           {&model.Config{}, true},
		"not installed": {proxyOn(), false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			j := newFakeJournal()
			j.write(audit("a", time.Now()), time.Now())
			l := watch(t, j, on(tc.cfg), tc.installed)
			time.Sleep(50 * time.Millisecond)
			if backs, follows := j.counts(); backs != 0 || follows != 0 {
				t.Errorf("%d back-fills and %d followers while the proxy is %s", backs, follows, name)
			}
			if n, _ := l.Held(); n != 0 {
				t.Errorf("held %d", n)
			}
		})
	}
}

// The ring is sized from the configuration whatever the proxy is doing.
func TestTheLogFollowsTheConfiguredSize(t *testing.T) {
	t.Parallel()
	cfg := proxyOn()
	cfg.Services.Proxy.Events = model.ProxyEvents{Entries: 42}
	cfg.System.Logging.Days = 3
	l := watch(t, newFakeJournal(), on(cfg), false)
	waitFor(t, "42 events for 3 days", func() bool {
		size, keep := l.limits()
		return size == 42 && keep == 72*time.Hour
	})
}

// panicky is a journal whose first back-fill panics, as a parser meeting
// something it did not expect would.
type panicky struct {
	*fakeJournal
	once sync.Once
}

func (p *panicky) Back(ctx context.Context, since time.Time, fn func(journalfeed.Record) bool) error {
	p.once.Do(func() { panic("unexpected input") })
	return p.fakeJournal.Back(ctx, since, fn)
}

func TestAPanicFailsTheReadNotTheDaemon(t *testing.T) {
	t.Parallel()
	j := newFakeJournal()
	j.write(audit("a", time.Now()), time.Now())
	l := watchLog(t, restored(t, time.Now().Add(-time.Hour), "zero"), &panicky{fakeJournal: j}, on(proxyOn()), true)
	eventually(t, l, "[a zero]")
}
