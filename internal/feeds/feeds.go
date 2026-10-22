// Package feeds keeps aliases whose contents come from somewhere else:
// a blocklist published on the internet, the address ranges of a country,
// or the prefixes a network operator announces. The entries are cached on
// disk, so a router that boots without a working line still loads the
// list it had, and refreshed on a schedule rather than on every apply.
package feeds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ostiole/internal/atomicfile"
	"ostiole/internal/fetch"
	"ostiole/internal/model"
	"ostiole/internal/nft"
	"ostiole/internal/retry"
	"ostiole/internal/version"
)

// Limits on what a feed may be. A blocklist with a million entries is
// either a mistake or an attack on this router's memory.
const (
	MaxBytes   = 16 << 20
	MaxEntries = 500_000
	// DefaultTimeout is how long one request may take. Some of these lists
	// are large and served slowly.
	DefaultTimeout = 60 * time.Second
	// MinRefresh is the shortest refresh anyone can ask for: these lists
	// change daily at most, and their publishers ask not to be hammered.
	MinRefresh = time.Hour
	// DefaultRefresh is used when an alias asks for no particular period.
	DefaultRefresh = 24 * time.Hour
)

// Status is what the UI shows about one feed.
type Status struct {
	Alias string `json:"alias"`
	// Sources are the URLs it is built from; a GeoIP alias has one per
	// country and template, an AS alias one per number.
	Sources []string `json:"sources"`
	// Parts is what each source contributed at the last fetch. For a
	// country list it is the answer to "how much of this ruleset is
	// China", which is the question somebody picking twelve countries is
	// actually asking. The parts add up to more than Entries: a range
	// published by two sources is counted by both and kept once.
	Parts       []Part     `json:"parts,omitempty"`
	Entries     int        `json:"entries"`
	FetchedAt   time.Time  `json:"fetchedAt,omitzero"`
	LastError   string     `json:"lastError,omitempty"`
	LastTriedAt *time.Time `json:"lastTriedAt,omitempty"`
	// Stale is true when the cache is older than the refresh period, which
	// usually means the router cannot reach the publisher, or was fetched
	// before the alias's sources changed.
	Stale bool `json:"stale"`
}

// Part is one source of an alias and what it held at the last fetch.
type Part struct {
	Source string `json:"source"`
	// Country is the code the source was expanded for, on a country
	// list, and empty on an ordinary one.
	Country string `json:"country,omitempty"`
	// ASN is the number the source was expanded for, on an AS list, in
	// its canonical form.
	ASN string `json:"asn,omitempty"`
	// Holder is who that AS belongs to, when the names lookup answered.
	Holder  string `json:"holder,omitempty"`
	Entries int    `json:"entries"`
}

// cached is the on-disk form. It is a cache: a file written by an older
// version simply has no breakdown until the next refresh fills one in.
type cached struct {
	Alias string `json:"alias"`
	// Select is the part of a JSON list an older release kept. A list
	// kept by one is fetched again whole.
	Select    []string  `json:"select,omitempty"`
	Parts     []Part    `json:"parts,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
	Entries   []string  `json:"entries"`
	// Lists is what each source held, so a source taken out of the alias
	// leaves the sets before the next fetch. Entries is still written, for
	// an older release reading the file.
	Lists map[string][]string `json:"lists,omitempty"`
}

// stored is how a file is read: with Lists there, Entries is rebuilt from
// them rather than decoded, so each string is held once.
type stored struct {
	cached
	Entries json.RawMessage `json:"entries"`
}

// fetchedFor reports whether a cached list is what the alias asks for now:
// fetched whole from the same sources. One fetched before its URLs,
// countries or numbers changed is not.
func (f cached) fetchedFor(cfg *model.Config, a model.Alias) bool {
	if len(f.Select) > 0 {
		return false
	}
	want := Sources(cfg, a)
	have := make([]string, 0, len(f.Parts))
	for _, p := range f.Parts {
		have = append(have, p.Source)
	}
	slices.Sort(want)
	slices.Sort(have)
	return slices.Equal(want, have)
}

// Cache stores fetched entries under a directory, one file per alias.
type Cache struct {
	Dir string

	mu     sync.RWMutex
	loaded map[string]cached
	errs   map[string]feedError
	// order is each browsed alias's entries in address order.
	order map[string][]string
	// narrowed is For's last answer for an alias that lost sources since
	// its fetch, keyed by the sources it kept.
	narrowed map[string]narrowing
}

type narrowing struct {
	key     string
	entries []string
}

type feedError struct {
	message string
	when    time.Time
	// fails counts the failures in a row from sources, sorted, which is
	// what the backoff reads.
	fails   int
	sources []string
}

// NewCache returns a cache over dir, reading whatever is already there.
func NewCache(dir string) *Cache {
	c := &Cache{Dir: dir, loaded: map[string]cached{}, errs: map[string]feedError{}, order: map[string][]string{},
		narrowed: map[string]narrowing{}}
	c.loadAll()
	return c
}

func (c *Cache) loadAll() {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(c.Dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var st stored
		err = json.Unmarshal(raw, &st)
		f := st.cached
		switch {
		case err != nil || f.Alias == "":
		case f.Lists != nil:
			lists := make([][]string, 0, len(f.Parts))
			for _, p := range f.Parts {
				lists = append(lists, f.Lists[p.Source])
			}
			f.Entries = merge(lists)
		case len(st.Entries) > 0:
			err = json.Unmarshal(st.Entries, &f.Entries)
		}
		if err != nil || f.Alias == "" {
			// Prune knows only the files that load, so nothing else would
			// remove this one.
			if os.Remove(path) == nil {
				slog.Info("removed a cached feed that does not parse", "file", path)
			}
			continue
		}
		// A file written before default routes were refused may hold one.
		f.Entries = slices.DeleteFunc(f.Entries, defaultRoute)
		for source, list := range f.Lists {
			f.Lists[source] = slices.DeleteFunc(list, defaultRoute)
		}
		c.loaded[f.Alias] = f
	}
}

// Entries returns the cached entries for every feed alias, which is what
// the renderer puts in the nftables sets.
func (c *Cache) Entries() map[string][]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string][]string, len(c.loaded))
	for name, f := range c.loaded {
		out[name] = append([]string(nil), f.Entries...)
	}
	return out
}

// For is Entries as cfg asks for them. A source taken out of an alias
// since its fetch is left out at once where the cache knows what each
// source held: otherwise an allow rule over a country just removed goes on
// allowing it until a fetch succeeds. It only reads, so a draft may ask.
func (c *Cache) For(cfg *model.Config) map[string][]string {
	wanted := map[string]model.Alias{}
	for _, a := range Wanted(cfg) {
		wanted[a.Name] = a
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]string, len(c.loaded))
	for name, f := range c.loaded {
		out[name] = slices.Clone(c.kept(f, wanted[name], cfg))
	}
	return out
}

// kept is what of f the alias a still asks for. Holding c.mu.
func (c *Cache) kept(f cached, a model.Alias, cfg *model.Config) []string {
	if f.Lists == nil || a.Name == "" {
		return f.Entries
	}
	want := map[string]bool{}
	for _, s := range Sources(cfg, a) {
		want[s] = true
	}
	var keep []string
	for _, p := range f.Parts {
		if want[p.Source] {
			keep = append(keep, p.Source)
		}
	}
	if len(keep) == len(f.Parts) {
		return f.Entries
	}
	slices.Sort(keep)
	key := strings.Join(keep, "\n")
	if n, ok := c.narrowed[f.Alias]; ok && n.key == key {
		return n.entries
	}
	lists := make([][]string, 0, len(keep))
	for _, s := range keep {
		lists = append(lists, f.Lists[s])
	}
	entries := merge(lists)
	c.narrowed[f.Alias] = narrowing{key: key, entries: entries}
	return entries
}

// merge is lists as one, each entry once, sorted as Fetch sorts.
func merge(lists [][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, e := range l {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (c *Cache) path(alias string) string {
	return filepath.Join(c.Dir, safeName(alias)+".json")
}

// Save records what an alias fetched: the entries and what each source
// contributed.
func (c *Cache) Save(a model.Alias, parts []Part, entries []string, when time.Time) error {
	return c.save(a, parts, entries, nil, when)
}

// save is Save with what each source held, when the fetch kept it.
func (c *Cache) save(a model.Alias, parts []Part, entries []string, lists map[string][]string, when time.Time) error {
	f := cached{Alias: a.Name, Parts: parts, FetchedAt: when.UTC().Truncate(time.Second), Entries: entries, Lists: lists}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	if err := atomicfile.Write(c.path(a.Name), raw, 0o600); err != nil {
		return err
	}
	c.mu.Lock()
	c.loaded[a.Name] = f
	delete(c.errs, a.Name)
	delete(c.order, a.Name)
	delete(c.narrowed, a.Name)
	c.mu.Unlock()
	return nil
}

// Forget drops an alias that no longer exists, so its file does not sit
// around holding a list nobody asked for.
func (c *Cache) Forget(alias string) {
	c.mu.Lock()
	delete(c.loaded, alias)
	delete(c.errs, alias)
	delete(c.order, alias)
	delete(c.narrowed, alias)
	c.mu.Unlock()
	_ = os.Remove(c.path(alias))
}

// Prune removes cached feeds for aliases the configuration no longer has.
func (c *Cache) Prune(cfg *model.Config) {
	keep := map[string]bool{}
	for _, a := range Wanted(cfg) {
		keep[a.Name] = true
	}
	c.mu.RLock()
	var gone []string
	for name := range c.loaded {
		if !keep[name] {
			gone = append(gone, name)
		}
	}
	c.mu.RUnlock()
	for _, name := range gone {
		c.Forget(name)
	}
}

func (c *Cache) recordError(alias string, err error, when time.Time, sources []string) {
	slices.Sort(sources)
	c.mu.Lock()
	fails := 1
	if prev, ok := c.errs[alias]; ok && slices.Equal(prev.sources, sources) {
		fails = prev.fails + 1
	}
	c.errs[alias] = feedError{message: err.Error(), when: when, fails: fails, sources: sources}
	c.mu.Unlock()
}

// backingOff reports whether an alias failed recently from the sources it
// has now. The wait starts at MinRefresh and doubles with each failure in
// a row, up to the alias's period, so a source having a bad day is not
// asked every tick. Changed sources are fetched at once.
func (c *Cache) backingOff(cfg *model.Config, a model.Alias, now time.Time) bool {
	c.mu.RLock()
	e, ok := c.errs[a.Name]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	sources := Sources(cfg, a)
	slices.Sort(sources)
	if !slices.Equal(e.sources, sources) {
		return false
	}
	wait := min(MinRefresh<<min(e.fails-1, 8), RefreshPeriod(a))
	return now.Sub(e.when) < wait
}

// last is what the cache holds for an alias.
func (c *Cache) last(alias string) (cached, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.loaded[alias]
	return f, ok
}

// FetchedAt reports when an alias was last refreshed successfully.
func (c *Cache) FetchedAt(alias string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.loaded[alias]
	return f.FetchedAt, ok
}

// Current reports whether the cache holds the list the alias asks for now,
// rather than one fetched before it changed.
func (c *Cache) Current(cfg *model.Config, a model.Alias) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.loaded[a.Name]
	return ok && f.fetchedFor(cfg, a)
}

// Statuses describes every feed in the configuration.
func (c *Cache) Statuses(cfg *model.Config) []Status {
	out := []Status{}
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	for _, a := range Wanted(cfg) {
		st := Status{Alias: a.Name, Sources: Sources(cfg, a)}
		if f, ok := c.loaded[a.Name]; ok {
			st.Parts = f.Parts
			st.Entries = len(f.Entries)
			st.FetchedAt = f.FetchedAt
			st.Stale = now.Sub(f.FetchedAt) > 2*RefreshPeriod(a) || !f.fetchedFor(cfg, a)
		} else {
			st.Stale = true
		}
		if e, ok := c.errs[a.Name]; ok {
			st.LastError = e.message
			when := e.when
			st.LastTriedAt = &when
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// BogonAlias is the name the bogon list is cached under. Nobody writes
// it as an alias: interfaces ask for it by turning on their bogon block.
const BogonAlias = nft.BogonFeed

// BogonFeed describes the bogon list in the same shape as an alias, so
// one fetcher, one cache, and one status list cover it too. Its sources
// are filled in by Sources, which knows it has one per family.
func BogonFeed() model.Alias {
	return model.Alias{
		Name:         BogonAlias,
		Type:         model.AliasHosts,
		Description:  "prefixes IANA has not allocated",
		RefreshHours: 24,
	}
}

// Wanted lists everything this configuration needs fetched: the aliases
// that name a source, and the bogon list when an interface blocks it.
func Wanted(cfg *model.Config) []model.Alias {
	var out []model.Alias
	for _, a := range cfg.Aliases {
		if IsFeed(a) {
			out = append(out, a)
		}
	}
	if cfg.BlocksBogons() {
		out = append(out, BogonFeed())
	}
	return out
}

// IsFeed reports whether an alias takes its entries from elsewhere.
func IsFeed(a model.Alias) bool {
	return a.Fetched() || a.Name == BogonAlias
}

// RefreshPeriod is how often a feed should be fetched.
func RefreshPeriod(a model.Alias) time.Duration {
	if a.RefreshHours <= 0 {
		return DefaultRefresh
	}
	d := time.Duration(a.RefreshHours) * time.Hour
	if d < MinRefresh {
		return MinRefresh
	}
	return d
}

// Sources lists the URLs an alias is built from: its URL lines, one per
// country and template for a GeoIP alias, or one per number for an AS
// alias.
func Sources(cfg *model.Config, a model.Alias) []string {
	parts := SourceParts(cfg, a)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, p.Source)
	}
	return out
}

// SourceParts is Sources with the country or AS each URL was expanded
// for, so a fetch can report what came from where. Entries are filled in
// by the fetch; here they are all zero.
func SourceParts(cfg *model.Config, a model.Alias) []Part {
	if a.Name == BogonAlias {
		v4, v6 := cfg.System.BogonTemplates()
		var out []Part
		for _, u := range []string{v4, v6} {
			if u != "" {
				out = append(out, Part{Source: u})
			}
		}
		return out
	}
	if a.Type == model.AliasASN {
		// Both families come back in one answer, so one source per AS.
		tmpl, _ := cfg.System.ASNTemplates()
		if tmpl == "" {
			return nil
		}
		var out []Part
		for _, e := range a.Entries {
			n, err := model.ParseASN(e)
			if err != nil {
				continue // validated upstream
			}
			bare := strconv.FormatUint(uint64(n), 10)
			out = append(out, Part{Source: strings.ReplaceAll(tmpl, "{asn}", bare), ASN: model.FormatASN(n)})
		}
		return out
	}
	if a.Type != model.AliasGeoIP {
		var out []Part
		for _, u := range a.URLs() {
			out = append(out, Part{Source: u})
		}
		return out
	}
	v4, v6 := cfg.System.GeoIPTemplates()
	var out []Part
	if v6 == v4 {
		// One URL that answers both families is asked once.
		v6 = ""
	}
	for _, code := range a.Entries {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			continue
		}
		for _, u := range []string{v4, v6} {
			if u != "" {
				out = append(out, Part{Source: strings.ReplaceAll(u, "{country}", code), Country: code})
			}
		}
	}
	return out
}

// Fetcher downloads and parses feeds.
type Fetcher struct {
	// Getter makes the requests; nil is the router's own, and tests give
	// fetch.Inside for their loopback servers.
	Getter  *fetch.Getter
	Timeout time.Duration
	// RetryWait is the first pause between attempts; zero is
	// retry.DefaultWait.
	RetryWait time.Duration
	// UserAgent identifies this router to the publisher, several of whom ask
	// for one.
	UserAgent string
	// Log is where a failure that does not fail the fetch is noted; nil
	// uses the default logger.
	Log *slog.Logger
}

// NewFetcher returns a fetcher with production defaults that reads
// through getter, nil being the router's own.
func NewFetcher(getter *fetch.Getter) *Fetcher {
	return &Fetcher{
		Getter:    getter,
		Timeout:   DefaultTimeout,
		RetryWait: retry.DefaultWait,
		UserAgent: version.Agent,
	}
}

// Fetch downloads every source of an alias and returns the entries with
// duplicates removed, and what each source held. One source failing fails
// the whole alias: half a blocklist is worse than yesterday's whole one.
func (f *Fetcher) Fetch(ctx context.Context, cfg *model.Config, a model.Alias) ([]string, []Part, error) {
	entries, parts, _, err := f.fetch(ctx, cfg, a)
	return entries, parts, err
}

// fetch is Fetch with each source's own entries, by source, which the
// cache keeps so a source taken out leaves the sets at once.
func (f *Fetcher) fetch(ctx context.Context, cfg *model.Config, a model.Alias) ([]string, []Part, map[string][]string, error) {
	parts := SourceParts(cfg, a)
	if len(parts) == 0 {
		return nil, nil, nil, errors.New("this alias has no source to fetch")
	}
	seen := map[string]bool{}
	var out []string
	lists := make(map[string][]string, len(parts))
	for i := range parts {
		entries, err := f.one(ctx, parts[i].Source, a.Type)
		if err != nil {
			return nil, parts, nil, fmt.Errorf("%s: %w", model.RedactURL(parts[i].Source), err)
		}
		// What this source held, whether or not another source had it
		// first: the question is how big this country is, not how much of
		// it arrived here first.
		parts[i].Entries = len(entries)
		lists[parts[i].Source] = entries
		for _, e := range entries {
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
			if len(out) > MaxEntries {
				return nil, parts, nil, fmt.Errorf("more than %d entries", MaxEntries)
			}
		}
	}
	if a.Type == model.AliasGeoIP {
		var empty []string
		for _, p := range parts {
			if p.Entries == 0 {
				empty = append(empty, p.Country)
			}
		}
		switch {
		case len(out) == 0:
			// One country can hold nothing, but every country empty at
			// once is a source having a bad day, or codes it does not know.
			return nil, parts, nil, fmt.Errorf("no country in this alias came back with any addresses (%s)", strings.Join(empty, ", "))
		case len(empty) > 0:
			f.log().Info("countries came back with no addresses", "alias", a.Name, "countries", strings.Join(empty, ", "))
		}
	}
	sort.Strings(out)
	if a.Type == model.AliasASN {
		f.holders(ctx, cfg, parts)
	}
	return out, parts, lists, nil
}

func (f *Fetcher) one(ctx context.Context, url string, typ model.AliasType) ([]string, error) {
	raw, err := f.get(ctx, url)
	if err != nil {
		return nil, err
	}
	return read(raw, typ)
}

// Inspect reads a list without keeping it, to say how many addresses it
// holds. The alias dialog asks as soon as a URL line is typed, before
// anything is saved.
// With publicOnly, it connects only to public addresses that are not this
// router's own; without, wherever an applied alias would.
func (f *Fetcher) Inspect(ctx context.Context, url string, publicOnly bool) (Part, error) {
	reach := fetch.Named
	if publicOnly {
		reach = fetch.Internet
	}
	raw, err := f.getOnce(ctx, url, reach)
	for _, refused := range []error{fetch.ErrNotPublic, fetch.ErrNotAllowed, fetch.ErrPlainHTTP} {
		if errors.Is(err, refused) {
			// The dial error around it names where a name or a redirect
			// led, which is not for whoever typed the URL to learn.
			return Part{}, refused
		}
	}
	if err != nil {
		return Part{}, err
	}
	entries, err := read(raw, model.AliasHosts)
	if err != nil {
		return Part{}, err
	}
	return Part{Source: url, Entries: len(entries)}, nil
}

// read picks the parser by what came back. The default AS and country
// sources answer in RIPEstat's JSON, and a mirror serving a plain list of
// prefixes reads like any other list. Any other JSON is searched for
// addresses.
func read(raw []byte, typ model.AliasType) ([]string, error) {
	raw = bytes.TrimPrefix(raw, bom)
	switch {
	case typ == model.AliasASN && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")):
		return parseAnnounced(raw)
	case typ == model.AliasGeoIP && isJSON(raw):
		var ans countryResources
		if json.Unmarshal(raw, &ans) == nil && ans.Call == "country-resource-list" {
			return ans.entries()
		}
		return ParseJSON(raw)
	case typ.HoldsAddresses() && isJSON(raw):
		return ParseJSON(raw)
	}
	return Parse(string(raw), typ)
}

// get downloads one URL for a refresh, asking again while the source
// errs, is busy or does not answer in time.
func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
	var raw []byte
	err := retry.Do(ctx, f.RetryWait, retry.Transient, func() (err error) {
		raw, err = f.getOnce(ctx, url, fetch.Named)
		return err
	})
	return raw, err
}

// getOnce downloads one URL from where reach allows, bounded by the
// timeout and MaxBytes.
func (f *Fetcher) getOnce(ctx context.Context, url string, reach fetch.Reach) ([]byte, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := f.Getter.Get(ctx, url, f.UserAgent, reach)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, retry.StatusError(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", MaxBytes)
	}
	return raw, nil
}

// announced is as much of RIPEstat's announced-prefixes answer as
// matters. Its messages are [level, text] pairs.
type announced struct {
	Status   string     `json:"status"`
	Messages [][]string `json:"messages"`
	Data     struct {
		Prefixes []struct {
			Prefix string `json:"prefix"`
		} `json:"prefixes"`
	} `json:"data"`
}

// parseAnnounced reads the prefixes out of a RIPEstat answer. An AS that
// announces nothing is a real answer, so an empty list is not the error
// it is for a text list.
func parseAnnounced(raw []byte) ([]string, error) {
	var ans announced
	if err := json.Unmarshal(raw, &ans); err != nil {
		return nil, fmt.Errorf("not a RIPEstat answer: %w", err)
	}
	if ans.Status != "" && ans.Status != "ok" {
		return nil, fmt.Errorf("the source answered %q: %s", ans.Status, errorText(ans.Messages))
	}
	prefixes := make([]string, 0, len(ans.Data.Prefixes))
	for _, p := range ans.Data.Prefixes {
		prefixes = append(prefixes, p.Prefix)
	}
	return normalizeAll(prefixes), nil
}

// countryResources is as much of RIPEstat's country-resource-list answer
// as matters. Its messages are [level, text] pairs. The lists are pointers
// so an answer missing one is told from an empty one.
type countryResources struct {
	Call     string     `json:"data_call_name"`
	Status   string     `json:"status"`
	Messages [][]string `json:"messages"`
	Data     struct {
		Resources struct {
			IPv4 *[]string `json:"ipv4"`
			IPv6 *[]string `json:"ipv6"`
		} `json:"resources"`
	} `json:"data"`
}

// entries reads both families out of the answer. A territory with no
// addresses of its own is a real answer, so an empty list is not an error
// here; Fetch refuses an alias that is empty whole, and the refresher a
// country that held addresses last time.
func (ans countryResources) entries() ([]string, error) {
	if ans.Status != "ok" {
		return nil, fmt.Errorf("the source answered %q: %s", ans.Status, errorText(ans.Messages))
	}
	res := ans.Data.Resources
	if res.IPv4 == nil || res.IPv6 == nil {
		return nil, errors.New("the answer holds no address lists")
	}
	all := append(slices.Clone(*res.IPv4), *res.IPv6...)
	for _, e := range all {
		// Without v4_format=prefix RIPEstat writes some blocks as ranges,
		// which no set entry parses; dropping them would leave holes.
		if strings.Contains(e, "-") {
			return nil, fmt.Errorf("the answer holds address ranges such as %s; add v4_format=prefix to the URL", e)
		}
	}
	return normalizeAll(all), nil
}

// normalizeAll normalizes a RIPEstat list, dropping repeats and anything
// normalizeAddress refuses.
func normalizeAll(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		norm, err := normalizeAddress(s)
		if err != nil || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out
}

func errorText(messages [][]string) string {
	for _, m := range messages {
		if len(m) == 2 && m[0] == "error" {
			return m[1]
		}
	}
	return "no reason given"
}

// holders fills in who each AS belongs to, from the names source, once
// for the whole alias. Best effort: a name is for the page, not the
// firewall, so a failure is noted and the alias stands.
func (f *Fetcher) holders(ctx context.Context, cfg *model.Config, parts []Part) {
	_, tmpl := cfg.System.ASNTemplates()
	if tmpl == "" || len(parts) == 0 {
		return
	}
	numbers := make([]string, 0, len(parts))
	for _, p := range parts {
		numbers = append(numbers, strings.TrimPrefix(p.ASN, "AS"))
	}
	var ans struct {
		Data struct {
			Names map[string]string `json:"names"`
		} `json:"data"`
	}
	raw, err := f.get(ctx, strings.ReplaceAll(tmpl, "{asns}", strings.Join(numbers, ",")))
	if err == nil {
		err = json.Unmarshal(raw, &ans)
	}
	if err != nil {
		f.log().Warn("could not name the AS numbers of an alias", "err", err)
		return
	}
	for i := range parts {
		parts[i].Holder = strings.TrimSpace(ans.Data.Names[numbers[i]])
	}
}

func (f *Fetcher) log() *slog.Logger {
	if f.Log == nil {
		return slog.Default()
	}
	return f.Log
}

// commentRe strips everything from the first comment marker. Spamhaus
// writes "192.0.2.0/24 ; SBL123", others use # or //.
var commentRe = regexp.MustCompile(`\s*(;|#|//).*$`)

// Parse reads a list, one entry per line, ignoring comments and blanks
// and anything that is not an address or a port. A list with a few
// malformed lines is still worth having; one with nothing usable is not.
func Parse(body string, typ model.AliasType) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	bad := 0
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(commentRe.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		// Some lists put several entries on a line.
		for field := range strings.FieldsSeq(line) {
			field = strings.Trim(field, ",")
			if field == "" {
				continue
			}
			var norm string
			switch typ {
			case model.AliasPorts:
				pr, err := model.ParsePortRange(field)
				if err != nil {
					bad++
					continue
				}
				norm = pr.String()
			default:
				var err error
				if norm, err = normalizeAddress(field); err != nil {
					bad++
					continue
				}
			}
			if !seen[norm] {
				seen[norm] = true
				out = append(out, norm)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("nothing usable in this list (%d unreadable lines)", bad)
	}
	return out, nil
}

// normalizeAddress is how an address or network is written in the cache
// and the sets: a host without its /32 or /128. A default route is
// refused: it covers every address there is, so one such line from a
// broken publisher, or a leaked route in RIPEstat's data, would turn an
// allow rule over the alias into allow-all.
func normalizeAddress(s string) (string, error) {
	p, err := model.ParseAddress(s)
	if err != nil {
		return "", err
	}
	if p.Bits() == 0 {
		return "", fmt.Errorf("%s covers every address", s)
	}
	return prefixString(p), nil
}

// defaultRoute reports whether a cached entry is 0.0.0.0/0 or ::/0.
func defaultRoute(entry string) bool {
	p, err := netip.ParsePrefix(entry)
	return err == nil && p.Bits() == 0
}

func prefixString(p netip.Prefix) string {
	if p.Bits() == p.Addr().BitLen() {
		return p.Addr().String()
	}
	return p.String()
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
