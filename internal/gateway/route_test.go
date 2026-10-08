package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/netnstest"
)

// addDefault installs a default route the way a network manager does.
func addDefault(t *testing.T, link netlink.Link, gw string, metric int, proto netlink.Protocol) {
	t.Helper()
	err := netlink.AddRoute(netlink.Route{
		LinkIndex: link.Index,
		Gw:        net.ParseIP(gw),
		Priority:  metric,
		Protocol:  proto,
	})
	if err != nil {
		t.Fatalf("add default via %s: %v", gw, err)
	}
}

// nextHop asks the kernel where it would send a packet for dst.
func nextHop(t *testing.T, dst string) string {
	t.Helper()
	routes, err := netlink.RouteGet(netlink.RouteQuery{Dst: net.ParseIP(dst)})
	if err != nil {
		t.Fatalf("route to %s: %v", dst, err)
	}
	if len(routes) != 1 || routes[0].Gw == nil {
		t.Fatalf("route to %s = %+v, want one through a gateway", dst, routes)
	}
	return routes[0].Gw.String()
}

// defaultOn returns the default route the main table holds on a link, or
// nil when there is none.
func defaultOn(t *testing.T, link netlink.Link, family int) *netlink.Route {
	t.Helper()
	routes, err := netlink.Routes(family, netlink.RouteFilter{LinkIndex: link.Index})
	if err != nil {
		t.Fatalf("list routes on %s: %v", link.Name, err)
	}
	for i := range routes {
		if routes[i].Gw != nil && (routes[i].Dst == nil || isDefault(routes[i].Dst)) {
			return &routes[i]
		}
	}
	return nil
}

// cable brings up a veth pair carrying addr on its near end. Taking the far
// end down takes the near end's carrier with it, the way a WAN port loses
// its link when the modem goes off or the cable comes out.
func cable(t *testing.T, name, far, addr string) (netlink.Link, netlink.Link) {
	t.Helper()
	if err := netlink.AddVeth(name, far); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	var links []netlink.Link
	for _, n := range []string{name, far} {
		link := netnstest.Link(t, n)
		if err := netlink.SetLinkUp(link.Index); err != nil {
			t.Fatalf("bring %s up: %v", n, err)
		}
		links = append(links, link)
	}
	if err := netlink.AddAddr(links[0].Index, netip.MustParsePrefix(addr)); err != nil {
		t.Fatalf("address %s on %s: %v", addr, name, err)
	}
	return links[0], links[1]
}

// When the primary line loses its link the kernel keeps its default route,
// marked linkdown and still preferred, so it is the monitor that moves the
// traffic to the backup, and back again once the line answers. What comes
// back is the route networkd installed, metric and all, so the priorities
// still order the gateways.
func TestLosingThePrimaryLinkMovesTrafficToTheBackupAndBack(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0, modem := cable(t, "wan0", "modem0", "203.0.113.2/24")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	addDefault(t, wan1, "198.51.100.1", 20, unix.RTPROT_STATIC)

	prober := &fakeProber{fail: map[string]bool{}}
	m := New(prober, NewNetlinkRouter(), slog.New(slog.DiscardHandler))
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "wan0", Address: "203.0.113.1"},
		{Name: "backup", Enabled: true, Interface: "wan1", Address: "198.51.100.1", Priority: 1},
	}})
	tick(m, RiseAfter)
	if got := nextHop(t, "1.1.1.1"); got != "203.0.113.1" {
		t.Fatalf("with both lines up traffic leaves via %s, want the primary", got)
	}

	if err := netlink.SetLinkDown(modem.Index); err != nil {
		t.Fatalf("pull the cable: %v", err)
	}
	prober.setFail("203.0.113.1", true)
	tick(m, FailAfter)
	if got := nextHop(t, "1.1.1.1"); got != "198.51.100.1" {
		t.Fatalf("with the primary down traffic leaves via %s, want the backup", got)
	}

	if err := netlink.SetLinkUp(modem.Index); err != nil {
		t.Fatalf("plug the cable back in: %v", err)
	}
	prober.setFail("203.0.113.1", false)
	tick(m, RiseAfter)
	if got := nextHop(t, "1.1.1.1"); got != "203.0.113.1" {
		t.Fatalf("with the primary back traffic leaves via %s, want the primary", got)
	}
	if r := defaultOn(t, wan0, unix.AF_INET); r == nil || r.Priority != 10 || r.Protocol != unix.RTPROT_STATIC {
		t.Errorf("restored route = %+v, want networkd's metric 10 and protocol static", r)
	}
}

// A gateway that takes its next hop from the network is found through the
// lease's route. Demoting it keeps that route, below every other, along with
// the IPv6 route networkd gave the gateway's metric, and restoring puts both
// back exactly: the metric, the protocol the gateways page and the route
// sweep read, and the source address. An IPv6 route of another metric is
// another gateway's and stays where it is.
func TestADynamicGatewayKeepsItsLeaseRoutesWhileDemoted(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	err := netlink.AddRoute(netlink.Route{
		LinkIndex: wan0.Index,
		Gw:        net.ParseIP("203.0.113.1"),
		Src:       net.ParseIP("203.0.113.2"),
		Priority:  10,
		Protocol:  unix.RTPROT_DHCP,
	})
	if err != nil {
		t.Fatalf("add the lease's route: %v", err)
	}
	addDefault(t, wan0, "fe80::1", 10, unix.RTPROT_RA)
	addDefault(t, wan0, "fe80::9", 30, unix.RTPROT_STATIC)

	r := NewNetlinkRouter()
	addr, v6 := r.Resolve(Status{Name: "wan", Interface: "wan0", Metric: 10, learned: true})
	if addr != "203.0.113.1" || v6 != "fe80::1" {
		t.Fatalf("resolved %q and %q, want the lease's next hop and the advertised one at the gateway's metric", addr, v6)
	}
	// The monitor hands the router the address it resolved and the metric
	// networkd was told to give the gateway's routes.
	g := Status{Name: "wan", Interface: "wan0", Address: addr, Metric: 10, learned: true}
	if moved, err := r.Demote(g); err != nil || !moved {
		t.Fatalf("demote = %v, %v", moved, err)
	}
	if route := defaultOn(t, wan0, unix.AF_INET); route == nil || route.Priority != DemoteMetric+10 ||
		route.Protocol != unix.RTPROT_DHCP || !route.Src.Equal(net.ParseIP("203.0.113.2")) {
		t.Fatalf("demoted lease route = %+v, want it kept at metric %d", route, DemoteMetric+10)
	}
	if got := metricsOn(t, wan0, unix.AF_INET6); !slices.Equal(got, []int{30, DemoteMetric + 10}) {
		t.Errorf("IPv6 metrics while demoted = %v, want the other gateway's 30 kept", got)
	}
	if moved, err := r.Restore(g); err != nil || !moved {
		t.Fatalf("restore = %v, %v", moved, err)
	}
	route := defaultOn(t, wan0, unix.AF_INET)
	if route == nil || !route.Gw.Equal(net.ParseIP("203.0.113.1")) || route.Priority != 10 ||
		route.Protocol != unix.RTPROT_DHCP || !route.Src.Equal(net.ParseIP("203.0.113.2")) {
		t.Errorf("restored route = %+v, want the lease's route as it was", route)
	}
	if got := metricsOn(t, wan0, unix.AF_INET6); !slices.Equal(got, []int{10, 30}) {
		t.Errorf("IPv6 metrics restored = %v", got)
	}
}

// metricsOn lists the metrics of the default routes on a link, lowest first.
func metricsOn(t *testing.T, link netlink.Link, family int) []int {
	t.Helper()
	routes, err := netlink.Routes(family, netlink.RouteFilter{LinkIndex: link.Index})
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, r := range routes {
		if r.Gw != nil && (r.Dst == nil || isDefault(r.Dst)) {
			out = append(out, r.Priority)
		}
	}
	slices.Sort(out)
	return out
}

// farSide moves the far end of a cable into a namespace of its own, the
// provider's side of a WAN link. The cable end carries the gateway's
// address, and monitor is an address of the far side that sits beyond the
// gateway, the way a public resolver does: the far side answers ARP only
// for the cable's own address, so the monitor is reached through the
// gateway or not at all. Taking monitor away makes the line look dead.
func farSide(t *testing.T, end netlink.Link, gateway, monitor string) (take, give func()) {
	t.Helper()
	there := netnstest.NewNS(t)
	netnstest.Do(t, there, func() {
		if err := os.WriteFile("/proc/sys/net/ipv4/conf/all/arp_ignore", []byte("1"), 0o600); err != nil {
			t.Fatalf("arp_ignore on the far side: %v", err)
		}
	})
	if err := netlink.SetLinkNamespace(end.Index, int(there.Fd())); err != nil {
		t.Fatalf("move %s: %v", end.Name, err)
	}
	var lo netlink.Link
	netnstest.Do(t, there, func() {
		cable := netnstest.Link(t, end.Name)
		lo = netnstest.Link(t, "lo")
		for _, l := range []netlink.Link{cable, lo} {
			if err := netlink.SetLinkUp(l.Index); err != nil {
				t.Fatal(err)
			}
		}
		if err := netlink.AddAddr(cable.Index, netip.MustParsePrefix(gateway+"/24")); err != nil {
			t.Fatal(err)
		}
	})
	far := netip.MustParsePrefix(monitor + "/32")
	give = func() {
		netnstest.Do(t, there, func() {
			if err := netlink.AddAddr(lo.Index, far); err != nil && !errors.Is(err, unix.EEXIST) {
				t.Fatalf("give the far side %s: %v", monitor, err)
			}
		})
	}
	take = func() {
		netnstest.Do(t, there, func() {
			if err := netlink.DeleteAddr(lo.Index, far); err != nil {
				t.Fatalf("take %s from the far side: %v", monitor, err)
			}
		})
	}
	give()
	return take, give
}

// probing sends real echo requests to one address and answers for the rest.
type probing struct {
	real    Prober
	address string
}

func (p probing) Probe(ctx context.Context, address, iface string, timeout time.Duration) (time.Duration, error) {
	if address != p.address {
		return time.Millisecond, nil
	}
	return p.real.Probe(ctx, address, iface, timeout)
}

// A gateway watched through an address beyond it, as the dialog suggests,
// is still probed through its own line while it is demoted, so it is seen
// to come back and takes the traffic again. Taking its route away instead
// left the probe with nowhere to go, and the gateway stayed down for good.
func TestAGatewayWatchedBeyondItselfComesBack(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0, modem := cable(t, "wan0", "modem0", "203.0.113.2/24")
	take, give := farSide(t, modem, "203.0.113.1", "192.0.2.53")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	addDefault(t, wan1, "198.51.100.1", 20, unix.RTPROT_STATIC)

	m := New(probing{real: NewICMPProber(), address: "192.0.2.53"}, NewNetlinkRouter(), slog.New(slog.DiscardHandler))
	m.Timeout = 300 * time.Millisecond
	m.Configure(&model.Config{Gateways: []model.Gateway{
		{Name: "primary", Enabled: true, Interface: "wan0", Address: "203.0.113.1", Monitor: "192.0.2.53"},
		{Name: "backup", Enabled: true, Interface: "wan1", Address: "198.51.100.1", Priority: 1},
	}})
	tick(m, RiseAfter)
	if s := m.Statuses()[0]; !s.Online {
		t.Fatalf("the primary never answered through its line: %+v", s)
	}

	take()
	tick(m, FailAfter)
	if got := nextHop(t, "1.1.1.1"); got != "198.51.100.1" {
		t.Fatalf("with the line dead traffic leaves via %s, want the backup", got)
	}

	give()
	tick(m, RiseAfter)
	if s := m.Statuses()[0]; !s.Online {
		t.Fatalf("the primary came back but the probe never saw it: %+v", s)
	}
	if got := nextHop(t, "1.1.1.1"); got != "203.0.113.1" {
		t.Errorf("with the line back traffic leaves via %s, want the primary", got)
	}
}

// Something can put a demoted route back at its own metric: networkd does
// on a lease renewal. The next demote moves it again, and nothing is moved
// twice.
func TestARouteThatComesBackWhileDemotedIsMovedAgain(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	r := NewNetlinkRouter()
	g := Status{Name: "primary", Interface: "wan0", Address: "203.0.113.1", Metric: 10}
	if moved, err := r.Demote(g); err != nil || !moved {
		t.Fatalf("demote = %v, %v", moved, err)
	}
	if moved, err := r.Demote(g); err != nil || moved {
		t.Errorf("demoting again = %v, %v, want nothing to move", moved, err)
	}

	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	if moved, err := r.Demote(g); err != nil || !moved {
		t.Fatalf("demote after the route came back = %v, %v", moved, err)
	}
	if got := metricsOn(t, wan0, unix.AF_INET); !slices.Equal(got, []int{DemoteMetric + 10}) {
		t.Errorf("metrics = %v, want the one demoted route", got)
	}

	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	if moved, err := r.Restore(g); err != nil || !moved {
		t.Fatalf("restore = %v, %v", moved, err)
	}
	if moved, err := r.Restore(g); err != nil || moved {
		t.Errorf("restoring again = %v, %v, want nothing to move", moved, err)
	}
	if got := metricsOn(t, wan0, unix.AF_INET); !slices.Equal(got, []int{10}) {
		t.Errorf("metrics = %v, want the route back once", got)
	}
}

// The kernel's choice decides which gateway is active: the lowest metric
// among the main table's default routes, a demoted one's included, and a
// route no gateway owns can beat them all.
func TestCarriersAreWhatTheKernelPrefers(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	lease := netnstest.Dummy(t, "wan2", "192.0.2.2/24")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	addDefault(t, wan1, "198.51.100.1", 20, unix.RTPROT_STATIC)
	r := NewNetlinkRouter()
	gs := []Status{
		{Name: "primary", Interface: "wan0", Address: "203.0.113.1", Metric: 10},
		{Name: "backup", Interface: "wan1", Address: "198.51.100.1", Metric: 20},
	}
	carriers := func() []string {
		t.Helper()
		got, err := r.Carriers(gs)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := carriers(); !slices.Equal(got, []string{"primary"}) {
		t.Errorf("carriers = %v, want primary", got)
	}
	if _, err := r.Demote(gs[0]); err != nil {
		t.Fatal(err)
	}
	if got := carriers(); !slices.Equal(got, []string{"backup"}) {
		t.Errorf("with the primary demoted, carriers = %v, want backup", got)
	}
	addDefault(t, lease, "192.0.2.1", 5, unix.RTPROT_DHCP)
	if got := carriers(); len(got) != 0 {
		t.Errorf("under a lower route no gateway owns, carriers = %v, want none", got)
	}
}

// A gateway nobody watches any more leaves no demoted route behind, unless
// that route is the last default route there is, which goes back in.
func TestForgettingAGatewayClearsItsDemotedRoute(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24")
	wan1 := netnstest.Dummy(t, "wan1", "198.51.100.2/24")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	addDefault(t, wan1, "198.51.100.1", 20, unix.RTPROT_STATIC)
	r := NewNetlinkRouter()
	primary := Status{Name: "primary", Interface: "wan0", Address: "203.0.113.1", Metric: 10}
	backup := Status{Name: "backup", Interface: "wan1", Address: "198.51.100.1", Metric: 20}

	if _, err := r.Demote(primary); err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(primary); err != nil {
		t.Fatal(err)
	}
	if got := metricsOn(t, wan0, unix.AF_INET); len(got) != 0 {
		t.Errorf("metrics on wan0 = %v, want the demoted route gone: the backup carries on", got)
	}

	if _, err := r.Demote(backup); err != nil {
		t.Fatal(err)
	}
	if err := r.Forget(backup); err != nil {
		t.Fatal(err)
	}
	if got := metricsOn(t, wan1, unix.AF_INET); !slices.Equal(got, []int{20}) {
		t.Errorf("metrics on wan1 = %v, want the last default route put back", got)
	}
}

// An IPv6 gateway, reached through the link-local next hop a router
// advertisement gives, is demoted and restored the same way, and only
// IPv6 moves: the line's IPv4 route belongs to another gateway entry.
func TestDemotingAnIPv6GatewayMovesOnlyIPv6(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "203.0.113.2/24", "2001:db8:1::2/64")
	wan1 := netnstest.Dummy(t, "wan1", "2001:db8:2::2/64")
	addDefault(t, wan0, "203.0.113.1", 10, unix.RTPROT_STATIC)
	addDefault(t, wan0, "fe80::1", 10, unix.RTPROT_RA)
	addDefault(t, wan1, "fe80::2", 20, unix.RTPROT_RA)

	r := NewNetlinkRouter()
	g := Status{Name: "primary6", Interface: "wan0", Address: "fe80::1"}
	if _, err := r.Demote(g); err != nil {
		t.Fatalf("demote: %v", err)
	}
	if got := nextHop(t, "2606:4700::1111"); got != "fe80::2" {
		t.Errorf("with the primary demoted IPv6 leaves via %s, want the backup", got)
	}
	if got := nextHop(t, "1.1.1.1"); got != "203.0.113.1" {
		t.Errorf("demoting the IPv6 gateway moved IPv4 to %s", got)
	}
	if _, err := r.Restore(g); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := nextHop(t, "2606:4700::1111"); got != "fe80::1" {
		t.Errorf("with the primary restored IPv6 leaves via %s, want the primary", got)
	}
}

// A gateway that learns its next hop and has no IPv4 lease is marked down
// without a probe ever being sent, which says nothing about the IPv6 line
// it also has, so its IPv6 route stays where it is.
func TestAGatewayWithNoLeaseLeavesItsIPv6RouteAlone(t *testing.T) {
	if !netnstest.Enter(t) {
		return
	}
	wan0 := netnstest.Dummy(t, "wan0", "2001:db8:1::2/64")
	addDefault(t, wan0, "fe80::1", 10, unix.RTPROT_RA)
	r := NewNetlinkRouter()
	if moved, err := r.Demote(Status{Name: "wan", Interface: "wan0", Metric: 10, learned: true}); err != nil || moved {
		t.Errorf("demote = %v, %v, want nothing moved", moved, err)
	}
	if got := metricsOn(t, wan0, unix.AF_INET6); !slices.Equal(got, []int{10}) {
		t.Errorf("IPv6 metrics = %v, want the route left at 10", got)
	}
}
