package feeds

import (
	"cmp"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"ostiole/internal/model"
)

// Page is one page of what an alias fetched.
type Page struct {
	// Total is everything the alias holds, Matches what the search found
	// in all of it.
	Total     int       `json:"total"`
	Matches   int       `json:"matches"`
	Offset    int       `json:"offset"`
	Entries   []string  `json:"entries"`
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
}

// Browse returns limit of the entries an alias fetched that match q,
// from offset, in address order. An address finds the networks that hold
// it, a network the ones it overlaps, a port the ranges that hold it, and
// anything else is looked for as text. A list never fetched is empty.
func (c *Cache) Browse(alias, q string, offset, limit int) Page {
	entries, fetchedAt := c.ordered(alias)
	page := Page{Total: len(entries), Offset: offset, Entries: []string{}, FetchedAt: fetchedAt}
	match := matcher(q)
	for _, e := range entries {
		if match != nil && !match(e) {
			continue
		}
		if page.Matches >= offset && len(page.Entries) < limit {
			page.Entries = append(page.Entries, e)
		}
		page.Matches++
	}
	return page
}

// ordered is an alias's entries in address order, sorted on first use and
// kept until the next fetch replaces them.
func (c *Cache) ordered(alias string) ([]string, time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.loaded[alias]
	if !ok {
		return nil, time.Time{}
	}
	o, ok := c.order[alias]
	if !ok {
		o = inOrder(f.Entries)
		c.order[alias] = o
	}
	return o, f.FetchedAt
}

// inOrder sorts a copy of entries the way they are read: IPv4 before IPv6,
// each by address and then by length, and ports by number.
func inOrder(entries []string) []string {
	type keyed struct {
		entry  string
		prefix netip.Prefix
		ports  model.PortRange
		kind   int // 0 an address or network, 1 ports, 2 neither
	}
	keys := make([]keyed, len(entries))
	for i, e := range entries {
		keys[i] = keyed{entry: e, kind: 2}
		if p, err := model.ParseAddress(e); err == nil {
			keys[i].prefix, keys[i].kind = p, 0
		} else if r, err := model.ParsePortRange(e); err == nil {
			keys[i].ports, keys[i].kind = r, 1
		}
	}
	slices.SortFunc(keys, func(a, b keyed) int {
		if c := cmp.Compare(a.kind, b.kind); c != 0 {
			return c
		}
		switch a.kind {
		case 0:
			if c := a.prefix.Addr().Compare(b.prefix.Addr()); c != 0 {
				return c
			}
			return cmp.Compare(a.prefix.Bits(), b.prefix.Bits())
		case 1:
			if c := cmp.Compare(a.ports.Lo, b.ports.Lo); c != 0 {
				return c
			}
			return cmp.Compare(a.ports.Hi, b.ports.Hi)
		}
		return strings.Compare(a.entry, b.entry)
	})
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k.entry
	}
	return out
}

// matcher is what a search asks of each entry, or nil for everything.
func matcher(q string) func(string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	if addr, err := netip.ParseAddr(q); err == nil {
		return func(e string) bool {
			p, err := model.ParseAddress(e)
			return err == nil && p.Contains(addr)
		}
	}
	if net, err := netip.ParsePrefix(q); err == nil {
		net = net.Masked()
		return func(e string) bool {
			p, err := model.ParseAddress(e)
			return err == nil && p.Overlaps(net)
		}
	}
	port, err := strconv.ParseUint(q, 10, 16)
	return func(e string) bool {
		if strings.Contains(e, q) {
			return true
		}
		r, perr := model.ParsePortRange(e)
		return err == nil && perr == nil && uint64(r.Lo) <= port && port <= uint64(r.Hi)
	}
}
