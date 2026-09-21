package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// StaleRouteWait is how long a sweep waits for the restored manager to
// put a default route of its own on a link before giving up on that link
// and leaving what it finds alone.
const StaleRouteWait = 20 * time.Second

// DefaultRoute is one default route the kernel holds, remembered
// precisely enough to delete exactly it and nothing beside it.
type DefaultRoute struct {
	Interface string
	Gateway   string
	Metric    int
	// Protocol says where the route came from: dhcp, ra, static, kernel.
	Protocol string
	Family   int

	route netlink.Route
}

// dynamic reports whether a lease or a router advertisement put this
// route in the kernel. Those are the only ones a sweep will remove: a
// static route is somebody's decision, and one a routing daemon owns is
// that daemon's business.
func (d DefaultRoute) dynamic() bool {
	switch int(d.route.Protocol) {
	case unix.RTPROT_DHCP, unix.RTPROT_RA:
		return true
	}
	return false
}

// is reports whether two records name the same route.
func (d DefaultRoute) is(o DefaultRoute) bool {
	return d.sameLink(o) && d.Gateway == o.Gateway && d.Metric == o.Metric &&
		d.route.Protocol == o.route.Protocol
}

// sameLink reports whether two records sit on one link, in one family.
func (d DefaultRoute) sameLink(o DefaultRoute) bool {
	return d.Family == o.Family && d.route.LinkIndex == o.route.LinkIndex
}

// KernelRoutes reads and removes routes through netlink.
type KernelRoutes struct{}

// Defaults lists the default routes in the main table.
func (KernelRoutes) Defaults() ([]DefaultRoute, error) { return defaultRoutes() }

// SweepStale removes the default routes in before that are still in the
// kernel on a link some other manager has since taken over. It is how a
// network revert tidies up after systemd-networkd, which leaves the route
// it was given by DHCP sitting next to the one the restored manager
// installs, at a metric that hides it.
//
// Nothing is removed unless a replacement default route is already in the
// kernel on the same link and family, so the router is never left with
// one route fewer than it had. A route nobody replaces within wait is
// left exactly where it is, which is what happened before this existed.
func (KernelRoutes) SweepStale(ctx context.Context, before []DefaultRoute, wait time.Duration) ([]DefaultRoute, error) {
	if len(before) == 0 {
		return nil, nil
	}
	deadline := time.Now().Add(wait)
	for {
		now, err := defaultRoutes()
		if err != nil {
			return nil, err
		}
		lingering := stillThere(before, now)
		if len(lingering) == 0 {
			// Either networkd took its routes with it or there were none
			// to begin with. Nothing to wait for.
			return nil, nil
		}
		var stale []DefaultRoute
		for _, l := range lingering {
			if replaced(l, before, now) {
				stale = append(stale, l)
			}
		}
		if len(stale) > 0 {
			return removeRoutes(stale)
		}
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// stillThere returns the dynamic routes from before that the kernel still
// has, as the kernel has them now.
func stillThere(before, now []DefaultRoute) []DefaultRoute {
	var out []DefaultRoute
	for _, b := range before {
		if !b.dynamic() {
			continue
		}
		for _, n := range now {
			if b.is(n) {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// replaced reports whether a default route has since been joined on its
// own link by one that was not there before, which is the evidence that
// another manager now owns that link.
func replaced(route DefaultRoute, before, now []DefaultRoute) bool {
	for _, n := range now {
		if !n.sameLink(route) || n.is(route) {
			continue
		}
		known := false
		for _, b := range before {
			if b.is(n) {
				known = true
				break
			}
		}
		if !known {
			return true
		}
	}
	return false
}

func removeRoutes(routes []DefaultRoute) ([]DefaultRoute, error) {
	var removed []DefaultRoute
	var errs []error
	for _, r := range routes {
		route := r.route
		if err := netlink.RouteDel(&route); err != nil {
			errs = append(errs, fmt.Errorf("remove default route via %s on %s: %w", r.Gateway, r.Interface, err))
			continue
		}
		removed = append(removed, r)
	}
	return removed, errors.Join(errs...)
}

func defaultRoutes() ([]DefaultRoute, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	byIndex := map[int]string{}
	for _, l := range links {
		byIndex[l.Attrs().Index] = l.Attrs().Name
	}
	var out []DefaultRoute
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		// RouteList answers with the main table only, which is the one
		// the default route lives in; a policy-routing table of our own
		// is never swept.
		routes, err := netlink.RouteList(nil, family)
		if err != nil {
			return nil, fmt.Errorf("list routes: %w", err)
		}
		for i := range routes {
			r := routes[i]
			if r.Gw == nil || !defaultDst(r.Dst) {
				continue
			}
			out = append(out, DefaultRoute{
				Interface: byIndex[r.LinkIndex],
				Gateway:   r.Gw.String(),
				Metric:    r.Priority,
				Protocol:  r.Protocol.String(),
				Family:    family,
				route:     r,
			})
		}
	}
	return out, nil
}

// defaultDst reports whether a destination covers everything. A listed
// default route carries either no destination at all or a zero-length
// prefix, depending on the family.
func defaultDst(dst *net.IPNet) bool {
	if dst == nil {
		return true
	}
	ones, _ := dst.Mask.Size()
	return ones == 0 && dst.IP.IsUnspecified()
}
