package dnslog

import (
	"testing"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
)

// testCache writes two small lists with nested and overlapping entries.
func testCache(t *testing.T) (*dnsblock.Cache, dnsblock.Options) {
	t.Helper()
	c := dnsblock.NewCache(t.TempDir())
	// ads.example.com is under example.com, so the reduce drops it and the
	// index has to find the child through its parent.
	if err := c.Save(dnsblock.Meta{Name: "one"}, []string{
		"example.com", "ads.example.com", "tracker.example.net", "single",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Save(dnsblock.Meta{Name: "two"}, []string{
		"example.com", "metrics.example.org",
	}); err != nil {
		t.Fatal(err)
	}
	o := dnsblock.Options{
		Enabled:   true,
		UseLists:  true,
		Mode:      model.BlockNXDomain,
		Lists:     []string{"one", "two"},
		Allow:     []string{"good.example.com"},
		Deny:      []string{"bad.example.org"},
		Never:     []string{"lan"},
		Delegated: []string{"ts.net"},
		Canary:    true,
	}
	return c, o
}

// The query log decides what was blocked from the index; the Blocking page
// decides it from the files. They have to agree on every shape a name can
// take, or the log accuses the wrong list.
func TestIndexDecidesLikeLookup(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	x, err := Build(o, c)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{
		"example.com",              // listed by both
		"ads.example.com",          // a child of a listed parent
		"deep.ads.example.com",     // deeper still
		"good.example.com",         // an allow carve-out under a listed parent
		"sub.good.example.com",     // and under the carve-out
		"tracker.example.net",      // one list only
		"metrics.example.org",      // the other list only
		"single",                   // a one-label name
		"bad.example.org",          // the deny list
		"sub.bad.example.org",      // under the deny list
		"host.lan",                 // a name this router answers for
		"peer.ts.net",              // delegated
		"use-application-dns.net",  // the canary
		"unlisted.example",         // nothing has it
		"notexample.com",           // shares a prefix with a listed name
		"example.com.evil.example", // holds a listed name as a label
		"",                         // not a name at all
	}
	for _, name := range names {
		want := dnsblock.Lookup(o, c, name)
		got := x.Decide(name)
		if got.Blocked != want.Blocked || got.Reason != want.Reason || got.Matched != want.Matched {
			t.Errorf("%q: index %+v, lookup %+v", name, got, want)
		}
		if len(got.Lists) != len(want.Lists) {
			t.Errorf("%q: index lists %v, lookup %v", name, got.Lists, want.Lists)
			continue
		}
		for i := range got.Lists {
			if got.Lists[i] != want.Lists[i] {
				t.Errorf("%q: index lists %v, lookup %v", name, got.Lists, want.Lists)
				break
			}
		}
	}
}

// A list that is switched off is not in Options, so nothing is indexed for
// it and nothing is attributed to it.
func TestIndexHoldsOnlyTheListsItWasGiven(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	o.Lists = []string{"two"}
	x, err := Build(o, c)
	if err != nil {
		t.Fatal(err)
	}
	f := x.Decide("tracker.example.net")
	if f.Blocked {
		t.Errorf("tracker.example.net blocked by %v", f.Lists)
	}
	if n := x.Names(); n != 2 {
		t.Errorf("names = %d, want 2", n)
	}
}

// A list applied but not fetched yet has no file. The lists that do have
// one are still indexed, so attribution does not stop for all six because
// a seventh was added a minute ago.
func TestIndexSkipsAListWithNoFileYet(t *testing.T) {
	t.Parallel()
	c, o := testCache(t)
	o.Lists = append(o.Lists, "never-fetched")
	x, err := Build(o, c)
	if err != nil {
		t.Fatal(err)
	}
	if f := x.Decide("tracker.example.net"); !f.Blocked || f.Matched != "tracker.example.net" {
		t.Errorf("tracker.example.net: %+v", f)
	}
}
