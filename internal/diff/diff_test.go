package diff

import (
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

func base() *model.Config {
	return model.Starter(model.StarterOptions{
		Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0",
	})
}

func paths(t *testing.T, a, b any) map[string]Change {
	t.Helper()
	changes, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Change{}
	for _, c := range changes {
		out[c.Path] = c
	}
	return out
}

func TestCompareFindsNothingInIdenticalConfigs(t *testing.T) {
	t.Parallel()
	changes, err := Compare(base(), base())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("identical configurations differ: %+v", changes)
	}
}

func TestCompareNamesTheField(t *testing.T) {
	t.Parallel()
	a, b := base(), base()
	b.System.Hostname = "edge"
	b.System.Management.WebPort = 8443

	got := paths(t, a, b)
	if c, ok := got["system.hostname"]; !ok || c.Kind != Changed || c.After != "edge" {
		t.Errorf("system.hostname = %+v, %v", c, ok)
	}
	if c, ok := got["system.management.webPort"]; !ok || c.Kind != Changed {
		t.Errorf("system.management.webPort = %+v, %v", c, ok)
	}
	if c := got["system.hostname"]; !strings.Contains(c.String(), `"fw" -> "edge"`) {
		t.Errorf("String() = %q", c.String())
	}
}

// Inserting a rule at the top must not report every rule below it as
// changed, which is what an index-based comparison would do.
func TestCompareKeepsRuleIdentity(t *testing.T) {
	t.Parallel()
	a, b := base(), base()
	first := b.Rules[0]
	b.Rules = append([]model.Rule{{
		ID: "block-smtp", Enabled: true, Zone: "lan", Action: model.ActionDrop,
		Protocol: model.ProtocolTCP, Destination: model.Endpoint{Ports: []string{"25"}},
	}}, b.Rules...)

	got := paths(t, a, b)
	if c, ok := got["rules[block-smtp]"]; !ok || c.Kind != Added {
		t.Fatalf("new rule = %+v, %v", c, ok)
	}
	for path := range got {
		if strings.HasPrefix(path, "rules["+first.ID+"]") {
			t.Errorf("untouched rule %s reported as %s", first.ID, path)
		}
	}
}

// The same label in two domains is two overrides, so each keeps a change
// path of its own instead of both collapsing onto "potato".
func TestCompareKeepsHostOverrideDomains(t *testing.T) {
	t.Parallel()
	hosts := func(ip string) []model.HostOverride {
		return []model.HostOverride{
			{Hostname: "potato", IP: "192.168.1.5"},
			{Hostname: "potato", Domain: "test", IP: ip},
		}
	}
	a, b := base(), base()
	a.Services.DNS.HostOverrides = hosts("192.168.1.6")
	b.Services.DNS.HostOverrides = hosts("192.168.1.7")

	got := paths(t, a, b)
	if c, ok := got["services.dns.hostOverrides[potato.test].ip"]; !ok || c.After != "192.168.1.7" {
		t.Fatalf("changed override = %+v, %v (have %v)", c, ok, got)
	}
	if len(got) != 1 {
		t.Errorf("the untouched override was reported too: %+v", got)
	}
}

// A domain override is named by its domain, so a change to one is
// reported under it, and the page can mark that row.
func TestCompareKeepsDomainOverrideIdentity(t *testing.T) {
	t.Parallel()
	domains := func(desc string) []model.DomainOverride {
		return []model.DomainOverride{
			{Domain: "corp.example", Servers: []string{"10.0.0.2"}},
			{Domain: "ts.net", Servers: []string{"100.100.100.100"}, Description: desc},
		}
	}
	a, b := base(), base()
	a.Services.DNS.DomainOverrides = domains("")
	b.Services.DNS.DomainOverrides = domains("the tailnet")

	got := paths(t, a, b)
	if c, ok := got["services.dns.domainOverrides[ts.net].description"]; !ok || c.After != "the tailnet" {
		t.Fatalf("changed override = %+v, %v (have %v)", c, ok, got)
	}
	if len(got) != 1 {
		t.Errorf("the untouched override was reported too: %+v", got)
	}
}

// Reordering rules changes what the firewall does, so it has to show up.
func TestCompareReportsReordering(t *testing.T) {
	t.Parallel()
	a := base()
	a.Rules = append(a.Rules, model.Rule{
		ID: "ping", Enabled: true, Zone: "wan", Action: model.ActionAccept, Protocol: model.ProtocolICMP,
	})
	b := base()
	b.Rules = append([]model.Rule{{
		ID: "ping", Enabled: true, Zone: "wan", Action: model.ActionAccept, Protocol: model.ProtocolICMP,
	}}, b.Rules...)

	got := paths(t, a, b)
	c, ok := got["rules (order)"]
	if !ok {
		t.Fatalf("reordering went unreported: %+v", got)
	}
	if !strings.Contains(c.String(), "ping") {
		t.Errorf("order change = %q", c.String())
	}
	if len(got) != 1 {
		t.Errorf("reordering reported extra changes: %+v", got)
	}
}

func TestCompareReportsAddedAndRemovedEntries(t *testing.T) {
	t.Parallel()
	a, b := base(), base()
	a.Aliases = []model.Alias{{Name: "admins", Type: model.AliasHosts, Entries: []string{"10.0.0.1"}}}
	b.Gateways = []model.Gateway{{Name: "wan1", Enabled: true, Interface: "eth0", Address: "10.0.0.254"}}

	got := paths(t, a, b)
	if c, ok := got["aliases[admins]"]; !ok || c.Kind != Removed {
		t.Errorf("removed alias = %+v, %v", c, ok)
	}
	if c, ok := got["gateways[wan1]"]; !ok || c.Kind != Added {
		t.Errorf("added gateway = %+v, %v", c, ok)
	}
	if c := got["gateways[wan1]"]; !strings.HasPrefix(c.String(), "+ gateways[wan1]") {
		t.Errorf("String() = %q", c.String())
	}
}

// A field that goes from absent to false, or from "" to absent, is not a
// change anyone needs to read about.
func TestCompareIgnoresEmptyToUnset(t *testing.T) {
	t.Parallel()
	a := map[string]any{"a": 1, "log": false, "note": "", "list": []any{}}
	b := map[string]any{"a": 1}
	changes, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Errorf("empty values reported as changes: %+v", changes)
	}
}

// Lists of plain values, and lists whose entries share a name, fall back
// to comparing by position rather than guessing.
func TestComparePositionalLists(t *testing.T) {
	t.Parallel()
	a := map[string]any{"upstreams": []any{"1.1.1.1", "9.9.9.9"}}
	b := map[string]any{"upstreams": []any{"1.1.1.1"}}
	got := paths(t, a, b)
	if c, ok := got["upstreams[1]"]; !ok || c.Kind != Removed || c.Before != "9.9.9.9" {
		t.Errorf("upstreams[1] = %+v, %v", c, ok)
	}
}

func TestCompareNestedEndpointChange(t *testing.T) {
	t.Parallel()
	a, b := base(), base()
	b.Rules[0].Destination = model.Endpoint{Ports: []string{"443"}}

	got := paths(t, a, b)
	// A list of bare ports is reported whole; only lists whose entries name
	// themselves are broken out entry by entry.
	want := "rules[" + b.Rules[0].ID + "].destination.ports"
	if c, ok := got[want]; !ok || c.Kind != Added {
		t.Errorf("%s = %+v, %v (all: %+v)", want, c, ok, got)
	}
}
