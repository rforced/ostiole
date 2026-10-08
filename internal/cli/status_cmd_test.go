package cli

import (
	"strings"
	"testing"

	"ostiole/internal/engine"
)

// `ostiole status --changes` lists the lines as the apply bar does, and
// counts the ones past those.
func TestStatusPrintsTheDrift(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	printDrift(&out, &engine.Drift{
		Parts: []string{"Reverse proxy"},
		Changes: []engine.DriftChange{
			{Path: "proxy/caddy.json", Kind: "removed", Before: "old"},
			{Path: "proxy/caddy.json", Kind: "added", After: "new"},
		},
		More: 3,
	})
	if want := "\n- proxy/caddy.json: old\n+ proxy/caddy.json: new\nand 3 more\n"; out.String() != want {
		t.Errorf("printed %q", out.String())
	}
}
