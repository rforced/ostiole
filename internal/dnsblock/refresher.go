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
	// OnTick, when set, is called after every pass, so the crons page can
	// say when the router last looked.
	OnTick func()
}

// DefaultTick is how often the refresher wakes up.
const DefaultTick = 15 * time.Minute

// Report says what a refresh pass did. The confusing case is a pass that
// fetched nothing at all — blocking is off, or the lists have not been
// applied yet — so it says which, rather than leaving the operator looking
// at a button that appeared to do nothing.
type Report struct {
	Fetched   []string          `json:"fetched"`
	Unchanged []string          `json:"unchanged"`
	Failed    map[string]string `json:"failed,omitempty"`
	// Note explains a pass that attempted nothing.
	Note string `json:"note,omitempty"`
}

// Attempted is how many lists were actually fetched.
func (r Report) Attempted() int { return len(r.Fetched) + len(r.Unchanged) + len(r.Failed) }

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
// it is due or not, which is what the "refresh now" button does. The report
// is for callers that have someone to tell; the timer ignores it.
func (r *Refresher) Tick(ctx context.Context, force bool) Report {
	rep := Report{Fetched: []string{}, Unchanged: []string{}}
	cfg := r.config()
	if cfg == nil {
		rep.Note = "nothing is configured yet"
		return rep
	}
	defer func() {
		if r.OnTick != nil {
			r.OnTick()
		}
	}()
	r.Cache.Prune(cfg)
	if !cfg.Blocking.Enabled {
		rep.Note = "DNS blocking is off, so no list was fetched"
		return rep
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
		switch {
		case err != nil:
			r.log().Warn("could not refresh a blocklist", "list", l.Name, "err", err)
			if rep.Failed == nil {
				rep.Failed = map[string]string{}
			}
			rep.Failed[l.Name] = err.Error()
		case updated:
			rep.Fetched = append(rep.Fetched, l.Name)
		default:
			rep.Unchanged = append(rep.Unchanged, l.Name)
		}
		changed = changed || updated
	}
	if changed {
		r.Push(ctx, cfg)
	}
	if rep.Attempted() == 0 && rep.Note == "" {
		rep.Note = "no list on this router has a URL to fetch"
	}
	return rep
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
	if r.Max > 0 {
		o.Max = r.Max
	}
	var res Result
	changed, err := r.Loader.Load(ctx, func(w io.Writer) error {
		var rerr error
		res, rerr = Render(w, o, r.Cache)
		return rerr
	})
	if err != nil {
		r.log().Warn("could not install the blocklist", "err", err)
		return
	}
	if err := r.Cache.SaveMerged(res); err != nil {
		r.log().Warn("could not record what the merge came to", "err", err)
	}
	if changed {
		r.log().Info("blocklist installed", "names", res.Domains, "fromLists", r.Cache.Total(cfg))
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
