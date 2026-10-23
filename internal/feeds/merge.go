package feeds

import (
	"net/netip"
	"slices"
	"sort"

	"ostiole/internal/model"
)

// merge is lists as one, sorted as the cache keeps them: each entry once,
// and a list of addresses as the fewest networks that cover it. The set
// matches the same addresses either way; fewer entries is less for nft to
// load and the cache to hold. pfBlockerNG calls this CIDR aggregation.
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
	out = aggregate(out)
	sort.Strings(out)
	return out
}

// aggregate collapses a list of addresses. One holding anything else, a
// port list, is left as it is.
func aggregate(entries []string) []string {
	nets := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		p, err := model.ParseAddress(e)
		if err != nil {
			return entries
		}
		// Merged into, a default route would cover the whole list.
		if p.Bits() > 0 {
			nets = append(nets, p)
		}
	}
	nets = collapse(nets)
	if len(nets) == len(entries) {
		return entries
	}
	out := make([]string, 0, len(nets))
	for _, p := range nets {
		out = append(out, prefixString(p))
	}
	return out
}

// collapse is the fewest networks that cover what nets covers, which it
// reorders. Two halves are never joined into a default route, which no
// list may hold.
func collapse(nets []netip.Prefix) []netip.Prefix {
	for i, p := range nets {
		nets[i] = p.Masked()
	}
	slices.SortFunc(nets, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	// Written over nets in place: out never grows past the element being read.
	out := nets[:0]
	for _, p := range nets {
		if n := len(out); n > 0 && out[n-1].Bits() <= p.Bits() && out[n-1].Contains(p.Addr()) {
			continue
		}
		out = append(out, p)
		for n := len(out); n >= 2; n = len(out) {
			a, b := out[n-2], out[n-1]
			if a.Bits() != b.Bits() || a.Bits() <= 1 {
				break
			}
			parent := netip.PrefixFrom(a.Addr(), a.Bits()-1).Masked()
			if parent.Addr() != a.Addr() || !parent.Contains(b.Addr()) {
				break
			}
			out = append(out[:n-2], parent)
		}
	}
	return out
}
