package waflog

import (
	"encoding/json"
	"slices"
	"testing"

	"ostiole/internal/logsearch/logsearchtest"
)

// The router searches what the Events tab shows, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "events") {
		var e Entry
		if err := json.Unmarshal(c.Entry, &e); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		e.Search(&got, nil)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
