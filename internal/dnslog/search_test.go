package dnslog

import (
	"encoding/json"
	"net/netip"
	"slices"
	"testing"

	"ostiole/internal/logsearch/logsearchtest"
)

// The router searches what the Queries tab shows, value for value. The
// case is the row the API sends; the entry behind it is put back together.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "queries") {
		var row struct {
			Client string   `json:"client"`
			Device string   `json:"device"`
			Name   string   `json:"name"`
			Type   string   `json:"type"`
			Status string   `json:"status"`
			Lists  []string `json:"lists"`
			Answer string   `json:"answer"`
		}
		if err := json.Unmarshal(c.Entry, &row); err != nil {
			t.Fatal(err)
		}
		e := Entry{Name: row.Name}
		e.Client, _ = netip.ParseAddr(row.Client)
		e.Answer, _ = netip.ParseAddr(row.Answer)
		e.Type, _ = ParseType(row.Type)
		e.Status, _ = ParseStatus(row.Status)
		// Another list is known to the log, and not carried by this entry.
		lists := append([]string{"unused"}, row.Lists...)
		for i := range row.Lists {
			e.Lists |= 1 << uint(i+1)
		}
		var got logsearchtest.Recorder
		e.Search(&got, nil, row.Device, lists)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
