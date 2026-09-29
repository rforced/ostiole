package dnsblock

import (
	"slices"
	"testing"
	"time"
)

func browsedList(t *testing.T, names ...string) *Cache {
	t.Helper()
	c := NewCache(t.TempDir())
	if err := c.Save(Meta{Name: "ads", FetchedAt: time.Now()}, names); err != nil {
		t.Fatal(err)
	}
	return c
}

// A list reads in the order it is kept, each name beside the others
// under the same parent, with the names a parent covers already gone.
func TestBrowsingAListKeepsNamesUnderTheirParent(t *testing.T) {
	t.Parallel()
	c := browsedList(t, "tracker.net", "ads.example.com", "example.com", "ads.example.org", "metrics.example.org")
	got, err := c.Browse("ads", "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "tracker.net", "ads.example.org", "metrics.example.org"}
	if !slices.Equal(got.Names, want) || got.Total != 4 || got.Matches != 4 || got.FetchedAt == nil {
		t.Errorf("page = %+v, want %v", got, want)
	}
}

// Searching for a name finds the entry that blocks it, which is often
// its parent, as well as the names that hold the text.
func TestSearchingAListFindsWhatBlocksAName(t *testing.T) {
	t.Parallel()
	c := browsedList(t, "example.com", "tracker.net", "ads.example.org", "metrics.example.org", "myexample.com")
	for q, want := range map[string][]string{
		"cdn.ads.EXAMPLE.com.": {"example.com"},
		"example.com":          {"example.com", "myexample.com"},
		"example.org":          {"ads.example.org", "metrics.example.org"},
		"track":                {"tracker.net"},
		"nothing.test":         {},
	} {
		got, err := c.Browse("ads", q, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Names, want) || got.Matches != len(want) || got.Total != 5 {
			t.Errorf("search %q = %+v, want %v", q, got, want)
		}
	}
}

func TestBrowsingAListPages(t *testing.T) {
	t.Parallel()
	c := browsedList(t, "a.test", "b.test", "c.test", "d.test", "e.test")
	for _, tc := range []struct {
		q              string
		offset, limit  int
		want           []string
		matches, total int
	}{
		{"", 0, 2, []string{"a.test", "b.test"}, 5, 5},
		{"", 4, 2, []string{"e.test"}, 5, 5},
		{"", 5, 2, []string{}, 5, 5},
		{"test", 1, 3, []string{"b.test", "c.test", "d.test"}, 5, 5},
	} {
		got, err := c.Browse("ads", tc.q, tc.offset, tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.Names, tc.want) || got.Matches != tc.matches || got.Total != tc.total || got.Offset != tc.offset {
			t.Errorf("%q from %d = %+v, want %v", tc.q, tc.offset, got, tc.want)
		}
	}
}

func TestBrowsingAListNeverFetched(t *testing.T) {
	t.Parallel()
	got, err := NewCache(t.TempDir()).Browse("ads", "", 0, 100)
	if err != nil || got.Names == nil || len(got.Names) != 0 || got.Total != 0 || got.FetchedAt != nil {
		t.Errorf("page = %+v, %v", got, err)
	}
}
