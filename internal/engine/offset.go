package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/timezone"
)

// nft converts a schedule's hours to UTC as it reads a ruleset, at the
// offset the router's clock has then. A ruleset loaded before a change of
// offset, which daylight saving is twice a year, keeps its schedules that
// far off until its scheduled chains are read again. The kernel matches a
// schedule's days in an offset of its own, which the engine keeps at the
// clock's. The offset each load was made at is kept in the store, where
// every process that loads one writes it.

type loadedAt struct {
	Offset int `json:"offset"`
	// Ruleset is the SHA-256 of the ruleset loaded, which says which of
	// the ones that could be in force is.
	Ruleset string `json:"ruleset,omitempty"`
}

// Reload says what Retime put in the kernel again.
type Reload int

// What Retime can load.
const (
	// ReloadedNothing: the offset had not moved, or nothing in force keeps
	// a schedule's hours.
	ReloadedNothing Reload = iota
	// ReloadedSchedules is the chains that keep a schedule's hours.
	ReloadedSchedules
	// ReloadedTable is the whole table, which takes what the old one held
	// outside the ruleset with it: the sets refreshed since and the port
	// mappings.
	ReloadedTable
)

// offsets is how the engine reads the router's UTC offset, when the clock
// it drives can say.
func (e *Engine) offsets() (timezone.Offsetter, bool) {
	o, ok := e.clock.(timezone.Offsetter)
	return o, ok
}

// setKernelOffset gives the kernel the router's offset, which it matches
// a schedule's days in each time it reads one.
func (e *Engine) setKernelOffset(offset int) {
	k, ok := e.clock.(timezone.KernelOffsetter)
	if !ok {
		return
	}
	err := k.SetKernelOffset(offset)
	if err == nil {
		e.kernelErr.Store(nil)
		return
	}
	// Once per cause: a sandbox without the capability would otherwise
	// say so every hour.
	msg := err.Error()
	if last := e.kernelErr.Swap(&msg); last == nil || *last != msg {
		e.log.Warn("could not give the kernel the router's UTC offset; a schedule's days start at the kernel's midnight",
			"offset", offset, "err", err)
	}
}

func rulesetDigest(ruleset string) string {
	sum := sha256.Sum256([]byte(ruleset))
	return hex.EncodeToString(sum[:])
}

func hasHours(ruleset string) bool { return strings.Contains(ruleset, "meta hour") }

// load puts a whole ruleset in the kernel and notes the offset nft read
// its schedules at.
func (e *Engine) load(ctx context.Context, ruleset string) error {
	o, known := e.offsets()
	var offset int
	if known {
		offset, _ = o.Offset(time.Now())
	}
	if err := e.nft.Apply(ctx, ruleset); err != nil {
		return err
	}
	if known {
		e.noteLoad(loadedAt{Offset: offset, Ruleset: rulesetDigest(ruleset)})
	}
	return nil
}

// reloadSchedules reads the chains of ruleset that keep a schedule's hours
// again, at the offset the clock has now, and leaves the rest of the table
// alone. ruleset is the one in the kernel.
func (e *Engine) reloadSchedules(ctx context.Context, ruleset string) error {
	o, known := e.offsets()
	if !known {
		return nil
	}
	offset, _ := o.Offset(time.Now())
	if script, ok := nft.ScheduleReload(ruleset); ok {
		if err := e.nft.Apply(ctx, script); err != nil {
			return err
		}
	}
	e.noteLoad(loadedAt{Offset: offset, Ruleset: rulesetDigest(ruleset)})
	return nil
}

// noteLoad records what the kernel was loaded with and at which offset,
// and gives the kernel that offset for the schedules' days.
func (e *Engine) noteLoad(at loadedAt) {
	e.retimed.Store(nil)
	e.setKernelOffset(at.Offset)
	raw, err := json.Marshal(at)
	if err == nil {
		err = e.store.WriteState(store.OffsetFile, raw)
	}
	if err != nil {
		e.log.Warn("could not record the UTC offset the ruleset was loaded at", "err", err)
	}
}

// readLoad is what the last load recorded, when one has.
func (e *Engine) readLoad() (loadedAt, bool) {
	var at loadedAt
	raw, err := e.store.ReadState(store.OffsetFile)
	if err != nil || json.Unmarshal(raw, &at) != nil {
		return loadedAt{}, false
	}
	return at, true
}

// retime follows a change of zone made after ruleset went in: the kernel
// takes the new offset and the chains that keep a schedule's hours are
// read again at it. The firewall goes in first so nothing slow holds it
// up.
func (e *Engine) retime(ctx context.Context, ruleset string) {
	o, ok := e.offsets()
	if !ok {
		return
	}
	now, _ := o.Offset(time.Now())
	if at, known := e.readLoad(); !known || at.Offset == now {
		return
	}
	if !hasHours(ruleset) {
		e.noteLoad(loadedAt{Offset: now, Ruleset: rulesetDigest(ruleset)})
		return
	}
	err := e.reloadSchedules(ctx, ruleset)
	if err != nil {
		e.log.Warn("could not read the schedules again in the new zone; loading the whole ruleset", "err", err)
		err = e.load(ctx, ruleset)
	}
	if err != nil {
		e.log.Warn("could not load the ruleset again in the new zone; its schedules run that far off", "err", err)
	}
}

// Retime keeps the kernel at the router's UTC offset and, when the offset
// has moved since the ruleset in force was loaded and a rule in it keeps
// a schedule's hours, reads its scheduled chains again. When the last load
// did not say which ruleset it put in, the whole table goes in again. It
// reports what it loaded.
func (e *Engine) Retime(ctx context.Context) (Reload, error) {
	o, ok := e.offsets()
	if !ok {
		return ReloadedNothing, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now, _ := o.Offset(time.Now())
	// The kernel reads the days in its own offset at every match, so that
	// follows the clock whatever the ruleset needs.
	e.setKernelOffset(now)
	at, known := e.readLoad()
	if known && at.Offset == now {
		return ReloadedNothing, nil
	}
	if last := e.retimed.Load(); last != nil && *last == now {
		// Loaded at this offset already, and the note of it did not stick.
		return ReloadedNothing, nil
	}
	// No other process starts an apply between the look at what is in
	// force and the load of it.
	unlock, err := e.store.Lock()
	if err != nil {
		return ReloadedNothing, err
	}
	defer unlock()
	rulesets, skip, err := e.inForce()
	if err != nil || skip {
		return ReloadedNothing, err
	}
	// A ruleset loaded before the offset was kept is taken to have been
	// loaded at this one: a guess, and cheaper than a reload on every
	// upgrade. One with no schedule reads no clock.
	if !known || !slices.ContainsFunc(rulesets, hasHours) {
		at.Offset = now
		e.noteLoad(at)
		return ReloadedNothing, nil
	}
	if i := slices.IndexFunc(rulesets, func(rs string) bool { return rulesetDigest(rs) == at.Ruleset }); i >= 0 {
		if !hasHours(rulesets[i]) {
			at.Offset = now
			e.noteLoad(at)
			return ReloadedNothing, nil
		}
		err = e.reloadSchedules(ctx, rulesets[i])
		if err == nil {
			e.retimed.Store(&now)
			e.log.Info("read the schedules again at the new UTC offset", "offset", now)
			return ReloadedSchedules, nil
		}
		e.log.Warn("could not read the schedules again at the new UTC offset; loading the whole ruleset", "err", err)
	}
	for _, ruleset := range rulesets {
		if err = e.load(ctx, ruleset); err == nil {
			e.retimed.Store(&now)
			e.log.Info("loaded the ruleset again at the new UTC offset, for its schedules", "offset", now)
			return ReloadedTable, nil
		}
	}
	return ReloadedNothing, err
}

// inForce is what the kernel runs, as the rulesets to try in turn: the
// pending one, else the saved one and the saved configuration rendered
// again, which is the order Load tries them in. skip says there is nothing
// to load: another process is applying, and notes its own offset when it
// loads, or the fallback is in, which keeps no schedule.
func (e *Engine) inForce() (rulesets []string, skip bool, err error) {
	if e.pending != nil {
		return []string{e.pending.ruleset}, false, nil
	}
	if e.readFallback() != nil {
		return nil, true, nil
	}
	if r, err := e.readRecord(); err != nil || r != nil {
		return nil, true, err
	}
	saved, err := e.store.LoadRuleset()
	switch {
	case err == nil:
		rulesets = append(rulesets, saved)
	case !errors.Is(err, store.ErrNotFound):
		return nil, false, err
	}
	if cfg, err := e.store.Load(); err == nil && cfg.Validate() == nil {
		if rendered, err := nft.RenderWithFeeds(cfg, e.FeedEntries()); err == nil && rendered != saved {
			rulesets = append(rulesets, rendered)
		}
	}
	return rulesets, false, nil
}

// FollowOffset calls Retime at the start, as the router's UTC offset
// changes, and once an hour between, for a zone changed from outside and a
// clock stepped while it waited. It runs after when the whole table went
// in again: what the old table held outside the ruleset, the sets
// refreshed since and the port mappings, went with it.
func (e *Engine) FollowOffset(ctx context.Context, after func(context.Context)) {
	o, ok := e.offsets()
	if !ok {
		return
	}
	var wait time.Duration
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		loaded, err := e.Retime(ctx)
		switch {
		case err != nil:
			e.log.Warn("could not load the ruleset again at the new UTC offset; schedules run that far off", "err", err)
		case loaded == ReloadedTable && after != nil:
			after(ctx)
		}
		wait = time.Hour
		now := time.Now()
		// A second past the change, so the clock reads the new offset. A
		// zone file's rule can name a change already past, which is no
		// reason to look again at once.
		if _, next := o.Offset(now); next.After(now) {
			wait = min(wait, next.Sub(now)+time.Second)
		}
	}
}
