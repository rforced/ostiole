package policy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
	"github.com/rforced/ostiole/internal/netnstest"
)

func policyRules(t *testing.T) map[int]netlink.Rule {
	t.Helper()
	rules, err := netlink.Rules(unix.AF_INET)
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
	routes, err := netlink.Routes(unix.AF_INET, netlink.RouteFilter{Table: table})
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
	if !netnstest.Enter(t) {
		return
	}

	netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	netnstest.Dummy(t, "wan1", "198.51.100.2/24")

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
	if mask := rules[lookup].Mask; mask != model.PolicyMarkMask {
		t.Errorf("lookup rule mask = %v, want the Ostiole mark mask", rules[lookup].Mask)
	}

	// The kernel's copy has to compare equal to what the installer would
	// put there, or every pass would delete and reinstall the route and
	// leave a gap each time.
	want := inst.routeFor(group, unix.AF_INET)
	if kernel := tableRoutes(t, group.Table); !sameRoute(&kernel[0], want) {
		t.Errorf("the kernel's route %+v does not match the desired %+v, so policy routes would churn",
			kernel[0], want)
	}
	if kernel := policyRules(t)[lookup]; kernel != rulesFor(group, unix.AF_INET)[1] {
		t.Errorf("the kernel's ip rule %+v does not match the desired one, so rules would churn", kernel)
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
	if !netnstest.Enter(t) {
		return
	}

	netnstest.Dummy(t, "wan0", "203.0.113.2/24")
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
	v6, err := netlink.Routes(unix.AF_INET6, netlink.RouteFilter{Table: group.Table})
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

// forwarding makes the namespace a router. The kernel will not say where
// it would forward a packet until it forwards at all.
func forwarding(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ipv4/ip_forward", "ipv6/conf/all/forwarding"} {
		if err := os.WriteFile("/proc/sys/net/"+key, []byte("1"), 0o600); err != nil {
			t.Fatalf("switch on %s: %v", key, err)
		}
	}
}

// addRoute puts a route in the main table, as networkd would.
func addRoute(t *testing.T, link netlink.Link, dst, gw string) {
	t.Helper()
	_, prefix, err := net.ParseCIDR(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddRoute(netlink.Route{LinkIndex: link.Index, Dst: prefix, Gw: net.ParseIP(gw)}); err != nil {
		t.Fatalf("add %s via %s: %v", dst, gw, err)
	}
}

// forwardedBy asks the kernel where it would forward a packet from src that
// arrived on lan0 carrying mark. It answers with the next hop, empty for a
// destination on the link, and the table that decided.
func forwardedBy(t *testing.T, dst, src string, mark uint32) (string, int) {
	t.Helper()
	routes, err := netlink.RouteGet(netlink.RouteQuery{
		Dst: net.ParseIP(dst), Src: net.ParseIP(src), Iif: netnstest.Link(t, "lan0").Index, Mark: mark,
	})
	if err != nil {
		t.Fatalf("route to %s from %s with mark %#x: %v", dst, src, mark, err)
	}
	if len(routes) != 1 {
		t.Fatalf("route to %s = %+v, want one answer", dst, routes)
	}
	gw := ""
	if routes[0].Gw != nil {
		gw = routes[0].Gw.String()
	}
	return gw, routes[0].Table
}

// routeChanges runs f and returns what the kernel announced it changed in
// the policy tables meanwhile.
func routeChanges(t *testing.T, f func()) []netlink.RouteUpdate {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	updates, err := netlink.WatchRoutes(ctx)
	if err != nil {
		t.Fatalf("watch route changes: %v", err)
	}
	f()

	// The kernel announces changes in order, so once a route added after f
	// comes through, whatever f changed has come through before it.
	const markerTable = 100
	_, dst, _ := net.ParseCIDR("192.0.2.0/24")
	marker := netlink.Route{Table: markerTable, Dst: dst, Type: unix.RTN_BLACKHOLE}
	if err := netlink.AddRoute(marker); err != nil {
		t.Fatalf("add marker route: %v", err)
	}
	defer func() { _ = netlink.DeleteRoute(marker) }()
	var changes []netlink.RouteUpdate
	timeout := time.After(5 * time.Second)
	for {
		select {
		case u, ok := <-updates:
			if !ok {
				t.Fatal("the route watch ended early")
			}
			if u.Table == markerTable {
				return changes
			}
			if ownTable(u.Table) {
				changes = append(changes, u)
			}
		case <-timeout:
			t.Fatal("the kernel never announced the marker route")
		}
	}
}

// A packet the firewall marked for a gateway leaves through that gateway
// whatever the main table's default route says, while a marked packet for
// the LAN, or for a site behind a static route, goes where it always did.
// That is the suppress rule at work, and asking the kernel where it would
// forward each packet is the only way to see it.
func TestAMarkedPacketLeavesThroughItsGatewayWhileLocalTrafficStaysPut(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	lan := netnstest.Dummy(t, "lan0", "192.168.1.1/24")
	forwarding(t)
	// The main table: the primary line carries the default route, and a
	// router on the LAN leads to another site.
	addRoute(t, wan0, "0.0.0.0/0", "203.0.113.1")
	addRoute(t, lan, "10.99.0.0/16", "192.168.1.254")

	cfg := &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "primary", Enabled: true, Interface: "wan0", Address: "203.0.113.1"},
			{Name: "backup", Enabled: true, Interface: "wan1", Address: "198.51.100.1"},
		},
	}
	hops := map[string]Hop{
		"primary": {Gateway: "primary", Address: "203.0.113.1", Interface: "wan0", Online: true},
		"backup":  {Gateway: "backup", Address: "198.51.100.1", Interface: "wan1", Online: true},
	}
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, hops)
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	backup, _ := targetByName(targets, "backup")

	const host = "192.168.1.10"
	for _, c := range []struct {
		what  string
		dst   string
		mark  uint32
		gw    string
		table int
	}{
		{"unmarked traffic", "1.1.1.1", 0, "203.0.113.1", unix.RT_TABLE_MAIN},
		{"traffic marked for the backup", "1.1.1.1", backup.Mark, "198.51.100.1", backup.Table},
		{"traffic the shaper has prioritised too", "1.1.1.1", backup.Mark | 1<<model.ShapeMarkShift, "198.51.100.1", backup.Table},
		{"a marked packet for another LAN host", "192.168.1.20", backup.Mark, "", unix.RT_TABLE_MAIN},
		{"a marked packet for the site behind the LAN router", "10.99.1.1", backup.Mark, "192.168.1.254", unix.RT_TABLE_MAIN},
	} {
		if gw, table := forwardedBy(t, c.dst, host, c.mark); gw != c.gw || table != c.table {
			t.Errorf("%s to %s leaves via %q from table %d, want %q from table %d", c.what, c.dst, gw, table, c.gw, c.table)
		}
	}

	// With the backup dead, what was marked for it takes the main table's
	// default route rather than going nowhere.
	hops["backup"] = Hop{Gateway: "backup", Address: "198.51.100.1", Interface: "wan1"}
	if err := inst.Sync(Plan(cfg, hops)); err != nil {
		t.Fatalf("sync with the backup down: %v", err)
	}
	if gw, table := forwardedBy(t, "1.1.1.1", host, backup.Mark); gw != "203.0.113.1" || table != unix.RT_TABLE_MAIN {
		t.Errorf("traffic marked for a dead backup leaves via %q from table %d, want the primary from the main table", gw, table)
	}
}

// IPv6 is routed the same way, through the link-local next hop a router
// advertisement gives as well as through a global one, and the kernel's
// copy of an IPv6 route is what the next pass asks for, so it is left
// alone rather than put back every few seconds.
func TestMarkedIPv6LeavesThroughItsGateway(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "2001:db8:1::2/64")
	netnstest.Dummy(t, "wan1", "2001:db8:2::2/64")
	netnstest.Dummy(t, "lan0", "2001:db8:10::1/64")
	forwarding(t)
	addRoute(t, wan0, "::/0", "2001:db8:1::1")

	cfg := &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "fibre", Enabled: true, Interface: "wan0", Address: "2001:db8:1::1"},
			{Name: "cable", Enabled: true, Interface: "wan1", Address: "fe80::1"},
		},
	}
	hops := map[string]Hop{
		"fibre": {Gateway: "fibre", Address: "2001:db8:1::1", Interface: "wan0", Online: true},
		"cable": {Gateway: "cable", Address: "fe80::1", Interface: "wan1", Online: true},
	}
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, hops)
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	fibre, _ := targetByName(targets, "fibre")
	cable, _ := targetByName(targets, "cable")

	const host = "2001:db8:10::10"
	for _, c := range []struct {
		what  string
		dst   string
		mark  uint32
		gw    string
		table int
	}{
		{"unmarked traffic", "2606:4700::1111", 0, "2001:db8:1::1", unix.RT_TABLE_MAIN},
		{"traffic marked for the cable line", "2606:4700::1111", cable.Mark, "fe80::1", cable.Table},
		{"traffic marked for the fibre line", "2606:4700::1111", fibre.Mark, "2001:db8:1::1", fibre.Table},
		{"a marked packet for another LAN host", "2001:db8:10::20", cable.Mark, "", unix.RT_TABLE_MAIN},
	} {
		if gw, table := forwardedBy(t, c.dst, host, c.mark); gw != c.gw || table != c.table {
			t.Errorf("%s to %s leaves via %q from table %d, want %q from table %d", c.what, c.dst, gw, table, c.gw, c.table)
		}
	}

	if changes := routeChanges(t, func() {
		if err := inst.Sync(targets); err != nil {
			t.Errorf("second sync: %v", err)
		}
	}); len(changes) != 0 {
		t.Errorf("a pass with nothing to do made %d changes to the policy tables, the first %+v", len(changes), changes[0])
	}
}

// Gateways that share a tier share the traffic: the kernel spreads flows
// over all of them, in both families. The kernel's copy of that route,
// next hops in the order it keeps them, is what the next pass asks for, so
// it is left alone rather than torn down and put back every few seconds.
func TestATierOfSeveralGatewaysSharesTheTrafficAndStaysPut(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	netnstest.Dummy(t, "wan1", "198.51.100.2/24", "2001:db8:2::2/64")
	netnstest.Dummy(t, "lan0", "192.168.1.1/24", "2001:db8:10::1/64")
	forwarding(t)

	cfg := &model.Config{
		Version: model.SchemaVersion,
		Gateways: []model.Gateway{
			{Name: "fibre", Enabled: true, Interface: "wan0", Address: "203.0.113.1"},
			{Name: "cable", Enabled: true, Interface: "wan1", Address: "198.51.100.1"},
			{Name: "fibre6", Enabled: true, Interface: "wan0", Address: "2001:db8:1::1"},
			{Name: "cable6", Enabled: true, Interface: "wan1", Address: "2001:db8:2::1"},
		},
		GatewayGroups: []model.GatewayGroup{{Name: "shared", Enabled: true, Members: []model.GatewayMember{
			{Gateway: "fibre"}, {Gateway: "cable"}, {Gateway: "fibre6"}, {Gateway: "cable6"},
		}}},
	}
	hops := map[string]Hop{}
	for _, g := range cfg.Gateways {
		hops[g.Name] = Hop{Gateway: g.Name, Address: g.Address, Interface: g.Interface, Online: true}
	}
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, hops)
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	shared, _ := targetByName(targets, "shared")

	for _, family := range []struct {
		name string
		host string
		dst  func(int) string
		hops []string
	}{
		{"IPv4", "192.168.1.10", func(i int) string { return fmt.Sprintf("198.18.%d.1", i) },
			[]string{"203.0.113.1", "198.51.100.1"}},
		{"IPv6", "2001:db8:10::10", func(i int) string { return fmt.Sprintf("2001:db8:ff:%x::1", i) },
			[]string{"2001:db8:1::1", "2001:db8:2::1"}},
	} {
		used := map[string]int{}
		for i := range 64 {
			gw, table := forwardedBy(t, family.dst(i), family.host, shared.Mark)
			if table != shared.Table {
				t.Fatalf("%s: marked traffic answered from table %d, want %d", family.name, table, shared.Table)
			}
			used[gw]++
		}
		for _, hop := range family.hops {
			if used[hop] == 0 {
				t.Errorf("%s: none of 64 flows left via %s, want the tier shared: %v", family.name, hop, used)
			}
		}
	}

	if changes := routeChanges(t, func() {
		if err := inst.Sync(targets); err != nil {
			t.Errorf("second sync: %v", err)
		}
	}); len(changes) != 0 {
		t.Errorf("a pass with nothing to do made %d changes to the policy tables, the first %+v", len(changes), changes[0])
	}
}

// wayOutNamespace lays out a router with a WAN carrying both families'
// default routes, a LAN, and a dummy link standing in for the tunnel:
// routing does not care what kind of device it is.
func wayOutNamespace(t *testing.T) (tun netlink.Link) {
	t.Helper()
	wan := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	tun = netnstest.Dummy(t, "tun0", "10.66.1.2/32")
	netnstest.Dummy(t, "lan0", "192.168.1.1/24", "2001:db8:10::1/64")
	forwarding(t)
	addRoute(t, wan, "0.0.0.0/0", "203.0.113.1")
	addRoute(t, wan, "::/0", "2001:db8:1::1")
	return tun
}

// familyRoutes lists a policy table in one family.
func familyRoutes(t *testing.T, family, table int) []netlink.Route {
	t.Helper()
	routes, err := netlink.Routes(family, netlink.RouteFilter{Table: table})
	if err != nil {
		t.Fatalf("list table %d: %v", table, err)
	}
	return routes
}

// noChurn fails when a second pass with the same plan changes anything:
// the kernel's copy of each route has to read back as what is asked for.
func noChurn(t *testing.T, inst *Installer, targets []Target) {
	t.Helper()
	if changes := routeChanges(t, func() {
		if err := inst.Sync(targets); err != nil {
			t.Errorf("second sync: %v", err)
		}
	}); len(changes) != 0 {
		t.Errorf("a pass with nothing to do made %d changes to the policy tables, the first %+v", len(changes), changes[0])
	}
}

// A tunnel gateway sends what is marked for it into the tunnel with no
// next hop, and refuses the family the tunnel does not carry rather than
// let it out the WAN. Down, it blocks both. Every one of those routes
// reads back from the kernel as the installer asks for it, so none of
// them is put back every few seconds.
func TestATunnelGatewayRoutesIntoItsTunnelAndRefusesTheRest(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	tun := wayOutNamespace(t)
	cfg := wayOutConfig()
	cfg.GatewayGroups = nil
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, wayOutHops(cfg, true))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	vpn, _ := targetByName(targets, "vpn")

	v4 := familyRoutes(t, unix.AF_INET, vpn.Table)
	if len(v4) != 1 || v4[0].LinkIndex != tun.Index || v4[0].Gw != nil || v4[0].Type != unix.RTN_UNICAST {
		t.Fatalf("IPv4 table = %+v, want the tunnel with no next hop", v4)
	}
	routes, err := netlink.RouteGet(netlink.RouteQuery{
		Dst: net.ParseIP("1.1.1.1"), Src: net.ParseIP("192.168.1.10"),
		Iif: netnstest.Link(t, "lan0").Index, Mark: vpn.Mark,
	})
	if err != nil || len(routes) != 1 || routes[0].LinkIndex != tun.Index || routes[0].Table != vpn.Table {
		t.Errorf("marked traffic goes %+v (%v), want into the tunnel from table %d", routes, err, vpn.Table)
	}
	if v6 := familyRoutes(t, unix.AF_INET6, vpn.Table); len(v6) != 1 || v6[0].Type != unix.RTN_UNREACHABLE {
		t.Errorf("IPv6 table = %+v, want it refused", v6)
	}
	noChurn(t, inst, targets)

	// Down, it blocks both families.
	targets = Plan(cfg, wayOutHops(cfg, false))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync with the tunnel down: %v", err)
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		if r := familyRoutes(t, family, vpn.Table); len(r) != 1 || r[0].Type != unix.RTN_BLACKHOLE {
			t.Errorf("family %d with the tunnel down = %+v, want a blackhole", family, r)
		}
	}
	noChurn(t, inst, targets)
}

// A group with the tunnel first and the WAN after it uses the tunnel for
// what it carries and refuses the rest while the tunnel is up; only when
// the tunnel is down does the WAN take both families.
func TestAGroupFallsBackFromTheTunnelOnlyWhenItIsDown(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	tun := wayOutNamespace(t)
	cfg := wayOutConfig()
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, wayOutHops(cfg, true))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	private, _ := targetByName(targets, "private")
	if r := familyRoutes(t, unix.AF_INET, private.Table); len(r) != 1 || r[0].LinkIndex != tun.Index {
		t.Errorf("IPv4 with the tunnel up = %+v, want the tunnel", r)
	}
	if r := familyRoutes(t, unix.AF_INET6, private.Table); len(r) != 1 || r[0].Type != unix.RTN_UNREACHABLE {
		t.Errorf("IPv6 with the tunnel up = %+v, want it refused, not sent out the WAN", r)
	}

	targets = Plan(cfg, wayOutHops(cfg, false))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync with the tunnel down: %v", err)
	}
	if r := familyRoutes(t, unix.AF_INET, private.Table); len(r) != 1 || !r[0].Gw.Equal(net.ParseIP("203.0.113.1")) {
		t.Errorf("IPv4 with the tunnel down = %+v, want the WAN", r)
	}
	if r := familyRoutes(t, unix.AF_INET6, private.Table); len(r) != 1 || !r[0].Gw.Equal(net.ParseIP("2001:db8:1::1")) {
		t.Errorf("IPv6 with the tunnel down = %+v, want the WAN", r)
	}
	noChurn(t, inst, targets)
}

// A tunnel and a line in one tier share IPv4, one route with a hop that
// has no next hop beside one that has, and the kernel's copy of it is
// what the next pass asks for.
func TestATunnelSharesATierWithALine(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	tun := wayOutNamespace(t)
	cfg := wayOutConfig()
	cfg.GatewayGroups[0].Members = []model.GatewayMember{{Gateway: "vpn"}, {Gateway: "wan"}, {Gateway: "wan6"}}
	inst := NewInstaller(slog.New(slog.DiscardHandler))
	targets := Plan(cfg, wayOutHops(cfg, true))
	if err := inst.Sync(targets); err != nil {
		t.Fatalf("sync: %v", err)
	}
	private, _ := targetByName(targets, "private")
	v4 := familyRoutes(t, unix.AF_INET, private.Table)
	if len(v4) != 1 || len(v4[0].MultiPath) != 2 {
		t.Fatalf("IPv4 = %+v, want one route over the tunnel and the line", v4)
	}
	devices := map[int]bool{}
	for _, h := range v4[0].MultiPath {
		devices[h.LinkIndex] = true
	}
	if !devices[tun.Index] {
		t.Errorf("IPv4 hops = %+v, want the tunnel among them", v4[0].MultiPath)
	}
	if v6 := familyRoutes(t, unix.AF_INET6, private.Table); len(v6) != 1 || !v6[0].Gw.Equal(net.ParseIP("2001:db8:1::1")) {
		t.Errorf("IPv6 = %+v, want the line alone, since the tunnel does not carry it", v6)
	}
	noChurn(t, inst, targets)
}
