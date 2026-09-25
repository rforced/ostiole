package nft

import (
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
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
				// A row below a zone's own rules names that one zone. There
				// can be more than one of them — the port scan tally sits
				// where refused traffic lands, under the rules — so what is
				// checked is that the last of them closes the zone.
				switch {
				case !row.After:
				case len(row.Zones) != 1:
					t.Errorf("row %d (%s) is below a zone's rules but names %v", i, row.Description, row.Zones)
				default:
					zones[row.Zones[0]] = closesZone(row)
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
					t.Errorf("zone %s has no closing row, or something follows it", z)
				}
			}
		})
	}
}

// closesZone reports whether a row is the one that counts what no rule
// matched, which is what has to come last in a zone.
func closesZone(row SystemRule) bool {
	for _, key := range row.Keys {
		if strings.HasSuffix(key, "/zone-unmatched") || strings.HasSuffix(key, "/zone-default") {
			return true
		}
	}
	return false
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

// Time is served on every enabled interface outside an external zone,
// and the row says where, so the rules page can link to the NTP page.
func TestSystemRulesNTP(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/ntp.json")
	rows, err := SystemRules(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := findRow(rows, "Time requests to this firewall")
	if !ok {
		t.Fatal("no row for serving time")
	}
	if got := strings.Join(r.Zones, ","); got != "lan,lab" {
		t.Errorf("zones = %q, want lan,lab: not wan, which is external", got)
	}
	if r.Setting != "ntp" || r.Protocol != "udp" || r.Destination != "this firewall : 123" {
		t.Errorf("row = %+v", r)
	}

	cfg.Services.NTP.Serve = false
	rows, err = SystemRules(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findRow(rows, "Time requests to this firewall"); ok {
		t.Error("a router that serves no time still opens udp/123")
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

// Every automatic rule the NAT page lists is a line in nat_postrouting, and
// every such line is listed, in the same order. The rows are recorded where
// the lines are written; this keeps it so when the renderer changes.
func TestSystemNATMatchesRuleset(t *testing.T) {
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
			var lines []string
			for _, c := range chainComments(t, out.Ruleset)["nat_postrouting"] {
				if strings.HasPrefix(c, "auto-nat:") {
					lines = append(lines, "nat_postrouting/"+c)
				}
			}
			var keys []string
			for i, row := range out.NAT {
				if z, ok := cfg.Zone(row.Zone); !ok || !z.External {
					t.Errorf("row %d names %q, which is not an external zone", i, row.Zone)
				}
				if want := cfg.ZoneInterfaces(row.Zone); !slices.Equal(row.Interfaces, want) {
					t.Errorf("row %d lists links %v, the zone has %v", i, row.Interfaces, want)
				}
				if row.Source == "" || row.Destination == "" {
					t.Errorf("row %d has an empty column: %+v", i, row)
				}
				keys = append(keys, row.Keys...)
			}
			if !slices.Equal(keys, lines) {
				t.Errorf("the rows count %v, the chain has %v", keys, lines)
			}
		})
	}
}

// Automatic and hybrid mode masquerade each external zone that has a link
// up; manual and disabled write nothing of their own.
func TestSystemNATFollowsTheMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		mode  model.OutboundMode
		zones []string
	}{
		{model.OutboundAutomatic, []string{"wan", "wan2"}},
		{model.OutboundHybrid, []string{"wan", "wan2"}},
		{model.OutboundManual, nil},
		{model.OutboundDisabled, nil},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/minimal.json")
			cfg.NAT.Outbound.Mode = tc.mode
			// wan3 is external but has no link up, so it has no rule to show.
			cfg.Zones = append(cfg.Zones,
				model.Zone{Name: "wan2", External: true},
				model.Zone{Name: "wan3", External: true})
			cfg.Interfaces = append(cfg.Interfaces,
				model.Interface{Name: "eth2", Zone: "wan2", Enabled: true, IPv4: model.IPv4{Mode: "dhcp"}, IPv6: model.IPv6{Mode: "none"}},
				model.Interface{Name: "eth3", Zone: "wan3", Enabled: false, IPv4: model.IPv4{Mode: "dhcp"}, IPv6: model.IPv6{Mode: "none"}})
			out, err := Build(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			var zones []string
			for _, row := range out.NAT {
				zones = append(zones, row.Zone)
			}
			if !slices.Equal(zones, tc.zones) {
				t.Fatalf("rows for %v, want %v", zones, tc.zones)
			}
			if len(out.NAT) > 0 {
				want := SystemNAT{
					Zone: "wan", Interfaces: []string{"eth0"}, Source: "any IPv4", Destination: "anywhere",
					Keys: []string{"nat_postrouting/auto-nat:wan"},
				}
				if !reflect.DeepEqual(out.NAT[0], want) {
					t.Errorf("row = %+v, want %+v", out.NAT[0], want)
				}
			}
		})
	}
}
