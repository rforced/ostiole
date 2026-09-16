package feeds

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
)

// SetUpdater loads a fragment of nftables text, which is how a refreshed
// list reaches the kernel without rebuilding the whole ruleset.
type SetUpdater interface {
	Apply(ctx context.Context, ruleset string) error
}

// Refresher keeps the cache current. It runs in the daemon and, when a
// list changes, replaces the contents of the matching nftables sets in
// one transaction: the rules that use them are untouched, so a blocklist
// update never disturbs a working firewall.
type Refresher struct {
	Cache   *Cache
	Fetcher *Fetcher
	// Source reads the configuration that is in force.
	Source func() *model.Config
	// Sets loads the fragment that replaces a set's elements; nil skips
	// that step and leaves the change for the next apply.
	Sets SetUpdater
	Log  *slog.Logger
	// Interval is how often the refresher looks for work, not how often a
	// list is fetched: each alias has its own period.
	Interval time.Duration
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

// Tick refreshes whatever is due. With force, everything is fetched
// whether it is due or not, which is what the "refresh now" button does.
func (r *Refresher) Tick(ctx context.Context, force bool) {
	cfg := r.config()
	if cfg == nil {
		return
	}
	r.Cache.Prune(cfg)
	changed := false
	for _, a := range cfg.Aliases {
		if !IsFeed(a) {
			continue
		}
		if !force && !r.due(a) {
			continue
		}
		updated, err := r.refresh(ctx, cfg, a)
		if err != nil {
			r.log().Warn("could not refresh an alias", "alias", a.Name, "err", err)
			continue
		}
		changed = changed || updated
	}
	if changed {
		r.push(ctx, cfg)
	}
	if r.OnTick != nil {
		r.OnTick()
	}
}

// RefreshOne fetches a single alias now and reports how many entries it
// ended up with.
func (r *Refresher) RefreshOne(ctx context.Context, name string) (int, error) {
	cfg := r.config()
	if cfg == nil {
		return 0, fmt.Errorf("nothing is configured yet")
	}
	a, ok := cfg.Alias(name)
	if !ok {
		return 0, fmt.Errorf("no alias called %q", name)
	}
	if !IsFeed(*a) {
		return 0, fmt.Errorf("alias %q has nothing to fetch", name)
	}
	if _, err := r.refresh(ctx, cfg, *a); err != nil {
		return 0, err
	}
	r.push(ctx, cfg)
	return len(r.Cache.Entries()[name]), nil
}

func (r *Refresher) refresh(ctx context.Context, cfg *model.Config, a model.Alias) (bool, error) {
	entries, sources, err := r.Fetcher.Fetch(ctx, cfg, a)
	if err != nil {
		r.Cache.recordError(a.Name, err, time.Now())
		return false, err
	}
	before := r.Cache.Entries()[a.Name]
	if err := r.Cache.Save(a.Name, sources, entries, time.Now()); err != nil {
		return false, err
	}
	same := len(before) == len(entries)
	if same {
		for i := range entries {
			if before[i] != entries[i] {
				same = false
				break
			}
		}
	}
	if !same {
		r.log().Info("alias refreshed", "alias", a.Name, "entries", len(entries), "was", len(before))
	}
	return !same, nil
}

// due reports whether an alias has gone long enough without a fetch.
func (r *Refresher) due(a model.Alias) bool {
	at, ok := r.Cache.FetchedAt(a.Name)
	if !ok {
		return true
	}
	return time.Since(at) >= RefreshPeriod(a)
}

// push replaces the elements of the fetched sets in the kernel. It is a
// small transaction of its own: flushing and refilling a set leaves every
// rule in place, so nothing is briefly unprotected.
func (r *Refresher) push(ctx context.Context, cfg *model.Config) {
	if r.Sets == nil {
		return
	}
	fragment := SetFragment(cfg, r.Cache.Entries())
	if fragment == "" {
		return
	}
	if err := r.Sets.Apply(ctx, fragment); err != nil {
		// A ruleset that is not loaded yet is the ordinary case at boot.
		r.log().Debug("could not update the firewall sets; the next apply will carry them", "err", err)
	}
}

// SetFragment renders the nftables text that replaces the contents of
// every fetched alias's sets.
func SetFragment(cfg *model.Config, entries map[string][]string) string {
	var b strings.Builder
	b.WriteString(nft.Header + "\n")
	wrote := false
	for _, a := range cfg.Aliases {
		if !IsFeed(a) {
			continue
		}
		list := entries[a.Name]
		if a.Type != model.AliasGeoIP {
			list = append(append([]string{}, a.Entries...), list...)
		}
		for _, set := range nft.AliasSets(a, list) {
			fmt.Fprintf(&b, "flush set %s %s\n", nft.Table, set.Name)
			if len(set.Elements) > 0 {
				fmt.Fprintf(&b, "add element %s %s { %s }\n", nft.Table, set.Name, strings.Join(set.Elements, ", "))
			}
			wrote = true
		}
	}
	if !wrote {
		return ""
	}
	return b.String()
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
