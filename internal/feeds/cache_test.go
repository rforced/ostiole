package feeds

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ostiole/internal/model"
)

// A cached feed that does not parse goes when the cache loads: nothing
// reads it, and Prune knows only the aliases that load.
func TestLoadDropsFeedsThatDoNotParse(t *testing.T) {
	t.Parallel()
	cache := NewCache(t.TempDir())
	if err := cache.Save(model.Alias{Name: "partners"}, []Part{{Source: "http://example.invalid/list", Entries: 1}}, []string{"192.0.2.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(cache.Dir, "broken.json")
	unnamed := filepath.Join(cache.Dir, "unnamed.json")
	for path, body := range map[string]string{broken: "{not json", unnamed: "{}"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := NewCache(cache.Dir).Entries(); len(got) != 1 || len(got["partners"]) != 1 {
		t.Errorf("entries = %v, want the one alias", got)
	}
	for _, path := range []string{broken, unnamed} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s stayed: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(cache.path("partners")); err != nil {
		t.Errorf("the alias's own file went: %v", err)
	}
}
