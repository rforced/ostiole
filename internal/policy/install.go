package policy

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/rforced/ostiole/internal/model"
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
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if err := i.syncFamily(family, targets); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Clear removes every table and rule Ostiole owns. It is the uninstall and
// shutdown path.
func (i *Installer) Clear() error { return i.Sync(nil) }

func (i *Installer) syncFamily(family int, targets []Target) error {
	wantRoutes := map[int]*netlink.Route{}
	wantRules := map[int]netlink.Rule{}
	for _, t := range targets {
		route := i.routeFor(t, family)
		if route == nil {
			// Nothing to route through: leave the table empty so the mark
			// falls through to the main table.
			continue
		}
		wantRoutes[t.Table] = route
		for _, rule := range rulesFor(t, family) {
			wantRules[rule.Priority] = rule
		}
	}

	var errs []error
	if err := i.syncRoutes(family, wantRoutes); err != nil {
		errs = append(errs, err)
	}
	// Rules come after routes so a mark never points at an empty table.
	if err := i.syncRules(family, wantRules); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ownTable reports whether a routing table id belongs to policy routing.
func ownTable(id int) bool {
	return id > model.PolicyTableBase && id <= model.PolicyTableBase+model.MaxPolicyTargets
}

// ownPriority reports whether an ip rule priority belongs to policy routing.
func ownPriority(p int) bool {
	return p >= RulePriorityBase && p < RulePriorityBase+2*(model.MaxPolicyTargets+1)
}

func (i *Installer) syncRoutes(family int, want map[int]*netlink.Route) error {
	existing, err := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("list policy routes: %w", err)
	}
	var errs []error
	seen := map[int]bool{}
	for k := range existing {
		route := existing[k]
		if !ownTable(route.Table) {
			continue
		}
		desired := want[route.Table]
		if desired != nil && sameRoute(&route, desired) {
			seen[route.Table] = true
			continue
		}
		if err := netlink.RouteDel(&route); err != nil {
			errs = append(errs, fmt.Errorf("remove route from table %d: %w", route.Table, err))
		}
	}
	for table, route := range want {
		if seen[table] {
			continue
		}
		if err := netlink.RouteReplace(route); err != nil {
			errs = append(errs, fmt.Errorf("install route in table %d: %w", table, err))
			continue
		}
		i.Log.Info("policy route installed", "table", table, "route", describe(route))
	}
	return errors.Join(errs...)
}

func (i *Installer) syncRules(family int, want map[int]netlink.Rule) error {
	existing, err := netlink.RuleList(family)
	if err != nil {
		return fmt.Errorf("list ip rules: %w", err)
	}
	var errs []error
	seen := map[int]bool{}
	for k := range existing {
		rule := existing[k]
		if !ownPriority(rule.Priority) {
			continue
		}
		rule.Family = family
		if desired, ok := want[rule.Priority]; ok && sameRule(rule, desired) {
			seen[rule.Priority] = true
			continue
		}
		if err := netlink.RuleDel(&rule); err != nil {
			errs = append(errs, fmt.Errorf("remove ip rule %d: %w", rule.Priority, err))
		}
	}
	for priority, rule := range want {
		if seen[priority] {
			continue
		}
		if err := netlink.RuleAdd(&rule); err != nil {
			errs = append(errs, fmt.Errorf("add ip rule %d: %w", priority, err))
			continue
		}
		i.Log.Info("policy rule installed", "priority", priority, "mark", fmt.Sprintf("0x%x", rule.Mark), "table", rule.Table)
	}
	return errors.Join(errs...)
}

// routeFor builds the default route for a target in one family: the best
// tier that has an online hop, a multipath route when that tier has
// several, a blackhole for a blocking group with nothing online, and nil
// when the traffic should fall back to the main table.
func (i *Installer) routeFor(t Target, family int) *netlink.Route {
	for _, tier := range t.Tiers {
		var hops []*netlink.NexthopInfo
		for _, h := range tier {
			if !h.Online {
				continue
			}
			ip := net.ParseIP(h.Address)
			if ip == nil || (ip.To4() != nil) != (family == netlink.FAMILY_V4) {
				continue
			}
			link, err := netlink.LinkByName(h.Interface)
			if err != nil {
				continue
			}
			hops = append(hops, &netlink.NexthopInfo{LinkIndex: link.Attrs().Index, Gw: ip})
		}
		if len(hops) == 0 {
			continue
		}
		route := &netlink.Route{Table: t.Table, Dst: defaultDst(family)}
		if len(hops) == 1 {
			route.LinkIndex, route.Gw = hops[0].LinkIndex, hops[0].Gw
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
	mask := uint32(model.PolicyMarkMask)
	suppressPriority, lookupPriority := t.Priorities()

	suppress := netlink.NewRule()
	suppress.Family = family
	suppress.Priority = suppressPriority
	suppress.Mark = t.Mark
	suppress.Mask = &mask
	suppress.Table = unix.RT_TABLE_MAIN
	suppress.SuppressPrefixlen = 0

	lookup := netlink.NewRule()
	lookup.Family = family
	lookup.Priority = lookupPriority
	lookup.Mark = t.Mark
	lookup.Mask = &mask
	lookup.Table = t.Table

	return []netlink.Rule{*suppress, *lookup}
}

func sameRule(a, b netlink.Rule) bool {
	if a.Priority != b.Priority || a.Table != b.Table || a.Mark != b.Mark ||
		a.SuppressPrefixlen != b.SuppressPrefixlen || a.Family != b.Family {
		return false
	}
	switch {
	case a.Mask == nil && b.Mask == nil:
		return true
	case a.Mask == nil || b.Mask == nil:
		return false
	}
	return *a.Mask == *b.Mask
}

func sameRoute(a, b *netlink.Route) bool {
	if a.Table != b.Table || a.Type != b.Type || !sameDst(a.Dst, b.Dst) {
		return false
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
	if family == netlink.FAMILY_V6 {
		return &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	return &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
}

func describe(r *netlink.Route) string {
	if r.Type == unix.RTN_BLACKHOLE {
		return "blackhole"
	}
	if len(r.MultiPath) > 0 {
		out := "multipath"
		for _, h := range r.MultiPath {
			out += " " + h.Gw.String()
		}
		return out
	}
	return "via " + r.Gw.String()
}
