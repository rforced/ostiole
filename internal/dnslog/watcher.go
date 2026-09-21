package dnslog

import (
	"context"
	"time"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

// DefaultWatch is how often the log is put back in step with the
// configuration the router is running.
const DefaultWatch = 5 * time.Second

// Watcher keeps the log in step with the configuration that is really in
// force. The log is not an apply step: the appliers that write to the
// system are not re-run when an apply is reverted or its confirmation
// window expires, and a log that kept collecting after a revert would be
// collecting under a setting nobody confirmed. Following Effective the way
// the gateway monitor does costs one comparison every few seconds and is
// right in every case.
type Watcher struct {
	Log *Log
	// Source is the configuration the kernel is actually running.
	Source func() *model.Config
	// Cache holds the fetched lists the index is built from.
	Cache *dnsblock.Cache
	// Interval is how often to look; zero means DefaultWatch.
	Interval time.Duration
}

// Run follows the configuration until ctx is done. The first pass happens
// immediately, so a log that was on before a restart starts indexing at
// boot rather than at the next apply.
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
	cfg := w.Source()
	if cfg == nil {
		w.Log.Configure(model.QueryLog{}, dnsblock.Options{}, nil)
		return
	}
	w.Log.Configure(cfg.Services.DNS.QueryLog, dnsblock.OptionsFor(cfg), w.Cache)
}
