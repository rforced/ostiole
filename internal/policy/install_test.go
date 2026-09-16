package policy

import (
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/model"
)

// runInNamespace re-runs the calling test inside a fresh unprivileged user
// and network namespace, where it can change routes without being root on
// the host. It skips where the sandbox forbids that, which is what
// GitHub's runners do.
func runInNamespace(t *testing.T, env, name string) {
	t.Helper()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	cmd := exec.Command("unshare", "-Urn", os.Args[0], "-test.run", "^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
}

// dummyLink brings up a dummy interface carrying addr, so routes through
// it have somewhere to go.
func dummyLink(t *testing.T, name, addr string) {
	t.Helper()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("bring %s up: %v", name, err)
	}
	a, err := netlink.ParseAddr(addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, a); err != nil {
		t.Fatalf("address on %s: %v", name, err)
	}
}

func policyRules(t *testing.T) map[int]netlink.Rule {
	t.Helper()
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	out := map[int]netlink.Rule{}
	for _, r := range rules {
		if ownPriority(r.Priority) {
			out[r.Priority] = r
		}
	}
	return out
}

func tableRoutes(t *testing.T, table int) []netlink.Route {
	t.Helper()
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4,
		&netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list table %d: %v", table, err)
	}
	return routes
}

// TestSyncInstallsAndReconciles drives the installer against a real
// kernel: routes and rules appear, a second pass with the same plan
// changes nothing, failover rewrites the table, and a shrinking plan takes
// the leftovers away.
func TestSyncInstallsAndReconciles(t *testing.T) {
	if os.Getenv("OSTIOLE_POLICY_NETNS") == "" {
		runInNamespace(t, "OSTIOLE_POLICY_NETNS", "TestSyncInstallsAndReconciles")
		return
	}

	dummyLink(t, "wan0", "203.0.113.2/24")
	dummyLink(t, "wan1", "198.51.100.2/24")

	cfg := &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "primary", Enabled: true, Interface: "wan0", Address: "203.0.113.1"},
			{Name: "backup", Enabled: true, Interface: "wan1", Address: "198.51.100.1"},
		},
		GatewayGroups: []model.GatewayGroup{
			{Name: "failover", Enabled: true, Members: []model.GatewayMember{
				{Gateway: "primary", Tier: 0},
				{Gateway: "backup", Tier: 1},
			}},
		},
	}
	hops := map[string]Hop{
		"primary": {Gateway: "primary", Address: "203.0.113.1", Interface: "wan0", Online: true},
		"backup":  {Gateway: "backup", Address: "198.51.100.1", Interface: "wan1", Online: true},
	}
	inst := NewInstaller(slog.New(slog.DiscardHandler))

	targets := Plan(cfg, hops)
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	group, ok := targetByName(targets, "failover")
	if !ok {
		t.Fatal("group missing from the plan")
	}
	routes := tableRoutes(t, group.Table)
	if len(routes) != 1 || !routes[0].Gw.Equal(net.ParseIP("203.0.113.1")) {
		t.Fatalf("group table = %+v, want the best tier's gateway", routes)
	}

	// Two rules per target: the main table without its default route, then
	// the policy table.
	rules := policyRules(t)
	if len(rules) != 2*len(targets) {
		t.Fatalf("installed %d rules, want %d", len(rules), 2*len(targets))
	}
	suppress, lookup := group.Priorities()
	if r := rules[suppress]; r.Table != unix.RT_TABLE_MAIN || r.SuppressPrefixlen != 0 || r.Mark != group.Mark {
		t.Errorf("suppress rule = %+v, want main table with the default route hidden", r)
	}
	if r := rules[lookup]; r.Table != group.Table || r.Mark != group.Mark {
		t.Errorf("lookup rule = %+v, want table %d", r, group.Table)
	}
	if mask := rules[lookup].Mask; mask == nil || *mask != uint32(model.PolicyMarkMask) {
		t.Errorf("lookup rule mask = %v, want the Ostiole mark mask", rules[lookup].Mask)
	}

	// Running again with the same plan must not disturb anything.
	before := tableRoutes(t, group.Table)[0]
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if after := tableRoutes(t, group.Table); len(after) != 1 || !sameRoute(&before, &after[0]) {
		t.Errorf("idempotent sync changed the route: %+v -> %+v", before, after)
	}

	// The best tier dies: the group falls to the next one.
	hops["primary"] = Hop{Gateway: "primary", Address: "203.0.113.1", Interface: "wan0"}
	if err := inst.Sync(Plan(cfg, hops)); err != nil {
		t.Fatalf("failover sync: %v", err)
	}
	routes = tableRoutes(t, group.Table)
	if len(routes) != 1 || !routes[0].Gw.Equal(net.ParseIP("198.51.100.1")) {
		t.Fatalf("after failover = %+v, want the backup gateway", routes)
	}
	primary, _ := targetByName(Plan(cfg, hops), "primary")
	if r := tableRoutes(t, primary.Table); len(r) != 0 {
		t.Errorf("a dead gateway kept its own table: %+v, want traffic to fall back", r)
	}

	// Removing the group takes its table and rules with it.
	cfg.GatewayGroups = nil
	if err := inst.Sync(Plan(cfg, hops)); err != nil {
		t.Fatalf("shrinking sync: %v", err)
	}
	if r := tableRoutes(t, group.Table); len(r) != 0 {
		t.Errorf("stale routes left behind: %+v", r)
	}
	if rules := policyRules(t); len(rules) != 2 {
		t.Errorf("rules after removal = %d, want only the surviving gateway's pair", len(rules))
	}

	if err := inst.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if rules := policyRules(t); len(rules) != 0 {
		t.Errorf("Clear left %d rules behind", len(rules))
	}
}

// A group set to block installs a blackhole rather than letting traffic
// out the ordinary default route, which is what keeps a tunnel from
// leaking while it is down.
func TestSyncBlocksWhenEveryMemberIsDown(t *testing.T) {
	if os.Getenv("OSTIOLE_POLICY_BLOCK_NETNS") == "" {
		runInNamespace(t, "OSTIOLE_POLICY_BLOCK_NETNS", "TestSyncBlocksWhenEveryMemberIsDown")
		return
	}

	dummyLink(t, "wan0", "203.0.113.2/24")
	cfg := &model.Config{
		Version:  model.SchemaVersion,
		Gateways: []model.Gateway{{Name: "vpn", Enabled: true, Interface: "wan0", Address: "203.0.113.1"}},
		GatewayGroups: []model.GatewayGroup{{
			Name: "novpnleak", Enabled: true, OnDown: model.OnDownBlock,
			Members: []model.GatewayMember{{Gateway: "vpn"}},
		}},
	}
	hops := map[string]Hop{"vpn": {Gateway: "vpn", Address: "203.0.113.1", Interface: "wan0"}}

	targets := Plan(cfg, hops)
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	group, _ := targetByName(targets, "novpnleak")
	routes := tableRoutes(t, group.Table)
	if len(routes) != 1 || routes[0].Type != unix.RTN_BLACKHOLE {
		t.Fatalf("table = %+v, want a blackhole route", routes)
	}

	// IPv6 is blocked too: a kill switch that only covers IPv4 is not one.
	v6, err := netlink.RouteListFiltered(netlink.FAMILY_V6,
		&netlink.Route{Table: group.Table}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatalf("list v6 table: %v", err)
	}
	if len(v6) != 1 || v6[0].Type != unix.RTN_BLACKHOLE {
		t.Errorf("IPv6 table = %+v, want a blackhole route", v6)
	}

	// When the tunnel comes back the blackhole gives way to the real route.
	hops["vpn"] = Hop{Gateway: "vpn", Address: "203.0.113.1", Interface: "wan0", Online: true}
	if err := inst.Sync(Plan(cfg, hops)); err != nil {
		t.Fatalf("recovery sync: %v", err)
	}
	routes = tableRoutes(t, group.Table)
	if len(routes) != 1 || routes[0].Type == unix.RTN_BLACKHOLE || !routes[0].Gw.Equal(net.ParseIP("203.0.113.1")) {
		t.Errorf("after recovery = %+v, want the real next hop", routes)
	}
}
