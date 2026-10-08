package dhcplog

import (
	"encoding/json"
	"slices"
	"testing"

	"ostiole/internal/logsearch/logsearchtest"
)

// The router searches what the Log tab shows, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "dhcp") {
		var row struct {
			Event
			Device string `json:"device"`
		}
		if err := json.Unmarshal(c.Entry, &row); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		row.Search(&got, nil, row.Device)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
