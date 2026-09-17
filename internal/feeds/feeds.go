// Package feeds keeps aliases whose contents come from somewhere else:
// a blocklist published on the internet, or the address ranges of a
// country. The entries are cached on disk, so a router that boots without a
// working line still loads the list it had, and refreshed on a schedule
// rather than on every apply.
package feeds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
)

// Limits on what a feed may be. A blocklist with a million entries is
// either a mistake or an attack on this router's memory.
const (
	MaxBytes   = 16 << 20
	MaxEntries = 500_000
	// DefaultTimeout is how long one fetch may take. Some of these lists
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
	// country and family.
	Sources     []string   `json:"sources"`
	Entries     int        `json:"entries"`
	FetchedAt   time.Time  `json:"fetchedAt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	LastTriedAt *time.Time `json:"lastTriedAt,omitempty"`
	// Stale is true when the cache is older than the refresh period, which
	// usually means the router cannot reach the publisher.
	Stale bool `json:"stale"`
}

// cached is the on-disk form.
type cached struct {
	Alias     string    `json:"alias"`
	Sources   []string  `json:"sources"`
	FetchedAt time.Time `json:"fetchedAt"`
	Entries   []string  `json:"entries"`
}

// Cache stores fetched entries under a directory, one file per alias.
type Cache struct {
	Dir string

	mu     sync.RWMutex
	loaded map[string]cached
	errs   map[string]feedError
}

type feedError struct {
	message string
	when    time.Time
}

// NewCache returns a cache over dir, reading whatever is already there.
func NewCache(dir string) *Cache {
	c := &Cache{Dir: dir, loaded: map[string]cached{}, errs: map[string]feedError{}}
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
		raw, err := os.ReadFile(filepath.Join(c.Dir, e.Name()))
		if err != nil {
			continue
		}
		var f cached
		if err := json.Unmarshal(raw, &f); err != nil || f.Alias == "" {
			continue
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

func (c *Cache) path(alias string) string {
	return filepath.Join(c.Dir, safeName(alias)+".json")
}

// Save records fetched entries.
func (c *Cache) Save(alias string, sources, entries []string, when time.Time) error {
	f := cached{Alias: alias, Sources: sources, FetchedAt: when.UTC().Truncate(time.Second), Entries: entries}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(c.path(alias), raw, 0o600); err != nil {
		return err
	}
	c.mu.Lock()
	c.loaded[alias] = f
	delete(c.errs, alias)
	c.mu.Unlock()
	return nil
}

// Forget drops an alias that no longer exists, so its file does not sit
// around holding a list nobody asked for.
func (c *Cache) Forget(alias string) {
	c.mu.Lock()
	delete(c.loaded, alias)
	delete(c.errs, alias)
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

func (c *Cache) recordError(alias string, err error, when time.Time) {
	c.mu.Lock()
	c.errs[alias] = feedError{message: err.Error(), when: when}
	c.mu.Unlock()
}

// FetchedAt reports when an alias was last refreshed successfully.
func (c *Cache) FetchedAt(alias string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, ok := c.loaded[alias]
	return f.FetchedAt, ok
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
			st.Entries = len(f.Entries)
			st.FetchedAt = f.FetchedAt
			st.Stale = now.Sub(f.FetchedAt) > 2*RefreshPeriod(a)
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
	return a.URL != "" || a.Type == model.AliasGeoIP || a.Name == BogonAlias
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

// Sources lists the URLs an alias is built from: its own, or one per
// country and address family for a GeoIP alias.
func Sources(cfg *model.Config, a model.Alias) []string {
	if a.Name == BogonAlias {
		v4, v6 := cfg.System.BogonTemplates()
		var out []string
		for _, u := range []string{v4, v6} {
			if u != "" {
				out = append(out, u)
			}
		}
		return out
	}
	if a.Type != model.AliasGeoIP {
		if a.URL == "" {
			return nil
		}
		return []string{a.URL}
	}
	v4, v6 := cfg.System.GeoIPTemplates()
	var out []string
	for _, code := range a.Entries {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			continue
		}
		if v4 != "" {
			out = append(out, strings.ReplaceAll(v4, "{country}", code))
		}
		if v6 != "" {
			out = append(out, strings.ReplaceAll(v6, "{country}", code))
		}
	}
	return out
}

// Fetcher downloads and parses feeds.
type Fetcher struct {
	Client  *http.Client
	Timeout time.Duration
	// UserAgent identifies this router to the publisher, several of whom ask
	// for one.
	UserAgent string
}

// NewFetcher returns a fetcher with production defaults.
func NewFetcher(version string) *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: DefaultTimeout},
		Timeout:   DefaultTimeout,
		UserAgent: "ostiole/" + version,
	}
}

// Fetch downloads every source of an alias and returns the entries, with
// duplicates removed. One source failing fails the whole alias: half a
// blocklist is worse than yesterday's whole one.
func (f *Fetcher) Fetch(ctx context.Context, cfg *model.Config, a model.Alias) ([]string, []string, error) {
	sources := Sources(cfg, a)
	if len(sources) == 0 {
		return nil, nil, errors.New("this alias has no source to fetch")
	}
	seen := map[string]bool{}
	var out []string
	for _, src := range sources {
		entries, err := f.one(ctx, src, a.Type)
		if err != nil {
			return nil, sources, fmt.Errorf("%s: %w", src, err)
		}
		for _, e := range entries {
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
			if len(out) > MaxEntries {
				return nil, sources, fmt.Errorf("more than %d entries", MaxEntries)
			}
		}
	}
	sort.Strings(out)
	return out, sources, nil
}

func (f *Fetcher) one(ctx context.Context, url string, typ model.AliasType) ([]string, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", MaxBytes)
	}
	return Parse(string(raw), typ)
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
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(commentRe.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		// Some lists put several entries on a line.
		for _, field := range strings.Fields(line) {
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
				p, err := model.ParseAddress(field)
				if err != nil {
					bad++
					continue
				}
				norm = p.String()
				if p.Bits() == p.Addr().BitLen() {
					norm = p.Addr().String()
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

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
}
