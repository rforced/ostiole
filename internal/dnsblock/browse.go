package dnsblock

import (
	"bufio"
	"errors"
	"io/fs"
	"strings"
	"time"
)

// Page is one page of the names a list holds.
type Page struct {
	// Total is every name the list holds, Matches what the search found in
	// all of them.
	Total     int        `json:"total"`
	Matches   int        `json:"matches"`
	Offset    int        `json:"offset"`
	Names     []string   `json:"names"`
	FetchedAt *time.Time `json:"fetchedAt,omitempty"`
}

// Browse returns limit of the names a list holds that match q, from
// offset, in the order the cache keeps them: each name beside the others
// under the same parent. A name matches when it holds q as text or covers
// it, so a search for ads.example.com finds the example.com that blocks
// it. A list never fetched is empty.
func (c *Cache) Browse(name, q string, offset, limit int) (Page, error) {
	page := Page{Offset: offset, Names: []string{}}
	m, ok := c.Meta(name)
	if !ok {
		return page, nil
	}
	page.Total = m.Domains
	if !m.FetchedAt.IsZero() {
		at := m.FetchedAt
		page.FetchedAt = &at
	}
	r, err := c.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return Page{Offset: offset, Names: []string{}}, nil
	}
	if err != nil {
		return page, err
	}
	defer r.Close()
	q = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(q)), ".")
	lines := bufio.NewScanner(r)
	for lines.Scan() {
		n := lines.Text()
		if q != "" && !strings.Contains(n, q) && !under(q, n) {
			continue
		}
		if page.Matches >= offset && len(page.Names) < limit {
			page.Names = append(page.Names, n)
		}
		page.Matches++
		// Without a search every name matches, so the count is known.
		if q == "" && len(page.Names) == limit {
			page.Matches = page.Total
			break
		}
	}
	return page, lines.Err()
}

// under reports whether name sits below parent.
func under(name, parent string) bool {
	return len(name) > len(parent) && strings.HasSuffix(name, parent) && name[len(name)-len(parent)-1] == '.'
}
