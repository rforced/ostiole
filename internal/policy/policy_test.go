package policy

import (
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

func testConfig() *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "primary", Enabled: true, Interface: "eth0"},
			{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.1"},
			{Name: "parked", Enabled: false, Interface: "eth1", Address: "198.51.100.9"},
		},
		GatewayGroups: []model.GatewayGroup{
			{Name: "balanced", Enabled: true, Members: []model.GatewayMember{
				{Gateway: "primary", Tier: 0},
				{Gateway: "backup", Tier: 1},
			}},
			{Name: "tunnel", Enabled: true, OnDown: model.OnDownBlock, Members: []model.GatewayMember{
				{Gateway: "primary"},
				{Gateway: "backup"},
			}},
			{Name: "off", Members: []model.GatewayMember{{Gateway: "backup"}}},
		},
	}
}

func liveHops() map[string]Hop {
	return map[string]Hop{
		"primary": {Gateway: "primary", Address: "203.0.113.1", Interface: "eth0", Online: true},
		"backup":  {Gateway: "backup", Address: "198.51.100.1", Interface: "eth1", Online: true},
	}
}

func targetByName(targets []Target, name string) (Target, bool) {
	for _, t := range targets {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}

// Marks and table ids come from the sorted list of enabled names, so the
// firewall and the routing tables agree without sharing state. The fourth
// name is number 5: 4 is reserved for Tailscale.
func TestPlanNumbersEnabledTargetsByName(t *testing.T) {
	t.Parallel()
	targets := Plan(testConfig(), liveHops())

	want := []struct {
		name  string
		mark  uint32
		table int
		index int
	}{
		{"backup", 0x10000, 2201, 1},
		{"balanced", 0x20000, 2202, 2},
		{"primary", 0x30000, 2203, 3},
		{"tunnel", 0x50000, 2205, 5},
	}
	if len(targets) != len(want) {
		t.Fatalf("planned %d targets, want %d: %+v", len(targets), len(want), targets)
	}
	for i, w := range want {
		got := targets[i]
		if got.Name != w.name || got.Mark != w.mark || got.Table != w.table || got.Index != w.index {
			t.Errorf("target %d = %+v, want %s mark 0x%x table %d index %d",
				i, got, w.name, w.mark, w.table, w.index)
		}
	}
	if _, ok := targetByName(targets, "parked"); ok {
		t.Error("a disabled gateway should get no mark")
	}
	if _, ok := targetByName(targets, "off"); ok {
		t.Error("a disabled group should get no mark")
	}
}

func TestPrioritiesDoNotOverlap(t *testing.T) {
	t.Parallel()
	seen := map[int]string{}
	for _, tg := range Plan(testConfig(), liveHops()) {
		suppress, lookup := tg.Priorities()
		for _, p := range []int{suppress, lookup} {
			if other, dup := seen[p]; dup {
				t.Errorf("priority %d is used by both %s and %s", p, other, tg.Name)
			}
			seen[p] = tg.Name
			if !ownPriority(p) {
				t.Errorf("priority %d for %s is outside the range Ostiole reconciles", p, tg.Name)
			}
		}
	}
}

func TestPlanGroupsMembersByTier(t *testing.T) {
	t.Parallel()
	tg, ok := targetByName(Plan(testConfig(), liveHops()), "balanced")
	if !ok {
		t.Fatal("group missing from the plan")
	}
	if len(tg.Tiers) != 2 {
		t.Fatalf("tiers = %+v, want one per tier", tg.Tiers)
	}
	if len(tg.Tiers[0]) != 1 || tg.Tiers[0][0].Gateway != "primary" {
		t.Errorf("best tier = %+v, want primary alone", tg.Tiers[0])
	}
	if len(tg.Tiers[1]) != 1 || tg.Tiers[1][0].Gateway != "backup" {
		t.Errorf("second tier = %+v, want backup", tg.Tiers[1])
	}
	if tg.Block {
		t.Error("a fallback group must not block")
	}
}

func TestPlanSharesOneTier(t *testing.T) {
	t.Parallel()
	tg, ok := targetByName(Plan(testConfig(), liveHops()), "tunnel")
	if !ok {
		t.Fatal("group missing from the plan")
	}
	if len(tg.Tiers) != 1 || len(tg.Tiers[0]) != 2 {
		t.Fatalf("tiers = %+v, want both members sharing one tier", tg.Tiers)
	}
	if !tg.Block {
		t.Error("a group set to block should say so")
	}
}

// A gateway the monitor has not resolved yet cannot carry traffic, and a
// plain gateway target without a hop leaves its table empty.
func TestPlanSkipsUnresolvedGateways(t *testing.T) {
	t.Parallel()
	hops := map[string]Hop{
		"backup": {Gateway: "backup", Address: "198.51.100.1", Interface: "eth1", Online: true},
	}
	targets := Plan(testConfig(), hops)

	primary, _ := targetByName(targets, "primary")
	if len(primary.Tiers) != 0 {
		t.Errorf("unresolved gateway got hops: %+v", primary.Tiers)
	}
	if primary.Online() {
		t.Error("a target with no hops cannot be online")
	}
	balanced, _ := targetByName(targets, "balanced")
	if len(balanced.Tiers) != 1 || balanced.Tiers[0][0].Gateway != "backup" {
		t.Errorf("group tiers = %+v, want only the resolved member", balanced.Tiers)
	}
}

func TestOnlineFollowsHops(t *testing.T) {
	t.Parallel()
	hops := liveHops()
	hops["primary"] = Hop{Gateway: "primary", Address: "203.0.113.1", Interface: "eth0"}
	hops["backup"] = Hop{Gateway: "backup", Address: "198.51.100.1", Interface: "eth1"}
	tg, _ := targetByName(Plan(testConfig(), hops), "balanced")
	if tg.Online() {
		t.Error("every member is down, so the target is not online")
	}
}
