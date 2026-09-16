package dnsblock

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Loader puts the rendered blocklist where the resolver reads it and makes
// the resolver pick it up. Keeping it behind an interface is what stops
// this package knowing about dnsmasq's paths or systemd.
type Loader interface {
	// Load renders through fn and reports whether the contents changed.
	// Nothing is reloaded when they did not: a daily refresh of a list
	// that has not moved should cost nothing.
	Load(ctx context.Context, fn func(io.Writer) error) (bool, error)
}

// Refresher keeps the lists current. It runs in the daemon, fetches what is
// due, and hands the merged result to the Loader.
type Refresher struct {
	Cache   *Cache
	Fetcher *Fetcher
	// Source reads the configuration that is in force.
	Source func() *model.Config
	// Loader installs the result; nil leaves it for the next apply.
	Loader Loader
	Log    *slog.Logger
	// Interval is how often the refresher looks for work, not how often a
	// list is fetched: each list has its own period.
	Interval time.Duration
	// Max is the ceiling on merged names; zero means the default.
	Max int
	// OnTick, when set, is called after every pass, so the scheduled jobs
	// page can say when the box last looked.
	OnTick func()
}

// DefaultTick is how often the refresher wakes up.
const DefaultTick = 15 * time.Minute

// Run refreshes until the context is cancelled.
func (r *Refresher) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultTick
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		r.Tick(ctx, false)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick fetches whatever is due. With force, every list is fetched whether
// it is due or not, which is what the "refresh now" button does.
func (r *Refresher) Tick(ctx context.Context, force bool) {
	cfg := r.config()
	if cfg == nil {
		return
	}
	r.Cache.Prune(cfg)
	if !cfg.Blocking.Enabled {
		if r.OnTick != nil {
			r.OnTick()
		}
		return
	}
	changed := false
	for _, l := range cfg.Blocking.EnabledLists() {
		if l.URL == "" {
			continue // written or uploaded by hand; there is nothing to fetch
		}
		if !force && !r.due(l) {
			continue
		}
		updated, err := r.refresh(ctx, l)
		if err != nil {
			r.log().Warn("could not refresh a blocklist", "list", l.Name, "err", err)
			continue
		}
		changed = changed || updated
	}
	if changed {
		r.Push(ctx, cfg)
	}
	if r.OnTick != nil {
		r.OnTick()
	}
}

// RefreshOne fetches a single list now and reports how many names it ended
// up with.
func (r *Refresher) RefreshOne(ctx context.Context, name string) (int, error) {
	cfg := r.config()
	if cfg == nil {
		return 0, fmt.Errorf("nothing is configured yet")
	}
	l, ok := cfg.Blocking.List(name)
	if !ok {
		return 0, fmt.Errorf("there is no list called %q", name)
	}
	if l.URL == "" {
		return 0, ErrNoSource
	}
	if _, err := r.refresh(ctx, *l); err != nil {
		return 0, err
	}
	r.Push(ctx, cfg)
	m, _ := r.Cache.Meta(name)
	return m.Domains, nil
}

func (r *Refresher) refresh(ctx context.Context, l model.BlockList) (bool, error) {
	domains, skipped, format, err := r.Fetcher.Fetch(ctx, l)
	if err != nil {
		r.Cache.recordError(l.Name, err, time.Now())
		return false, err
	}
	before, _ := r.Cache.Meta(l.Name)
	m := Meta{
		Name:      l.Name,
		URL:       l.URL,
		Format:    format,
		FetchedAt: time.Now().UTC().Truncate(time.Second),
		Skipped:   skipped,
	}
	if err := r.Cache.Save(m, domains); err != nil {
		return false, err
	}
	after, _ := r.Cache.Meta(l.Name)
	if before.Sum == after.Sum {
		return false, nil
	}
	r.log().Info("blocklist refreshed", "list", l.Name, "names", after.Domains, "was", before.Domains)
	return true, nil
}

// Store records a list that arrived by upload or by hand rather than over
// the wire, and installs the result.
func (r *Refresher) Store(ctx context.Context, name string, domains []string, skipped int, format model.ListFormat) (int, error) {
	cfg := r.config()
	if cfg == nil {
		return 0, fmt.Errorf("nothing is configured yet")
	}
	m := Meta{
		Name:      name,
		Format:    format,
		FetchedAt: time.Now().UTC().Truncate(time.Second),
		Skipped:   skipped,
	}
	if err := r.Cache.Save(m, domains); err != nil {
		return 0, err
	}
	r.Push(ctx, cfg)
	saved, _ := r.Cache.Meta(name)
	return saved.Domains, nil
}

// due reports whether a list has gone long enough without a fetch.
func (r *Refresher) due(l model.BlockList) bool {
	at, ok := r.Cache.FetchedAt(l.Name)
	if !ok {
		return true
	}
	return time.Since(at) >= RefreshPeriod(l)
}

// Push renders the merged list and hands it to the Loader. It is also what
// an apply calls when the settings changed but no list did.
func (r *Refresher) Push(ctx context.Context, cfg *model.Config) {
	if r.Loader == nil {
		return
	}
	o := OptionsFor(cfg)
	o.Max = r.Max
	changed, err := r.Loader.Load(ctx, func(w io.Writer) error {
		_, rerr := Render(w, o, r.Cache)
		return rerr
	})
	if err != nil {
		r.log().Warn("could not install the blocklist", "err", err)
		return
	}
	if changed {
		r.log().Info("blocklist installed", "names", r.Cache.Total(cfg))
	}
}

func (r *Refresher) config() *model.Config {
	if r.Source == nil {
		return nil
	}
	return r.Source()
}

func (r *Refresher) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
}
