package dnsblock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ostiole/internal/model"
)

// What no list can be read from goes when the cache loads and when it
// prunes: metadata that does not parse, and names with no metadata
// beside them. A list, names a save is still writing, and the record of
// the last merge stay.
func TestCacheDropsFilesThatDoNotParse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := NewCache(dir)
	if err := c.Save(Meta{Name: "ads", FetchedAt: time.Now()}, []string{"ads.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SaveMerged(Result{Domains: 1}); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string, age time.Duration) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	gone := func(paths ...string) {
		t.Helper()
		for _, path := range paths {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s stayed: %v", filepath.Base(path), err)
			}
		}
	}
	kept := []string{c.namesPath("ads"), c.metaPath("ads"), filepath.Join(dir, mergedName),
		write("saving.txt", "saving.example.com\n", 0)}
	stayed := func() {
		t.Helper()
		for _, path := range kept {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s went: %v", filepath.Base(path), err)
			}
		}
	}

	broken := []string{
		write("trackers.json", "{not json", time.Hour),
		write("trackers.txt", "tracker.example.net\n", time.Hour),
		write("unnamed.json", "{}", time.Hour),
		write("orphan.txt", "orphan.example.org\n", time.Hour),
	}
	reopened := NewCache(dir)
	gone(broken...)
	stayed()
	if _, ok := reopened.Meta("ads"); !ok {
		t.Error("the list did not load")
	}
	if _, ok := reopened.Merged(); !ok {
		t.Error("the merge's record did not load")
	}

	late := write("late.txt", "late.example.com\n", time.Hour)
	reopened.Prune(&model.Config{Blocking: model.Blocking{Lists: []model.BlockList{{Name: "ads"}}}})
	gone(late)
	stayed()
}
