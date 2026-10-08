package netlink_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
	"ostiole/internal/testenv"
)

// isDefault reports whether a route read from the kernel is a default one.
func isDefault(r netlink.Route) bool {
	ones, _ := r.Dst.Mask.Size()
	return ones == 0
}

func cidr(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func TestLinksAreMadeAndRead(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	netnstest.Dummy(t, "lan0")
	lan := netnstest.Link(t, "lan0")
	if lan.Kind != "dummy" || lan.Flags&net.FlagUp == 0 || lan.MTU != 1500 || len(lan.HardwareAddr) != 6 || lan.Statistics == nil {
		t.Errorf("lan0 = %+v", lan)
	}
	links, err := netlink.Links()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]netlink.Link{}
	for _, l := range links {
		names[l.Name] = l
	}
	if lo, ok := names["lo"]; !ok || lo.Flags&net.FlagLoopback == 0 || lo.Kind != "" {
		t.Errorf("lo = %+v", lo)
	}
	if _, ok := names["lan0"]; !ok {
		t.Error("lan0 is not listed")
	}

	for _, name := range []string{"nothere0", "a-name-far-too-long-for-a-link"} {
		if _, err := netlink.LinkByName(name); !errors.Is(err, netlink.ErrLinkNotFound) {
			t.Errorf("look up %s: %v, want not found", name, err)
		}
	}
	if err := netlink.AddLink("lan0", "dummy"); !errors.Is(err, unix.EEXIST) {
		t.Errorf("a second lan0: %v, want EEXIST", err)
	}

	if err := netlink.AddVeth("wan0", "modem0"); err != nil {
		t.Fatal(err)
	}
	wan, modem := netnstest.Link(t, "wan0"), netnstest.Link(t, "modem0")
	if wan.Kind != "veth" || wan.ParentIndex != modem.Index {
		t.Errorf("wan0 = %+v, want a veth whose peer is modem0 (%d)", wan, modem.Index)
	}
	if err := netlink.DeleteLink(wan.Index); err != nil {
		t.Fatal(err)
	}
	if _, err := netlink.LinkByName("modem0"); !errors.Is(err, netlink.ErrLinkNotFound) {
		t.Errorf("modem0 outlived its pair: %v", err)
	}

	// Shaping's helper, where the kernel has the module.
	if err := netlink.AddLink("ifb-lan0", "ifb"); err != nil {
		t.Logf("no ifb here: %v", err)
		return
	}
	ifb := netnstest.Link(t, "ifb-lan0")
	if ifb.Kind != "ifb" {
		t.Errorf("ifb-lan0 = %+v", ifb)
	}
	if err := netlink.SetLinkUp(ifb.Index); err != nil {
		t.Fatal(err)
	}
	if netnstest.Link(t, "ifb-lan0").Flags&net.FlagUp == 0 {
		t.Error("ifb-lan0 did not come up")
	}
}

func TestAddressesAreMadeAndRead(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	lan := netnstest.Dummy(t, "lan0", "192.0.2.1/24", "2001:db8::1/64")
	mine := func(fam int) []string {
		t.Helper()
		addrs, err := netlink.Addrs(fam)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range addrs {
			// The link-local address the kernel gave the link is its own.
			if a.LinkIndex == lan.Index && !a.IPNet.IP.IsLinkLocalUnicast() {
				out = append(out, a.IPNet.String())
				if a.Flags&unix.IFA_F_PERMANENT == 0 {
					t.Errorf("%s is not permanent: %#x", a.IPNet, a.Flags)
				}
			}
		}
		slices.Sort(out)
		return out
	}
	if got := mine(unix.AF_UNSPEC); !slices.Equal(got, []string{"192.0.2.1/24", "2001:db8::1/64"}) {
		t.Errorf("addresses = %v", got)
	}
	if got := mine(unix.AF_INET); !slices.Equal(got, []string{"192.0.2.1/24"}) {
		t.Errorf("IPv4 addresses = %v", got)
	}
	if err := netlink.DeleteAddr(lan.Index, netip.MustParsePrefix("192.0.2.1/24")); err != nil {
		t.Fatal(err)
	}
	if got := mine(unix.AF_UNSPEC); !slices.Equal(got, []string{"2001:db8::1/64"}) {
		t.Errorf("after deleting = %v", got)
	}
}

func TestRoutesAreMadeAndRead(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	add := func(f func(netlink.Route) error, r netlink.Route) {
		t.Helper()
		if err := f(r); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	list := func(fam int, f netlink.RouteFilter) []netlink.Route {
		t.Helper()
		routes, err := netlink.Routes(fam, f)
		if err != nil {
			t.Fatal(err)
		}
		return routes
	}
	defaults := func(routes []netlink.Route) []netlink.Route {
		var out []netlink.Route
		for _, r := range routes {
			if isDefault(r) {
				out = append(out, r)
			}
		}
		return out
	}

	// Two lines at one metric: IPv4 keys a route by its metric, so the
	// second is appended.
	add(netlink.AddRoute, netlink.Route{LinkIndex: wan0.Index, Gw: net.ParseIP("203.0.113.1"), Priority: 10, Protocol: unix.RTPROT_DHCP})
	add(netlink.AppendRoute, netlink.Route{LinkIndex: wan1.Index, Gw: net.ParseIP("198.51.100.1"), Priority: 10})
	main := defaults(list(unix.AF_INET, netlink.RouteFilter{}))
	if len(main) != 2 || main[0].Protocol != unix.RTPROT_DHCP || main[1].Protocol != unix.RTPROT_BOOT ||
		main[0].Priority != 10 || main[0].Table != unix.RT_TABLE_MAIN || main[0].Type != unix.RTN_UNICAST {
		t.Fatalf("main defaults = %+v", main)
	}

	// A policy table, past 255, multipath and then a single hop.
	add(netlink.ReplaceRoute, netlink.Route{Table: 2201, Dst: cidr("0.0.0.0/0"), MultiPath: []netlink.Nexthop{
		{LinkIndex: wan0.Index, Gw: net.ParseIP("203.0.113.1")},
		{LinkIndex: wan1.Index, Gw: net.ParseIP("198.51.100.1")},
	}})
	table := list(unix.AF_INET, netlink.RouteFilter{Table: 2201})
	if len(table) != 1 || len(table[0].MultiPath) != 2 || table[0].MultiPath[1].LinkIndex != wan1.Index || table[0].Table != 2201 {
		t.Fatalf("table 2201 = %+v", table)
	}
	add(netlink.ReplaceRoute, netlink.Route{Table: 2201, Dst: cidr("0.0.0.0/0"), LinkIndex: wan0.Index, Gw: net.ParseIP("203.0.113.1")})
	table = list(unix.AF_INET, netlink.RouteFilter{Table: 2201})
	if len(table) != 1 || len(table[0].MultiPath) != 0 || !table[0].Gw.Equal(net.ParseIP("203.0.113.1")) {
		t.Fatalf("table 2201 after replacing = %+v", table)
	}
	if len(defaults(list(unix.AF_INET, netlink.RouteFilter{}))) != 2 {
		t.Error("the main table lists another table's route")
	}
	all := list(unix.AF_INET, netlink.RouteFilter{AllTables: true})
	if !slices.ContainsFunc(all, func(r netlink.Route) bool { return r.Table == 2201 }) ||
		!slices.ContainsFunc(all, func(r netlink.Route) bool { return r.Table == unix.RT_TABLE_LOCAL }) {
		t.Error("every table left one out")
	}

	// A kill switch blocks both families.
	for _, dst := range []string{"0.0.0.0/0", "::/0"} {
		add(netlink.AddRoute, netlink.Route{Table: 2203, Dst: cidr(dst), Type: unix.RTN_BLACKHOLE})
	}
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		if got := list(fam, netlink.RouteFilter{Table: 2203}); len(got) != 1 || got[0].Type != unix.RTN_BLACKHOLE {
			t.Errorf("family %d blackhole = %+v", fam, got)
		}
	}

	// One link's routes, and deleting the one read removes it alone.
	onWAN1 := list(unix.AF_INET, netlink.RouteFilter{LinkIndex: wan1.Index})
	gone := defaults(onWAN1)
	if len(onWAN1) != 2 || len(gone) != 1 {
		t.Fatalf("wan1's routes = %+v", onWAN1)
	}
	add(netlink.DeleteRoute, gone[0])
	if main := defaults(list(unix.AF_INET, netlink.RouteFilter{})); len(main) != 1 || main[0].LinkIndex != wan0.Index {
		t.Errorf("after deleting wan1's = %+v", main)
	}

	add(netlink.AddRoute, netlink.Route{LinkIndex: wan0.Index, Gw: net.ParseIP("2001:db8:1::1"), Priority: 1024, Protocol: unix.RTPROT_RA})
	if v6 := defaults(list(unix.AF_INET6, netlink.RouteFilter{})); len(v6) != 1 || v6[0].Family != unix.AF_INET6 ||
		!v6[0].Gw.Equal(net.ParseIP("2001:db8:1::1")) || v6[0].Protocol != unix.RTPROT_RA {
		t.Errorf("IPv6 defaults = %+v", v6)
	}
}

// Failover moves a dead line's route by adding a copy at another metric.
// Read while the line had no carrier, the route is flagged linkdown, which
// the kernel refuses on an add; the copy goes in all the same.
func TestALinkdownRouteMovesToAnotherMetric(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	if err := netlink.AddVeth("wan0", "modem0"); err != nil {
		t.Fatal(err)
	}
	wan, modem := netnstest.Link(t, "wan0"), netnstest.Link(t, "modem0")
	for _, l := range []netlink.Link{wan, modem} {
		if err := netlink.SetLinkUp(l.Index); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.AddAddr(wan.Index, netip.MustParsePrefix("203.0.113.2/24")); err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("203.0.113.1"), Priority: 10}); err != nil {
		t.Fatal(err)
	}
	if err := netlink.SetLinkDown(modem.Index); err != nil {
		t.Fatal(err)
	}
	var route netlink.Route
	for start := time.Now(); route.Flags&unix.RTNH_F_LINKDOWN == 0; time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 5*time.Second {
			t.Fatalf("the route never went linkdown: %+v", route)
		}
		routes, err := netlink.Routes(unix.AF_INET, netlink.RouteFilter{LinkIndex: wan.Index})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			if isDefault(r) {
				route = r
			}
		}
	}
	moved := route
	moved.Priority += 1_000_000
	if err := netlink.AppendRoute(moved); err != nil {
		t.Fatalf("append the linkdown route at another metric: %v", err)
	}
	if err := netlink.DeleteRoute(route); err != nil {
		t.Fatalf("delete the linkdown route: %v", err)
	}
	routes, err := netlink.Routes(unix.AF_INET, netlink.RouteFilter{LinkIndex: wan.Index})
	if err != nil {
		t.Fatal(err)
	}
	var metrics []int
	for _, r := range routes {
		if isDefault(r) {
			metrics = append(metrics, r.Priority)
		}
	}
	if !slices.Equal(metrics, []int{1_000_010}) {
		t.Errorf("default route metrics = %v", metrics)
	}
}

// A gateway outside the link's prefix needs the onlink flag, and a copy of
// its route keeps it.
func TestAnOnlinkRouteKeepsItsFlag(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan := netnstest.Dummy(t, "wan0", "203.0.113.2/32")
	if err := netlink.AddRoute(netlink.Route{LinkIndex: wan.Index, Gw: net.ParseIP("198.18.0.1"), Priority: 10, Flags: unix.RTNH_F_ONLINK}); err != nil {
		t.Fatal(err)
	}
	routes, err := netlink.Routes(unix.AF_INET, netlink.RouteFilter{LinkIndex: wan.Index})
	if err != nil || len(routes) != 1 || routes[0].Flags&unix.RTNH_F_ONLINK == 0 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	moved := routes[0]
	moved.Priority += 1_000_000
	if err := netlink.AppendRoute(moved); err != nil {
		t.Errorf("append the onlink route at another metric: %v", err)
	}
}

// The two rules policy routing installs send marked traffic to a table,
// except what the main table knows a way to without its default route.
func TestRulesSendAMarkToItsTable(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	netnstest.Dummy(t, "lan0", "192.0.2.1/24")
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	for _, r := range []netlink.Route{
		{LinkIndex: wan0.Index, Gw: net.ParseIP("203.0.113.1")},
		{Table: 2201, Dst: cidr("0.0.0.0/0"), LinkIndex: wan1.Index, Gw: net.ParseIP("198.51.100.1")},
	} {
		if err := netlink.AddRoute(r); err != nil {
			t.Fatal(err)
		}
	}
	suppress := netlink.NewRule()
	suppress.Family, suppress.Priority, suppress.Mark, suppress.Mask = unix.AF_INET, 22000, 0x10000, 0xff0000
	suppress.Table, suppress.SuppressPrefixlen = unix.RT_TABLE_MAIN, 0
	lookup := netlink.NewRule()
	lookup.Family, lookup.Priority, lookup.Mark, lookup.Mask, lookup.Table = unix.AF_INET, 22001, 0x10000, 0xff0000, 2201
	for _, r := range []netlink.Rule{suppress, lookup} {
		if err := netlink.AddRule(r); err != nil {
			t.Fatalf("add %+v: %v", r, err)
		}
	}
	rules, err := netlink.Rules(unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []netlink.Rule{suppress, lookup} {
		if !slices.Contains(rules, want) {
			t.Errorf("rules %+v lack %+v", rules, want)
		}
	}

	for _, tc := range []struct {
		dst   string
		mark  uint32
		gw    string
		table int
	}{
		{"1.1.1.1", 0x10000, "198.51.100.1", 2201},
		{"1.1.1.1", 0, "203.0.113.1", unix.RT_TABLE_MAIN},
		{"192.0.2.50", 0x10000, "<nil>", unix.RT_TABLE_MAIN},
	} {
		routes, err := netlink.RouteGet(netlink.RouteQuery{Dst: net.ParseIP(tc.dst), Mark: tc.mark})
		if err != nil || len(routes) != 1 {
			t.Fatalf("route to %s: %+v, %v", tc.dst, routes, err)
		}
		if routes[0].Gw.String() != tc.gw || routes[0].Table != tc.table {
			t.Errorf("route to %s with mark %#x = via %s in %d, want via %s in %d",
				tc.dst, tc.mark, routes[0].Gw, routes[0].Table, tc.gw, tc.table)
		}
	}

	for _, r := range []netlink.Rule{suppress, lookup} {
		if err := netlink.DeleteRule(r); err != nil {
			t.Fatalf("delete %+v: %v", r, err)
		}
	}
	if rules, _ := netlink.Rules(unix.AF_INET); slices.Contains(rules, lookup) {
		t.Error("the lookup rule is still there")
	}
}

// A rule can match a source address and a source port, as the ones that
// keep WireGuard's answers on a line do: a lookup from that address and
// port goes to the rule's table, one from another port or address, or to
// a network the main table knows without its default route, does not. The
// rules read back as they were written, in both families.
func TestRulesSendASourceAndPortToItsTable(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	for _, key := range []string{"all", "default"} {
		if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+key+"/accept_dad", []byte("0"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	netnstest.Dummy(t, "lan0", "192.0.2.1/24", "2001:db8:10::1/64")
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24", "2001:db8:2::2/64")
	for _, r := range []netlink.Route{
		{LinkIndex: wan0.Index, Gw: net.ParseIP("203.0.113.1")},
		{LinkIndex: wan0.Index, Dst: cidr("::/0"), Gw: net.ParseIP("2001:db8:1::1")},
		{Table: 2425, Dst: cidr("0.0.0.0/0"), LinkIndex: wan1.Index, Gw: net.ParseIP("198.51.100.1")},
		{Table: 2425, Dst: cidr("::/0"), LinkIndex: wan1.Index, Gw: net.ParseIP("2001:db8:2::1")},
	} {
		if err := netlink.AddRoute(r); err != nil {
			t.Fatalf("route %+v: %v", r, err)
		}
	}
	var rules []netlink.Rule
	for _, fam := range []struct {
		family int
		src    string
	}{{unix.AF_INET, "198.51.100.2/32"}, {unix.AF_INET6, "2001:db8:2::2/128"}} {
		suppress := netlink.NewRule()
		suppress.Family, suppress.Priority, suppress.Src = fam.family, 22450, netip.MustParsePrefix(fam.src)
		suppress.IPProto, suppress.Sport = unix.IPPROTO_UDP, netlink.PortRange{Start: 51820, End: 51820}
		suppress.Table, suppress.SuppressPrefixlen = unix.RT_TABLE_MAIN, 0
		lookup := suppress
		lookup.Priority, lookup.Table, lookup.SuppressPrefixlen = 22451, 2425, -1
		rules = append(rules, suppress, lookup)
	}
	for _, r := range rules {
		if err := netlink.AddRule(r); err != nil {
			t.Fatalf("add %+v: %v", r, err)
		}
	}
	for _, want := range rules {
		have, err := netlink.Rules(want.Family)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(have, want) {
			t.Errorf("rules %+v lack %+v", have, want)
		}
	}

	for _, tc := range []struct {
		dst, src string
		proto    uint8
		sport    uint16
		gw       string
		table    int
	}{
		{"1.1.1.1", "198.51.100.2", unix.IPPROTO_UDP, 51820, "198.51.100.1", 2425},
		{"1.1.1.1", "198.51.100.2", unix.IPPROTO_UDP, 51821, "203.0.113.1", unix.RT_TABLE_MAIN},
		{"1.1.1.1", "198.51.100.2", unix.IPPROTO_TCP, 51820, "203.0.113.1", unix.RT_TABLE_MAIN},
		{"1.1.1.1", "203.0.113.2", unix.IPPROTO_UDP, 51820, "203.0.113.1", unix.RT_TABLE_MAIN},
		{"192.0.2.50", "198.51.100.2", unix.IPPROTO_UDP, 51820, "<nil>", unix.RT_TABLE_MAIN},
		{"2001:db8:ff::9", "2001:db8:2::2", unix.IPPROTO_UDP, 51820, "2001:db8:2::1", 2425},
		{"2001:db8:ff::9", "2001:db8:2::2", unix.IPPROTO_UDP, 51821, "2001:db8:1::1", unix.RT_TABLE_MAIN},
		{"2001:db8:10::50", "2001:db8:2::2", unix.IPPROTO_UDP, 51820, "<nil>", unix.RT_TABLE_MAIN},
	} {
		routes, err := netlink.RouteGet(netlink.RouteQuery{
			Dst: net.ParseIP(tc.dst), Src: net.ParseIP(tc.src), IPProto: tc.proto, Sport: tc.sport,
		})
		if err != nil || len(routes) != 1 {
			t.Fatalf("route to %s from %s: %+v, %v", tc.dst, tc.src, routes, err)
		}
		if routes[0].Gw.String() != tc.gw || routes[0].Table != tc.table {
			t.Errorf("route to %s from %s, protocol %d port %d = via %s in %d, want via %s in %d",
				tc.dst, tc.src, tc.proto, tc.sport, routes[0].Gw, routes[0].Table, tc.gw, tc.table)
		}
	}

	for _, r := range rules {
		if err := netlink.DeleteRule(r); err != nil {
			t.Fatalf("delete %+v: %v", r, err)
		}
	}
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		have, _ := netlink.Rules(fam)
		if slices.ContainsFunc(have, func(r netlink.Rule) bool { return r.Src.IsValid() }) {
			t.Errorf("rules %+v still match a source", have)
		}
	}
}

func TestAddressChangesAreAnnounced(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	link := netnstest.Dummy(t, "dyn0")
	ctx, cancel := context.WithCancel(t.Context())
	changes, err := netlink.WatchAddrs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddAddr(link.Index, netip.MustParsePrefix("203.0.113.7/24")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("a new address was not announced")
	}
	cancel()
	timeout := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-changes:
		case <-timeout:
			t.Fatal("the watch outlived its context")
		}
	}
}

func TestRouteChangesAreAnnounced(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	updates, err := netlink.WatchRoutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marker := netlink.Route{Table: 100, Dst: cidr("192.0.2.0/24"), Type: unix.RTN_BLACKHOLE}
	wait := func(deleted bool) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case u := <-updates:
				if u.Table == 100 && u.Deleted == deleted {
					return
				}
			case <-timeout:
				t.Fatalf("no announcement, deleted %v", deleted)
			}
		}
	}
	if err := netlink.AddRoute(marker); err != nil {
		t.Fatal(err)
	}
	wait(false)
	if err := netlink.DeleteRoute(marker); err != nil {
		t.Fatal(err)
	}
	wait(true)
	cancel()
	timeout := time.After(5 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-updates:
		case <-timeout:
			t.Fatal("the watch outlived its context")
		}
	}
}

// A route leaving a table the watcher keeps is signalled; one leaving
// another table, or one coming in, is not.
func TestRouteDeletesAreSignalledForTheTablesKept(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	link := netnstest.Dummy(t, "dummy0", "192.0.2.1/24")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	signals, err := netlink.WatchRouteDeletes(ctx, func(r netlink.Route) bool { return r.Table == 2201 })
	if err != nil {
		t.Fatal(err)
	}
	expect := func(want bool, what string) {
		t.Helper()
		select {
		case <-signals:
			if !want {
				t.Errorf("%s was signalled", what)
			}
		case <-time.After(300 * time.Millisecond):
			if want {
				t.Errorf("%s was not signalled", what)
			}
		}
	}
	for _, table := range []int{2201, 100} {
		for _, dst := range []string{"198.51.100.0/24", "203.0.113.0/24"} {
			if err := netlink.AddRoute(netlink.Route{Table: table, Dst: cidr(dst), Gw: net.ParseIP("192.0.2.254"), LinkIndex: link.Index}); err != nil {
				t.Fatal(err)
			}
		}
	}
	expect(false, "adding routes")
	for _, dst := range []string{"198.51.100.0/24", "203.0.113.0/24"} {
		if err := netlink.DeleteRoute(netlink.Route{Table: 100, Dst: cidr(dst)}); err != nil {
			t.Fatal(err)
		}
	}
	expect(false, "a route leaving another table")
	for _, dst := range []string{"198.51.100.0/24", "203.0.113.0/24"} {
		if err := netlink.DeleteRoute(netlink.Route{Table: 2201, Dst: cidr(dst)}); err != nil {
			t.Fatal(err)
		}
	}
	expect(true, "a route leaving the table kept")
}

func TestNeighboursAreRead(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	lan := netnstest.Dummy(t, "lan0", "192.0.2.1/24")
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	for _, n := range []netlink.Neighbour{
		{LinkIndex: lan.Index, Family: unix.AF_INET, State: unix.NUD_PERMANENT, IP: net.ParseIP("192.0.2.50"), HardwareAddr: mac},
		{LinkIndex: lan.Index, Family: unix.AF_INET, State: unix.NUD_REACHABLE, IP: net.ParseIP("192.0.2.51"), HardwareAddr: mac},
	} {
		if err := netlink.AddNeighbour(n); err != nil {
			t.Fatal(err)
		}
	}
	ns, err := netlink.Neighbours(unix.AF_INET)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]netlink.Neighbour{}
	for _, n := range ns {
		found[n.IP.String()] = n
	}
	if n := found["192.0.2.50"]; n.State != unix.NUD_PERMANENT || n.HardwareAddr.String() != mac.String() || n.LinkIndex != lan.Index {
		t.Errorf("192.0.2.50 = %+v", n)
	}
	// Confirmed counts clock ticks, a hundred to the second.
	if n := found["192.0.2.51"]; n.State != unix.NUD_REACHABLE || n.Confirmed > 500 {
		t.Errorf("192.0.2.51 = %+v", n)
	}
	if v6, _ := netlink.Neighbours(unix.AF_INET6); slices.ContainsFunc(v6, func(n netlink.Neighbour) bool { return n.IP.To4() != nil }) {
		t.Error("the IPv6 table lists an IPv4 neighbour")
	}
}

func TestQdiscsAreReadPerDevice(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	lan := netnstest.Dummy(t, "lan0")
	netnstest.Dummy(t, "lan1")
	qs, err := netlink.Qdiscs(lan.Index)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) == 0 || !slices.ContainsFunc(qs, func(q netlink.Qdisc) bool { return q.Parent == 0xFFFFFFFF && q.Kind != "" }) {
		t.Errorf("lan0's qdiscs = %+v, want a root", qs)
	}
	for _, q := range qs {
		if q.LinkIndex != lan.Index {
			t.Errorf("lan0's list holds %+v", q)
		}
	}
}

// A group is its reader's until the reader closes, and free again after.
func TestALogGroupIsHeldUntilItsReaderCloses(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	first, err := netlink.OpenLog(5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := netlink.OpenLog(5); !errors.Is(err, unix.EPERM) {
		t.Errorf("a second reader of a held group: %v, want EPERM", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := netlink.OpenLog(5)
	if err != nil {
		t.Fatalf("the group after its reader closed: %v", err)
	}
	_ = again.Close()
}

// A packet as large as IPv4 allows is cut at the kernel's copy range, not
// lost, and what follows it still arrives.
func TestABigLoggedPacketIsCutNotDropped(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	if err := netlink.SetLinkUp(netnstest.Link(t, "lo").Index); err != nil {
		t.Fatal(err)
	}
	netnstest.Ruleset(t, `table inet t {
	chain out { type filter hook output priority 0; oifname "lo" udp dport 9 log prefix "big: " group 7; }
}`)
	r, err := netlink.OpenLog(7)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	packets := make(chan netlink.LogPacket, 4)
	done := make(chan error, 1)
	go func() {
		done <- r.Read(ctx, func(p netlink.LogPacket) { packets <- p }, func(err error) { t.Errorf("read: %v", err) })
	}()

	// Something listens, or the first datagram's port unreachable fails
	// the next write.
	sink, err := net.ListenPacket("udp", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sink.Close() }()
	c, err := net.Dial("udp", "127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, n := range []int{65507, 5} {
		if _, err := c.Write(make([]byte, n)); err != nil {
			t.Fatalf("send %d bytes: %v", n, err)
		}
	}
	// The kernel hands packets over in batches, within a second.
	for _, want := range []int{65531, 20 + 8 + 5} {
		select {
		case p := <-packets:
			if len(p.Payload) != want || p.Prefix != "big: " || p.OutDev != 1 || p.InDev != 0 || !p.Time.IsZero() {
				t.Errorf("packet of %d bytes, prefix %q, in %d, out %d, time %v; want %d bytes out of lo",
					len(p.Payload), p.Prefix, p.InDev, p.OutDev, p.Time, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no packet of %d bytes", want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("read ended with %v", err)
	}
}

// Failover moves a route by adding a copy at another metric and deleting
// the original. The copy keeps everything else the kernel held of it: the
// lease's source address, its metrics, its realm, an IPv6 router's
// preference.
func TestACopiedRouteKeepsWhatTheKernelHeld(t *testing.T) {
	if !netnstest.Enter(t, "ip") {
		return
	}
	wan := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	ip := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), "ip", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	ip("route", "add", "default", "via", "203.0.113.1", "dev", "wan0", "proto", "dhcp", "src", "203.0.113.2",
		"metric", "10", "mtu", "1492", "hoplimit", "32", "realm", "7")
	ip("-6", "route", "add", "default", "via", "2001:db8:1::1", "dev", "wan0", "proto", "ra",
		"metric", "10", "pref", "high", "mtu", "1480", "hoplimit", "64")
	for _, fam := range []int{unix.AF_INET, unix.AF_INET6} {
		flag := "-4"
		if fam == unix.AF_INET6 {
			flag = "-6"
		}
		before := ip(flag, "-d", "route", "show", "default")
		routes, err := netlink.Routes(fam, netlink.RouteFilter{LinkIndex: wan.Index})
		if err != nil {
			t.Fatal(err)
		}
		var route netlink.Route
		for _, r := range routes {
			if isDefault(r) {
				route = r
			}
		}
		moved := route
		moved.Priority += 1_000_000
		if err := netlink.AppendRoute(moved); err != nil {
			t.Fatalf("family %d copy: %v", fam, err)
		}
		if err := netlink.DeleteRoute(route); err != nil {
			t.Fatalf("family %d original: %v", fam, err)
		}
		want := strings.Replace(before, "metric 10 ", "metric 1000010 ", 1)
		if got := ip(flag, "-d", "route", "show", "default"); got != want {
			t.Errorf("family %d moved:\n%s\nwant\n%s", fam, got, want)
		}
	}
}

// Two ends of a tunnel, the far one in a namespace of its own, shake hands
// over a veth once the near one sends a datagram through it. The dump then
// reads back the peer, where it was heard from, the handshake and the
// bytes each way.
func TestAWireGuardDeviceReportsItsPeerInKernel(t *testing.T) {
	if _, err := os.Stat("/sys/module/wireguard"); err != nil {
		testenv.Unavailable(t, "the wireguard module is not loaded")
	}
	if !netnstest.Enter(t) {
		return
	}
	near, far := wgKeys(t), wgKeys(t)
	if err := netlink.AddVeth("v0", "v1"); err != nil {
		t.Fatal(err)
	}
	ns := netnstest.NewNS(t)
	if err := netlink.SetLinkNamespace(netnstest.Link(t, "v1").Index, int(ns.Fd())); err != nil {
		t.Fatal(err)
	}
	end := func(veth, addr, tunnel string, self *ecdh.PrivateKey, peer *ecdh.PrivateKey, port uint16, endpoint, allowed string) {
		v := netnstest.Link(t, veth)
		if err := netlink.AddAddr(v.Index, netip.MustParsePrefix(addr)); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetLinkUp(v.Index); err != nil {
			t.Fatal(err)
		}
		if err := netlink.AddLink("wg0", "wireguard"); err != nil {
			t.Fatalf("add a WireGuard device: %v", err)
		}
		wg := netnstest.Link(t, "wg0")
		if err := netlink.AddAddr(wg.Index, netip.MustParsePrefix(tunnel)); err != nil {
			t.Fatal(err)
		}
		if err := netlink.SetWireGuard("wg0", self.Bytes(), port, []netlink.WireGuardPeerConfig{{
			PublicKey:  peer.PublicKey().Bytes(),
			Endpoint:   netip.MustParseAddrPort(endpoint),
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix(allowed)},
		}}); err != nil {
			t.Fatalf("set the WireGuard device: %v", err)
		}
		if err := netlink.SetLinkUp(wg.Index); err != nil {
			t.Fatal(err)
		}
	}
	end("v0", "10.0.0.1/24", "10.66.0.1/24", near, far, 51820, "10.0.0.2:51821", "10.66.0.2/32")
	netnstest.Do(t, ns, func() {
		end("v1", "10.0.0.2/24", "10.66.0.2/24", far, near, 51821, "10.0.0.1:51820", "10.66.0.1/32")
	})

	// Unconnected, so the far end's "port unreachable" for the datagram,
	// proof the tunnel carried it, is not handed back as an error.
	c, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	to := &net.UDPAddr{IP: net.ParseIP("10.66.0.2"), Port: 9}
	var dev netlink.WireGuardDevice
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := c.WriteTo([]byte("hello"), to); err != nil {
			t.Fatal(err)
		}
		if dev, err = netlink.WireGuard("wg0"); err != nil {
			t.Fatal(err)
		}
		if len(dev.Peers) == 1 && !dev.Peers[0].LastHandshake.IsZero() && dev.Peers[0].RxBytes > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no handshake in five seconds: %+v", dev)
		}
	}
	if dev.Name != "wg0" || dev.ListenPort != 51820 || !bytes.Equal(dev.PublicKey, near.PublicKey().Bytes()) {
		t.Errorf("device %s on %d with key %x", dev.Name, dev.ListenPort, dev.PublicKey)
	}
	p := dev.Peers[0]
	if !bytes.Equal(p.PublicKey, far.PublicKey().Bytes()) || p.Endpoint.String() != "10.0.0.2:51821" || p.TxBytes == 0 {
		t.Errorf("peer = %+v", p)
	}
	if since := time.Since(p.LastHandshake); since < 0 || since > time.Minute {
		t.Errorf("last handshake %v ago", since)
	}
}

// wgKeys makes a WireGuard key pair.
func wgKeys(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// conntrackOn tracks what the namespace sends and sets its accounting and
// its events. The keys are the namespace's own.
func conntrackOn(t *testing.T, acct, events string) {
	t.Helper()
	if err := netlink.SetLinkUp(netnstest.Link(t, "lo").Index); err != nil {
		t.Fatal(err)
	}
	netnstest.Ruleset(t, `table inet t {
	chain out { type filter hook output priority 0; ct state new counter; }
}`)
	sysctl(t, "nf_conntrack_acct", acct)
	sysctl(t, "nf_conntrack_events", events)
}

func sysctl(t *testing.T, key, value string) {
	t.Helper()
	if err := os.WriteFile("/proc/sys/net/netfilter/"+key, []byte(value+"\n"), 0o644); err != nil {
		t.Fatalf("%s: %v", key, err)
	}
}

// udpPair binds a receiver on a loopback port and dials it: what the
// socket it returns writes is one connection.
func udpPair(t *testing.T, port int) net.Conn {
	t.Helper()
	sink, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	c, err := net.Dial("udp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// send writes datagrams of the sizes given.
func send(t *testing.T, c net.Conn, sizes ...int) {
	t.Helper()
	for _, n := range sizes {
		if _, err := c.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
}

// flowTo reads the tracked connection to a loopback port.
func flowTo(t *testing.T, port int) netlink.Flow {
	t.Helper()
	var found []netlink.Flow
	if err := netlink.Flows(unix.AF_INET, func(f netlink.Flow) {
		if f.Forward.DstPort == uint16(port) {
			found = append(found, f)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("%d connections to %d", len(found), port)
	}
	return found[0]
}

// ends collects the connections announced as they end.
func ends(t *testing.T) (<-chan netlink.Flow, func()) {
	t.Helper()
	r, err := netlink.OpenFlowEnds()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	out := make(chan netlink.Flow, 16)
	done := make(chan error, 1)
	go func() {
		done <- r.Read(ctx, func(f netlink.Flow) { out <- f }, func(err error) { t.Errorf("read: %v", err) })
	}()
	return out, func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("read ended with %v", err)
		}
		_ = r.Close()
	}
}

// endOf waits for the end of the connection with id.
func endOf(t *testing.T, got <-chan netlink.Flow, id uint32) netlink.Flow {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case f := <-got:
			if f.ID == id {
				return f
			}
		case <-deadline:
			t.Fatalf("no end for connection %d", id)
		}
	}
}

// A connection has counters only if it opened while accounting was on,
// and its end carries them: switching accounting on later does not count
// what is already open, which is why the router keeps it on.
func TestAConnectionsCountersAndItsEnd(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	conntrackOn(t, "0", "2")
	early := udpPair(t, 9000)
	send(t, early, 1000)
	sysctl(t, "nf_conntrack_acct", "1")
	send(t, early, 1000)
	if f := flowTo(t, 9000); f.Forward.Packets != 0 || f.Forward.Bytes != 0 {
		t.Errorf("opened without accounting: %d packets, %d bytes", f.Forward.Packets, f.Forward.Bytes)
	}

	got, stop := ends(t)
	defer stop()
	c := udpPair(t, 9001)
	send(t, c, 1000, 1000)
	f := flowTo(t, 9001)
	// Two datagrams of 1,000 bytes, and 28 bytes of headers each.
	if f.ID == 0 || f.Forward.Packets != 2 || f.Forward.Bytes != 2056 {
		t.Errorf("flow = id %d, %d packets, %d bytes", f.ID, f.Forward.Packets, f.Forward.Bytes)
	}
	send(t, c, 500)
	if err := netlink.FlushFlows(); err != nil {
		t.Fatal(err)
	}
	end := endOf(t, got, f.ID)
	if end.Forward.Packets != 3 || end.Forward.Bytes != 2584 || end.Forward.DstPort != 9001 {
		t.Errorf("end = %d packets, %d bytes to %d", end.Forward.Packets, end.Forward.Bytes, end.Forward.DstPort)
	}
}

// With events at 1 a connection opened while nobody listened still
// announces its end, so a restarted daemon loses no connection's last
// bytes.
func TestEventsAtOneReachAConnectionOpenedBeforeTheListener(t *testing.T) {
	if !netnstest.Enter(t, "nft") {
		return
	}
	conntrackOn(t, "1", "1")
	send(t, udpPair(t, 9002), 700)
	f := flowTo(t, 9002)
	got, stop := ends(t)
	defer stop()
	if err := netlink.FlushFlows(); err != nil {
		t.Fatal(err)
	}
	if end := endOf(t, got, f.ID); end.Forward.Bytes != 728 {
		t.Errorf("end = %+v", end.Forward)
	}
}
