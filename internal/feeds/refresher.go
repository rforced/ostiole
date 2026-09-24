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
	// OnTick, when set, is called after every pass, so the crons page can
	// say when the router last looked.
	OnTick func()
}

// DefaultTick is how often the refresher wakes up.
const DefaultTick = 15 * time.Minute

// Run refreshes until the context is cancelled. It starts by putting the
// cache into the loaded sets: the ruleset loaded at boot holds the entries
// of the last apply, and one refreshed since would otherwise wait for the
// list to change again. That is also what takes out an entry a fix since
// refuses, such as a default route.
func (r *Refresher) Run(ctx context.Context) {
	r.Push(ctx)
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
	for _, a := range Wanted(cfg) {
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
		r.push(ctx, SetFragment(cfg, r.Cache.Entries()))
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
	var want *model.Alias
	for _, a := range Wanted(cfg) {
		if a.Name == name {
			want = &a
			break
		}
	}
	if want == nil {
		return 0, fmt.Errorf("nothing called %q is fetched from anywhere", name)
	}
	if _, err := r.refresh(ctx, cfg, *want); err != nil {
		return 0, err
	}
	r.push(ctx, SetFragment(cfg, r.Cache.Entries()))
	return len(r.Cache.Entries()[name]), nil
}

func (r *Refresher) refresh(ctx context.Context, cfg *model.Config, a model.Alias) (bool, error) {
	entries, parts, err := r.Fetcher.Fetch(ctx, cfg, a)
	if err != nil {
		r.Cache.recordError(a.Name, err, time.Now())
		return false, err
	}
	before := r.Cache.Entries()[a.Name]
	if err := r.Cache.Save(a.Name, parts, entries, time.Now()); err != nil {
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

// Push puts what the cache holds into the loaded sets. A ruleset loaded
// outside an apply carries the entries it was saved with, which the
// refreshes since may have changed. A list the cache has nothing for is
// left as the ruleset has it rather than emptied.
func (r *Refresher) Push(ctx context.Context) {
	if cfg := r.config(); cfg != nil {
		r.push(ctx, setFragment(cfg, r.Cache.Entries(), true))
	}
}

// push replaces the elements of the fetched sets in the kernel. It is a
// small transaction of its own: flushing and refilling a set leaves every
// rule in place, so nothing is briefly unprotected.
func (r *Refresher) push(ctx context.Context, fragment string) {
	if r.Sets == nil || fragment == "" {
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
	return setFragment(cfg, entries, false)
}

// setFragment is SetFragment; cachedOnly leaves out the sets of a list
// entries has nothing for.
func setFragment(cfg *model.Config, entries map[string][]string, cachedOnly bool) string {
	var b strings.Builder
	b.WriteString(nft.Header + "\n")
	wrote := false
	replace := func(sets []nft.Set) {
		for _, set := range sets {
			fmt.Fprintf(&b, "flush set %s %s\n", nft.Table, set.Name)
			if len(set.Elements) > 0 {
				fmt.Fprintf(&b, "add element %s %s { %s }\n", nft.Table, set.Name, strings.Join(set.Elements, ", "))
			}
			wrote = true
		}
	}
	for _, a := range cfg.Aliases {
		if !IsFeed(a) {
			continue
		}
		list, cached := entries[a.Name]
		if cachedOnly && !cached {
			continue
		}
		if !a.Keyed() {
			list = append(append([]string{}, a.Entries...), list...)
		}
		replace(nft.AliasSets(a, list))
	}
	// The bogon list fills sets of its own, not an alias's.
	if bogons, cached := entries[BogonAlias]; cached || !cachedOnly {
		replace(nft.BlockSets(cfg, bogons))
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
