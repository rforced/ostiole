package nft

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadConfig(t *testing.T, path string) *model.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &cfg
}

func goldenCases(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/*.json")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	return matches
}

func TestRenderGolden(t *testing.T) {
	t.Parallel()
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			got, err := Render(cfg)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".nft"
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden %s (run with -update): %v", golden, err)
			}
			if got != string(want) {
				t.Errorf("render mismatch for %s (run with -update to accept)\n--- got ---\n%s", name, got)
			}
		})
	}
}

func TestRenderRejectsInvalid(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{Version: model.SchemaVersion}
	if _, err := Render(cfg); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	a, _ := Render(cfg)
	b, _ := Render(cfg)
	if a != b {
		t.Fatal("render is not deterministic")
	}
}

func TestEmptyRuleset(t *testing.T) {
	t.Parallel()
	s := EmptyRuleset()
	if !strings.Contains(s, "delete table inet ostiole") || strings.Contains(s, "{") {
		t.Errorf("unexpected empty ruleset:\n%s", s)
	}
}

func TestParseCounters(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"nftables":[
	  {"metainfo":{"version":"1.1.6"}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:r2","expr":[{"match":{}},{"counter":{"packets":5,"bytes":500}},{"accept":null}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"zone_lan","comment":"id:r2","expr":[{"counter":{"packets":1,"bytes":10}},{"accept":null}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"input","comment":"default-drop","expr":[{"counter":{"packets":7,"bytes":70}}]}},
	  {"rule":{"family":"inet","table":"ostiole","chain":"input","expr":[{"counter":{"packets":9,"bytes":90}}]}}
	]}`)
	c, err := ParseCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := c["r2"]; got != (Counter{Packets: 6, Bytes: 510}) {
		t.Errorf("r2 = %+v", got)
	}
	if got := c["input/default-drop"]; got != (Counter{Packets: 7, Bytes: 70}) {
		t.Errorf("input/default-drop = %+v", got)
	}
	if len(c) != 2 {
		t.Errorf("len = %d, want 2 (uncommented rules ignored)", len(c))
	}
}
