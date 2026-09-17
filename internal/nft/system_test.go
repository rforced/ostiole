package nft

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// chainComments maps each chain in a rendered ruleset to the comments of
// the rules in it, which is what the counter keys are made of.
func chainComments(t *testing.T, ruleset string) map[string][]string {
	t.Helper()
	open := regexp.MustCompile(`^\tchain (\S+) \{$`)
	comment := regexp.MustCompile(`comment "([^"]+)"`)
	out := map[string][]string{}
	chain := ""
	for _, line := range strings.Split(ruleset, "\n") {
		if m := open.FindStringSubmatch(line); m != nil {
			chain = m[1]
			out[chain] = nil
			continue
		}
		if line == "\t}" {
			chain = ""
			continue
		}
		if chain == "" {
			continue
		}
		if m := comment.FindStringSubmatch(line); m != nil {
			out[chain] = append(out[chain], m[1])
		}
	}
	return out
}

// Every row has to describe a rule that is in the ruleset: its counter keys
// name a chain and a comment that the rendered text carries, its zones are
// zones the configuration has, and the rows come in evaluation order. This
// is what keeps the rules page honest when the renderer changes.
func TestSystemRulesMatchRuleset(t *testing.T) {
	t.Parallel()
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			out, err := Build(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			comments := chainComments(t, out.Ruleset)
			zones := map[string]bool{}
			for _, z := range cfg.Zones {
				zones[z.Name] = false
			}
			rank := -1
			for i, row := range out.System {
				if r := chainRank(row.Chain); r < rank {
					t.Errorf("row %d (%s) is in chain %s, out of evaluation order", i, row.Description, row.Chain)
				} else {
					rank = r
				}
				if row.Action == "" || row.Protocol == "" || row.Source == "" || row.Destination == "" || row.Description == "" {
					t.Errorf("row %d has an empty column: %+v", i, row)
				}
				for _, z := range row.Zones {
					if _, ok := zones[z]; !ok {
						t.Errorf("row %d (%s) names zone %q, which does not exist", i, row.Description, z)
					}
				}
				switch {
				case !row.After:
				case len(row.Zones) != 1:
					t.Errorf("row %d (%s) ends a zone but names %v", i, row.Description, row.Zones)
				case zones[row.Zones[0]]:
					t.Errorf("zone %s ends twice", row.Zones[0])
				default:
					zones[row.Zones[0]] = true
				}
				for _, key := range row.Keys {
					chain, comment, ok := strings.Cut(key, "/")
					if !ok {
						t.Errorf("row %d (%s) has key %q without a chain", i, row.Description, key)
						continue
					}
					found := false
					for _, c := range comments[chain] {
						if c == comment {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("row %d (%s) counts %q, but chain %s carries no such comment", i, row.Description, key, chain)
					}
				}
			}
			for z, ended := range zones {
				if !ended {
					t.Errorf("zone %s has no closing row", z)
				}
			}
		})
	}
}

func findRow(rows []SystemRule, description string) (SystemRule, bool) {
	for _, r := range rows {
		if r.Description == description {
			return r, true
		}
	}
	return SystemRule{}, false
}

func TestSystemRulesFull(t *testing.T) {
	t.Parallel()
	rows, err := SystemRules(loadConfig(t, "testdata/full.json"), nil)
	if err != nil {
		t.Fatal(err)
	}

	lockout, ok := findRow(rows, "Anti-lockout, keeps the web UI and SSH reachable")
	if !ok {
		t.Fatal("no anti-lockout row")
	}
	if got, want := strings.Join(lockout.Zones, ","), "lan"; got != want {
		t.Errorf("anti-lockout zones = %q, want %q", got, want)
	}
	if lockout.Destination != "this firewall : 8443, 22" {
		t.Errorf("anti-lockout destination = %q", lockout.Destination)
	}
	if lockout.Setting != "zone" {
		t.Errorf("anti-lockout setting = %q", lockout.Setting)
	}

	// The DNS service listens on every internal interface unless told
	// otherwise, so the row is on every internal zone's tab.
	dns, ok := findRow(rows, "DNS queries to this firewall")
	if !ok {
		t.Fatal("no DNS row")
	}
	if got, want := strings.Join(dns.Zones, ","), "lan,dmz"; got != want {
		t.Errorf("dns zones = %q, want %q", got, want)
	}

	// A zone that logs its own drops counts them under zone-default; the
	// rest keep a tally of what fell through under zone-unmatched.
	tails := map[string]string{}
	for _, r := range rows {
		if r.After {
			tails[r.Zones[0]] = r.Keys[0]
		}
	}
	if tails["lan"] != "zone_lan/zone-default" || tails["dmz"] != "zone_dmz/zone-unmatched" {
		t.Errorf("zone tails = %v", tails)
	}

	// Nothing here is scoped to no zone at all.
	for _, r := range rows {
		if r.Keys != nil && len(r.Zones) == 0 && !strings.HasPrefix(r.Description, "Replies") {
			t.Errorf("row %q counts packets but names no zone", r.Description)
		}
	}
}

func TestSystemRulesDNSEnforcement(t *testing.T) {
	t.Parallel()
	rows, err := SystemRules(loadConfig(t, "testdata/dns-blocking.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The redirect happens before routing, so it is the first thing shown.
	if rows[0].Action != "redirect" || rows[0].Chain != "nat_prerouting" {
		t.Errorf("first row = %+v, want the DNS redirect", rows[0])
	}
	for _, want := range []string{"DNS over TLS", "DNS over HTTPS servers", "Plain DNS answered by this firewall instead"} {
		r, ok := findRow(rows, want)
		if !ok {
			t.Errorf("no %q row", want)
			continue
		}
		if r.Source != "not @resolver_exempt" {
			t.Errorf("%s source = %q, want the exempt alias excluded", want, r.Source)
		}
		if got := strings.Join(r.Zones, ","); got != "lan" {
			t.Errorf("%s zones = %q, want lan", want, got)
		}
		if r.Setting != "enforcement" {
			t.Errorf("%s setting = %q", want, r.Setting)
		}
	}
}

func TestSystemRulesBlockedSourcesCountBothChains(t *testing.T) {
	t.Parallel()
	rows, err := SystemRules(loadConfig(t, "testdata/blocked-sources.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := findRow(rows, "Block bogon sources, set on eth0")
	if !ok {
		t.Fatal("no bogon row")
	}
	if got, want := strings.Join(r.Keys, " "), "input/block-bogons forward/block-bogons"; got != want {
		t.Errorf("keys = %q, want %q", got, want)
	}
	if got := strings.Join(r.Zones, ","); got != "wan" {
		t.Errorf("zones = %q, want wan", got)
	}
}
