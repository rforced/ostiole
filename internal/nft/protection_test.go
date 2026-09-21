package nft

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// protectedConfig is a router with the whole edge defence on, and one
// rule that holds SSH to three attempts a minute per source.
func protectedConfig(t *testing.T) *model.Config {
	t.Helper()
	return loadConfig(t, "testdata/protection.json")
}

func TestProtectionRendersOnePairOfRulesPerZone(t *testing.T) {
	t.Parallel()
	out, err := Render(protectedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	// The defence is on the external zone only, because that is what an
	// empty protection.zones means.
	for _, want := range []string{
		`ip saddr @scanners_wan_v4 counter drop comment "protect:scanner"`,
		`ct state new add @synflood_wan_v4 { ip saddr limit rate over 30/second burst 60 packets } counter drop comment "protect:synflood"`,
		`ct state new add @synflood_wan_v6 { ip6 saddr limit rate over 30/second burst 60 packets } counter drop comment "protect:synflood"`,
		`icmp type echo-request add @icmpflood_wan_v4 { ip saddr limit rate over 10/second burst 20 packets } counter drop comment "protect:icmpflood"`,
		`icmpv6 type echo-request add @icmpflood_wan_v6 { ip6 saddr limit rate over 10/second burst 20 packets } counter drop comment "protect:icmpflood"`,
		`add @scan_wan_v4 { ip saddr limit rate over 20/minute } add @scanners_wan_v4 { ip saddr } counter comment "protect:scan"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ruleset is missing:\n%s", want)
		}
	}
	if strings.Contains(out, "scanners_lan") || strings.Contains(out, "synflood_lan") {
		t.Error("the inside of the network was defended against itself")
	}
	// The order is the point: a source already held is dropped before
	// anything else in the zone is evaluated, and the tally that holds it
	// comes after the operator's rules, where refused traffic lands.
	zone := zoneChain(t, out, "wan")
	held := strings.Index(zone, `comment "protect:scanner"`)
	flood := strings.Index(zone, `comment "protect:synflood"`)
	rule := strings.Index(zone, `comment "id:ssh-in"`)
	tally := strings.Index(zone, `comment "protect:scan"`)
	if held > flood || flood > rule || rule > tally {
		t.Errorf("rules are in the wrong order (held %d, flood %d, rule %d, tally %d):\n%s",
			held, flood, rule, tally, zone)
	}
}

// A limit per source is two rules: one that drops what one source has
// sent too much of, and the rule itself for everything else. A limit for
// everybody together is one rule, because nftables can match "under the
// rate" directly.
func TestRuleLimits(t *testing.T) {
	t.Parallel()
	out, err := Render(protectedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`fib daddr type local tcp dport 22 add @rlimit_ssh-in_v4 { ip saddr limit rate over 3/minute burst 3 packets } counter drop comment "limit:ssh-in"`,
		`fib daddr type local tcp dport 22 counter accept comment "id:ssh-in"`,
		`ip daddr 192.168.1.10 tcp dport 443 limit rate 500/second burst 1000 packets counter accept comment "id:web-in"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ruleset is missing:\n%s", want)
		}
	}
	// The per-source guard has to come before the rule it guards, or the
	// rule accepts the traffic the guard was meant to drop.
	zone := zoneChain(t, out, "wan")
	if strings.Index(zone, `comment "limit:ssh-in"`) > strings.Index(zone, `comment "id:ssh-in"`) {
		t.Errorf("the limit is after the rule it limits:\n%s", zone)
	}
	// A rule with no limit has no set and no extra rule.
	if strings.Contains(out, "rlimit_allow-lan") {
		t.Error("a rule with no limit got a set")
	}
	// The cap for everybody together needs no set at all.
	if strings.Contains(out, "rlimit_web-in") {
		t.Error("a limit that is not per source made a per-source set")
	}
}

// A rule whose addresses pin one family gets a limit for that family and
// nothing for the other: a v6 set for a v4-only rule is a set nothing
// ever adds to.
func TestRuleLimitFollowsTheFamily(t *testing.T) {
	t.Parallel()
	cfg := protectedConfig(t)
	for i := range cfg.Rules {
		if cfg.Rules[i].ID == "ssh-in" {
			cfg.Rules[i].Source = model.Endpoint{Addresses: []string{"203.0.113.0/24"}}
		}
	}
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "add @rlimit_ssh-in_v4") {
		t.Error("the v4 limit is missing")
	}
	if strings.Contains(out, "add @rlimit_ssh-in_v6 {") {
		t.Error("a v6 rule was written for a rule that can only match v4")
	}
}

func TestProtectionZonesCanBeNamed(t *testing.T) {
	t.Parallel()
	cfg := protectedConfig(t)
	cfg.Protection.Zones = []string{"lan"}
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "synflood_lan_v4") {
		t.Error("a named zone was not defended")
	}
	if strings.Contains(out, "synflood_wan_v4") {
		t.Error("a zone that was not named was defended anyway")
	}
}

func TestProtectionOffRendersNothing(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/minimal.json")
	out, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"synflood", "icmpflood", "scanners_", "rlimit_", "limit rate"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("a router with no protection rendered %q", unwanted)
		}
	}
}

// The sets have to be real sets in a real kernel: dynamic, with a
// timeout, and writable from userspace — which is what lets the hold list
// be filled by the packet path and read back by the page.
func TestProtectionSetsHoldASourceInKernel(t *testing.T) {
	namespaced(t) // skip where namespaces are unavailable
	ruleset, err := Render(protectedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	// One namespace, one shell: each unshare is a fresh network stack, so
	// loading and reading back have to happen together.
	script := "nft -f - <<'EOF'\n" + ruleset + "EOF\n" +
		"nft add element inet ostiole scanners_wan_v4 '{ 203.0.113.5 }' && " +
		"nft -j list set inet ostiole scanners_wan_v4"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("load and hold a source failed: %v\n%s", err, out)
	}
	var doc struct {
		Nftables []struct {
			Set *struct {
				Name    string   `json:"name"`
				Type    string   `json:"type"`
				Flags   []string `json:"flags"`
				Timeout int      `json:"timeout"`
				Elem    []any    `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("parse set: %v\n%s", err, out)
	}
	for _, item := range doc.Nftables {
		if item.Set == nil || item.Set.Name != "scanners_wan_v4" {
			continue
		}
		if item.Set.Timeout != 600 {
			t.Errorf("hold timeout = %ds, want 600 (10m)", item.Set.Timeout)
		}
		if !strings.Contains(strings.Join(item.Set.Flags, ","), "dynamic") {
			t.Errorf("flags = %v, want a dynamic set the packet path can add to", item.Set.Flags)
		}
		if len(item.Set.Elem) != 1 {
			t.Errorf("elements = %v, want the one source that was held", item.Set.Elem)
		}
		return
	}
	t.Fatalf("no scanners_wan_v4 set in the kernel:\n%s", out)
}

// zoneChain returns the body of one zone's chain, for the tests that are
// about what order things are in.
func zoneChain(t *testing.T, ruleset, zone string) string {
	t.Helper()
	open := "chain zone_" + zone + " {"
	i := strings.Index(ruleset, open)
	if i < 0 {
		t.Fatalf("no chain for zone %q", zone)
	}
	rest := ruleset[i+len(open):]
	end := strings.Index(rest, "\n\t}")
	if end < 0 {
		t.Fatalf("chain for zone %q does not end", zone)
	}
	return rest[:end]
}
