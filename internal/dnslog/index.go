// Package dnslog keeps what this router's resolver answered: a bounded
// in-memory log fed by the kernel, and the index that says which blocklist
// carried each refused name. Nothing here reaches the disk.
package dnslog

import (
	"bufio"
	"errors"
	"fmt"
	"hash/maphash"
	"io/fs"
	"slices"
	"strings"

	"github.com/rforced/ostiole/internal/dnsblock"
)

// Index answers "which list holds this name" from memory: one sorted slice
// of 64-bit hashes per enabled list, eight bytes a name. The six lists on
// the router this was designed on come to 3.3 million names, so 26 MB; one
// list is a few. It is built off the packet path and swapped in whole.
type Index struct {
	opts  dnsblock.Options
	seed  maphash.Seed
	lists map[string][]uint64
}

// Build reads every enabled list out of the cache.
func Build(o dnsblock.Options, c *dnsblock.Cache) (*Index, error) {
	x := &Index{opts: o, seed: maphash.MakeSeed(), lists: make(map[string][]uint64, len(o.Lists))}
	if c == nil {
		return x, nil
	}
	for _, list := range o.Lists {
		hashes, err := x.read(c, list)
		switch {
		// A list that has been applied but not fetched yet has no file.
		// Indexing the other five is better than indexing none.
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return nil, fmt.Errorf("index %s: %w", list, err)
		}
		x.lists[list] = hashes
	}
	return x, nil
}

// Names is how many names the index holds, which is what it costs eight
// bytes each of.
func (x *Index) Names() int {
	var n int
	for _, h := range x.lists {
		n += len(h)
	}
	return n
}

// Decide is dnsblock.Decide over the hashes.
func (x *Index) Decide(name string) dnsblock.Finding {
	return dnsblock.Decide(x.opts, x.has, name)
}

// Options are the inputs the index was built from, so a caller can tell
// whether a rebuild is due.
func (x *Index) Options() dnsblock.Options { return x.opts }

// read hashes one cached list. The file holds one name per line, sorted by
// reversed labels and already reduced.
func (x *Index) read(c *dnsblock.Cache, list string) ([]uint64, error) {
	rc, err := c.Open(list)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var out []uint64
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		out = append(out, maphash.String(x.seed, dnsblock.ReverseLabels(line)))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	slices.Sort(out)
	return slices.Clip(slices.Compact(out)), nil
}

// has walks the parents of key, topmost first, and looks each up. The
// cached lists are reduced, so a child is held by its parent and the
// shortest parent that hits is the entry a file scan would have returned.
func (x *Index) has(list, key string) (string, bool) {
	hashes := x.lists[list]
	if len(hashes) == 0 {
		return "", false
	}
	for end := 1; end <= len(key); end++ {
		if end != len(key) && key[end] != '.' {
			continue
		}
		parent := key[:end]
		if _, found := slices.BinarySearch(hashes, maphash.String(x.seed, parent)); found {
			return dnsblock.ReverseLabels(parent), true
		}
	}
	return "", false
}
