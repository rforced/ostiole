package fwlog

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/rforced/ostiole/internal/logsearch/logsearchtest"
)

// The router searches what the Logs page shows, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "firewall") {
		var e Entry
		if err := json.Unmarshal(c.Entry, &e); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		e.Search(&got, nil, func(id string) string { return c.Access[id] })
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
