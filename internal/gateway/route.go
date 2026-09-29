package gateway

import (
	"errors"
	"fmt"
	"net"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/netlink"
)

// DemoteMetric is added to the metric of every default route through a
// gateway that fails its monitor. Gateway metrics stop at 2560 and
// networkd's default is 1024, so a demoted route sits below every working
// one, but it stays in the table: the probe, pinned to the gateway's
// interface, still has a way out to a monitor beyond the gateway, and a
// family that no other gateway serves keeps the only route it has.
const DemoteMetric = 1_000_000

// NetlinkRouter moves default routes with netlink. What it did is kept in
// the kernel, in the metric, so it outlives a restart of the daemon, and a
// step that failed is simply taken again on the next tick.
type NetlinkRouter struct{}

// NewNetlinkRouter returns a router backed by the kernel.
func NewNetlinkRouter() *NetlinkRouter { return &NetlinkRouter{} }

// Resolve reports the next hop currently installed for a gateway. A
// gateway with a configured address answers with it; a dynamic one is
// looked up in the routing table of its interface.
func (r *NetlinkRouter) Resolve(g Status) (string, bool) {
	if g.Address != "" {
		return g.Address, true
	}
	routes, err := r.defaultRoutes(g)
	if err != nil {
		return "", false
	}
	for _, route := range routes {
		if route.Family == unix.AF_INET {
			return route.Gw.String(), true
		}
	}
	return "", false
}

// Demote moves every default route through a dead gateway below the rest.
// It reports whether anything moved, so a route that came back at its own
// metric since, networkd renewing a lease for one, is moved again.
func (r *NetlinkRouter) Demote(g Status) (bool, error) {
	// A gateway that learns its next hop and has none yet was never probed,
	// only found without an IPv4 route, which says nothing about its IPv6
	// ones.
	if g.learned && g.Address == "" {
		return false, nil
	}
	routes, err := r.defaultRoutes(g)
	if err != nil {
		return false, err
	}
	return shift(routes, func(m int) bool { return m < DemoteMetric }, DemoteMetric, g.Name)
}

// Restore puts a gateway's demoted default routes back at their own metric.
func (r *NetlinkRouter) Restore(g Status) (bool, error) {
	routes, err := r.defaultRoutes(g)
	if err != nil {
		return false, err
	}
	return shift(routes, func(m int) bool { return m >= DemoteMetric }, -DemoteMetric, g.Name)
}

// Forget is for a gateway nothing watches any more. Its demoted routes go,
// except where one is the last default route its family has, which is put
// back instead.
func (r *NetlinkRouter) Forget(g Status) error {
	routes, err := r.defaultRoutes(g)
	if err != nil {
		return err
	}
	for _, route := range routes {
		if route.Priority < DemoteMetric {
			continue
		}
		others, err := otherDefaults(route)
		if err != nil {
			return err
		}
		if others == 0 {
			if _, err := shift([]netlink.Route{route}, func(int) bool { return true }, -DemoteMetric, g.Name); err != nil {
				return err
			}
			continue
		}
		if err := netlink.DeleteRoute(route); err != nil && !errors.Is(err, unix.ESRCH) {
			return fmt.Errorf("remove the demoted route via %s: %w", g.Name, err)
		}
	}
	return nil
}

// shift moves each route that picked chooses by delta. The copy goes in
// first and the original comes out second, so the gateway is never
// without a route between the two.
func shift(routes []netlink.Route, picked func(metric int) bool, delta int, name string) (bool, error) {
	moved := false
	for _, route := range routes {
		if !picked(route.Priority) {
			continue
		}
		next := route
		next.Priority = route.Priority + delta
		if !holds(routes, next) {
			// Appended, because another gateway of the same priority may
			// already hold that metric, and the kernel keys a route by it.
			if err := netlink.AppendRoute(next); err != nil {
				return moved, fmt.Errorf("move the default route via %s: %w", name, err)
			}
		}
		if err := netlink.DeleteRoute(route); err != nil && !errors.Is(err, unix.ESRCH) {
			return moved, fmt.Errorf("move the default route via %s: %w", name, err)
		}
		moved = true
	}
	return moved, nil
}

// holds reports whether routes already has want: networkd may have put the
// original back while it was demoted, or a demoted copy may be left over.
func holds(routes []netlink.Route, want netlink.Route) bool {
	for _, r := range routes {
		if r.Family == want.Family && r.LinkIndex == want.LinkIndex && r.Priority == want.Priority && r.Gw.Equal(want.Gw) {
			return true
		}
	}
	return false
}

// otherDefaults counts the default routes of route's family that are not
// route itself.
func otherDefaults(route netlink.Route) (int, error) {
	all, err := netlink.Routes(route.Family, netlink.RouteFilter{})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range all {
		if (r.Dst != nil && !isDefault(r.Dst)) || (r.Gw == nil && len(r.MultiPath) == 0) {
			continue
		}
		if r.LinkIndex == route.LinkIndex && r.Priority == route.Priority && r.Gw.Equal(route.Gw) {
			continue
		}
		n++
	}
	return n, nil
}

// defaultRoutes finds a gateway's default routes, demoted or not: those
// through its address when it has one of its own. A gateway that learns
// its next hop from the network has every IPv4 default route on its
// interface, and the IPv6 ones carrying its metric, which is how networkd
// marks the routes it learned for that gateway.
func (r *NetlinkRouter) defaultRoutes(g Status) ([]netlink.Route, error) {
	link, err := netlink.LinkByName(g.Interface)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", g.Interface, err)
	}
	want := net.ParseIP(g.Address)
	families := []int{unix.AF_INET}
	switch {
	case g.learned:
		families = append(families, unix.AF_INET6)
	case want != nil && want.To4() == nil:
		families = []int{unix.AF_INET6}
	}
	var out []netlink.Route
	for _, family := range families {
		routes, err := netlink.Routes(family, netlink.RouteFilter{LinkIndex: link.Index})
		if err != nil {
			return nil, err
		}
		for _, route := range routes {
			if route.Gw == nil || (route.Dst != nil && !isDefault(route.Dst)) {
				continue
			}
			switch {
			case family == unix.AF_INET6 && g.learned:
				if route.Priority%DemoteMetric != g.Metric {
					continue
				}
			case want != nil:
				if !route.Gw.Equal(want) {
					continue
				}
			}
			out = append(out, route)
		}
	}
	return out, nil
}

// Carriers names the gateways whose default routes the main table
// prefers, at most one per family: the route with the lowest metric, the
// first of equals. A route no gateway owns can win instead, such as
// networkd's for a DHCP lease no gateway names, and that family then has
// none.
func (r *NetlinkRouter) Carriers(gs []Status) ([]string, error) {
	var out []string
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		routes, err := netlink.Routes(family, netlink.RouteFilter{})
		if err != nil {
			return nil, err
		}
		var best *netlink.Route
		for i, route := range routes {
			if (route.Dst == nil || isDefault(route.Dst)) && (best == nil || route.Priority < best.Priority) {
				best = &routes[i]
			}
		}
		if best == nil || best.Gw == nil {
			continue
		}
		for _, g := range gs {
			if own, err := r.defaultRoutes(g); err == nil && holds(own, *best) {
				if !slices.Contains(out, g.Name) {
					out = append(out, g.Name)
				}
				break
			}
		}
	}
	return out, nil
}

// isDefault reports whether a prefix is 0.0.0.0/0 or ::/0.
func isDefault(n *net.IPNet) bool {
	ones, _ := n.Mask.Size()
	return ones == 0 && n.IP.IsUnspecified()
}

// ReadOnlyRouter resolves gateways but never touches the routing table.
// The CLI uses it so a diagnostic run cannot fight the daemon's monitor.
type ReadOnlyRouter struct{ Router *NetlinkRouter }

// Resolve implements Router.
func (r ReadOnlyRouter) Resolve(g Status) (string, bool) { return r.Router.Resolve(g) }

// Demote implements Router and does nothing.
func (ReadOnlyRouter) Demote(Status) (bool, error) { return false, nil }

// Restore implements Router and does nothing.
func (ReadOnlyRouter) Restore(Status) (bool, error) { return false, nil }

// Forget implements Router and does nothing.
func (ReadOnlyRouter) Forget(Status) error { return nil }

// Carriers implements Router; it only reads.
func (r ReadOnlyRouter) Carriers(gs []Status) ([]string, error) { return r.Router.Carriers(gs) }
