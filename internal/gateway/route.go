package gateway

import (
	"fmt"
	"net"
	"sync"

	"github.com/vishvananda/netlink"
)

// NetlinkRouter moves default routes with netlink. A demoted gateway has
// its default route removed and remembered; restoring puts the same route
// back, metric and all.
type NetlinkRouter struct {
	mu      sync.Mutex
	removed map[string]*netlink.Route
}

// NewNetlinkRouter returns a router backed by the kernel.
func NewNetlinkRouter() *NetlinkRouter {
	return &NetlinkRouter{removed: map[string]*netlink.Route{}}
}

// Resolve reports the next hop currently installed for a gateway. A
// gateway with a configured address answers with it; a dynamic one is
// looked up in the routing table of its interface.
func (r *NetlinkRouter) Resolve(g Status) (string, bool) {
	if g.Address != "" {
		return g.Address, true
	}
	route, err := r.defaultRoute(g)
	if err != nil || route == nil || route.Gw == nil {
		return "", false
	}
	return route.Gw.String(), true
}

// Demote removes the default route that runs through a dead gateway.
func (r *NetlinkRouter) Demote(g Status) error {
	route, err := r.defaultRoute(g)
	if err != nil {
		return err
	}
	if route == nil {
		return nil // nothing to remove; the kernel already agrees
	}
	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("remove default route via %s: %w", g.Name, err)
	}
	r.mu.Lock()
	r.removed[g.Name] = route
	r.mu.Unlock()
	return nil
}

// Restore puts a previously removed default route back.
func (r *NetlinkRouter) Restore(g Status) error {
	r.mu.Lock()
	route := r.removed[g.Name]
	delete(r.removed, g.Name)
	r.mu.Unlock()
	if route == nil {
		return nil
	}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("restore default route via %s: %w", g.Name, err)
	}
	return nil
}

// defaultRoute finds the default route on the gateway's interface, and
// through its address when one is configured.
func (r *NetlinkRouter) defaultRoute(g Status) (*netlink.Route, error) {
	link, err := netlink.LinkByName(g.Interface)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", g.Interface, err)
	}
	family := netlink.FAMILY_V4
	want := net.ParseIP(g.Address)
	if want != nil && want.To4() == nil {
		family = netlink.FAMILY_V6
	}
	routes, err := netlink.RouteList(link, family)
	if err != nil {
		return nil, err
	}
	for i := range routes {
		route := routes[i]
		if route.Dst != nil && !isDefault(route.Dst) {
			continue
		}
		if route.Gw == nil {
			continue
		}
		if want != nil && !route.Gw.Equal(want) {
			continue
		}
		return &route, nil
	}
	return nil, nil
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
func (ReadOnlyRouter) Demote(Status) error { return nil }

// Restore implements Router and does nothing.
func (ReadOnlyRouter) Restore(Status) error { return nil }
