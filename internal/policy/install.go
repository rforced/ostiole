package policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/netlink"
)

// Installer keeps the kernel's policy routing in step with a plan. Sync is
// idempotent: entries that are already right are left alone, and entries
// left over from an earlier configuration are removed, so a revert takes
// effect on the next pass.
type Installer struct {
	Log *slog.Logger
}

// NewInstaller returns an installer that logs through log.
func NewInstaller(log *slog.Logger) *Installer {
	if log == nil {
		log = slog.Default()
	}
	return &Installer{Log: log}
}

// Sync reconciles both address families with targets.
func (i *Installer) Sync(targets []Target) error {
	var errs []error
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		if err := i.syncFamily(family, targets); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Clear removes every table and rule Ostiole owns. It is the uninstall path:
// a daemon that stops leaves them, so routing holds across a restart.
func (i *Installer) Clear() error { return i.Sync(nil) }

func (i *Installer) syncFamily(family int, targets []Target) error {
	existing, err := netlink.Routes(family, netlink.RouteFilter{AllTables: true})
	if err != nil {
		return fmt.Errorf("list policy routes: %w", err)
	}
	var addrs []netlink.Addr
	if slices.ContainsFunc(targets, func(t Target) bool { return t.Line != "" && t.Up && len(t.Ports) > 0 }) {
		if addrs, err = netlink.Addrs(family); err != nil {
			return fmt.Errorf("list addresses: %w", err)
		}
	}
	wantRoutes := map[int][]*netlink.Route{}
	var wantRules []netlink.Rule
	for _, t := range targets {
		var routes []*netlink.Route
		link := 0
		if t.Line != "" {
			if l, err := netlink.LinkByName(t.Line); err == nil {
				link = l.Index
				routes = lineRoutes(t, family, link, existing)
			}
		} else {
			routes = i.routesFor(t, family)
		}
		if len(routes) == 0 {
			// Nothing to route through: leave the table empty so the mark
			// falls through to the main table.
			continue
		}
		wantRoutes[t.Table] = routes
		wantRules = append(wantRules, rulesFor(t, family)...)
		wantRules = append(wantRules, sourceRules(t, family, link, addrs)...)
	}

	var errs []error
	if err := i.syncRoutes(existing, wantRoutes); err != nil {
		errs = append(errs, err)
	}
	// Rules come after routes so a mark never points at an empty table.
	if err := i.syncRules(family, wantRules); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ownTable reports whether a routing table id belongs to policy routing:
// the gateways', groups' and translations', then the lines'.
func ownTable(id int) bool {
	return id > model.PolicyTableBase && id <= model.ReplyTableBase+model.MaxReplyLines
}

// lineTable reports whether a routing table id is a line's.
func lineTable(id int) bool {
	return id > model.ReplyTableBase && id <= model.ReplyTableBase+model.MaxReplyLines
}

// WatchRemoved signals each time a route leaves one of policy routing's
// tables, until ctx is done.
func WatchRemoved(ctx context.Context) (<-chan struct{}, error) {
	return netlink.WatchRouteDeletes(ctx, func(r netlink.Route) bool { return ownTable(r.Table) })
}

// ownPriority reports whether an ip rule priority belongs to policy routing:
// the translations' and the lines', then the gateways' and the source rules.
// They are the ranges every release has owned, so a release removes what
// another left behind.
func ownPriority(p int) bool {
	const numbers = model.MaxPolicyTargets + model.MaxReplyLines + 1
	return (p >= TranslatePriorityBase && p < TranslatePriorityBase+numbers) ||
		(p >= RulePriorityBase && p < RulePriorityBase+2*numbers)
}

// syncRoutes makes the policy tables among existing, every route of the
// family, what want holds.
func (i *Installer) syncRoutes(existing []netlink.Route, want map[int][]*netlink.Route) error {
	var errs []error
	have := map[*netlink.Route]bool{}
	for k := range existing {
		route := existing[k]
		if !ownTable(route.Table) {
			continue
		}
		same := sameRoute
		if lineTable(route.Table) {
			same = sameCopy
		}
		at := slices.IndexFunc(want[route.Table], func(d *netlink.Route) bool { return !have[d] && same(&route, d) })
		if at >= 0 {
			have[want[route.Table][at]] = true
			continue
		}
		if err := netlink.DeleteRoute(route); err != nil {
			errs = append(errs, fmt.Errorf("remove route from table %d: %w", route.Table, err))
		}
	}
	for table, routes := range want {
		for _, route := range routes {
			if have[route] {
				continue
			}
			// A line's table can hold several routes the kernel keys alike,
			// as the main table does, so they go in beside each other.
			install := netlink.ReplaceRoute
			if lineTable(table) {
				install = netlink.AppendRoute
			}
			if err := install(*route); err != nil {
				errs = append(errs, fmt.Errorf("install route in table %d: %w", table, err))
				continue
			}
			i.Log.Info("policy route installed", "table", table, "route", describe(route))
		}
	}
	return errors.Join(errs...)
}

// syncRules makes the rules at policy routing's priorities what want holds.
// Several can share a priority.
func (i *Installer) syncRules(family int, want []netlink.Rule) error {
	existing, err := netlink.Rules(family)
	if err != nil {
		return fmt.Errorf("list ip rules: %w", err)
	}
	var errs []error
	kept := make([]bool, len(want))
	for _, rule := range existing {
		if !ownPriority(rule.Priority) {
			continue
		}
		at := -1
		for k, w := range want {
			if !kept[k] && w == rule {
				at = k
				break
			}
		}
		if at >= 0 {
			kept[at] = true
			continue
		}
		if err := netlink.DeleteRule(rule); err != nil {
			errs = append(errs, fmt.Errorf("remove ip rule %d: %w", rule.Priority, err))
		}
	}
	for k, rule := range want {
		if kept[k] {
			continue
		}
		if err := netlink.AddRule(rule); err != nil {
			errs = append(errs, fmt.Errorf("add ip rule %d: %w", rule.Priority, err))
			continue
		}
		args := []any{"priority", rule.Priority, "mark", fmt.Sprintf("0x%x", rule.Mark), "table", rule.Table}
		if rule.Src.IsValid() {
			args = append(args, "from", rule.Src, "sport", rule.Sport.Start)
		}
		i.Log.Info("policy rule installed", args...)
	}
	return errors.Join(errs...)
}

// routesFor is a target's table in one family: the networks a translation
// sends into its tunnel, or routeFor's default route.
func (i *Installer) routesFor(t Target, family int) []*netlink.Route {
	if len(t.Networks) == 0 {
		if r := i.routeFor(t, family); r != nil {
			return []*netlink.Route{r}
		}
		return nil
	}
	link, err := netlink.LinkByName(t.Tunnel)
	if err != nil {
		return nil
	}
	var out []*netlink.Route
	for _, n := range t.Networks {
		if n.Addr().Is4() != (family == unix.AF_INET) {
			continue
		}
		dst := &net.IPNet{IP: n.Addr().AsSlice(), Mask: net.CIDRMask(n.Bits(), n.Addr().BitLen())}
		r := &netlink.Route{Table: t.Table, Dst: dst, LinkIndex: link.Index}
		if family == unix.AF_INET {
			r.Scope = unix.RT_SCOPE_LINK
		}
		out = append(out, r)
	}
	return out
}

// routeFor builds the default route for a target in one family: the best
// tier that has an online hop, a multipath route when that tier has
// several, a blackhole for a blocking target with nothing online, and nil
// when the traffic should fall back to the main table. A tier holding a
// tunnel that does not carry the family refuses it, rather than let a
// later tier's line take what the tunnel was meant to hide.
func (i *Installer) routeFor(t Target, family int) *netlink.Route {
	v4 := family == unix.AF_INET
	for _, tier := range t.Tiers {
		var hops []netlink.Nexthop
		tunnel := false
		for _, h := range tier {
			if !h.Online {
				continue
			}
			if h.Device {
				link, err := netlink.LinkByName(h.Interface)
				if err != nil {
					continue
				}
				tunnel = true
				if h.carries(v4) {
					hops = append(hops, netlink.Nexthop{LinkIndex: link.Index})
				}
				continue
			}
			ip := net.ParseIP(h.Address)
			if ip == nil || (ip.To4() != nil) != v4 {
				continue
			}
			link, err := netlink.LinkByName(h.Interface)
			if err != nil {
				continue
			}
			hops = append(hops, netlink.Nexthop{LinkIndex: link.Index, Gw: ip})
		}
		if len(hops) == 0 {
			if tunnel {
				return &netlink.Route{Table: t.Table, Dst: defaultDst(family), Type: unix.RTN_UNREACHABLE}
			}
			continue
		}
		route := &netlink.Route{Table: t.Table, Dst: defaultDst(family)}
		if len(hops) == 1 {
			route.LinkIndex, route.Gw = hops[0].LinkIndex, hops[0].Gw
			if route.Gw == nil && v4 {
				// What ip(8) gives an IPv4 route with no next hop.
				route.Scope = unix.RT_SCOPE_LINK
			}
		} else {
			route.MultiPath = hops
		}
		return route
	}
	if t.Block {
		return &netlink.Route{Table: t.Table, Dst: defaultDst(family), Type: unix.RTN_BLACKHOLE}
	}
	return nil
}

// rulesFor returns the two ip rules a target needs. The first sends the
// mark to the main table but hides its default route, so connected
// networks, static routes, and the LAN keep working; only traffic that
// would have taken the default route reaches the second rule and the
// policy table.
func rulesFor(t Target, family int) []netlink.Rule {
	// A line's table holds nothing but its own routes, so it is asked
	// alone: an answer goes back by its line even when the main table knows
	// a shorter way to where it is going.
	if t.Line != "" {
		r := netlink.NewRule()
		r.Family = family
		r.Priority = LinePriorityBase + t.Index
		r.Mark = t.Mark
		r.Mask = model.ReplyMarkMask
		r.Table = t.Table
		return []netlink.Rule{r}
	}
	// A translation's table routes networks the main table has too, so it
	// is asked first, and alone.
	if len(t.Networks) > 0 {
		r := netlink.NewRule()
		r.Family = family
		r.Priority = TranslatePriorityBase + t.Index
		r.Mark = t.Mark
		r.Mask = model.PolicyMarkMask
		r.Table = t.Table
		return []netlink.Rule{r}
	}
	suppressPriority, lookupPriority := t.Priorities()

	suppress := netlink.NewRule()
	suppress.Family = family
	suppress.Priority = suppressPriority
	suppress.Mark = t.Mark
	suppress.Mask = model.PolicyMarkMask
	suppress.Table = unix.RT_TABLE_MAIN
	suppress.SuppressPrefixlen = 0

	lookup := netlink.NewRule()
	lookup.Family = family
	lookup.Priority = lookupPriority
	lookup.Mark = t.Mark
	lookup.Mask = model.PolicyMarkMask
	lookup.Table = t.Table

	return []netlink.Rule{suppress, lookup}
}

// lineRoutes is a line's table in one family: a copy of every route the
// main table has out of the line, whatever its metric, a demoted one too,
// so answers leave by the line whatever the monitor makes of it. A
// multipath route keeps its hops out of the line. A tunnel's line gets a
// default route into it in the families it carries.
func lineRoutes(t Target, family, link int, existing []netlink.Route) []*netlink.Route {
	var out []*netlink.Route
	for _, r := range existing {
		if r.Table != unix.RT_TABLE_MAIN || routeType(&r) != unix.RTN_UNICAST {
			continue
		}
		if len(r.MultiPath) > 0 {
			var hops []netlink.Nexthop
			for _, h := range r.MultiPath {
				if h.LinkIndex == link {
					hops = append(hops, h)
				}
			}
			switch len(hops) {
			case 0:
				continue
			case 1:
				r.LinkIndex, r.Gw, r.MultiPath = hops[0].LinkIndex, hops[0].Gw, nil
			default:
				r.MultiPath = hops
			}
		} else if r.LinkIndex != link {
			continue
		}
		r.Table = t.Table
		out = append(out, &r)
	}
	if into := (family == unix.AF_INET && t.IntoV4) || (family == unix.AF_INET6 && t.IntoV6); into &&
		!slices.ContainsFunc(out, func(r *netlink.Route) bool { return isDefault(r.Dst) }) {
		r := &netlink.Route{Table: t.Table, Dst: defaultDst(family), LinkIndex: link}
		if family == unix.AF_INET {
			// What ip(8) gives an IPv4 route with no next hop.
			r.Scope = unix.RT_SCOPE_LINK
		}
		out = append(out, r)
	}
	return out
}

// sourceRules keep WireGuard's answers on the line a peer called, while
// the line is up: from the line's own addresses and a tunnel's listen
// port, what would take the default route takes the line's table.
// WireGuard looks its route up before the firewall sees the packet, with
// the address the peer called and its listen port, and when that route
// leaves by another line it answers from that line's address instead, so
// the marks never reach it. A tunnel this router dials has a port of its
// own and keeps following the main table.
func sourceRules(t Target, family, link int, addrs []netlink.Addr) []netlink.Rule {
	if t.Line == "" || !t.Up {
		return nil
	}
	var out []netlink.Rule
	for _, a := range addrs {
		if a.LinkIndex != link || a.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) != 0 {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.IPNet.IP)
		if ip = ip.Unmap(); !ok || !ip.IsGlobalUnicast() || ip.Is4() != (family == unix.AF_INET) {
			continue
		}
		for _, port := range t.Ports {
			suppress := netlink.NewRule()
			suppress.Family = family
			suppress.Priority = SourcePriority
			suppress.Src = netip.PrefixFrom(ip, ip.BitLen())
			suppress.IPProto = unix.IPPROTO_UDP
			suppress.Sport = netlink.PortRange{Start: port, End: port}
			suppress.Table = unix.RT_TABLE_MAIN
			suppress.SuppressPrefixlen = 0
			lookup := suppress
			lookup.Priority, lookup.Table, lookup.SuppressPrefixlen = SourcePriority+1, t.Table, -1
			out = append(out, suppress, lookup)
		}
	}
	return out
}

// sameCopy compares routes in a line's table, where several can lead to
// one destination at different metrics: the metric and the preferred
// source count too.
func sameCopy(a, b *netlink.Route) bool {
	return sameRoute(a, b) && metric(a) == metric(b) && a.Src.Equal(b.Src)
}

// metric is a route's metric as the kernel keeps it: IPv6 gives a route
// asked for with none 1024.
func metric(r *netlink.Route) int {
	if r.Priority == 0 && (r.Family == unix.AF_INET6 || (r.Dst != nil && r.Dst.IP.To4() == nil)) {
		return 1024
	}
	return r.Priority
}

func sameRoute(a, b *netlink.Route) bool {
	if a.Table != b.Table || routeType(a) != routeType(b) || !sameDst(a.Dst, b.Dst) {
		return false
	}
	// A blackhole or an unreachable route leads nowhere, and the kernel
	// reads an IPv6 one back on the loopback device.
	if routeType(a) != unix.RTN_UNICAST {
		return true
	}
	if len(a.MultiPath) != len(b.MultiPath) {
		return false
	}
	if len(a.MultiPath) == 0 {
		return a.LinkIndex == b.LinkIndex && a.Gw.Equal(b.Gw)
	}
	for i := range a.MultiPath {
		if a.MultiPath[i].LinkIndex != b.MultiPath[i].LinkIndex || !a.MultiPath[i].Gw.Equal(b.MultiPath[i].Gw) {
			return false
		}
	}
	return true
}

// routeType normalises the route type: an ordinary route is asked for with
// the field left at zero but comes back from the kernel as unicast, and
// mistaking one for the other would delete and reinstall every route on
// every pass.
func routeType(r *netlink.Route) int {
	if r.Type == 0 {
		return unix.RTN_UNICAST
	}
	return r.Type
}

// sameDst compares destinations, treating the nil the kernel reports for a
// default route as the zero-length prefix we ask for.
func sameDst(a, b *net.IPNet) bool {
	aDefault, bDefault := isDefault(a), isDefault(b)
	if aDefault || bDefault {
		return aDefault && bDefault
	}
	return a.String() == b.String()
}

func isDefault(n *net.IPNet) bool {
	if n == nil {
		return true
	}
	ones, _ := n.Mask.Size()
	return ones == 0
}

func defaultDst(family int) *net.IPNet {
	if family == unix.AF_INET6 {
		return &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	return &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
}

func describe(r *netlink.Route) string {
	switch r.Type {
	case unix.RTN_BLACKHOLE:
		return "blackhole"
	case unix.RTN_UNREACHABLE:
		return "unreachable"
	}
	if len(r.MultiPath) > 0 {
		out := "multipath"
		for _, h := range r.MultiPath {
			out += " " + hopName(h.Gw, h.LinkIndex)
		}
		return out
	}
	if !isDefault(r.Dst) {
		return r.Dst.String() + " " + hopName(r.Gw, r.LinkIndex)
	}
	return hopName(r.Gw, r.LinkIndex)
}

// hopName is a next hop as a log line gives it, or the device a hop with
// none goes into.
func hopName(gw net.IP, link int) string {
	if gw != nil {
		return "via " + gw.String()
	}
	if l, err := net.InterfaceByIndex(link); err == nil {
		return "dev " + l.Name
	}
	return fmt.Sprintf("dev %d", link)
}

// Reply is a line as the Routing page shows it: the table that answers
// what came in on it, and where that table sends answers now.
type Reply struct {
	Interface string `json:"interface"`
	Table     int    `json:"table"`
	// NextHops are its default routes' next hops, or the device one with
	// none goes into; empty while it has none and answers take the main
	// table's.
	NextHops []string `json:"nextHops"`
}

// Replies reads the lines' tables from the kernel.
func Replies(cfg *model.Config) ([]Reply, error) {
	out := []Reply{}
	for _, l := range cfg.ReplyLines() {
		r := Reply{Interface: l.Interface, Table: l.Table, NextHops: []string{}}
		for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
			routes, err := netlink.Routes(family, netlink.RouteFilter{Table: l.Table})
			if err != nil {
				return nil, fmt.Errorf("list table %d: %w", l.Table, err)
			}
			for _, route := range routes {
				if !isDefault(route.Dst) {
					continue
				}
				hops := route.MultiPath
				if len(hops) == 0 {
					hops = []netlink.Nexthop{{LinkIndex: route.LinkIndex, Gw: route.Gw}}
				}
				for _, h := range hops {
					switch {
					case h.Gw != nil:
						r.NextHops = append(r.NextHops, h.Gw.String())
					default:
						if link, err := net.InterfaceByIndex(h.LinkIndex); err == nil {
							r.NextHops = append(r.NextHops, link.Name)
						}
					}
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}
