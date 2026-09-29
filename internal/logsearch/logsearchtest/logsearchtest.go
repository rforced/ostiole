// Package logsearchtest reads the fixtures the logs and their pages share:
// the values each row shows, so the router searches what the page shows.
package logsearchtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Recorder keeps the values a row is given, the empty ones left out as
// the page leaves them out.
type Recorder []string

// Add implements logsearch.Adder.
func (r *Recorder) Add(v string) {
	if v != "" {
		*r = append(*r, v)
	}
}

// AddBytes implements logsearch.Adder.
func (r *Recorder) AddBytes(v []byte) {
	if len(v) > 0 {
		*r = append(*r, string(v))
	}
}

// Case is one row of a log: what the API sends for it and what its page
// shows, value by value.
type Case struct {
	Why    string            `json:"why"`
	Entry  json.RawMessage   `json:"entry"`
	Access map[string]string `json:"access"`
	Values []string          `json:"values"`
}

// Cases reads the rows of one log from web/src/lib/__tests__/log-values.json.
func Cases(t *testing.T, log string) []Case {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "../../../web/src/lib/__tests__/log-values.json"))
	if err != nil {
		t.Fatal(err)
	}
	var all map[string][]Case
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	if len(all[log]) == 0 {
		t.Fatalf("no cases for %s", log)
	}
	return all[log]
}
