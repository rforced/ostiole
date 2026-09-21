package nft

import (
	"regexp"
	"testing"

	"github.com/rforced/ostiole/internal/fwlog"
)

// Every log prefix the renderer writes is one the log reader can name,
// and the verdict it carries is the one the rule beside it hands down.
// The two live in different packages and the reader was tested on
// hand-written prefixes: a change to the format here would turn every
// entry on the log page into "other" with both suites green.
func TestEveryLogPrefixIsReadable(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	for i := range cfg.Rules {
		cfg.Rules[i].Log = true
	}
	actions := map[string]string{}
	for _, r := range cfg.Rules {
		actions[r.ID] = string(r.Action)
	}
	out, err := Build(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	prefixes := regexp.MustCompile(`log prefix "([^"]+)"`).FindAllStringSubmatch(out.Ruleset, -1)
	if len(prefixes) < 3 {
		t.Fatalf("only %d log statements rendered:\n%s", len(prefixes), out.Ruleset)
	}
	kinds := map[string]int{}
	seen := map[string]int{}
	for _, m := range prefixes {
		ruleID, zone, kind, action := fwlog.ParsePrefix(m[1])
		if kind == "other" {
			t.Errorf("prefix %q is not one the reader knows", m[1])
			continue
		}
		if action == "" {
			t.Errorf("prefix %q says nothing about what happened to the packet", m[1])
		}
		switch kind {
		case "rule":
			switch {
			case ruleID == "":
				t.Errorf("rule prefix %q carries no rule id", m[1])
			case action != actions[ruleID]:
				t.Errorf("prefix %q logs %q but rule %s is an %s", m[1], action, ruleID, actions[ruleID])
			}
		case "zone-drop":
			if zone == "" {
				t.Errorf("zone-drop prefix %q names no zone", m[1])
			}
		}
		kinds[kind]++
		seen[action]++
	}
	if len(kinds) < 2 {
		t.Errorf("only these kinds were rendered: %v", kinds)
	}
	// An accept that logs is the case the prefix was widened for; without
	// one in the fixture the check above proves nothing.
	if seen["accept"] == 0 {
		t.Errorf("no accepted packet is logged, so the action was never exercised: %v", seen)
	}
}
