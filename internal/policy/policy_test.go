package policy

import (
	"slices"
	"testing"

	"ostiole/internal/model"
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

// wayOutConfig is a router with a tunnel to a provider that carries IPv4,
// a gateway through it, and a group that falls back to the WAN.
func wayOutConfig() *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		Interfaces: []model.Interface{{
			Name: "tun0", Enabled: true,
			IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.1.2/32"},
			WireGuard: &model.WireGuard{Peers: []model.WireGuardPeer{
				{Name: "provider", Enabled: true, AllowedIPs: []string{"0.0.0.0/0", "::/0"}},
			}},
		}},
		Gateways: []model.Gateway{
			{Name: "vpn", Enabled: true, Interface: "tun0", Monitor: "10.64.0.1"},
			{Name: "wan", Enabled: true, Interface: "wan0", Address: "203.0.113.1"},
			{Name: "wan6", Enabled: true, Interface: "wan0", Address: "2001:db8:1::1"},
		},
		GatewayGroups: []model.GatewayGroup{{Name: "private", Enabled: true, Members: []model.GatewayMember{
			{Gateway: "vpn", Tier: 1}, {Gateway: "wan", Tier: 2}, {Gateway: "wan6", Tier: 2},
		}}},
	}
}

// wayOutHops describes every gateway of wayOutConfig as the monitor would,
// the tunnel online or not.
func wayOutHops(cfg *model.Config, tunnelUp bool) map[string]Hop {
	hops := map[string]Hop{}
	for _, g := range cfg.Gateways {
		hops[g.Name] = NewHop(cfg, g, g.Address, g.Name != "vpn" || tunnelUp)
	}
	return hops
}

func TestNewHopMakesATunnelGatewayADevice(t *testing.T) {
	cfg := wayOutConfig()
	h := NewHop(cfg, cfg.Gateways[0], "10.66.1.1", true)
	if !h.Device || h.Address != "" || h.Interface != "tun0" {
		t.Errorf("tunnel hop = %+v, want the device with no next hop", h)
	}
	// The tunnel has no IPv6 address, so its peer's ::/0 carries nothing.
	if !h.V4 || h.V6 {
		t.Errorf("families = %v, %v; want IPv4 alone", h.V4, h.V6)
	}
	if w := NewHop(cfg, cfg.Gateways[1], "203.0.113.1", true); w.Device || w.Address != "203.0.113.1" {
		t.Errorf("WAN hop = %+v, want its next hop", w)
	}
}

// A tunnel gateway on its own blocks while it is down, where an ordinary
// gateway falls back to the main table.
func TestPlanBlocksALoneTunnelGateway(t *testing.T) {
	cfg := wayOutConfig()
	targets := Plan(cfg, wayOutHops(cfg, false))
	vpn, _ := targetByName(targets, "vpn")
	if !vpn.Block {
		t.Error("a lone tunnel gateway would fall back to the WAN while down")
	}
	if len(vpn.Tiers) != 1 || !vpn.Tiers[0][0].Device {
		t.Errorf("tiers = %+v, want the tunnel", vpn.Tiers)
	}
	if vpn.Online() {
		t.Error("a tunnel gateway that is down counts as online")
	}
	if wan, _ := targetByName(targets, "wan"); wan.Block {
		t.Error("an ordinary gateway blocks")
	}
	private, _ := targetByName(targets, "private")
	if private.Block || len(private.Tiers) != 2 {
		t.Errorf("group = %+v, want two tiers falling back", private)
	}
	if !targetsOnline(Plan(cfg, wayOutHops(cfg, true)), "vpn") {
		t.Error("a tunnel gateway that is up counts as offline")
	}
}

func targetsOnline(targets []Target, name string) bool {
	t, ok := targetByName(targets, name)
	return ok && t.Online()
}

// Lines come numbered from the configuration with what their tables need:
// the listen ports whose answers stay on a line while it is up, and for a
// tunnel's line the families its table sends into the tunnel. A tunnel's
// line gets no ports, since WireGuard answers from the line under it.
func TestLinesCarryTheirPortsAndTunnels(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{
		Version: model.SchemaVersion,
		Zones:   []model.Zone{{Name: "wan", External: true}, {Name: "vpn", External: true}, {Name: "phones"}},
		Interfaces: []model.Interface{
			{Name: "eth0", Zone: "wan", Enabled: true},
			{Name: "eth1", Zone: "wan", Enabled: true},
			{Name: "wg0", Zone: "phones", Enabled: true, WireGuard: &model.WireGuard{ListenPort: 51820}},
			{Name: "wg2", Zone: "phones", Enabled: true, WireGuard: &model.WireGuard{ListenPort: 443}},
			{Name: "wg3", Zone: "phones", WireGuard: &model.WireGuard{ListenPort: 51823}},
			{
				Name: "wg1", Zone: "vpn", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "10.66.1.2/32"}, IPv6: model.IPv6{Mode: model.AddrNone},
				WireGuard: &model.WireGuard{Peers: []model.WireGuardPeer{{
					Name: "provider", Enabled: true, AllowedIPs: []string{"0.0.0.0/0", "::/0"},
				}}},
			},
		},
		Gateways: []model.Gateway{{Name: "vpn", Enabled: true, Interface: "wg1"}},
	}
	lines := Lines(cfg, func(iface string) bool { return iface != "eth1" })
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want eth0, eth1 and wg1", lines)
	}
	for i, want := range []struct {
		line   string
		up     bool
		ports  []uint16
		v4, v6 bool
	}{
		{"eth0", true, []uint16{443, 51820}, false, false},
		{"eth1", false, []uint16{443, 51820}, false, false},
		{"wg1", true, nil, true, false},
	} {
		got := lines[i]
		if got.Line != want.line || got.Up != want.up || !slices.Equal(got.Ports, want.ports) ||
			got.IntoV4 != want.v4 || got.IntoV6 != want.v6 {
			t.Errorf("line %d = %+v, want %+v", i, got, want)
		}
		if got.Index != i+1 || got.Mark != uint32(i+1)<<model.ReplyMarkShift || got.Table != model.ReplyTableBase+i+1 {
			t.Errorf("%s is numbered %d, mark %#x, table %d", got.Line, got.Index, got.Mark, got.Table)
		}
	}
}

// A line's rule comes after every translation's and before every
// gateway's, the source rules after every gateway's, and all of them sit
// in the ranges every release has reconciled, so each removes what
// another left behind.
func TestLinePrioritiesSitBetweenTheOthers(t *testing.T) {
	t.Parallel()
	lastTranslation := TranslatePriorityBase + model.MaxPolicyTargets
	firstLine, lastLine := LinePriorityBase+1, LinePriorityBase+model.MaxReplyLines
	_, lastGateway := Target{Index: model.MaxPolicyTargets}.Priorities()
	if firstLine <= lastTranslation || lastLine >= RulePriorityBase || SourcePriority <= lastGateway {
		t.Errorf("lines at %d-%d, sources at %d: want them after the translations (to %d), "+
			"before the gateways (from %d) and after them (to %d)",
			firstLine, lastLine, SourcePriority, lastTranslation, RulePriorityBase, lastGateway)
	}
	for _, p := range []int{firstLine, lastLine, SourcePriority, SourcePriority + 1} {
		if !ownPriority(p) {
			t.Errorf("priority %d is outside the range Ostiole reconciles", p)
		}
	}
	if lastLine != 21255 || SourcePriority+1 > 22511 {
		t.Errorf("lines end at %d and sources at %d, outside the ranges older releases reconcile", lastLine, SourcePriority+1)
	}
}
