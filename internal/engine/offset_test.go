package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
)

// offsetClock is the router's clock as the engine sees it: a zone to set,
// and an offset from UTC that daylight saving moves.
type offsetClock struct {
	mu     sync.Mutex
	offset int
	next   time.Time
	// zones is the offset each zone a test sets has.
	zones map[string]int
	// kernel is every offset the kernel was given, in order.
	kernel []int
}

func (c *offsetClock) Apply(_ context.Context, zone string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if offset, ok := c.zones[zone]; ok {
		c.offset = offset
	}
	return nil
}

func (c *offsetClock) Offset(time.Time) (int, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.offset, c.next
}

func (c *offsetClock) SetKernelOffset(seconds int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.kernel = append(c.kernel, seconds)
	return nil
}

func (c *offsetClock) move(offset int, next time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset, c.next = offset, next
}

// kernelOffset is the offset the kernel was last given, false for none.
func (c *offsetClock) kernelOffset() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.kernel) == 0 {
		return 0, false
	}
	return c.kernel[len(c.kernel)-1], true
}

// scheduled has a rule that holds only in working hours.
func scheduled(hostname string) *model.Config {
	c := cfg(hostname)
	c.Schedules = []model.Schedule{{Name: "work", Start: "08:00", End: "17:00"}}
	c.Rules[0].Schedule = "work"
	return c
}

// reloadOf is what the engine loads to read ruleset's schedules again.
func reloadOf(t *testing.T, ruleset string) string {
	t.Helper()
	script, ok := nft.ScheduleReload(ruleset)
	if !ok {
		t.Fatalf("no scheduled chain in:\n%s", ruleset)
	}
	return script
}

func offsetEngine(t *testing.T) (*Engine, *fakeRunner, *offsetClock, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	clock := &offsetClock{offset: 7200}
	return New(st, fr, nil, slog.New(slog.DiscardHandler)).WithTimezone(clock), fr, clock, st
}

// nft turns a schedule's hours into UTC as it loads them, so when the
// offset moves the chains that keep them go in again, once, and nothing
// else does.
func TestRetimeReadsTheSchedulesAgainAtANewOffset(t *testing.T) {
	t.Parallel()
	e, fr, clock, st := offsetEngine(t)
	ctx := context.Background()
	res, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Ruleset, "meta hour") {
		t.Fatalf("the schedule is not in the ruleset:\n%s", res.Ruleset)
	}
	var at loadedAt
	if raw, err := st.ReadState(store.OffsetFile); err != nil || json.Unmarshal(raw, &at) != nil || at.Offset != 7200 {
		t.Fatalf("offset noted at the apply: %+v, %v", at, err)
	}
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedNothing {
		t.Fatalf("Retime with nothing moved = %v, %v", loaded, err)
	}

	clock.move(3600, time.Time{})
	loads := fr.count()
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedSchedules {
		t.Fatalf("Retime after the offset moved = %v, %v", loaded, err)
	}
	if fr.count() != loads+1 || fr.last() != reloadOf(t, res.Ruleset) {
		t.Errorf("the scheduled chains did not go in again, alone:\n%s", fr.last())
	}
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedNothing {
		t.Errorf("a second Retime = %v, %v; want nothing to do", loaded, err)
	}
}

// A ruleset with no schedule reads no clock, so a new offset is only noted.
func TestRetimeLeavesARulesetWithoutSchedules(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("plain"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.move(3600, time.Time{})
	loads := fr.count()
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedNothing || fr.count() != loads {
		t.Errorf("Retime = %v, %v, with %d loads; want none", loaded, err, fr.count()-loads)
	}
}

// The kernel matches a schedule's days in its own offset, so it is given
// the clock's at each load and each look, whether the ruleset reloads or
// not.
func TestTheKernelFollowsTheOffset(t *testing.T) {
	t.Parallel()
	e, _, clock, _ := offsetEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("plain"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, ok := clock.kernelOffset(); !ok || got != 7200 {
		t.Errorf("kernel after the apply = %d, %v; want 7200", got, ok)
	}
	clock.move(-14400, time.Time{})
	if _, err := e.Retime(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := clock.kernelOffset(); got != -14400 {
		t.Errorf("kernel after the offset moved = %d, want -14400", got)
	}
}

// Inside a confirmation window the ruleset in force is the pending one.
func TestRetimeReadsThePendingSchedules(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, cfg("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, scheduled("pending"), ApplyOptions{ConfirmTimeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	e.pending.timer.Stop()
	clock.move(3600, time.Time{})
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedSchedules || fr.last() != reloadOf(t, res.Ruleset) {
		t.Errorf("Retime = %v, %v; want the pending schedules in again", loaded, err)
	}
}

// A ruleset loaded by an older release left no offset. It is taken to be
// the one the clock has, rather than every upgrade reloading the table.
func TestRetimeTakesAnUnrecordedLoadAsCurrent(t *testing.T) {
	t.Parallel()
	e, fr, _, st := offsetEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.Dir, store.OffsetFile)); err != nil {
		t.Fatal(err)
	}
	loads := fr.count()
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedNothing || fr.count() != loads {
		t.Errorf("Retime = %v, %v; want the offset noted and nothing loaded", loaded, err)
	}
	if _, err := st.ReadState(store.OffsetFile); err != nil {
		t.Errorf("the offset was not noted: %v", err)
	}
}

// A load that did not say which ruleset it put in leaves the chains to
// read in doubt, so the whole table goes in again, and FollowOffset hands
// over for what the old one held.
func TestFollowOffsetLoadsTheWholeTableForAnUnnamedLoad(t *testing.T) {
	t.Parallel()
	e, fr, clock, st := offsetEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteState(store.OffsetFile, []byte(`{"offset":7200}`)); err != nil {
		t.Fatal(err)
	}
	clock.move(3600, time.Time{})
	loads := fr.count()
	after := make(chan struct{}, 1)
	go e.FollowOffset(ctx, func(context.Context) { after <- struct{}{} })
	select {
	case <-after:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was handed over after the table went in")
	}
	if fr.count() != loads+1 || fr.last() != res.Ruleset {
		t.Errorf("loads = %d; want the whole ruleset once", fr.count()-loads)
	}
}

// A reload of the chains that fails falls back to the whole table.
func TestRetimeLoadsTheTableWhenTheChainsWillNotLoad(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx := context.Background()
	res, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fr.refuse = "flush chain"
	clock.move(3600, time.Time{})
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedTable || fr.last() != res.Ruleset {
		t.Errorf("Retime = %v, %v; want the whole ruleset in", loaded, err)
	}
}

// A new zone moves the offset the ruleset was just read at. Its schedules
// go in again after the zone, and only when a rule keeps one.
func TestAnApplyInANewZoneReadsTheSchedulesAgain(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	clock.zones = map[string]int{"UTC": 0, "Europe/Berlin": 7200}
	clock.move(0, time.Time{})
	ctx := context.Background()
	if _, err := e.Apply(ctx, scheduled("utc"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	loads := fr.count()
	berlin := scheduled("berlin")
	berlin.System.Timezone = "Europe/Berlin"
	res, err := e.Apply(ctx, berlin, ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fr.count() != loads+2 || fr.last() != reloadOf(t, res.Ruleset) {
		t.Errorf("loads = %d, want the ruleset and then its schedules in the new zone", fr.count()-loads)
	}
	if got, _ := clock.kernelOffset(); got != 7200 {
		t.Errorf("kernel = %d, want the new zone's 7200", got)
	}
	loads = fr.count()
	if _, err := e.Apply(ctx, cfg("plain"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if fr.count() != loads+1 {
		t.Errorf("a ruleset with no schedule loaded %d times", fr.count()-loads)
	}
}

// A revert across a change of zone puts the old zone back after the old
// ruleset, and reads its schedules again in it.
func TestARevertAcrossAZoneReadsThePreviousSchedulesAgain(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	clock.zones = map[string]int{"UTC": 0, "Europe/Berlin": 7200}
	clock.move(0, time.Time{})
	ctx := context.Background()
	saved, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	berlin := scheduled("berlin")
	berlin.System.Timezone = "Europe/Berlin"
	if _, err := e.Apply(ctx, berlin, ApplyOptions{ConfirmTimeout: time.Hour}); err != nil {
		t.Fatal(err)
	}
	loads := fr.count()
	if err := e.Revert(ctx); err != nil {
		t.Fatal(err)
	}
	if fr.count() != loads+2 || fr.last() != reloadOf(t, saved.Ruleset) {
		t.Errorf("loads = %d, want the saved ruleset and then its schedules", fr.count()-loads)
	}
}

// While another process is applying, what is in force is its to load; the
// offset is not noted, so the next look loads it once that is done.
func TestRetimeWaitsForAnotherProcessesApply(t *testing.T) {
	t.Parallel()
	e, fr, clock, st := offsetEngine(t)
	e.alive = func(int) bool { return true }
	ctx := context.Background()
	if _, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(record{ID: "x", PID: os.Getpid() + 1, Boot: bootID(), Config: "other"})
	if err := st.WriteState(store.PendingFile, raw); err != nil {
		t.Fatal(err)
	}
	clock.move(3600, time.Time{})
	loads := fr.count()
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedNothing || fr.count() != loads {
		t.Fatalf("Retime during another apply = %v, %v", loaded, err)
	}
	if err := st.RemoveState(store.PendingFile); err != nil {
		t.Fatal(err)
	}
	if loaded, err := e.Retime(ctx); err != nil || loaded != ReloadedSchedules {
		t.Errorf("Retime once it was done = %v, %v; want the schedules in again", loaded, err)
	}
}

// Were the offset not to be noted, every look would load the schedules
// again.
func TestRetimeLoadsOnceWhenTheOffsetCannotBeNoted(t *testing.T) {
	t.Parallel()
	e, fr, clock, st := offsetEngine(t)
	ctx := context.Background()
	if _, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(st.Dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.Dir, 0o700) })
	clock.move(3600, time.Time{})
	loads := fr.count()
	for range 3 {
		if _, err := e.Retime(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fr.count() != loads+1 {
		t.Errorf("loads = %d, want one", fr.count()-loads)
	}
}

// An offset that moved while nothing watched, across a restart say, is
// seen at the start rather than an hour on. Reading the schedules again
// leaves the rest of the table, so there is nothing to hand over.
func TestFollowOffsetLooksAtTheStart(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clock.move(3600, time.Time{})
	loads := fr.count()
	var handed atomic.Int32
	done := make(chan struct{})
	go func() {
		e.FollowOffset(ctx, func(context.Context) { handed.Add(1) })
		close(done)
	}()
	waitFor(t, func() bool { return fr.count() > loads })
	cancel()
	<-done
	if fr.count() != loads+1 || fr.last() != reloadOf(t, res.Ruleset) {
		t.Errorf("loads = %d, want the schedules once", fr.count()-loads)
	}
	if handed.Load() != 0 {
		t.Error("handed over after the schedules alone went in")
	}
}

// FollowOffset wakes when the offset changes and reads the schedules again.
func TestFollowOffsetReloadsWhenTheOffsetChanges(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	loads := fr.count()
	change := time.Now().Add(50 * time.Millisecond)
	clock.move(7200, change)
	go e.FollowOffset(ctx, nil)
	// The watcher sleeps until the change; the offset moves on time.
	time.Sleep(time.Until(change))
	clock.move(3600, time.Time{})
	waitFor(t, func() bool { return fr.count() > loads })
	if fr.count() != loads+1 || fr.last() != reloadOf(t, res.Ruleset) {
		t.Errorf("loads = %d, want the schedules once more", fr.count()-loads)
	}
	if got, _ := clock.kernelOffset(); got != 3600 {
		t.Errorf("kernel = %d, want 3600", got)
	}
}
