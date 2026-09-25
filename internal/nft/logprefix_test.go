package nft

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/fwlog"
)

var logPrefixRe = regexp.MustCompile(`log prefix "([^"]+)"`)

// Every log prefix the renderer writes is one the log reader can name,
// and the verdict it carries is the one the rule beside it hands down.
// The two live in different packages and the reader was tested on
// hand-written prefixes: a change to the format here would turn every
// entry on the log page into "other" with both suites green.
//
// Every fixture is walked, not one: a prefix is only rendered when the
// configuration asks for it, so checking a single fixture proves nothing
// about the kinds it happens not to switch on.
func TestEveryLogPrefixIsReadable(t *testing.T) {
	t.Parallel()
	kinds := map[string]int{}
	actionsSeen := map[string]int{}
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		cfg := loadConfig(t, in)
		for i := range cfg.Rules {
			cfg.Rules[i].Log = true
		}
		actions := map[string]string{}
		for _, r := range cfg.Rules {
			actions[r.ID] = string(r.Action)
		}
		out, err := Build(cfg, nil)
		if err != nil {
			t.Fatalf("%s: Build: %v", name, err)
		}
		for _, m := range logPrefixRe.FindAllStringSubmatch(out.Ruleset, -1) {
			ruleID, zone, kind, action := fwlog.ParsePrefix(m[1])
			if kind == "other" {
				t.Errorf("%s: prefix %q is not one the reader knows", name, m[1])
				continue
			}
			if action == "" {
				t.Errorf("%s: prefix %q says nothing about what happened to the packet", name, m[1])
			}
			switch kind {
			case "rule":
				switch {
				case ruleID == "":
					t.Errorf("%s: rule prefix %q carries no rule id", name, m[1])
				case action != actions[ruleID]:
					t.Errorf("%s: prefix %q logs %q but rule %s is an %s",
						name, m[1], action, ruleID, actions[ruleID])
				}
			case "zone-drop":
				if zone == "" {
					t.Errorf("%s: zone-drop prefix %q names no zone", name, m[1])
				}
			}
			kinds[kind]++
			actionsSeen[action]++
		}
	}
	// The fixtures between them have to exercise every shape, or a broken
	// one goes unnoticed until a router renders it.
	for _, want := range []string{"rule", "zone-drop", "default-drop", "block-private", "block-bogons"} {
		if kinds[want] == 0 {
			t.Errorf("no fixture renders a %s prefix: %v", want, kinds)
		}
	}
	for kind := range fwlog.SystemKinds {
		if kinds[kind] == 0 {
			t.Errorf("no fixture renders a %s prefix: %v", kind, kinds)
		}
	}
	// An accept that logs is the case the prefix was widened for; without
	// one in the fixtures the check above proves nothing.
	if actionsSeen["accept"] == 0 {
		t.Errorf("no accepted packet is logged, so the action was never exercised: %v", actionsSeen)
	}
}

// The drops Ostiole makes on its own account obey the same setting as the
// drop at the end of a zone's chain, and every one of them is sampled: a
// held scanner sends as fast as it likes and the log has to stay readable.
func TestSystemDropsLogWhereTheZoneSaysSo(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/system-drop-logs.json")
	out, err := Build(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"block-dot", "block-doh", "protect-scanner", "protect-synflood", "protect-icmpflood"} {
		prefix := "ostiole:s:" + kind + ":drop: "
		if !strings.Contains(out.Ruleset, prefix) {
			t.Errorf("%s is never logged:\n%s", kind, out.Ruleset)
		}
	}
	// Every log statement of a system drop is rate limited; the drop beside
	// it is not, so the counter stays the true figure.
	for line := range strings.SplitSeq(out.Ruleset, "\n") {
		if !strings.Contains(line, "ostiole:s:") {
			continue
		}
		if !strings.Contains(line, "limit rate "+SystemLogRate) {
			t.Errorf("a system drop logs unsampled: %s", strings.TrimSpace(line))
		}
		// A sampled rule must hand down no verdict, or a packet over the
		// rate would fail the match and escape the drop entirely.
		for _, verdict := range []string{" drop", " accept", " reject"} {
			if strings.Contains(line, verdict) {
				t.Errorf("a sampled log rule carries a verdict: %s", strings.TrimSpace(line))
			}
		}
	}
	// The guest zone logs nothing, so the chain both zones share has to tell
	// them apart: the log statement carries the interface set, the drop does
	// not.
	for _, want := range []string{
		`iifname "eth1" meta l4proto { tcp, udp } th dport 853 limit rate`,
		`meta l4proto { tcp, udp } th dport 853 counter drop comment "block:dot"`,
	} {
		if !strings.Contains(out.Ruleset, want) {
			t.Errorf("missing %q in:\n%s", want, out.Ruleset)
		}
	}
	if strings.Contains(out.Ruleset, `chain plog_guest_`) {
		t.Error("a zone that logs nothing got a log chain")
	}
	// The rows say which of them log, so the rules page is honest about it.
	rows := map[string]bool{}
	for _, s := range out.System {
		if s.Setting == "enforcement" || s.Setting == "protection" {
			rows[s.Description] = s.Log
		}
	}
	for want, log := range map[string]bool{"DNS over TLS": true, "DNS over HTTPS servers": true} {
		got, ok := rows[want]
		if !ok {
			t.Errorf("no system row for %q", want)
		} else if got != log {
			t.Errorf("row %q says log=%v, want %v", want, got, log)
		}
	}
}

// A zone that logs nothing renders exactly what it rendered before the
// setting learned to reach these drops: no log statements, and the flood
// drops still inline in the zone chain rather than in a chain of their own.
func TestSystemDropsStaySilentWithoutTheSetting(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/system-drop-logs.json")
	for i := range cfg.Zones {
		cfg.Zones[i].LogDrops = false
	}
	out, err := Build(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Ruleset, "ostiole:s:") {
		t.Errorf("a system drop logs with the setting off:\n%s", out.Ruleset)
	}
	if strings.Contains(out.Ruleset, "chain plog_") {
		t.Errorf("a log chain was rendered with the setting off:\n%s", out.Ruleset)
	}
	for _, s := range out.System {
		if s.Setting == "protection" && s.Log {
			t.Errorf("row %q claims to log with the setting off", s.Description)
		}
	}
	// The counter keys of the inline drops are the ones the rules page reads.
	for _, want := range []string{"zone_lan/protect:synflood", "zone_lan/protect:icmpflood"} {
		found := false
		for _, s := range out.System {
			for _, k := range s.Keys {
				if k == want {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("no system row counts %s", want)
		}
	}
}

// With every internal zone logging — the usual router, one LAN — the log
// statement needs no interface set: everything that reaches the shared
// chain logs, so scoping it would match everything and cost a comparison
// per packet for nothing.
func TestSharedChainDropsTheSetWhenEveryZoneLogs(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/system-drop-logs.json")
	for i := range cfg.Zones {
		cfg.Zones[i].LogDrops = true
	}
	out, err := Build(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(out.Ruleset, "\n") {
		if strings.Contains(line, "ostiole:s:block-") && strings.Contains(line, "iifname") {
			t.Errorf("the log statement is scoped when it need not be: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(out.Ruleset, `th dport 853 limit rate`) {
		t.Errorf("the unscoped log statement is missing:\n%s", out.Ruleset)
	}
}
