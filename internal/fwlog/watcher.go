package fwlog

import (
	"context"
	"time"

	"ostiole/internal/model"
)

// DefaultWatch is how often the ring is put back in step with the
// configuration the router is running.
const DefaultWatch = 5 * time.Second

// Watcher keeps the ring's ceiling and retention in step with the
// configuration that is really in force. It follows Effective rather than the store for the
// reason the query log's watcher does: an apply that is reverted, or whose
// confirmation window expires, does not re-run the appliers, and a ring
// sized by a setting nobody confirmed would outlive it.
type Watcher struct {
	Ring *Ring
	// Source is the configuration the kernel is actually running.
	Source func() *model.Config
	// Zones, when set, is kept to the interfaces' zones in Source.
	Zones *Zones
	// Interval is how often to look; zero means DefaultWatch.
	Interval time.Duration
}

// Run follows the configuration until ctx is done. The first pass happens
// immediately, so a ring sized before a restart is that size at boot rather
// than at the next apply.
func (w *Watcher) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = DefaultWatch
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		w.tick()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *Watcher) tick() {
	var f model.FirewallLog
	cfg := w.Source()
	if cfg != nil {
		f = cfg.System.Management.FirewallLog
	}
	w.Ring.Configure(f.Size(), f.Retention())
	if w.Zones != nil {
		w.Zones.Set(cfg)
	}
}
