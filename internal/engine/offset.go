package engine

import (
	"context"
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
// far off until something loads it again. The offset each load was made
// at is kept in the store, where every process that loads one writes it.

type loadedAt struct {
	Offset int `json:"offset"`
}

// offsets is how the engine reads the router's UTC offset, when the clock
// it drives can say.
func (e *Engine) offsets() (timezone.Offsetter, bool) {
	o, ok := e.clock.(timezone.Offsetter)
	return o, ok
}

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
		e.retimed.Store(nil)
		e.noteOffset(offset)
	}
	return nil
}

func (e *Engine) noteOffset(offset int) {
	raw, err := json.Marshal(loadedAt{Offset: offset})
	if err == nil {
		err = e.store.WriteState(store.OffsetFile, raw)
	}
	if err != nil {
		e.log.Warn("could not record the UTC offset the ruleset was loaded at", "err", err)
	}
}

// readOffset is the offset the ruleset in the kernel was loaded at, when a
// load has said.
func (e *Engine) readOffset() (int, bool) {
	raw, err := e.store.ReadState(store.OffsetFile)
	if err != nil {
		return 0, false
	}
	var at loadedAt
	if json.Unmarshal(raw, &at) != nil {
		return 0, false
	}
	return at.Offset, true
}

// retime loads a ruleset that has just gone in again when the zone set
// after it moved the UTC offset nft read its schedules at. The firewall
// goes in first so nothing slow holds it up, and loads a second time only
// for a new zone and a rule that keeps a schedule.
func (e *Engine) retime(ctx context.Context, ruleset string) {
	o, ok := e.offsets()
	if !ok || !strings.Contains(ruleset, "meta hour") {
		return
	}
	now, _ := o.Offset(time.Now())
	if was, known := e.readOffset(); !known || was == now {
		return
	}
	if err := e.load(ctx, ruleset); err != nil {
		e.log.Warn("could not load the ruleset again in the new zone; its schedules run that far off", "err", err)
	}
}

// Retime loads the ruleset in force again when the router's UTC offset has
// moved since it was loaded and a rule in it keeps a schedule. It reports
// whether it loaded anything.
func (e *Engine) Retime(ctx context.Context) (bool, error) {
	o, ok := e.offsets()
	if !ok {
		return false, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now, _ := o.Offset(time.Now())
	was, known := e.readOffset()
	if known && was == now {
		return false, nil
	}
	if last := e.retimed.Load(); last != nil && *last == now {
		// Loaded at this offset already, and the note of it did not stick.
		return false, nil
	}
	// No other process starts an apply between the look at what is in
	// force and the load of it.
	unlock, err := e.store.Lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	rulesets, skip, err := e.inForce()
	if err != nil || skip {
		return false, err
	}
	// A ruleset loaded before the offset was kept is taken to have been
	// loaded at this one: a guess, and cheaper than a reload on every
	// upgrade. One with no schedule reads no clock.
	if !known || !slices.ContainsFunc(rulesets, func(rs string) bool { return strings.Contains(rs, "meta hour") }) {
		e.noteOffset(now)
		return false, nil
	}
	for _, ruleset := range rulesets {
		if err = e.load(ctx, ruleset); err == nil {
			e.retimed.Store(&now)
			e.log.Info("loaded the ruleset again at the new UTC offset, for its schedules", "offset", now)
			return true, nil
		}
	}
	return false, err
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
// clock stepped while it waited. It runs after when a ruleset went in:
// what the old table held outside the ruleset, the sets refreshed since
// and the port mappings, went with it.
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
		case loaded && after != nil:
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
