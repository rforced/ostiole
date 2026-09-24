package engine

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
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

func (c *offsetClock) move(offset int, next time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset, c.next = offset, next
}

// scheduled has a rule that holds only in working hours.
func scheduled(hostname string) *model.Config {
	c := cfg(hostname)
	c.Schedules = []model.Schedule{{Name: "work", Start: "08:00", End: "17:00"}}
	c.Rules[0].Schedule = "work"
	return c
}

func offsetEngine(t *testing.T) (*Engine, *fakeRunner, *offsetClock, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	fr := &fakeRunner{}
	clock := &offsetClock{offset: 7200}
	return New(st, fr, nil, slog.New(slog.DiscardHandler)).WithTimezone(clock), fr, clock, st
}

// nft turns a schedule's hours into UTC as it loads them, so when the
// offset moves the ruleset in force goes in again, once.
func TestRetimeLoadsTheRulesetAgainAtANewOffset(t *testing.T) {
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
	if again, err := e.Retime(ctx); err != nil || again {
		t.Fatalf("Retime with nothing moved = %v, %v", again, err)
	}

	clock.move(3600, time.Time{})
	loads := fr.count()
	if again, err := e.Retime(ctx); err != nil || !again {
		t.Fatalf("Retime after the offset moved = %v, %v", again, err)
	}
	if fr.count() != loads+1 || fr.last() != res.Ruleset {
		t.Error("the saved ruleset did not go in again")
	}
	if again, err := e.Retime(ctx); err != nil || again {
		t.Errorf("a second Retime = %v, %v; want nothing to do", again, err)
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
	if again, err := e.Retime(ctx); err != nil || again || fr.count() != loads {
		t.Errorf("Retime = %v, %v, with %d loads; want none", again, err, fr.count()-loads)
	}
}

// Inside a confirmation window the ruleset in force is the pending one.
func TestRetimeLoadsThePendingRuleset(t *testing.T) {
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
	if again, err := e.Retime(ctx); err != nil || !again || fr.last() != res.Ruleset {
		t.Errorf("Retime = %v, %v; want the pending ruleset in again", again, err)
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
	if again, err := e.Retime(ctx); err != nil || again || fr.count() != loads {
		t.Errorf("Retime = %v, %v; want the offset noted and nothing loaded", again, err)
	}
	if _, err := st.ReadState(store.OffsetFile); err != nil {
		t.Errorf("the offset was not noted: %v", err)
	}
}

// A new zone moves the offset the ruleset was just read at. It goes in again
// after the zone, and only when a rule keeps a schedule.
func TestAnApplyInANewZoneLoadsTheRulesetAgain(t *testing.T) {
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
	if fr.count() != loads+2 || fr.last() != res.Ruleset {
		t.Errorf("loads = %d, want the ruleset twice, the second time in the new zone", fr.count()-loads)
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
// ruleset, and loads that again in it.
func TestARevertAcrossAZoneLoadsThePreviousRulesetAgain(t *testing.T) {
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
	if fr.count() != loads+2 || fr.last() != saved.Ruleset {
		t.Errorf("loads = %d, want the saved ruleset twice", fr.count()-loads)
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
	if again, err := e.Retime(ctx); err != nil || again || fr.count() != loads {
		t.Fatalf("Retime during another apply = %v, %v", again, err)
	}
	if err := st.RemoveState(store.PendingFile); err != nil {
		t.Fatal(err)
	}
	if again, err := e.Retime(ctx); err != nil || !again {
		t.Errorf("Retime once it was done = %v, %v; want the ruleset in again", again, err)
	}
}

// Were the offset not to be noted, every look would load the table again,
// and every load takes the port mappings with it.
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
// seen at the start rather than an hour on.
func TestFollowOffsetLooksAtTheStart(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	clock.move(3600, time.Time{})
	loads := fr.count()
	after := make(chan struct{}, 1)
	go e.FollowOffset(ctx, func(context.Context) { after <- struct{}{} })
	select {
	case <-after:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was loaded at the start")
	}
	if fr.count() != loads+1 {
		t.Errorf("loads = %d, want one", fr.count()-loads)
	}
}

// FollowOffset wakes when the offset changes, loads the ruleset, and hands
// over for what the old table held outside it.
func TestFollowOffsetReloadsWhenTheOffsetChanges(t *testing.T) {
	t.Parallel()
	e, fr, clock, _ := offsetEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := e.Apply(ctx, scheduled("saved"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	loads := fr.count()
	change := time.Now().Add(50 * time.Millisecond)
	clock.move(7200, change)
	after := make(chan struct{}, 1)
	go e.FollowOffset(ctx, func(context.Context) { after <- struct{}{} })
	// The watcher sleeps until the change; the offset moves on time.
	time.Sleep(time.Until(change))
	clock.move(3600, time.Time{})
	select {
	case <-after:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing reloaded at the change")
	}
	if fr.count() != loads+1 {
		t.Errorf("loads = %d, want one more", fr.count()-loads)
	}
}
