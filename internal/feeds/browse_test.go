package feeds

import (
	"slices"
	"testing"
	"time"

	"ostiole/internal/model"
)

func browsed(t *testing.T, entries ...string) *Cache {
	t.Helper()
	c := NewCache(t.TempDir())
	if err := c.Save(model.Alias{Name: "list", Type: model.AliasHosts, URL: "https://example.test/list"}, nil, entries, time.Now()); err != nil {
		t.Fatal(err)
	}
	return c
}

// A fetched list reads in address order, not the text order it is kept in.
func TestBrowsingAnAliasReadsItInAddressOrder(t *testing.T) {
	t.Parallel()
	c := browsed(t, "10.0.0.0/8", "192.0.2.0/24", "2.0.0.0/8", "2001:db8::/32", "9.9.9.9", "2.0.0.0/7")
	got := c.Browse("list", "", 0, 100)
	want := []string{"2.0.0.0/7", "2.0.0.0/8", "9.9.9.9", "10.0.0.0/8", "192.0.2.0/24", "2001:db8::/32"}
	if !slices.Equal(got.Entries, want) || got.Total != 6 || got.Matches != 6 {
		t.Errorf("page = %+v, want %v", got, want)
	}
	if got.FetchedAt.IsZero() {
		t.Error("the page does not say when the list was fetched")
	}
}

// Searching for an address finds the network that holds it, which is the
// question someone looking at a country list is asking.
func TestSearchingAnAliasFindsWhatHoldsAnAddress(t *testing.T) {
	t.Parallel()
	c := browsed(t, "10.0.0.0/8", "192.0.2.0/24", "198.51.100.7", "2001:db8::/32")
	for q, want := range map[string][]string{
		"192.0.2.77":  {"192.0.2.0/24"},
		"2001:DB8::1": {"2001:db8::/32"},
		"10.1.0.0/16": {"10.0.0.0/8"},
		"0.0.0.0/1":   {"10.0.0.0/8"},
		"198.51":      {"198.51.100.7"},
		"203.0.113.9": {},
	} {
		got := c.Browse("list", q, 0, 100)
		if !slices.Equal(got.Entries, want) || got.Matches != len(want) || got.Total != 4 {
			t.Errorf("search %q = %+v, want %v", q, got, want)
		}
	}
}

func TestSearchingAPortListFindsTheRangeThatHoldsAPort(t *testing.T) {
	t.Parallel()
	c := browsed(t, "8443", "1000-2000", "80", "443")
	if got := c.Browse("list", "", 0, 100).Entries; !slices.Equal(got, []string{"80", "443", "1000-2000", "8443"}) {
		t.Errorf("order = %v", got)
	}
	if got := c.Browse("list", "1500", 0, 100).Entries; !slices.Equal(got, []string{"1000-2000"}) {
		t.Errorf("port 1500 = %v", got)
	}
	if got := c.Browse("list", "44", 0, 100).Entries; !slices.Equal(got, []string{"443", "8443"}) {
		t.Errorf("text 44 = %v", got)
	}
}

// Pages count every match, so the dialog can say where it is in the list.
func TestBrowsingAnAliasPages(t *testing.T) {
	t.Parallel()
	c := browsed(t, "192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "198.51.100.1")
	got := c.Browse("list", "192.0.2", 2, 2)
	if !slices.Equal(got.Entries, []string{"192.0.2.3", "192.0.2.4"}) || got.Matches != 5 || got.Offset != 2 || got.Total != 6 {
		t.Errorf("third and fourth = %+v", got)
	}
	if got := c.Browse("list", "192.0.2", 5, 2); len(got.Entries) != 0 || got.Matches != 5 {
		t.Errorf("past the end = %+v", got)
	}
}

// A refresh replaces what is shown, and a list never fetched is empty.
func TestBrowsingFollowsTheLatestFetch(t *testing.T) {
	t.Parallel()
	c := browsed(t, "192.0.2.0/24")
	c.Browse("list", "", 0, 100)
	if err := c.Save(model.Alias{Name: "list", Type: model.AliasHosts}, nil, []string{"198.51.100.0/24"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := c.Browse("list", "", 0, 100).Entries; !slices.Equal(got, []string{"198.51.100.0/24"}) {
		t.Errorf("after a refresh = %v", got)
	}
	got := c.Browse("other", "", 0, 100)
	if got.Entries == nil || len(got.Entries) != 0 || got.Total != 0 || !got.FetchedAt.IsZero() {
		t.Errorf("never fetched = %+v", got)
	}
}
