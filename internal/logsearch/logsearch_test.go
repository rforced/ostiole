package logsearch

import (
	"encoding/json"
	"os"
	"testing"
)

// searchCase is one line of the table matches() in web/src/lib/search.js is
// tested against as well.
type searchCase struct {
	Why    string   `json:"why"`
	Query  string   `json:"query"`
	Values []string `json:"values"`
	MACs   []string `json:"macs"`
	Match  bool     `json:"match"`
}

func TestMatchesAgreesWithThePage(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../web/src/lib/__tests__/search-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []searchCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 10 {
		t.Fatalf("only %d cases", len(cases))
	}
	for _, c := range cases {
		if got := Matches(c.Query, c.Values, c.MACs); got != c.Match {
			t.Errorf("%s: Matches(%q, %q, %q) = %v", c.Why, c.Query, c.Values, c.MACs, got)
		}
	}
}

// A search walks a million entries in a second at most, so matching one
// must not allocate: the garbage would cost more than the search.
func TestMatchingAllocatesNothing(t *testing.T) {
	q := Parse("drop 203.0.113 aa-bb-cc")
	buf := []byte("203.0.113.9:443")
	allocs := testing.AllocsPerRun(1000, func() {
		row := q.Row()
		row.Add("DROP")
		row.AddBytes(buf)
		row.AddMAC("aa:bb:cc:dd:ee:ff")
		if !row.Match() {
			t.Fatal("no match")
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations a row", allocs)
	}
}
