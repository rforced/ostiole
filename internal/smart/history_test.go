package smart

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/rforced/ostiole/internal/logsearch/logsearchtest"
)

// The router searches what the History card shows, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "drives") {
		var r Reading
		if err := json.Unmarshal(c.Entry, &r); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		r.Search(&got, nil)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
