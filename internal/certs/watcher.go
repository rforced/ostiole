package certs

import (
	"context"
	"log/slog"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// DefaultWatch is how often the store is put back in step with the
// configuration the router is running.
const DefaultWatch = 5 * time.Second

// Watcher keeps the store in step with the configuration that is really
// in force. It is not an apply step: restoring a configuration does not
// re-run the appliers, so a revert or an expired confirmation window
// would otherwise leave a removed upload on disk and the wrong
// certificate on the listener.
type Watcher struct {
	Store *Store
	// Manager serves the web UI's certificate; nil on a server without
	// TLS, where there is nothing to reload.
	Manager *Manager
	// Source is the configuration the router is actually running.
	Source func() *model.Config
	// Interval is how often to look; zero means DefaultWatch.
	Interval time.Duration
	Log      *slog.Logger

	// last is the failure most recently logged, so a corrupt certificate
	// is reported once rather than every five seconds.
	last string
}

// Run follows the configuration until ctx is done. The first pass happens
// immediately, so a restart serves the right certificate before the first
// request rather than five seconds into it.
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
	failed := false
	changed, err := w.Store.Apply(w.Source())
	if err != nil {
		w.warn("could not put the certificate store in step", err)
		failed = true
	}
	if w.Manager != nil && (changed || w.Manager.Serving() != w.selected()) {
		if err := w.Manager.Reload(); err != nil {
			w.warn("could not serve the selected certificate", err)
			failed = true
		}
	}
	if !failed {
		w.last = ""
	}
}

func (w *Watcher) selected() string {
	cfg := w.Source()
	if cfg == nil {
		return ""
	}
	return cfg.System.Management.Certificate
}

// warn logs a failure once until it changes or clears.
func (w *Watcher) warn(msg string, err error) {
	line := msg + ": " + err.Error()
	if w.Log == nil || line == w.last {
		return
	}
	w.last = line
	w.Log.Warn(msg, "err", err)
}
