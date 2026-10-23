package feeds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/nft"
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

	wakeOnce sync.Once
	wake     chan struct{}
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
		case <-r.woken():
		}
	}
}

// Wake asks Run for a pass now rather than at the next tick. An apply
// calls it, so an alias whose sources changed follows within seconds. Two
// Wakes before the pass ask for one pass.
func (r *Refresher) Wake() {
	select {
	case r.woken() <- struct{}{}:
	default:
	}
}

func (r *Refresher) woken() chan struct{} {
	r.wakeOnce.Do(func() { r.wake = make(chan struct{}, 1) })
	return r.wake
}

// Inspect reads a list the configuration does not name yet.
func (r *Refresher) Inspect(ctx context.Context, url string, publicOnly bool) (Part, error) {
	return r.Fetcher.Inspect(ctx, url, publicOnly)
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
		if !force && !r.due(cfg, a) {
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
		r.push(ctx, SetFragment(cfg, r.Cache.For(cfg)))
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
	r.push(ctx, SetFragment(cfg, r.Cache.For(cfg)))
	return len(r.Cache.Entries()[name]), nil
}

func (r *Refresher) refresh(ctx context.Context, cfg *model.Config, a model.Alias) (bool, error) {
	entries, parts, lists, err := r.Fetcher.fetch(ctx, cfg, a, r.Cache.Dir)
	if err == nil && a.Type == model.AliasGeoIP {
		if prev, ok := r.Cache.last(a.Name); ok {
			err = emptied(prev, parts, entries)
		}
	}
	if err != nil {
		r.Cache.recordError(a.Name, err, time.Now(), Sources(cfg, a), r.Fetcher.Budget.AliasEntries(cfg))
		return false, err
	}
	before := r.Cache.Entries()[a.Name]
	if err := r.Cache.save(a, parts, entries, lists, time.Now()); err != nil {
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

// due reports whether an alias has gone long enough without a fetch, or
// has changed since the one it has: a new URL, country or number is
// fetched on the next pass, not a refresh period later. One that failed
// from the same sources waits out its backoff.
func (r *Refresher) due(cfg *model.Config, a model.Alias) bool {
	if r.Cache.backingOff(cfg, a, time.Now(), r.Fetcher.Budget.AliasEntries(cfg)) {
		return false
	}
	at, ok := r.Cache.FetchedAt(a.Name)
	if !ok || !r.Cache.Current(cfg, a) {
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
		r.push(ctx, setFragment(cfg, r.Cache.For(cfg), true))
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
			list = append(a.Written(), list...)
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

// emptied refuses a country list that lost a country, or a family, since
// the last fetch from the same sources. RIPEstat answers a code it has no
// rows for just as it answers a territory with no addresses, so a gap in
// its data would read as a real answer, and open a deny rule or shut an
// allow rule until the next refresh. Yesterday's list is better.
func emptied(prev cached, parts []Part, entries []string) error {
	held := make(map[string]int, len(prev.Parts))
	have := make([]string, 0, len(prev.Parts))
	for _, p := range prev.Parts {
		held[p.Source] = p.Entries
		have = append(have, p.Source)
	}
	want := make([]string, 0, len(parts))
	for _, p := range parts {
		if p.Entries == 0 && held[p.Source] > 0 {
			return fmt.Errorf("%s came back empty; it held %d at the last fetch", p.Country, held[p.Source])
		}
		want = append(want, p.Source)
	}
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		// A country taken out may have been the only one with IPv6.
		return nil
	}
	v4, v6 := families(prev.Entries)
	n4, n6 := families(entries)
	switch {
	case v4 && !n4:
		return errors.New("no IPv4 addresses came back; the last fetch had some")
	case v6 && !n6:
		return errors.New("no IPv6 addresses came back; the last fetch had some")
	}
	return nil
}

func families(entries []string) (v4, v6 bool) {
	for _, e := range entries {
		if strings.Contains(e, ":") {
			v6 = true
		} else {
			v4 = true
		}
		if v4 && v6 {
			break
		}
	}
	return v4, v6
}
