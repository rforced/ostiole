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
	"sort"
	"strconv"
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
	// country and family, an AS alias one per number.
	Sources []string `json:"sources"`
	// Parts is what each source contributed at the last fetch. For a
	// country list it is the answer to "how much of this ruleset is
	// China", which is the question somebody picking twelve countries is
	// actually asking. The parts add up to more than Entries: a range
	// published by two sources is counted by both and kept once.
	Parts       []Part     `json:"parts,omitempty"`
	Entries     int        `json:"entries"`
	FetchedAt   time.Time  `json:"fetchedAt,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
	LastTriedAt *time.Time `json:"lastTriedAt,omitempty"`
	// Stale is true when the cache is older than the refresh period, which
	// usually means the router cannot reach the publisher.
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
	Alias     string    `json:"alias"`
	Parts     []Part    `json:"parts,omitempty"`
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

// Save records fetched entries and what each source contributed.
func (c *Cache) Save(alias string, parts []Part, entries []string, when time.Time) error {
	f := cached{Alias: alias, Parts: parts, FetchedAt: when.UTC().Truncate(time.Second), Entries: entries}
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
			st.Parts = f.Parts
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

// Sources lists the URLs an alias is built from: its own, one per
// country and address family for a GeoIP alias, or one per number for an
// AS alias.
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
		if a.URL == "" {
			return nil
		}
		return []Part{{Source: a.URL}}
	}
	v4, v6 := cfg.System.GeoIPTemplates()
	var out []Part
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
	Client  *http.Client
	Timeout time.Duration
	// UserAgent identifies this router to the publisher, several of whom ask
	// for one.
	UserAgent string
	// Log is where a failure that does not fail the fetch is noted; nil
	// uses the default logger.
	Log *slog.Logger
}

// NewFetcher returns a fetcher with production defaults.
func NewFetcher(version string) *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: DefaultTimeout},
		Timeout:   DefaultTimeout,
		UserAgent: "ostiole/" + version,
	}
}

// Fetch downloads every source of an alias and returns the entries with
// duplicates removed, and what each source held. One source failing fails
// the whole alias: half a blocklist is worse than yesterday's whole one.
func (f *Fetcher) Fetch(ctx context.Context, cfg *model.Config, a model.Alias) ([]string, []Part, error) {
	parts := SourceParts(cfg, a)
	if len(parts) == 0 {
		return nil, nil, errors.New("this alias has no source to fetch")
	}
	seen := map[string]bool{}
	var out []string
	for i := range parts {
		entries, err := f.one(ctx, parts[i].Source, a.Type)
		if err != nil {
			return nil, parts, fmt.Errorf("%s: %w", parts[i].Source, err)
		}
		// What this source held, whether or not another source had it
		// first: the question is how big this country is, not how much of
		// it arrived here first.
		parts[i].Entries = len(entries)
		for _, e := range entries {
			if seen[e] {
				continue
			}
			seen[e] = true
			out = append(out, e)
			if len(out) > MaxEntries {
				return nil, parts, fmt.Errorf("more than %d entries", MaxEntries)
			}
		}
	}
	sort.Strings(out)
	if a.Type == model.AliasASN {
		f.holders(ctx, cfg, parts)
	}
	return out, parts, nil
}

func (f *Fetcher) one(ctx context.Context, url string, typ model.AliasType) ([]string, error) {
	raw, err := f.get(ctx, url)
	if err != nil {
		return nil, err
	}
	// The default AS source answers in JSON; a mirror serving a plain
	// list of prefixes reads like any other list.
	if typ == model.AliasASN && bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return parseAnnounced(raw)
	}
	return Parse(string(raw), typ)
}

// get downloads one URL, bounded by the timeout and MaxBytes.
func (f *Fetcher) get(ctx context.Context, url string) ([]byte, error) {
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
		return nil, fmt.Errorf("the source answered %q: %s", ans.Status, ans.errorText())
	}
	out := []string{}
	seen := map[string]bool{}
	for _, p := range ans.Data.Prefixes {
		norm, err := normalizeAddress(p.Prefix)
		if err != nil || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out, nil
}

func (a announced) errorText() string {
	for _, m := range a.Messages {
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
// and the sets: a host without its /32 or /128.
func normalizeAddress(s string) (string, error) {
	p, err := model.ParseAddress(s)
	if err != nil {
		return "", err
	}
	return prefixString(p), nil
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
