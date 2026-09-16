package dnsblock

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// Cache keeps what each list last gave us: the names in one plain text file
// per list, sorted so the merge can stream them, and a little metadata
// beside it. A box that boots without a working line blocks what it blocked
// yesterday.
type Cache struct {
	Dir string

	mu   sync.RWMutex
	meta map[string]Meta
	errs map[string]cacheError
}

// Meta is what is known about one cached list.
type Meta struct {
	Name      string           `json:"name"`
	URL       string           `json:"url,omitempty"`
	Format    model.ListFormat `json:"format,omitempty"`
	FetchedAt time.Time        `json:"fetchedAt"`
	Domains   int              `json:"domains"`
	// Skipped is how many lines held nothing usable. A list that is mostly
	// skipped is a list being read the wrong way.
	Skipped int `json:"skipped,omitempty"`
	// Sum identifies the contents, so a refresh that changed nothing does
	// not restart dnsmasq.
	Sum string `json:"sum,omitempty"`
}

type cacheError struct {
	message string
	when    time.Time
}

// Status is what the UI shows about one list.
type Status struct {
	Name        string           `json:"name"`
	URL         string           `json:"url,omitempty"`
	Format      model.ListFormat `json:"format,omitempty"`
	Enabled     bool             `json:"enabled"`
	Domains     int              `json:"domains"`
	Skipped     int              `json:"skipped,omitempty"`
	FetchedAt   time.Time        `json:"fetchedAt,omitempty"`
	LastError   string           `json:"lastError,omitempty"`
	LastTriedAt *time.Time       `json:"lastTriedAt,omitempty"`
	// Stale is true when the cache is older than twice the refresh period,
	// which usually means the box cannot reach the publisher.
	Stale bool `json:"stale"`
}

// NewCache returns a cache over dir, reading whatever metadata is there.
func NewCache(dir string) *Cache {
	c := &Cache{Dir: dir, meta: map[string]Meta{}, errs: map[string]cacheError{}}
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
		var m Meta
		if err := json.Unmarshal(raw, &m); err != nil || m.Name == "" {
			continue
		}
		c.meta[m.Name] = m
	}
}

func (c *Cache) namesPath(name string) string { return filepath.Join(c.Dir, safeName(name)+".txt") }
func (c *Cache) metaPath(name string) string  { return filepath.Join(c.Dir, safeName(name)+".json") }

// Save records the names a list gave us. They are sorted by reversed labels
// and reduced: a list that names both example.com and ads.example.com keeps
// only example.com, because blocking is by subtree.
func (c *Cache) Save(m Meta, domains []string) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	reduced := Reduce(domains)
	m.Domains = len(reduced)

	sum := sha256.New()
	tmp, err := os.CreateTemp(c.Dir, "."+safeName(m.Name)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	w := bufio.NewWriterSize(io.MultiWriter(tmp, sum), 64<<10)
	for _, d := range reduced {
		if _, err := w.WriteString(d); err != nil {
			return cleanup(tmp, name, err)
		}
		if err := w.WriteByte('\n'); err != nil {
			return cleanup(tmp, name, err)
		}
	}
	if err := w.Flush(); err != nil {
		return cleanup(tmp, name, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, c.namesPath(m.Name)); err != nil {
		_ = os.Remove(name)
		return err
	}
	m.Sum = hex.EncodeToString(sum.Sum(nil))

	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.metaPath(m.Name), raw, 0o600); err != nil {
		return err
	}
	c.mu.Lock()
	c.meta[m.Name] = m
	delete(c.errs, m.Name)
	c.mu.Unlock()
	return nil
}

func cleanup(f *os.File, name string, err error) error {
	_ = f.Close()
	_ = os.Remove(name)
	return err
}

// Reduce sorts names so a parent comes before everything under it, and
// drops the ones a parent already covers.
func Reduce(domains []string) []string {
	keys := make([]string, 0, len(domains))
	for _, d := range domains {
		keys = append(keys, reverseLabels(d))
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	last := ""
	for _, k := range keys {
		if last != "" && covers(last, k) {
			continue
		}
		last = k
		out = append(out, reverseLabels(k))
	}
	return slices.Clip(out)
}

// Open reads the names of one list, in reversed-label order.
func (c *Cache) Open(name string) (io.ReadCloser, error) {
	return os.Open(c.namesPath(name))
}

// Meta returns what is known about a cached list.
func (c *Cache) Meta(name string) (Meta, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.meta[name]
	return m, ok
}

// Forget drops a list that no longer exists, so its names do not sit on
// disk holding a list nobody asked for.
func (c *Cache) Forget(name string) {
	c.mu.Lock()
	delete(c.meta, name)
	delete(c.errs, name)
	c.mu.Unlock()
	_ = os.Remove(c.namesPath(name))
	_ = os.Remove(c.metaPath(name))
}

// Prune removes cached lists the configuration no longer has.
func (c *Cache) Prune(cfg *model.Config) {
	keep := map[string]bool{}
	for _, l := range cfg.Blocking.Lists {
		keep[l.Name] = true
	}
	c.mu.RLock()
	var gone []string
	for name := range c.meta {
		if !keep[name] {
			gone = append(gone, name)
		}
	}
	c.mu.RUnlock()
	for _, name := range gone {
		c.Forget(name)
	}
}

func (c *Cache) recordError(name string, err error, when time.Time) {
	c.mu.Lock()
	c.errs[name] = cacheError{message: err.Error(), when: when}
	c.mu.Unlock()
}

// FetchedAt reports when a list was last fetched successfully.
func (c *Cache) FetchedAt(name string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m, ok := c.meta[name]
	return m.FetchedAt, ok
}

// Statuses describes every list in the configuration.
func (c *Cache) Statuses(cfg *model.Config) []Status {
	out := []Status{}
	c.mu.RLock()
	defer c.mu.RUnlock()
	now := time.Now()
	for _, l := range cfg.Blocking.Lists {
		st := Status{Name: l.Name, URL: l.URL, Format: l.FormatOrAuto(), Enabled: l.Enabled}
		if m, ok := c.meta[l.Name]; ok {
			st.Domains = m.Domains
			st.Skipped = m.Skipped
			st.FetchedAt = m.FetchedAt
			st.Format = m.Format
			st.Stale = l.URL != "" && now.Sub(m.FetchedAt) > 2*RefreshPeriod(l)
		} else {
			st.Stale = l.URL != ""
		}
		if e, ok := c.errs[l.Name]; ok {
			st.LastError = e.message
			when := e.when
			st.LastTriedAt = &when
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Total is how many names all the enabled lists come to once merged. It is
// the number the UI shows and the number the ceiling applies to.
func (c *Cache) Total(cfg *model.Config) int {
	n := 0
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, l := range cfg.Blocking.EnabledLists() {
		n += c.meta[l.Name].Domains
	}
	return n
}

// RefreshPeriod is how often a list should be fetched.
func RefreshPeriod(l model.BlockList) time.Duration {
	const (
		minRefresh     = time.Hour
		defaultRefresh = 24 * time.Hour
	)
	if l.RefreshHours <= 0 {
		return defaultRefresh
	}
	d := time.Duration(l.RefreshHours) * time.Hour
	if d < minRefresh {
		return minRefresh
	}
	return d
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

// ErrNoSource is returned for a list that has nothing to fetch.
var ErrNoSource = errors.New("this list has no URL to fetch")
