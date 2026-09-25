package model

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/timezone"
	"github.com/rforced/ostiole/internal/wg"
)

// Issue is one validation problem, located by a JSON-ish path.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (i Issue) String() string { return i.Path + ": " + i.Message }

// ValidationError collects every issue found in a Config.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	lines := make([]string, 0, len(e.Issues))
	for _, i := range e.Issues {
		lines = append(lines, i.String())
	}
	return fmt.Sprintf("invalid configuration (%d issue(s)):\n  %s", len(e.Issues), strings.Join(lines, "\n  "))
}

type validator struct {
	issues []Issue
}

func (v *validator) add(path, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Validate checks referential integrity and field syntax. It returns a
// *ValidationError listing every problem, or nil.
func (c *Config) Validate() error {
	v := &validator{}

	if c.Version != SchemaVersion {
		v.add("version", "unsupported schema version %d (want %d)", c.Version, SchemaVersion)
	}
	v.system(&c.System)

	zones := map[string]bool{}
	if len(c.Zones) == 0 {
		v.add("zones", "at least one zone is required")
	}
	for i, z := range c.Zones {
		path := fmt.Sprintf("zones[%d]", i)
		if !nameRe.MatchString(z.Name) {
			v.add(path+".name", "%q must match %s", z.Name, nameRe)
		} else if zones[z.Name] {
			v.add(path+".name", "duplicate zone %q", z.Name)
		}
		zones[z.Name] = true
		v.busyHosts(path, z)
	}

	ifaces := map[string]bool{}
	tailscales := 0
	for i, in := range c.Interfaces {
		path := fmt.Sprintf("interfaces[%d]", i)
		if !ifaceRe.MatchString(in.Name) {
			v.add(path+".name", "%q is not a valid interface name", in.Name)
		} else if ifaces[in.Name] {
			v.add(path+".name", "duplicate interface %q", in.Name)
		}
		ifaces[in.Name] = true
		if in.Zone != "" && !zones[in.Zone] {
			v.add(path+".zone", "unknown zone %q", in.Zone)
		}
		v.addr4(path+".ipv4", in.IPv4)
		v.addr6(path+".ipv6", in.IPv6)
		if in.VLAN != nil {
			if !ifaceRe.MatchString(in.VLAN.Parent) {
				v.add(path+".vlan.parent", "%q is not a valid interface name", in.VLAN.Parent)
			} else if in.VLAN.Parent == in.Name {
				v.add(path+".vlan.parent", "interface cannot be its own VLAN parent")
			}
			if in.VLAN.ID < 1 || in.VLAN.ID > 4094 {
				v.add(path+".vlan.id", "VLAN ID %d must be 1-4094", in.VLAN.ID)
			}
		}
		if in.MTU != 0 && (in.MTU < 68 || in.MTU > 65535) {
			v.add(path+".mtu", "MTU %d must be 68-65535 (or 0 for default)", in.MTU)
		}
		v.macAddress(path, in)
		if in.WireGuard != nil {
			v.wireguard(path+".wireguard", in)
		}
		if in.PPPoE != nil {
			v.pppoe(path, in)
		} else if in.IPv4.Mode == AddrPPP || in.IPv6.Mode == AddrPPP {
			v.add(path+".ipv4.mode", "only a PPPoE interface takes its address from a dialled session")
		}
		if in.Tailscale != nil {
			if tailscales++; tailscales > 1 {
				v.add(path+".tailscale", "this router can join one tailnet")
			}
			v.tailscale(path, in)
		}
		if in.Wireless != nil {
			v.wirelessNetwork(path, in, c)
		}
		if in.BlockPrivate || in.BlockBogons {
			// These drop traffic by source address, so an interface that is
			// itself on such a network would cut itself off.
			if in.BlockPrivate && isPrivatePrefix(in.IPv4.Address) {
				v.add(path+".blockPrivate",
					"%s is itself a private address, so blocking private sources here would cut this network off",
					in.IPv4.Address)
			}
			if z, ok := c.Zone(in.Zone); in.Zone != "" && ok && !z.External {
				v.add(path+".blockPrivate",
					"zone %q is internal; blocking sources by address belongs on an interface facing the internet",
					in.Zone)
			}
		}
		if kinds := builtFrom(in); len(kinds) > 1 {
			v.add(path+".kind", "an interface is one thing at a time, and this one is %s",
				strings.Join(kinds, " and "))
		}
	}
	v.sharedNetworks(c)
	v.shaping(c, v.enslaved(c, ifaces))
	v.delegation(c)
	v.wireless(c, ifaces)

	aliases := map[string]AliasType{}
	for i, a := range c.Aliases {
		path := fmt.Sprintf("aliases[%d]", i)
		if !nameRe.MatchString(a.Name) {
			v.add(path+".name", "%q must match %s", a.Name, nameRe)
		} else if _, dup := aliases[a.Name]; dup {
			v.add(path+".name", "duplicate alias %q", a.Name)
		}
		aliases[a.Name] = a.Type
		switch a.Type {
		case AliasHosts:
			for j, e := range a.Entries {
				if _, err := ParseAddress(e); err != nil {
					v.add(fmt.Sprintf("%s.entries[%d]", path, j), "%v", err)
				}
			}
		case AliasPorts:
			for j, e := range a.Entries {
				if _, err := ParsePortRange(e); err != nil {
					v.add(fmt.Sprintf("%s.entries[%d]", path, j), "%v", err)
				}
			}
		case AliasGeoIP:
			if len(a.Entries) == 0 {
				v.add(path+".entries", "list the countries as two-letter codes, like de or fr")
			}
			for j, e := range a.Entries {
				if !countryRe.MatchString(e) {
					v.add(fmt.Sprintf("%s.entries[%d]", path, j), "%q is not a two-letter country code", e)
				}
			}
			if a.URL != "" {
				v.add(path+".url", "a country alias fetches from the GeoIP source set under System")
			}
		case AliasASN:
			if len(a.Entries) == 0 {
				v.add(path+".entries", "list the AS numbers, like AS15169")
			}
			numbers := map[uint32]bool{}
			for j, e := range a.Entries {
				n, err := ParseASN(e)
				if err != nil {
					v.add(fmt.Sprintf("%s.entries[%d]", path, j), "%v", err)
				} else if numbers[n] {
					v.add(fmt.Sprintf("%s.entries[%d]", path, j), "%s is listed twice", FormatASN(n))
				}
				numbers[n] = true
			}
			if a.URL != "" {
				v.add(path+".url", "an AS alias fetches from the ASN source set under System")
			}
		default:
			v.add(path+".type", "unknown alias type %q", a.Type)
		}
		if a.URL != "" {
			if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				v.add(path+".url", "%q must be an http or https URL", a.URL)
			}
		}
		if a.RefreshHours < 0 || a.RefreshHours > 24*30 {
			v.add(path+".refreshHours", "%d must be 0-720 (0 means once a day)", a.RefreshHours)
		}
		v.aliasSelect(path, a)
	}

	schedules := map[string]bool{}
	for i, sc := range c.Schedules {
		path := fmt.Sprintf("schedules[%d]", i)
		if !nameRe.MatchString(sc.Name) {
			v.add(path+".name", "%q must match %s", sc.Name, nameRe)
		} else if schedules[sc.Name] {
			v.add(path+".name", "duplicate schedule %q", sc.Name)
		}
		schedules[sc.Name] = true
		start, serr := ParseClock(sc.Start)
		if serr != nil {
			v.add(path+".start", "%v", serr)
		}
		end, eerr := ParseClock(sc.End)
		if eerr != nil {
			v.add(path+".end", "%v", eerr)
		}
		if serr == nil && eerr == nil && start == end {
			v.add(path+".end", "start and end are the same, which matches nothing")
		}
		for j, d := range sc.Days {
			if _, ok := Weekday(d); !ok {
				v.add(fmt.Sprintf("%s.days[%d]", path, j), "%q is not a day of the week", d)
			}
		}
	}

	// Rules may name a gateway or a group, so both sets are collected
	// before the rules are walked.
	routeTargets := map[string]bool{}
	for _, g := range c.Gateways {
		routeTargets[g.Name] = true
	}
	for _, g := range c.GatewayGroups {
		routeTargets[g.Name] = true
	}

	ids := map[string]bool{}
	for i, r := range c.Rules {
		path := fmt.Sprintf("rules[%d]", i)
		v.id(path+".id", r.ID, ids)
		if r.Schedule != "" && !schedules[r.Schedule] {
			v.add(path+".schedule", "unknown schedule %q", r.Schedule)
		}
		if r.Gateway != "" {
			switch {
			case !routeTargets[r.Gateway]:
				v.add(path+".gateway", "unknown gateway or gateway group %q", r.Gateway)
			case r.Action != ActionAccept:
				v.add(path+".gateway", "only an accept rule can choose a gateway")
			case r.DestZone != "":
				v.add(path+".gateway", "a rule cannot pick both a gateway and a destination zone: "+
					"the outgoing interface is decided by the gateway")
			}
		}
		if r.Priority != "" {
			switch {
			case !r.Priority.Valid():
				v.add(path+".priority", "unknown priority %q", r.Priority)
			case r.Action != ActionAccept:
				v.add(path+".priority", "only an accept rule can set a priority: "+
					"a dropped connection has no traffic to prioritise")
			}
		}
		if r.Limit != nil {
			if r.Action != ActionAccept {
				v.add(path+".limit", "only an accept rule can hold traffic to a rate: "+
					"there is no sense in rationing a refusal")
			}
			v.rateLimit(path+".limit", *r.Limit)
		}
		if !zones[r.Zone] {
			v.add(path+".zone", "unknown zone %q", r.Zone)
		}
		if r.DestZone != "" && !zones[r.DestZone] {
			v.add(path+".destZone", "unknown zone %q", r.DestZone)
		}
		switch r.Action {
		case ActionAccept, ActionDrop, ActionReject:
		default:
			v.add(path+".action", "unknown action %q", r.Action)
		}
		hasPorts := false
		switch r.Protocol {
		case ProtocolTCP, ProtocolUDP, ProtocolTCPUDP:
			hasPorts = true
		case ProtocolAny, ProtocolICMP:
		default:
			v.add(path+".protocol", "unknown protocol %q", r.Protocol)
		}
		v.endpoint(path+".source", r.Source, aliases, hasPorts, false)
		v.endpoint(path+".destination", r.Destination, aliases, hasPorts, true)
	}

	if !slices.Contains(OutboundModes, c.NAT.Outbound.Mode) {
		v.add("nat.outbound.mode", "unknown mode %q", c.NAT.Outbound.Mode)
	}
	if c.NAT.Outbound.Mode == OutboundAutomatic {
		for i, r := range c.NAT.Outbound.Rules {
			if r.Enabled {
				v.add(fmt.Sprintf("nat.outbound.rules[%d]", i),
					"automatic mode ignores the rules you write; switch to hybrid to have both")
				break
			}
		}
	}
	v.protection(c, zones)

	natIDs := map[string]bool{}
	for i, r := range c.NAT.Outbound.Rules {
		path := fmt.Sprintf("nat.outbound.rules[%d]", i)
		v.id(path+".id", r.ID, natIDs)
		if !zones[r.Zone] {
			v.add(path+".zone", "unknown zone %q", r.Zone)
		}
		for j, s := range r.Source {
			if _, err := ParseAddress(s); err != nil {
				v.add(fmt.Sprintf("%s.source[%d]", path, j), "%v", err)
			}
		}
		for j, s := range r.Destination {
			if _, err := ParseAddress(s); err != nil {
				v.add(fmt.Sprintf("%s.destination[%d]", path, j), "%v", err)
			}
		}
		if r.Address != "" {
			switch {
			case r.NoNAT:
				v.add(path+".address", "a rule that leaves traffic alone translates it to nothing")
			default:
				if _, err := ParseIP(r.Address); err != nil {
					v.add(path+".address", "%v", err)
				}
			}
		}
	}
	pfIDs := map[string]bool{}
	for i, pf := range c.NAT.PortForwards {
		path := fmt.Sprintf("nat.portForwards[%d]", i)
		v.id(path+".id", pf.ID, pfIDs)
		if !zones[pf.Zone] {
			v.add(path+".zone", "unknown zone %q", pf.Zone)
		}
		switch pf.Protocol {
		case ProtocolTCP, ProtocolUDP, ProtocolTCPUDP:
		default:
			v.add(path+".protocol", "port forwards need tcp, udp, or tcp+udp, got %q", pf.Protocol)
		}
		if len(pf.Ports) == 0 {
			v.add(path+".ports", "at least one port is required")
		}
		for j, p := range pf.Ports {
			if _, err := ParsePortRange(p); err != nil {
				v.add(fmt.Sprintf("%s.ports[%d]", path, j), "%v", err)
			}
		}
		if _, err := ParseIP(pf.Target); err != nil {
			v.add(path+".target", "%v", err)
		}
		if pf.TargetPort != "" {
			pr, err := ParsePortRange(pf.TargetPort)
			if err != nil {
				v.add(path+".targetPort", "%v", err)
			} else if pr.Lo != pr.Hi {
				v.add(path+".targetPort", "must be a single port")
			}
		}
		if !pf.Priority.Valid() {
			v.add(path+".priority", "unknown priority %q", pf.Priority)
		}
	}

	oneIDs := map[string]bool{}
	for i, o := range c.NAT.OneToOne {
		path := fmt.Sprintf("nat.oneToOne[%d]", i)
		v.id(path+".id", o.ID, oneIDs)
		if !zones[o.Zone] {
			v.add(path+".zone", "unknown zone %q", o.Zone)
		} else if z, ok := c.Zone(o.Zone); ok && !z.External {
			v.add(path+".zone", "1:1 NAT belongs on an external zone, and %q is internal", o.Zone)
		}
		ext, eerr := ParseIP(o.External)
		if eerr != nil {
			v.add(path+".external", "%v", eerr)
		}
		in, ierr := ParseIP(o.Internal)
		if ierr != nil {
			v.add(path+".internal", "%v", ierr)
		}
		if eerr == nil && ierr == nil && ext.Is4() != in.Is4() {
			v.add(path+".internal", "address family does not match the external address")
		}
	}

	gateways := map[string]bool{}
	for i, g := range c.Gateways {
		path := fmt.Sprintf("gateways[%d]", i)
		if !nameRe.MatchString(g.Name) {
			v.add(path+".name", "%q must match %s", g.Name, nameRe)
		} else if gateways[g.Name] {
			v.add(path+".name", "duplicate gateway %q", g.Name)
		}
		gateways[g.Name] = true
		if !ifaces[g.Interface] {
			v.add(path+".interface", "unknown interface %q", g.Interface)
		}
		var addr netip.Addr
		if g.Address != "" {
			a, err := ParseIP(g.Address)
			if err != nil {
				v.add(path+".address", "%v", err)
			}
			addr = a
		} else if in, ok := c.Interface(g.Interface); ok && !learnsGateway(*in) {
			v.add(path+".address", "this interface gets no gateway from the network; give one here")
		}
		if g.Monitor != "" {
			m, err := ParseIP(g.Monitor)
			if err != nil {
				v.add(path+".monitor", "%v", err)
			} else if addr.IsValid() && m.Is4() != addr.Is4() {
				v.add(path+".monitor", "address family does not match the gateway")
			}
		}
		if g.Priority < 0 || g.Priority > 255 {
			v.add(path+".priority", "%d must be 0-255", g.Priority)
		}
	}

	groups := map[string]bool{}
	for i, g := range c.GatewayGroups {
		path := fmt.Sprintf("gatewayGroups[%d]", i)
		switch {
		case !nameRe.MatchString(g.Name):
			v.add(path+".name", "%q must match %s", g.Name, nameRe)
		case groups[g.Name]:
			v.add(path+".name", "duplicate gateway group %q", g.Name)
		case gateways[g.Name]:
			v.add(path+".name", "%q is already a gateway; a group needs its own name", g.Name)
		}
		groups[g.Name] = true
		switch g.OnDown {
		case "", OnDownFallback, OnDownBlock:
		default:
			v.add(path+".onDown", "unknown mode %q (fallback or block)", g.OnDown)
		}
		if len(g.Members) == 0 {
			v.add(path+".members", "a group needs at least one gateway")
		}
		members := map[string]bool{}
		for j, m := range g.Members {
			mpath := fmt.Sprintf("%s.members[%d]", path, j)
			if !gateways[m.Gateway] {
				v.add(mpath+".gateway", "unknown gateway %q", m.Gateway)
			} else if members[m.Gateway] {
				v.add(mpath+".gateway", "gateway %q is in this group twice", m.Gateway)
			}
			members[m.Gateway] = true
			if m.Tier < 0 || m.Tier > 255 {
				v.add(mpath+".tier", "%d must be 0-255", m.Tier)
			}
		}
	}

	enabled := 0
	for _, g := range c.Gateways {
		if g.Enabled {
			enabled++
		}
	}
	for _, g := range c.GatewayGroups {
		if g.Enabled {
			enabled++
		}
	}
	if limit := MaxPolicyTargets - len(reservedPolicyNumbers); enabled > limit {
		v.add("gateways", "at most %d gateways and gateway groups can be enabled at once, found %d",
			limit, enabled)
	}

	routeIDs := map[string]bool{}
	for i, r := range c.Routes {
		path := fmt.Sprintf("routes[%d]", i)
		v.id(path+".id", r.ID, routeIDs)
		dst, derr := ParseAddress(r.Destination)
		if derr != nil {
			v.add(path+".destination", "%v", derr)
		}
		gw, gerr := ParseIP(r.Gateway)
		if gerr != nil {
			v.add(path+".gateway", "%v", gerr)
		}
		if derr == nil && gerr == nil && dst.Addr().Is4() != gw.Is4() {
			v.add(path+".gateway", "address family does not match destination")
		}
		if r.Interface != "" && !ifaces[r.Interface] {
			v.add(path+".interface", "unknown interface %q", r.Interface)
		}
	}

	v.services(c, ifaces, zones)
	v.blocking(c, aliases)
	v.crons(c)
	v.updates(&c.Updates)
	v.backup(&c.Backup)
	v.notifications(&c.Notifications)
	v.acme(c)
	v.certificates(c, ifaces)

	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
}

var countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

var (
	macRe       = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	leaseTimeRe = regexp.MustCompile(`^([0-9]+[smhdw]|infinite)$`)
	domainRe    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	// blockNameRe is domainRe with underscores, as dnsblock reads a list.
	blockNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?(\.[a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?)*$`)
)

// blocking checks the DNS blocking section: the lists, the exceptions to
// them, and the rules that keep clients on this resolver. The lists are
// checked whether they are on or not, so turning them on later does not
// fail on something that was wrong all along.
func (v *validator) blocking(c *Config, aliases map[string]AliasType) {
	b := &c.Blocking
	if b.Enabled && !c.Services.DNS.Enabled {
		v.add("blocking.enabled", "block lists need the DNS server on: dnsmasq is what refuses the names")
	}
	if b.Enforce.RedirectDNS && !c.Services.DNS.Enabled {
		v.add("blocking.enforce.redirectDns", "redirecting plain DNS needs the DNS server on: sent to a router that is not answering, every client would lose DNS")
	}
	if b.Mode != "" && !slices.Contains(BlockModes, b.Mode) {
		v.add("blocking.mode", "unknown block mode %q", b.Mode)
	}
	names := map[string]bool{}
	for i, l := range b.Lists {
		path := fmt.Sprintf("blocking.lists[%d]", i)
		if !nameRe.MatchString(l.Name) {
			v.add(path+".name", "%q must match %s", l.Name, nameRe)
		} else if names[l.Name] {
			v.add(path+".name", "duplicate list %q", l.Name)
		}
		names[l.Name] = true
		if l.URL != "" {
			if u, err := url.Parse(l.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				v.add(path+".url", "%q must be an http or https URL", l.URL)
			}
		}
		if l.Format != "" && !slices.Contains(ListFormats, l.Format) {
			v.add(path+".format", "unknown list format %q", l.Format)
		}
		if l.RefreshHours < 0 || l.RefreshHours > 24*30 {
			v.add(path+".refreshHours", "%d must be 0-720 (0 means once a day)", l.RefreshHours)
		}
	}

	never := c.NeverBlocked()
	delegated := c.DelegatedDomains()
	for i, d := range b.Deny {
		path := fmt.Sprintf("blocking.deny[%d]", i)
		v.blockName(path, d)
		// Blocking is by subtree, so denying a parent of one of this router's
		// own names takes it away just as surely as denying it outright.
		for _, n := range never {
			if CoversName(d, n) {
				v.add(path, "%q covers %q, a name this router answers for; blocking it would take the UI away from anyone reaching it by name", d, n)
				break
			}
		}
		for _, n := range delegated {
			if CoversName(d, n) {
				v.add(path, "%q covers %q, a domain override; blocking it would undo the delegation", d, n)
				break
			}
		}
	}
	for i, d := range b.Allow {
		v.blockName(fmt.Sprintf("blocking.allow[%d]", i), d)
	}

	for _, ref := range []struct{ field, name string }{
		{"dohAlias", b.Enforce.DoHAlias},
		{"exemptAlias", b.Enforce.ExemptAlias},
	} {
		if ref.name == "" {
			continue
		}
		path := "blocking.enforce." + ref.field
		if t, ok := aliases[ref.name]; !ok {
			v.add(path, "unknown alias %q", ref.name)
		} else if !t.HoldsAddresses() {
			v.add(path, "alias %q holds ports, not addresses", ref.name)
		}
	}
	if b.MaxDomains < 0 || b.MaxDomains > MaxBlockedDomains {
		v.add("blocking.maxDomains", "%d must be 0-%d (0 means the default); dnsmasq holds about 90 MB per million names",
			b.MaxDomains, MaxBlockedDomains)
	}
}

// blockName checks an allow or deny entry. It also takes the underscores of
// service names like _dns.resolver.arpa, which the lists carry too; what it
// refuses, domainName refuses and says why.
func (v *validator) blockName(path, name string) {
	n := strings.Trim(strings.TrimSpace(name), ".")
	if len(n) <= 253 && blockNameRe.MatchString(n) {
		return
	}
	v.domainName(path, name)
}

// domainName checks one name written by hand, such as a domain override. A
// trailing dot is accepted and ignored, because that is how a resolver
// writes a fully qualified name.
func (v *validator) domainName(path, name string) {
	n := strings.Trim(strings.TrimSpace(name), ".")
	switch {
	case n == "":
		v.add(path, "a domain name is needed here")
	case len(n) > 253:
		v.add(path, "%q is longer than a domain name may be", name)
	case !domainRe.MatchString(n):
		v.add(path, "%q is not a domain name", name)
	}
}

// hostLabel checks one label of a host override: its hostname or one of
// its aliases. The domain has a field of its own, so a dot here is worth
// naming rather than refusing as a bad character.
// Nothing trims the label, so a name with a space in it is refused rather
// than quietly becoming a second spelling of an existing one.
func (v *validator) hostLabel(path, name string) {
	switch {
	case name == "":
		v.add(path, "a hostname is needed here")
	case strings.Contains(name, "."):
		v.add(path, "%q has a dot in it; the domain goes in its own field", name)
	case !labelRe.MatchString(name):
		v.add(path, "%q is not a valid hostname", name)
	}
}

// macAddress checks a hardware address override. It has to be a unicast
// address: a card that answers to a multicast address is never sent
// anything.
func (v *validator) macAddress(path string, in Interface) {
	if in.MACAddress == "" {
		return
	}
	if in.Kind() != KindPhysical {
		v.add(path+".macAddress", "only a physical interface has a hardware address to set")
		return
	}
	hw, err := net.ParseMAC(in.MACAddress)
	if err != nil {
		v.add(path+".macAddress", "%q is not a hardware address", in.MACAddress)
		return
	}
	switch {
	case len(hw) != 6:
		v.add(path+".macAddress", "%q must be six bytes", in.MACAddress)
	case hw[0]&1 == 1:
		v.add(path+".macAddress", "%q is a multicast address; the first byte has to be even", in.MACAddress)
	case slices.Max(hw) == 0:
		v.add(path+".macAddress", "all zeroes is not an address")
	}
}

func (v *validator) services(c *Config, ifaces, zones map[string]bool) {
	seen := map[string]bool{}
	for i, sc := range c.Services.DHCP.Servers {
		path := fmt.Sprintf("services.dhcp.servers[%d]", i)
		in, ok := c.Interface(sc.Interface)
		if !ok || !ifaces[sc.Interface] {
			v.add(path+".interface", "unknown interface %q", sc.Interface)
			continue
		}
		if seen[sc.Interface] {
			v.add(path+".interface", "interface %q already has a server", sc.Interface)
		}
		seen[sc.Interface] = true
		if in.IPv4.Mode != AddrStatic {
			v.add(path+".interface", "interface %q needs a static IPv4 address to serve DHCP", sc.Interface)
			continue
		}
		subnet, err := netip.ParsePrefix(in.IPv4.Address)
		if err != nil {
			continue // reported on the interface
		}
		subnet = subnet.Masked()
		start, serr := ParseIP(sc.RangeStart)
		end, eerr := ParseIP(sc.RangeEnd)
		if serr != nil {
			v.add(path+".rangeStart", "%v", serr)
		} else if !start.Is4() || !subnet.Contains(start) {
			v.add(path+".rangeStart", "%s is not inside %s", start, subnet)
		}
		if eerr != nil {
			v.add(path+".rangeEnd", "%v", eerr)
		} else if !end.Is4() || !subnet.Contains(end) {
			v.add(path+".rangeEnd", "%s is not inside %s", end, subnet)
		}
		if serr == nil && eerr == nil && start.Compare(end) > 0 {
			v.add(path+".rangeEnd", "range end is before its start")
		}
		if sc.LeaseTime != "" && !leaseTimeRe.MatchString(sc.LeaseTime) {
			v.add(path+".leaseTime", "%q must look like 24h, 2d, or infinite", sc.LeaseTime)
		}
		if sc.Gateway != "" {
			if gw, err := ParseIP(sc.Gateway); err != nil {
				v.add(path+".gateway", "%v", err)
			} else if !subnet.Contains(gw) {
				v.add(path+".gateway", "%s is not inside %s", gw, subnet)
			}
		}
		for j, d := range sc.DNS {
			if _, err := ParseIP(d); err != nil {
				v.add(fmt.Sprintf("%s.dns[%d]", path, j), "%v", err)
			}
		}
		if sc.Domain != "" && !domainRe.MatchString(sc.Domain) {
			v.add(path+".domain", "%q is not a valid domain", sc.Domain)
		}
	}
	v.dhcpv6(c, ifaces)
	macs := map[string]bool{}
	for i, l := range c.Services.DHCP.StaticLeases {
		path := fmt.Sprintf("services.dhcp.staticLeases[%d]", i)
		mac := strings.ToLower(l.MAC)
		if !macRe.MatchString(l.MAC) {
			v.add(path+".mac", "%q is not a MAC address like aa:bb:cc:dd:ee:ff", l.MAC)
		} else if macs[mac] {
			v.add(path+".mac", "duplicate MAC %s", l.MAC)
		}
		macs[mac] = true
		if l.IP == "" && l.IPv6 == "" {
			v.add(path+".ip", "a static lease needs an IPv4 or an IPv6 address")
		}
		if l.IP != "" {
			if ip, err := ParseIP(l.IP); err != nil {
				v.add(path+".ip", "%v", err)
			} else if !ip.Is4() {
				v.add(path+".ip", "%s is not an IPv4 address; use the ipv6 field", l.IP)
			}
		}
		if l.IPv6 != "" {
			if ip, err := ParseIP(l.IPv6); err != nil {
				v.add(path+".ipv6", "%v", err)
			} else if ip.Is4() {
				v.add(path+".ipv6", "%s is not an IPv6 address", l.IPv6)
			}
		}
		if l.Hostname != "" && !hostnameRe.MatchString(l.Hostname) {
			v.add(path+".hostname", "%q is not a valid hostname", l.Hostname)
		}
	}
	dns := c.Services.DNS
	for i, name := range dns.Interfaces {
		if !ifaces[name] {
			v.add(fmt.Sprintf("services.dns.interfaces[%d]", i), "unknown interface %q", name)
		}
	}
	for i, u := range dns.Upstreams {
		if _, err := ParseIP(u); err != nil {
			v.add(fmt.Sprintf("services.dns.upstreams[%d]", i), "%v", err)
		}
	}
	switch dns.Resolver {
	case "", ResolverForward:
		if dns.Enabled && len(dns.Upstreams) == 0 && len(c.System.DNSServers) == 0 {
			v.add("services.dns.upstreams", "at least one upstream DNS server is required (or set system.dnsServers)")
		}
	case ResolverValidate:
	case ResolverTLS:
		if dns.Enabled && len(dns.TLSUpstreams) == 0 {
			v.add("services.dns.tlsUpstreams", "DNS over TLS needs at least one resolver")
		}
	default:
		v.add("services.dns.resolver", "%q must be forward, validate, or tls", dns.Resolver)
	}
	for i, u := range dns.TLSUpstreams {
		path := fmt.Sprintf("services.dns.tlsUpstreams[%d]", i)
		if _, err := ParseIP(u.Address); err != nil {
			v.add(path+".address", "%v", err)
		}
		if u.Hostname == "" {
			v.add(path+".hostname", "a certificate name is required; without it the connection is not verified")
		} else if !domainRe.MatchString(u.Hostname) {
			v.add(path+".hostname", "%q is not a valid hostname", u.Hostname)
		}
	}
	if dns.Domain != "" && !domainRe.MatchString(dns.Domain) {
		v.add("services.dns.domain", "%q is not a valid domain", dns.Domain)
	}
	if n := dns.CacheSize; n != 0 && (n < 100 || n > MaxCacheSize) {
		v.add("services.dns.cacheSize", "%d must be 100-%d (0 keeps %d)", n, MaxCacheSize, DefaultCacheSize)
	}
	if n := dns.ResolverCacheMB; n < 0 || n > MaxResolverCacheMB {
		v.add("services.dns.resolverCacheMB", "%d must be 0-%d (0 keeps %d)",
			n, MaxResolverCacheMB, DefaultResolverCacheMB)
	}
	if n := dns.QueryLog.Entries; n < 0 || n > MaxQueryLogEntries {
		v.add("services.dns.queryLog.entries", "%d must be 0-%d (0 means %d)",
			n, MaxQueryLogEntries, DefaultQueryLogEntries)
	}
	if n := dns.QueryLog.Hours; n < 0 || n > MaxQueryLogHours {
		v.add("services.dns.queryLog.hours", "%d must be 0-%d (0 means %d)",
			n, MaxQueryLogHours, DefaultQueryLogHours)
	}
	allowed := map[string]bool{}
	for i, d := range dns.Rebind.Allow {
		path := fmt.Sprintf("services.dns.rebind.allow[%d]", i)
		v.domainName(path, d)
		if name := NormalizeDomain(d); name != "" {
			if allowed[name] {
				v.add(path, "duplicate domain %q", d)
			}
			allowed[name] = true
		}
	}
	local := NormalizeDomain(dns.Domain)
	names := map[string]bool{}
	claim := func(path, name string) {
		key := strings.ToLower(name)
		if names[key] {
			v.add(path, "%q is answered twice", name)
		}
		names[key] = true
	}
	leased := leasedNames(c.Services.DHCP.StaticLeases, local)
	for i, h := range dns.HostOverrides {
		path := fmt.Sprintf("services.dns.hostOverrides[%d]", i)
		v.hostLabel(path+".hostname", h.Hostname)
		if h.Domain != "" {
			v.domainName(path+".domain", h.Domain)
		}
		claim(path+".hostname", h.FQDN(local))
		v.sharedWithLease(path+".hostname", HostOverride{Hostname: h.Hostname, Domain: h.Domain, IP: h.IP}, local, leased)
		for j, a := range h.Aliases {
			p := fmt.Sprintf("%s.aliases[%d]", path, j)
			v.hostLabel(p, a)
			claim(p, HostOverride{Hostname: a, Domain: h.Domain}.FQDN(local))
			v.sharedWithLease(p, HostOverride{Hostname: a, Domain: h.Domain, IP: h.IP}, local, leased)
		}
		if _, err := ParseIP(h.IP); err != nil {
			v.add(path+".ip", "%v", err)
		}
	}
	domains := map[string]bool{}
	for i, d := range dns.DomainOverrides {
		path := fmt.Sprintf("services.dns.domainOverrides[%d]", i)
		v.domainName(path+".domain", d.Domain)
		if name := NormalizeDomain(d.Domain); name != "" {
			switch {
			case domains[name]:
				v.add(path+".domain", "duplicate domain %q", d.Domain)
			case name == local:
				v.add(path+".domain", "%q is the local domain, which this router answers itself; override a name under it instead", d.Domain)
			}
			domains[name] = true
		}
		if len(d.Servers) == 0 {
			v.add(path+".servers", "at least one resolver is required; without one the domain has nowhere to go")
		}
		for j, s := range d.Servers {
			if _, err := ParseDNSServer(s); err != nil {
				v.add(fmt.Sprintf("%s.servers[%d]", path, j), "%v", err)
			}
		}
	}
	v.upnp(c, ifaces)
	v.ntp(c, ifaces)
	v.wol(c)
	v.proxy(c, zones)
}

// leasedNames maps every name the static leases answer to the leases that
// answer it. A bare hostname answers under the local domain as well.
func leasedNames(leases []StaticLease, local string) map[string][]StaticLease {
	out := map[string][]StaticLease{}
	for _, l := range leases {
		name := strings.ToLower(l.Hostname)
		if name == "" {
			continue
		}
		out[name] = append(out[name], l)
		if local != "" && !strings.Contains(name, ".") {
			out[name+"."+local] = append(out[name+"."+local], l)
		}
	}
	return out
}

// sharedWithLease refuses an override name that a static lease answers at
// another address of the same family: the name would answer both, and
// the device would lose it to the override.
func (v *validator) sharedWithLease(path string, o HostOverride, local string, leased map[string][]StaticLease) {
	ip, err := ParseIP(o.IP)
	if err != nil {
		return
	}
	for _, name := range o.Names(local) {
		for _, l := range leased[strings.ToLower(name)] {
			if at, ok := leaseAddr(l, ip); ok && at != ip {
				v.add(path, "%q is also the static lease name for %s at %s, so the name would answer both addresses", o.Hostname, l.MAC, at)
				return
			}
		}
	}
}

// leaseAddr is the lease's address in ip's family. An IPv6 host part like
// ::20 takes its prefix from the interface, so it cannot be compared.
func leaseAddr(l StaticLease, ip netip.Addr) (netip.Addr, bool) {
	s := l.IP
	if ip.Is6() {
		s = l.IPv6
	}
	a, err := ParseIP(s)
	if err != nil || a.Is6() != ip.Is6() {
		return netip.Addr{}, false
	}
	if b := a.As16(); a.Is6() && [8]byte(b[:8]) == [8]byte{} {
		return netip.Addr{}, false
	}
	return a, true
}

// upnp checks the mapping service. A name that is written down is checked
// whether or not the service is on, so a typo is not hidden by a switch;
// what the interfaces have to be is only asked of a router that runs it.
func (v *validator) upnp(c *Config, ifaces map[string]bool) {
	u := c.Services.UPnP
	if u.Enabled && !u.IGD && !u.PCP {
		v.add("services.upnp.enabled", "switch on UPnP IGD, PCP and NAT-PMP, or both; with neither nothing answers")
	}
	switch {
	case u.ExternalInterface == "":
		if u.Enabled {
			v.add("services.upnp.externalInterface", "the interface facing the internet is required: it is where a mapped port is opened")
		}
	case !ifaces[u.ExternalInterface]:
		v.add("services.upnp.externalInterface", "unknown interface %q", u.ExternalInterface)
	case u.Enabled:
		in, _ := c.Interface(u.ExternalInterface)
		z, known := c.Zone(in.Zone)
		switch {
		case !in.Enabled:
			v.add("services.upnp.externalInterface", "interface %q is disabled", u.ExternalInterface)
		case !known || !z.External:
			v.add("services.upnp.externalInterface",
				"interface %q is not in an external zone; a port opened anywhere else reaches nothing", u.ExternalInterface)
		}
	}
	for i, name := range u.Interfaces {
		path := fmt.Sprintf("services.upnp.interfaces[%d]", i)
		in, known := c.Interface(name)
		switch {
		case !known || !ifaces[name]:
			v.add(path, "unknown interface %q", name)
		case name == u.ExternalInterface:
			v.add(path, "%q is the external interface; clients ask for mappings from the inside", name)
		case u.Enabled && !in.Enabled:
			v.add(path, "interface %q is disabled", name)
		}
	}
	for i, r := range u.ACL {
		path := fmt.Sprintf("services.upnp.acl[%d]", i)
		if r.Action != "allow" && r.Action != "deny" {
			v.add(path+".action", "%q must be allow or deny", r.Action)
		}
		if _, err := ParsePortRange(r.ExternalPorts); err != nil {
			v.add(path+".externalPorts", "%v", err)
		}
		if _, err := ParsePortRange(r.InternalPorts); err != nil {
			v.add(path+".internalPorts", "%v", err)
		}
		// miniupnpd matches an access list entry against the client's own
		// address, and only ever an IPv4 one: it is what asked for the
		// mapping, and IPv6 has no mapping to ask for.
		if p, err := ParseAddress(r.Source); err != nil {
			v.add(path+".source", "%v", err)
		} else if !p.Addr().Is4() {
			v.add(path+".source", "%q is not IPv4; an access list entry does not apply to an IPv6 client", r.Source)
		}
	}
}

var (
	// wafTargetRe is a Coraza variable, optionally with a member:
	// ARGS, ARGS:password, REQUEST_HEADERS:User-Agent, ARGS:tags[]. The
	// member goes into a rule's action list, where a comma starts another
	// action and a semicolon another target.
	wafTargetRe = regexp.MustCompile(`^[A-Z_]+(:[A-Za-z0-9_.\[\]-]+)?$`)
	// wafRuleRe is a rule ID or a range of them.
	wafRuleRe = regexp.MustCompile(`^([0-9]{1,9})(-([0-9]{1,9}))?$`)
)

// proxy checks the reverse proxy. What is written down is checked whether
// or not it is switched on; the clashes with the router's own ports are
// only asked of a router that runs it, because a web UI still on 443, where
// older installs put it, collides with the defaults, and that is what the
// operator is told to move.
func (v *validator) proxy(c *Config, zones map[string]bool) {
	p := c.Services.Proxy
	ids := map[string]string{}
	for i, name := range p.Zones {
		if !zones[name] {
			v.add(fmt.Sprintf("services.proxy.zones[%d]", i), "unknown zone %q", name)
		}
	}
	if p.Enabled {
		v.proxyPorts(c)
	}
	for i := range p.Pools {
		v.proxyPool(fmt.Sprintf("services.proxy.pools[%d]", i), p.Pools[i], ids)
	}
	for i := range p.Profiles {
		v.wafProfile(fmt.Sprintf("services.proxy.wafProfiles[%d]", i), p.Profiles[i], ids)
	}
	hosts := map[string]string{}
	for i := range p.Sites {
		v.proxySite(fmt.Sprintf("services.proxy.sites[%d]", i), p.Sites[i], c, ids, hosts)
	}
	for i := range p.Routes {
		v.l4Route(fmt.Sprintf("services.proxy.routes[%d]", i), p.Routes[i], c, ids)
	}
	v.l4RouteClashes(c)
	v.routesShadowSites(c)
	switch {
	case !c.ProxyEnabled():
	case len(p.Zones) == 0:
		v.add("services.proxy.zones", "the proxy serves something but is open on no zone: tick the zones its listeners answer on")
	case c.HTTP01Certificates() && !v.proxyOnExternalZone(c):
		v.add("services.proxy.zones", "an http-01 certificate needs the proxy on an external zone: the proxy answers the challenge on port 80")
	}
}

// proxyOnExternalZone reports whether the listeners reach the internet.
func (v *validator) proxyOnExternalZone(c *Config) bool {
	for _, name := range c.Services.Proxy.ProxyZones(c) {
		if z, ok := c.Zone(name); ok && z.External {
			return true
		}
	}
	return false
}

func (v *validator) proxyPorts(c *Config) {
	p := c.Services.Proxy
	m := c.System.Management
	for _, port := range []struct {
		path string
		n    uint16
	}{{"httpPort", p.HTTPPortOr()}, {"httpsPort", p.HTTPSPortOr()}} {
		v.proxyPort("services.proxy."+port.path, port.n, m)
	}
	if p.HTTPPortOr() == p.HTTPSPortOr() {
		v.add("services.proxy.httpsPort", "port %d is already the plain HTTP port", p.HTTPSPortOr())
	}
	for i, r := range p.Routes {
		if r.Enabled {
			v.proxyPort(fmt.Sprintf("services.proxy.routes[%d].port", i), r.Port, m)
		}
	}
}

// proxyPort refuses a listener on a port the router answers on itself.
func (v *validator) proxyPort(path string, port uint16, m Management) {
	switch port {
	case 0:
		v.add(path, "port 0 is not a port")
	case m.WebPort:
		v.add(path, "port %d is the web UI's; change system.management.webPort first", port)
	case m.SSHPort:
		v.add(path, "port %d is SSH's; change system.management.sshPort first", port)
	}
}

func (v *validator) proxyPool(path string, pool ProxyPool, ids map[string]string) {
	v.proxyID(path+".id", pool.ID, "pool", ids)
	v.proxyUpstreams(path, pool.Upstreams)
	if !slices.Contains(ProxyPolicies, pool.Policy) {
		v.add(path+".policy", "%q is not a balancing policy", pool.Policy)
	}
	if pool.HealthPath != "" && !strings.HasPrefix(pool.HealthPath, "/") {
		v.add(path+".healthPath", "%q must start with /", pool.HealthPath)
	}
	if n := pool.HealthSeconds; n != 0 && (n < 5 || n > 3600) {
		v.add(path+".healthSeconds", "%d must be 0 or 5-3600", n)
	}
	if n := pool.FailSeconds; n < 0 || n > 3600 {
		v.add(path+".failSeconds", "%d must be 0-3600", n)
	}
	if !slices.Contains(ProxyProtocols, pool.ProxyProtocol) {
		v.add(path+".proxyProtocol", "%q must be v1, v2 or empty", pool.ProxyProtocol)
	}
	if !pool.TLS {
		switch {
		case pool.TLSServerName != "":
			v.add(path+".tlsServerName", "switch on TLS to the upstreams first")
		case pool.TLSCAPEM != "":
			v.add(path+".tlsCaPem", "switch on TLS to the upstreams first")
		case pool.TLSInsecure:
			v.add(path+".tlsInsecure", "switch on TLS to the upstreams first")
		}
		return
	}
	if pool.TLSServerName != "" && !hostnameRe.MatchString(pool.TLSServerName) {
		v.add(path+".tlsServerName", "%q is not a valid hostname", pool.TLSServerName)
	}
	if pool.TLSCAPEM != "" {
		if err := checkCertificates(pool.TLSCAPEM); err != nil {
			v.add(path+".tlsCaPem", "%v", err)
		}
	}
}

func (v *validator) proxyUpstreams(path string, ups []ProxyUpstream) {
	if len(ups) == 0 {
		v.add(path+".upstreams", "at least one upstream is required")
	}
	for i, u := range ups {
		upath := fmt.Sprintf("%s.upstreams[%d].address", path, i)
		host, port, err := net.SplitHostPort(u.Address)
		if err != nil {
			v.add(upath, "%q must be host:port", u.Address)
			continue
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			v.add(upath, "port %q must be 1-65535", port)
		}
		if _, err := netip.ParseAddr(host); err != nil && !hostnameRe.MatchString(host) {
			v.add(upath, "%q is neither an address nor a hostname", host)
		}
	}
}

func (v *validator) proxySite(path string, s ProxySite, c *Config, ids map[string]string, hosts map[string]string) {
	p := &c.Services.Proxy
	v.proxyID(path+".id", s.ID, "site", ids)
	if len(s.Hosts) == 0 {
		v.add(path+".hosts", "at least one hostname is required")
	}
	for i, h := range s.Hosts {
		hpath := fmt.Sprintf("%s.hosts[%d]", path, i)
		if !validProxyHost(h) {
			v.add(hpath, "%q is not a hostname or a wildcard like *.example.com", h)
			continue
		}
		key := strings.ToLower(h)
		if owner, taken := hosts[key]; taken {
			v.add(hpath, "%q is already served by site %q", h, owner)
			continue
		}
		hosts[key] = s.ID
	}
	if _, ok := p.Pool(s.Pool); !ok {
		v.add(path+".pool", "unknown pool %q", s.Pool)
	}
	prefixes := map[string]bool{}
	for i, pa := range s.Paths {
		ppath := fmt.Sprintf("%s.paths[%d]", path, i)
		switch {
		case !strings.HasPrefix(pa.Prefix, "/"):
			v.add(ppath+".prefix", "%q must start with /", pa.Prefix)
		case prefixes[pa.Prefix]:
			v.add(ppath+".prefix", "duplicate prefix %q", pa.Prefix)
		}
		prefixes[pa.Prefix] = true
		if _, ok := p.Pool(pa.Pool); !ok {
			v.add(ppath+".pool", "unknown pool %q", pa.Pool)
		}
	}
	if s.Certificate != "" {
		cert, ok := c.Certificate(s.Certificate)
		switch {
		case !ok:
			v.add(path+".certificate", "unknown certificate %q", s.Certificate)
		case !cert.Enabled:
			v.add(path+".certificate", "certificate %q is disabled", s.Certificate)
		}
	}
	if s.HostHeader != "" && s.HostHeader != "upstream" {
		v.add(path+".hostHeader", "%q must be upstream or empty", s.HostHeader)
	}
	v.proxyAllowFrom(path, s.AllowFrom)
	if s.WAF != "" {
		if _, ok := p.Profile(s.WAF); !ok {
			v.add(path+".waf", "unknown WAF profile %q", s.WAF)
		}
	}
}

func (v *validator) proxyAllowFrom(path string, from []string) {
	for i, a := range from {
		apath := fmt.Sprintf("%s.allowFrom[%d]", path, i)
		if _, err := ParseAddress(a); err != nil {
			v.add(apath, "%v", err)
			continue
		}
		// ParseAddress masks a prefix for us, so the host bits have to be
		// caught on the text the operator wrote.
		if pre, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil && pre.Masked() != pre {
			v.add(apath, "%q has host bits set; write %s", a, pre.Masked())
		}
	}
}

func (v *validator) wafProfile(path string, w WAFProfile, ids map[string]string) {
	v.proxyID(path+".id", w.ID, "profile", ids)
	if !slices.Contains(WAFModes, w.Mode) {
		v.add(path+".mode", "%q must be detect or block", w.Mode)
	}
	if w.Paranoia < 0 || w.Paranoia > 4 {
		v.add(path+".paranoia", "%d must be 1-4", w.Paranoia)
	}
	for _, t := range []struct {
		path string
		n    int
	}{{"inboundThreshold", w.InboundThreshold}, {"outboundThreshold", w.OutboundThreshold}} {
		if t.n < 0 || t.n > 1000 {
			v.add(path+"."+t.path, "%d must be 0-1000", t.n)
		}
	}
	seen := map[string]bool{}
	for i, app := range w.Applications {
		apath := fmt.Sprintf("%s.applications[%d]", path, i)
		switch {
		case !slices.Contains(WAFApplications, app):
			v.add(apath, "unknown application %q", app)
		case seen[app]:
			v.add(apath, "duplicate application %q", app)
		}
		seen[app] = true
	}
	if w.BodyLimitMB < 0 || w.BodyLimitMB > 1024 {
		v.add(path+".bodyLimitMB", "%d must be 0-1024", w.BodyLimitMB)
	}
	for i, e := range w.Exclusions {
		v.wafExclusion(fmt.Sprintf("%s.exclusions[%d]", path, i), e)
	}
}

func (v *validator) wafExclusion(path string, e WAFExclusion) {
	if m := wafRuleRe.FindStringSubmatch(e.Rule); m == nil {
		v.add(path+".rule", "%q must be a rule ID or a range like 942100-942199", e.Rule)
	} else {
		first, _ := strconv.Atoi(m[1])
		if first < 1 {
			v.add(path+".rule", "rule IDs start at 1")
		}
		if m[3] != "" {
			if last, _ := strconv.Atoi(m[3]); last <= first {
				v.add(path+".rule", "%q ends before it starts", e.Rule)
			}
		}
	}
	switch {
	case e.Path == "":
	case !strings.HasPrefix(e.Path, "/"):
		v.add(path+".path", "%q must start with /", e.Path)
	case strings.ContainsAny(e.Path, `"\`):
		// The path is quoted into a rule, and the rule language has no
		// escape for a quote inside one; a backslash escapes the closing one.
		v.add(path+".path", "a path cannot hold a quote or a backslash")
	case strings.ContainsFunc(e.Path, unicode.IsControl):
		// A line break ends the rule halfway and the proxy refuses the file.
		v.add(path+".path", "a path cannot hold a control character")
	}
	if e.Target != "" && !wafTargetRe.MatchString(e.Target) {
		v.add(path+".target", "%q must be a variable like ARGS or ARGS:password", e.Target)
	}
}

func (v *validator) l4Route(path string, r L4Route, c *Config, ids map[string]string) {
	p := c.Services.Proxy
	v.proxyID(path+".id", r.ID, "route", ids)
	if r.Protocol != "tcp" && r.Protocol != "udp" {
		v.add(path+".protocol", "%q must be tcp or udp", r.Protocol)
	}
	switch {
	case r.Port == 0:
		v.add(path+".port", "port 0 is not a port")
	case r.Port == p.HTTPPortOr():
		v.add(path+".port", "port %d is the proxy's plain HTTP port", r.Port)
	case r.Port == p.HTTPSPortOr() && r.Protocol == "tcp" && len(r.SNI) == 0:
		v.add(path+".sni", "a route on the HTTPS port takes the connections that ask for a name, so it needs one")
	case r.Port == p.HTTPSPortOr() && r.Protocol == "udp" && p.HTTP3:
		v.add(path+".port", "port %d carries HTTP/3; switch that off or move the route", r.Port)
	}
	if r.Protocol == "udp" {
		if len(r.SNI) > 0 {
			v.add(path+".sni", "a server name is read from a TLS handshake, which UDP does not carry")
		}
		if r.HealthSeconds != 0 {
			v.add(path+".healthSeconds", "a health check connects, which UDP does not do")
		}
	}
	for i, name := range r.SNI {
		if !validProxyHost(name) {
			v.add(fmt.Sprintf("%s.sni[%d]", path, i), "%q is not a hostname or a wildcard like *.example.com", name)
		}
	}
	v.proxyUpstreams(path, r.Upstreams)
	if !slices.Contains(L4Policies, r.Policy) {
		v.add(path+".policy", "%q is not a balancing policy", r.Policy)
	}
	if n := r.HealthSeconds; n != 0 && (n < 5 || n > 3600) {
		v.add(path+".healthSeconds", "%d must be 0 or 5-3600", n)
	}
	if !slices.Contains(ProxyProtocols, r.ProxyProtocol) {
		v.add(path+".proxyProtocol", "%q must be v1, v2 or empty", r.ProxyProtocol)
	}
	v.proxyAllowFrom(path, r.AllowFrom)
}

// l4RouteClashes checks the routes against each other: one listener per
// port and protocol, shared only by routes that each pick a name.
func (v *validator) l4RouteClashes(c *Config) {
	type key struct {
		proto string
		port  uint16
	}
	seen := map[key][]int{}
	routes := c.Services.Proxy.Routes
	for i, r := range routes {
		k := key{r.Protocol, r.Port}
		for _, j := range seen[k] {
			path := fmt.Sprintf("services.proxy.routes[%d]", i)
			other := routes[j]
			switch {
			case len(r.SNI) == 0 || len(other.SNI) == 0:
				v.add(path+".port", "route %q already has %s port %d; two routes on one port each need a server name",
					other.ID, r.Protocol, r.Port)
			default:
				for _, name := range r.SNI {
					if slices.ContainsFunc(other.SNI, func(o string) bool { return strings.EqualFold(o, name) }) {
						v.add(path+".sni", "route %q already takes %q on %s port %d", other.ID, name, r.Protocol, r.Port)
					}
				}
			}
		}
		seen[k] = append(seen[k], i)
	}
}

// routesShadowSites refuses a named route on the HTTPS port for a name an
// enabled site serves: the route takes the handshake before the site, its
// certificate and its rules ever see it.
func (v *validator) routesShadowSites(c *Config) {
	p := c.Services.Proxy
	for i, r := range p.Routes {
		if !r.Enabled || r.Protocol != "tcp" || r.Port != p.HTTPSPortOr() {
			continue
		}
		for j, name := range r.SNI {
			if site, ok := siteServing(p, name); ok {
				v.add(fmt.Sprintf("services.proxy.routes[%d].sni[%d]", i, j),
					"%q is served by site %q, which the route would take the connections from", name, site)
			}
		}
	}
}

// siteServing names the enabled site with a host that name can match.
func siteServing(p Proxy, name string) (string, bool) {
	for _, s := range p.Sites {
		if !s.Enabled {
			continue
		}
		for _, host := range s.Hosts {
			if hostsOverlap(name, host) {
				return s.ID, true
			}
		}
	}
	return "", false
}

// hostsOverlap reports whether two names, either a wildcard, can match one
// server name. A wildcard stands for exactly one label, as it does in a
// certificate.
func hostsOverlap(a, b string) bool { return hostCovers(a, b) || hostCovers(b, a) }

func hostCovers(pattern, name string) bool {
	pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	if pattern == name {
		return true
	}
	rest, ok := strings.CutPrefix(pattern, "*.")
	if !ok {
		return false
	}
	first, tail, found := strings.Cut(name, ".")
	return found && first != "" && first != "*" && tail == rest
}

// validProxyHost accepts a hostname or a single leading wildcard label.
func validProxyHost(h string) bool {
	if rest, ok := strings.CutPrefix(h, "*."); ok {
		return hostnameRe.MatchString(rest) && strings.Contains(rest, ".")
	}
	return hostnameRe.MatchString(h)
}

// dhcpv6 checks the router advertisement servers. The prefix itself is not
// configured here: dnsmasq takes it from the interface at run time, so an
// interface only needs IPv6 to be switched on.
func (v *validator) dhcpv6(c *Config, ifaces map[string]bool) {
	seen := map[string]bool{}
	for i, sc := range c.Services.DHCP.V6 {
		path := fmt.Sprintf("services.dhcp.v6[%d]", i)
		in, ok := c.Interface(sc.Interface)
		if !ok || !ifaces[sc.Interface] {
			v.add(path+".interface", "unknown interface %q", sc.Interface)
			continue
		}
		if seen[sc.Interface] {
			v.add(path+".interface", "interface %q already advertises IPv6", sc.Interface)
		}
		seen[sc.Interface] = true
		if in.IPv6.Mode == AddrNone {
			v.add(path+".interface", "interface %q has IPv6 switched off", sc.Interface)
		}
		switch sc.Mode {
		case RASLAAC, RAStateless:
			if sc.RangeStart != "" || sc.RangeEnd != "" {
				v.add(path+".mode", "%q hands out no addresses, so it takes no range", sc.Mode)
			}
		case RAManaged:
			start, serr := ParseIP(sc.RangeStart)
			end, eerr := ParseIP(sc.RangeEnd)
			if serr != nil {
				v.add(path+".rangeStart", "%v", serr)
			} else if start.Is4() {
				v.add(path+".rangeStart", "%s is not an IPv6 address", sc.RangeStart)
			}
			if eerr != nil {
				v.add(path+".rangeEnd", "%v", eerr)
			} else if end.Is4() {
				v.add(path+".rangeEnd", "%s is not an IPv6 address", sc.RangeEnd)
			}
			if serr == nil && eerr == nil && start.Compare(end) > 0 {
				v.add(path+".rangeEnd", "range end is before its start")
			}
		default:
			v.add(path+".mode", "%q must be slaac, stateless, or managed", sc.Mode)
		}
		if sc.LeaseTime != "" && !leaseTimeRe.MatchString(sc.LeaseTime) {
			v.add(path+".leaseTime", "%q must look like 24h, 2d, or infinite", sc.LeaseTime)
		}
		for j, d := range sc.DNS {
			if ip, err := ParseIP(d); err != nil {
				v.add(fmt.Sprintf("%s.dns[%d]", path, j), "%v", err)
			} else if ip.Is4() {
				v.add(fmt.Sprintf("%s.dns[%d]", path, j), "%s is not an IPv6 address", d)
			}
		}
		if sc.Domain != "" && !domainRe.MatchString(sc.Domain) {
			v.add(path+".domain", "%q is not a valid domain", sc.Domain)
		}
	}
}

// wireguard checks a tunnel's keys, peers, and addressing.
// isPrivatePrefix reports whether a configured address sits in one of the
// ranges the private block would drop.
func isPrivatePrefix(addr string) bool {
	p, err := netip.ParsePrefix(addr)
	if err != nil {
		return false
	}
	for _, s := range PrivateSources {
		block, err := netip.ParsePrefix(s)
		if err != nil {
			continue
		}
		if block.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// sharedNetworks refuses two interfaces on one network. Clients on one of
// them are handed the other's address as their gateway; the shape people
// want is a bridge.
func (v *validator) sharedNetworks(c *Config) {
	type claim struct {
		prefix netip.Prefix
		name   string
	}
	var claims []claim
	for i, in := range c.Interfaces {
		if !in.Enabled {
			continue
		}
		for _, f := range []struct {
			mode  AddrMode
			addr  string
			field string
		}{{in.IPv4.Mode, in.IPv4.Address, "ipv4"}, {in.IPv6.Mode, in.IPv6.Address, "ipv6"}} {
			if f.mode != AddrStatic {
				continue
			}
			p, err := netip.ParsePrefix(f.addr)
			if err != nil {
				continue
			}
			for _, other := range claims {
				if other.prefix.Overlaps(p) {
					v.add(fmt.Sprintf("interfaces[%d].%s.address", i, f.field),
						"%s is on the same network as %q; give each interface a network of its own, or bridge them",
						f.addr, other.name)
				}
			}
			claims = append(claims, claim{p, in.Name})
		}
	}
}

// builtFrom names every kind an interface claims to be. More than one is
// a contradiction.
func builtFrom(in Interface) []string {
	var kinds []string
	if in.VLAN != nil {
		kinds = append(kinds, "a VLAN")
	}
	if in.Bridge != nil {
		kinds = append(kinds, "a bridge")
	}
	if in.Bond != nil {
		kinds = append(kinds, "a bond")
	}
	if in.WireGuard != nil {
		kinds = append(kinds, "a WireGuard tunnel")
	}
	if in.Tailscale != nil {
		kinds = append(kinds, "a Tailscale node")
	}
	if in.Wireless != nil {
		kinds = append(kinds, "a wireless network")
	}
	return kinds
}

// enslaved checks bridge and bond membership across the whole
// configuration: a link belongs to one master, and a member carries no
// addressing of its own because the master holds it for the segment. It
// returns the member-to-master map, which is also what says where the
// traffic of a port really flows.
func (v *validator) enslaved(c *Config, ifaces map[string]bool) map[string]string {
	masters := map[string]string{} // member -> master
	kinds := map[string]Kind{}
	for _, in := range c.Interfaces {
		kinds[in.Name] = in.Kind()
	}

	for i, in := range c.Interfaces {
		path := fmt.Sprintf("interfaces[%d]", i)
		members := in.Members()
		if in.Bridge == nil && in.Bond == nil {
			continue
		}
		field := path + ".bridge"
		if in.Bond != nil {
			field = path + ".bond"
			v.bond(field, *in.Bond)
		}
		if len(members) == 0 {
			v.add(field+".members", "a %s needs at least one interface", in.Kind())
		}
		seen := map[string]bool{}
		for j, m := range members {
			mpath := fmt.Sprintf("%s.members[%d]", field, j)
			switch {
			case !ifaceRe.MatchString(m):
				v.add(mpath, "%q is not a valid interface name", m)
				continue
			case m == in.Name:
				v.add(mpath, "an interface cannot contain itself")
				continue
			case seen[m]:
				v.add(mpath, "%q is listed twice", m)
				continue
			}
			seen[m] = true
			if other, taken := masters[m]; taken {
				v.add(mpath, "%q is already part of %q", m, other)
				continue
			}
			masters[m] = in.Name

			switch kinds[m] {
			case KindBridge:
				v.add(mpath, "a bridge cannot be put inside another interface; bridge its members instead")
			case KindBond:
				if in.Bond != nil {
					v.add(mpath, "a bond cannot contain another bond")
				}
			case KindWireGuard:
				v.add(mpath, "a WireGuard tunnel carries routed traffic and cannot be a member")
			case KindTailscale:
				v.add(mpath, "a Tailscale node carries routed traffic and cannot be a member")
			}
		}
	}

	// The Ethernet link under a dialled session is a port too: the session
	// holds the address, not the wire.
	for i, in := range c.Interfaces {
		if in.PPPoE == nil || in.PPPoE.Parent == "" {
			continue
		}
		if other, taken := masters[in.PPPoE.Parent]; taken {
			v.add(fmt.Sprintf("interfaces[%d].pppoe.parent", i),
				"%q is already part of %q", in.PPPoE.Parent, other)
			continue
		}
		masters[in.PPPoE.Parent] = in.Name
	}

	// Anything enslaved must be free of a zone and of addresses: it is a
	// port on its master, not an interface in its own right.
	for i, in := range c.Interfaces {
		master, ok := masters[in.Name]
		if !ok {
			continue
		}
		path := fmt.Sprintf("interfaces[%d]", i)
		if in.Zone != "" {
			v.add(path+".zone", "%q is part of %q, so rules belong on %q instead", in.Name, master, master)
		}
		if in.IPv4.Mode != AddrNone && in.IPv4.Mode != "" {
			v.add(path+".ipv4.mode", "%q is part of %q, which carries the addresses", in.Name, master)
		}
		if in.IPv6.Mode != AddrNone && in.IPv6.Mode != "" {
			v.add(path+".ipv6.mode", "%q is part of %q, which carries the addresses", in.Name, master)
		}
	}
	_ = ifaces
	return masters
}

// busyHosts checks a zone's connection tally. The count is kept per
// source address, so the zone has to be one whose hosts are a known,
// bounded set: on a zone facing the internet the sources are the whole
// of it, and a tally of that says nothing about anybody.
func (v *validator) busyHosts(path string, z Zone) {
	b := z.Busy
	if b == nil {
		return
	}
	if b.Connections < MinBusyConnections || b.Connections > MaxBusyConnections {
		v.add(path+".busy.connections", "%d must be between %d and %d",
			b.Connections, MinBusyConnections, MaxBusyConnections)
	}
	switch {
	case b.Priority == "":
		v.add(path+".busy.priority", "say which priority the connections over the limit go in")
	case !b.Priority.Valid():
		v.add(path+".busy.priority", "unknown priority %q", b.Priority)
	}
	if z.External {
		v.add(path+".busy", "zone %q faces the internet, where the hosts are everybody; "+
			"counting connections belongs on a zone whose hosts are yours", z.Name)
	}
}

// shaping checks the speeds interfaces are given. A wrong figure here
// does not break a rule, it makes the whole line feel broken, so the
// checks are about pointing the shaper at something that can carry a
// queue at all.
func (v *validator) shaping(c *Config, masters map[string]string) {
	devices := map[string]string{} // ifb name -> interface that claimed it
	for i, in := range c.Interfaces {
		s := in.Shaping
		if s == nil {
			continue
		}
		path := fmt.Sprintf("interfaces[%d].shaping", i)
		for _, dir := range []struct {
			field string
			rate  int64
		}{{"download", s.Download}, {"upload", s.Upload}} {
			if dir.rate != 0 && (dir.rate < MinRate || dir.rate > MaxRate) {
				v.add(path+"."+dir.field, "%d bit/s must be between %d and %d", dir.rate, MinRate, MaxRate)
			}
		}
		if !s.Active() {
			v.add(path, "give a download or an upload speed, or remove shaping")
		}
		if !slices.Contains(LinkTypes, s.LinkType()) {
			v.add(path+".link", "unknown link type %q", s.Link)
		}
		if in.Name == "lo" {
			v.add(path, "loopback traffic never leaves this router, so there is nothing to queue")
		}
		if master, ok := masters[in.Name]; ok {
			v.add(path, "%q is part of %q, which carries the traffic", in.Name, master)
		}
		if other, taken := devices[IFBName(in.Name)]; taken {
			v.add(path, "%q and %q would need the same helper device; rename one of them", in.Name, other)
			continue
		}
		devices[IFBName(in.Name)] = in.Name
	}
}

// pppoe checks a dialled session. The address always comes from the other
// end, so the only real questions are which link it runs over and who to
// log in as.
func (v *validator) pppoe(path string, in Interface) {
	p := in.PPPoE
	switch {
	case !ifaceRe.MatchString(p.Parent):
		v.add(path+".pppoe.parent", "%q is not a valid interface name", p.Parent)
	case p.Parent == in.Name:
		v.add(path+".pppoe.parent", "a session cannot run over itself")
	}
	if p.Username == "" {
		v.add(path+".pppoe.username", "the provider's username is required")
	}
	if p.Password == "" {
		v.add(path+".pppoe.password", "the provider's password is required")
	}
	if p.LCPInterval < 0 || p.LCPInterval > 3600 {
		v.add(path+".pppoe.lcpInterval", "%d seconds must be 0-3600", p.LCPInterval)
	}
	if p.LCPFailures < 0 || p.LCPFailures > 100 {
		v.add(path+".pppoe.lcpFailures", "%d must be 0-100", p.LCPFailures)
	}
	if in.IPv4.Mode != AddrPPP && in.IPv4.Mode != AddrNone {
		v.add(path+".ipv4.mode", "a PPPoE interface takes its IPv4 address from the session, so the mode is ppp")
	}
	switch in.IPv6.Mode {
	case AddrNone, AddrPPP:
	default:
		v.add(path+".ipv6.mode", "a PPPoE interface takes its IPv6 address from the session, so the mode is ppp or none")
	}
	if in.IPv6.Mode == AddrPPP && !p.IPv6 {
		v.add(path+".pppoe.ipv6", "turn on IPv6 for this session, or set the interface's IPv6 mode to none")
	}
}

// learnsGateway reports whether an interface is told where to send
// traffic by the other end: DHCP, a router advertisement, or a dialled
// session all do that, a static address does not.
func learnsGateway(in Interface) bool {
	for _, m := range []AddrMode{in.IPv4.Mode, in.IPv6.Mode} {
		switch m {
		case AddrDHCP, AddrSLAAC, AddrPPP:
			return true
		}
	}
	return false
}

// delegation checks that everything taking a delegated prefix has an
// upstream asking for one, and that two interfaces do not claim the same
// piece of it.
func (v *validator) delegation(c *Config) {
	claimed := map[string]map[int]string{} // upstream -> subnet -> interface
	for i, in := range c.Interfaces {
		if in.IPv6.Mode != AddrDelegated {
			continue
		}
		path := fmt.Sprintf("interfaces[%d].ipv6", i)
		from := in.IPv6.DelegatedFrom
		up, ok := c.Interface(from)
		switch {
		case from == "":
			v.add(path+".delegatedFrom", "name the interface whose delegated prefix this one uses")
			continue
		case from == in.Name:
			v.add(path+".delegatedFrom", "an interface cannot take a prefix from itself")
			continue
		case !ok:
			v.add(path+".delegatedFrom", "unknown interface %q", from)
			continue
		case up.IPv6.PrefixHint == "":
			v.add(path+".delegatedFrom", "%q is not asking the upstream for a prefix; give it an IPv6 prefix hint first", from)
			continue
		}
		// A /56 leaves eight bits of subnet, so subnet 300 would fall
		// outside anything the upstream handed over.
		if p, err := netip.ParsePrefix(up.IPv6.PrefixHint); err == nil && p.Bits() <= 64 {
			if room := 64 - p.Bits(); room < 16 && in.IPv6.SubnetID >= 1<<room {
				v.add(path+".subnetId", "%d does not fit in a %s prefix, which has %d subnets",
					in.IPv6.SubnetID, up.IPv6.PrefixHint, 1<<room)
			}
		}
		if claimed[from] == nil {
			claimed[from] = map[int]string{}
		}
		if other, dup := claimed[from][in.IPv6.SubnetID]; dup {
			v.add(path+".subnetId", "%q already takes subnet %d of the prefix from %q",
				other, in.IPv6.SubnetID, from)
			continue
		}
		claimed[from][in.IPv6.SubnetID] = in.Name
	}
}

func (v *validator) bond(path string, b Bond) {
	known := false
	for _, m := range BondModes {
		if b.Mode == m {
			known = true
			break
		}
	}
	if !known {
		v.add(path+".mode", "unknown bond mode %q", b.Mode)
	}
	if b.MIIMonitorMS < 0 || b.MIIMonitorMS > 10000 {
		v.add(path+".miiMonitorMs", "%d must be 0-10000", b.MIIMonitorMS)
	}
	if b.TransmitHashPolicy != "" && !slices.Contains(HashPolicies, b.TransmitHashPolicy) {
		v.add(path+".transmitHashPolicy", "unknown policy %q", b.TransmitHashPolicy)
	}
	switch b.LACPRate {
	case "", "slow", "fast":
	default:
		v.add(path+".lacpRate", "%q must be slow or fast", b.LACPRate)
	}
	if b.Primary != "" && !slices.Contains(b.Members, b.Primary) {
		v.add(path+".primary", "%q is not one of this bond's interfaces", b.Primary)
	}
}

func (v *validator) wireguard(path string, in Interface) {
	w := in.WireGuard
	if !wg.ValidKey(w.PrivateKey) {
		v.add(path+".privateKey", "not a WireGuard key: want 32 bytes, base64 encoded")
	} else if w.PublicKey != "" {
		if derived, err := wg.PublicKey(w.PrivateKey); err == nil && derived != w.PublicKey {
			v.add(path+".publicKey", "does not belong to this private key")
		}
	}
	if in.IPv4.Mode == AddrDHCP {
		v.add(path+".privateKey", "a tunnel has no DHCP server; give it a static address")
	}
	if in.IPv6.Mode == AddrDHCP || in.IPv6.Mode == AddrSLAAC {
		v.add(path+".privateKey", "a tunnel has no router advertisements; give it a static address")
	}
	names := map[string]bool{}
	keys := map[string]bool{}
	for i, p := range w.Peers {
		ppath := fmt.Sprintf("%s.peers[%d]", path, i)
		if !nameRe.MatchString(p.Name) {
			v.add(ppath+".name", "%q must match %s", p.Name, nameRe)
		} else if names[p.Name] {
			v.add(ppath+".name", "duplicate peer %q", p.Name)
		}
		names[p.Name] = true
		if !wg.ValidKey(p.PublicKey) {
			v.add(ppath+".publicKey", "not a WireGuard key: want 32 bytes, base64 encoded")
		} else if keys[p.PublicKey] {
			v.add(ppath+".publicKey", "duplicate peer key")
		}
		keys[p.PublicKey] = true
		if p.PresharedKey != "" && !wg.ValidKey(p.PresharedKey) {
			v.add(ppath+".presharedKey", "not a WireGuard key: want 32 bytes, base64 encoded")
		}
		if len(p.AllowedIPs) == 0 {
			v.add(ppath+".allowedIps", "at least one address or network is required")
		}
		for j, a := range p.AllowedIPs {
			if _, err := ParseAddress(a); err != nil {
				v.add(fmt.Sprintf("%s.allowedIps[%d]", ppath, j), "%v", err)
			}
		}
		if p.Endpoint != "" {
			host, port, found := strings.Cut(p.Endpoint, ":")
			if n, err := strconv.Atoi(port); !found || host == "" || err != nil || n < 1 || n > 65535 {
				v.add(ppath+".endpoint", "%q must be host:port", p.Endpoint)
			}
		}
		if p.Keepalive < 0 || p.Keepalive > 65535 {
			v.add(ppath+".keepalive", "%d must be 0-65535 seconds", p.Keepalive)
		}
	}
}

// tailscaleCGNAT and tailscaleULA are the ranges the tailnet itself uses;
// advertising one of them from here would fight the daemon.
var (
	tailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleULA   = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
	// tsNameRe is a single DNS label: what a tailnet takes as a node name.
	tsNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// tailscale checks a Tailscale interface. The daemon creates the device and
// addresses it, so everything this router would normally decide is refused.
func (v *validator) tailscale(path string, in Interface) {
	t := in.Tailscale
	if in.Name != TailscaleDevice {
		v.add(path+".name", "a Tailscale interface must be called %s", TailscaleDevice)
	}
	if in.IPv4.Mode != AddrNone {
		v.add(path+".ipv4.mode", "the tailnet addresses this interface; set it to none")
	}
	if in.IPv6.Mode != AddrNone {
		v.add(path+".ipv6.mode", "the tailnet addresses this interface; set it to none")
	}
	if in.MTU != 0 {
		v.add(path+".mtu", "the MTU comes from the tailnet")
	}
	if t.Port != 0 && t.Port < 1024 {
		v.add(path+".tailscale.port", "%d must be 0 or 1024-65535", t.Port)
	}
	if t.Hostname != "" && !tsNameRe.MatchString(t.Hostname) {
		v.add(path+".tailscale.hostname", "%q must match %s", t.Hostname, tsNameRe)
	}
	if t.LoginServer != "" {
		if u, err := url.Parse(t.LoginServer); err != nil || u.Scheme != "https" || u.Host == "" {
			v.add(path+".tailscale.loginServer", "%q must be an https URL", t.LoginServer)
		}
	}
	for i, r := range t.AdvertiseRoutes {
		rpath := fmt.Sprintf("%s.tailscale.advertiseRoutes[%d]", path, i)
		p, err := netip.ParsePrefix(r)
		switch {
		case err != nil:
			v.add(rpath, "%q is not a network", r)
			continue
		case p.Masked() != p:
			v.add(rpath, "%q has host bits set; write %s", r, p.Masked())
			continue
		}
		if (p.Addr().Is4() && tailscaleCGNAT.Overlaps(p)) || (p.Addr().Is6() && tailscaleULA.Overlaps(p)) {
			v.add(rpath, "%s is the tailnet's own range", r)
		}
	}
}

// regCountryRe is a regulatory domain: an ISO 3166-1 alpha-2 code, which
// the kernel and hostapd both take in upper case.
var regCountryRe = regexp.MustCompile(`^[A-Z]{2}$`)

// wireless checks the radios: one country for the router, and a band,
// channel and width the card can be asked for.
func (v *validator) wireless(c *Config, ifaces map[string]bool) {
	w := c.Wireless
	switch {
	case w.Country != "" && !regCountryRe.MatchString(w.Country):
		v.add("wireless.country", "%q must be a two-letter country code", w.Country)
	case w.Country == "" && c.WirelessEnabled():
		v.add("wireless.country", "set the country before a radio can transmit")
	}

	seen := map[string]bool{}
	for i, r := range w.Radios {
		path := fmt.Sprintf("wireless.radios[%d]", i)
		switch {
		case !ifaceRe.MatchString(r.Name):
			v.add(path+".name", "%q is not a valid interface name", r.Name)
		case seen[r.Name]:
			v.add(path+".name", "duplicate radio %q", r.Name)
		case ifaces[r.Name]:
			v.add(path+".name", "%q is a radio; its networks are interfaces of their own", r.Name)
		}
		seen[r.Name] = true

		if !slices.Contains(Bands, r.Band) {
			v.add(path+".band", "%q is not a band", r.Band)
			continue
		}
		if r.Channel != 0 && !slices.Contains(Channels(r.Band), r.Channel) {
			v.add(path+".channel", "channel %d is not on %s", r.Channel, r.Band)
		} else if r.Band == Band5G && slices.Contains(RadarChannels, r.Channel) {
			v.add(path+".channel", "channel %d is a radar channel, which is not supported yet", r.Channel)
		}
		if r.Band == Band5G && r.Width == 160 {
			v.add(path+".width", "160 MHz on 5 GHz needs radar channels, which are not supported yet")
		} else if !slices.Contains(Widths(r.Band), r.Width) {
			v.add(path+".width", "%d MHz is not a width on %s", r.Width, r.Band)
		}
		switch {
		case !slices.Contains(Standards, r.Standard):
			v.add(path+".standard", "%q is not a standard", r.Standard)
		case r.Band == Band2G && r.Standard == StandardAC:
			v.add(path+".standard", "ac is 5 GHz only")
		case r.Band == Band6G && r.Standard != StandardAX:
			v.add(path+".standard", "6 GHz is ax only")
		}
		if r.Power < 0 || r.Power > 30 {
			v.add(path+".power", "%d must be 0-30 dBm", r.Power)
		}
	}
}

// wirelessNetwork checks one SSID: the radio that serves it and what a
// client needs to join.
func (v *validator) wirelessNetwork(path string, in Interface, c *Config) {
	n := in.Wireless
	radio, known := c.Radio(n.Radio)
	if !known {
		v.add(path+".wireless.radio", "unknown radio %q", n.Radio)
	}
	if in.Name == n.Radio {
		v.add(path+".name", "the radio's own interface stays idle; name the network something else")
	}
	switch {
	case len(n.SSID) < 1 || len(n.SSID) > 32:
		v.add(path+".wireless.ssid", "a network name is 1-32 bytes")
	case strings.ContainsFunc(n.SSID, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		v.add(path+".wireless.ssid", "a network name takes no control characters")
	}
	switch {
	case !slices.Contains(Securities, n.Security):
		v.add(path+".wireless.security", "%q is not a security mode", n.Security)
	case known && radio.Band == Band6G && n.Security != SecurityWPA3 && n.Security != SecurityOWE:
		v.add(path+".wireless.security", "6 GHz takes wpa3 or owe")
	}
	switch {
	case !n.Security.NeedsPassphrase():
		if n.Passphrase != "" {
			v.add(path+".wireless.passphrase", "%s takes no passphrase", n.Security)
		}
	case len(n.Passphrase) < 8 || len(n.Passphrase) > 63:
		v.add(path+".wireless.passphrase", "a passphrase is 8-63 characters")
	case !printableASCII(n.Passphrase):
		v.add(path+".wireless.passphrase", "a passphrase is printable ASCII")
	}
	if n.MaxClients < 0 || n.MaxClients > 2007 {
		v.add(path+".wireless.maxClients", "%d must be 0-2007", n.MaxClients)
	}
}

// printableASCII reports whether every byte is one a WPA passphrase may
// hold.
func printableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// crons checks the scheduled work. The schedule itself is parsed by the
// cron package at run time; here it is only checked for shape, so the
// model keeps no dependency on it.
func (v *validator) crons(c *Config) {
	ids := map[string]bool{}
	for i, cr := range c.Crons {
		path := fmt.Sprintf("crons[%d]", i)
		v.id(path+".id", cr.ID, ids)
		if err := checkCronSchedule(cr.Schedule); err != nil {
			v.add(path+".schedule", "%v", err)
		}
		switch cr.Kind {
		case CronBackup:
			if cr.Directory == "" {
				v.add(path+".directory", "say where the backups should go")
			} else if !strings.HasPrefix(cr.Directory, "/") {
				v.add(path+".directory", "%q must be an absolute path", cr.Directory)
			}
			if cr.Keep < 0 || cr.Keep > 1000 {
				v.add(path+".keep", "%d must be 0-1000 (0 keeps %d)", cr.Keep, DefaultBackupsKept)
			}
		case CronRefreshAliases, CronRefreshBlocklists:
		case CronRestartService:
			if !slices.Contains(CronServices, cr.Service) {
				v.add(path+".service", "%q is not a service this router runs (%s)",
					cr.Service, strings.Join(CronServices, ", "))
			}
		case CronWake:
			if cr.Device == "" {
				v.add(path+".device", "say which device to wake")
			} else if _, ok := c.WoLDevice(cr.Device); !ok {
				v.add(path+".device", "no Wake on LAN device has the id %q", cr.Device)
			}
		case CronCommand:
			if cr.Command == "" {
				v.add(path+".command", "say what to run")
			} else if !strings.HasPrefix(cr.Command, "/") {
				v.add(path+".command", "%q must be an absolute path, so it cannot depend on a PATH", cr.Command)
			}
		case CronSystemUpdate, CronOstioleUpdate, CronSystemUpdateCheck, CronOstioleUpdateCheck,
			CronRemoteBackup, CronCertificates:
			// These are scheduled from the update, backup and certificate
			// settings; a cron written out by hand is allowed to name them
			// as well.
		default:
			v.add(path+".kind", "unknown cron kind %q (%s)", cr.Kind, joinCronKinds())
		}
		if cr.TimeoutSeconds < 0 || cr.TimeoutSeconds > 3600 {
			v.add(path+".timeoutSeconds", "%d must be 0-3600", cr.TimeoutSeconds)
		}
		if cr.Passphrase != "" && cr.Kind != CronBackup {
			v.add(path+".passphrase", "only a backup has a file to encrypt")
		}
		if cr.Device != "" && cr.Kind != CronWake {
			v.add(path+".device", "only a wake cron names a device")
		}
	}
}

// checkCronSchedule accepts the shorthands and the five-field form. The
// fields themselves are checked when the cron is scheduled; this catches
// the mistakes people actually make.
func checkCronSchedule(expr string) error {
	expr = strings.TrimSpace(expr)
	switch {
	case expr == "":
		return fmt.Errorf("a schedule is required, like \"0 4 * * *\" or @daily")
	case strings.HasPrefix(expr, "@"):
		if !slices.Contains(cronShorthands, strings.ToLower(expr)) {
			return fmt.Errorf("%q is not a shorthand I know (%s)", expr, strings.Join(cronShorthands, ", "))
		}
		return nil
	}
	if n := len(strings.Fields(expr)); n != 5 {
		return fmt.Errorf("a schedule has five fields (minute hour day month weekday), got %d", n)
	}
	return nil
}

var cronShorthands = []string{"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly"}

// packageRe is what an entry on the never-upgrade list may look like. It
// is deliberately permissive — every distro spells package names
// differently — and exists to keep an argument list free of stray flags,
// which is why an entry cannot start with a dash.
//
// "*" and "?" are allowed so the list can hold globs: "kernel*" holds
// back every kernel package. Nothing here goes through a shell, so a
// pattern reaches the package manager as written.
var packageRe = regexp.MustCompile(`^[A-Za-z0-9*][A-Za-z0-9._+*?-]*$`)

// updates checks the settings the update crons read. An empty mode or
// schedule is the default rather than a mistake.
func (v *validator) updates(u *Updates) {
	schedule := func(path string, expr string) {
		if expr == "" {
			return
		}
		if err := checkCronSchedule(expr); err != nil {
			v.add(path, "%v", err)
		}
	}
	check := func(path string, mode UpdateMode, checkSchedule, installSchedule string) {
		if mode != "" && !slices.Contains(UpdateModes, mode) {
			v.add(path+".mode", "%q is not an update mode (%s)", mode, joinModes())
		}
		schedule(path+".checkSchedule", checkSchedule)
		schedule(path+".installSchedule", installSchedule)
	}
	check("updates.system", u.System.Mode, u.System.CheckSchedule, u.System.InstallSchedule)
	check("updates.ostiole", u.Ostiole.Mode, u.Ostiole.CheckSchedule, u.Ostiole.InstallSchedule)

	for i, p := range u.System.Exclude {
		if !packageRe.MatchString(p) {
			v.add(fmt.Sprintf("updates.system.exclude[%d]", i),
				"%q does not look like a package name or a glob such as \"kernel*\"", p)
		}
	}
	if c := u.Ostiole.Channel; c != "" && c != ChannelStable && c != ChannelBeta {
		v.add("updates.ostiole.channel", "%q must be %s or %s", c, ChannelStable, ChannelBeta)
	}
}

// bucketRe is what an S3 bucket may be called. Every service is stricter
// than this in its own way; this catches the mistakes, such as a whole
// URL pasted into the field.
var bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// backup checks the remote copies. The fields are checked whether the
// copies are switched on or not, so that the form can be filled in
// stages and turned on last; the fields that have to be there at all are
// only required once it is on.
func (v *validator) backup(b *Backup) {
	r := &b.Remote
	if r.Enabled {
		switch u, err := url.Parse(strings.TrimSpace(r.Endpoint)); {
		case r.Endpoint == "":
			v.add("backup.remote.endpoint", "say which service the bucket is at")
		case err != nil:
			v.add("backup.remote.endpoint", "%q is not a URL", r.Endpoint)
		case u.Scheme != "https":
			v.add("backup.remote.endpoint", "%q has to be an https:// URL", r.Endpoint)
		case u.Host == "":
			v.add("backup.remote.endpoint", "%q has no host name", r.Endpoint)
		case strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "":
			v.add("backup.remote.endpoint", "%q is the service and nothing else, without a path", r.Endpoint)
		}
		if r.Bucket == "" {
			v.add("backup.remote.bucket", "say which bucket the copies go in")
		} else if !bucketRe.MatchString(r.Bucket) {
			v.add("backup.remote.bucket", "%q is not a bucket name: 3-63 lower-case letters, digits, dots and dashes", r.Bucket)
		}
		if r.KeyID == "" {
			v.add("backup.remote.keyId", "say which application key to use")
		}
		if r.Secret == "" {
			v.add("backup.remote.secret", "the application key's secret is missing")
		}
		if r.Passphrase == "" {
			v.add("backup.remote.passphrase",
				"a passphrase is required; the copies are never uploaded unencrypted")
		}
	}
	if p := strings.TrimSpace(r.Prefix); p != "" {
		segments := strings.Split(strings.TrimSuffix(p, "/"), "/")
		switch {
		case strings.HasPrefix(p, "/"):
			v.add("backup.remote.prefix", "%q cannot start with a slash", p)
		case slices.Contains(segments, ""):
			v.add("backup.remote.prefix", "%q has an empty folder in it", p)
		case slices.Contains(segments, ".."):
			v.add("backup.remote.prefix", "%q cannot go up a folder", p)
		}
	}
	if r.Schedule != "" {
		if err := checkCronSchedule(r.Schedule); err != nil {
			v.add("backup.remote.schedule", "%v", err)
		}
	}
	if r.Keep < 0 || r.Keep > 1000 {
		v.add("backup.remote.keep", "%d must be 0-1000 (0 keeps every copy)", r.Keep)
	}
	if r.Days < 0 || r.Days > 3650 {
		v.add("backup.remote.days", "%d must be 0-3650 (0 writes no rule)", r.Days)
	}
}

// acme checks the accounts at the CAs and the DNS providers a dns-01
// challenge writes to. Both are checked whether a certificate uses them
// or not, so a half-filled account is found before the CA is asked.
func (v *validator) acme(c *Config) {
	ids := map[string]bool{}
	for i, a := range c.ACME.Accounts {
		path := fmt.Sprintf("acme.accounts[%d]", i)
		v.id(path+".id", a.ID, ids)
		switch u, err := url.Parse(strings.TrimSpace(a.Directory)); {
		case a.Directory == "":
			v.add(path+".directory", "say which CA this account is at")
		case err != nil || u.Host == "":
			v.add(path+".directory", "%q is not a URL", a.Directory)
		case u.Scheme != "https":
			v.add(path+".directory", "%q has to be an https:// URL", a.Directory)
		}
		if a.Email != "" && !strings.Contains(a.Email, "@") {
			v.add(path+".email", "%q is not an email address", a.Email)
		}
		if (a.EABKeyID == "") != (a.EABHMAC == "") {
			v.add(path+".eabKeyId", "external account credentials need both the key id and the HMAC")
		}
		if a.PrivateKey == "" {
			v.add(path+".privateKey", "an account key is missing")
		} else if err := checkPrivateKey(a.PrivateKey); err != nil {
			v.add(path+".privateKey", "%v", err)
		}
		if a.CACert != "" {
			if err := checkCertificates(a.CACert); err != nil {
				v.add(path+".caCert", "%v", err)
			}
		}
	}

	providers := map[string]bool{}
	for i, p := range c.ACME.Providers {
		path := fmt.Sprintf("acme.providers[%d]", i)
		v.id(path+".id", p.ID, providers)
		kind, ok := ProviderKindOf(p.Kind)
		if !ok {
			v.add(path+".kind", "%q is not a DNS provider this build knows (%s)", p.Kind, joinProviderKinds())
		} else {
			for _, f := range kind.Fields {
				if f.Required && strings.TrimSpace(p.Settings[f.Key]) == "" {
					v.add(path+".settings."+f.Key, "%s is required for %s", f.Label, kind.Label)
				}
			}
			for key := range p.Settings {
				if _, known := kind.Field(key); !known {
					v.add(path+".settings."+key, "%s has no setting called %q", kind.Label, key)
				}
			}
		}
		if p.PropagationSeconds < 0 || p.PropagationSeconds > 600 {
			v.add(path+".propagationSeconds", "%d must be 0-600 (0 is the provider's own)", p.PropagationSeconds)
		}
	}
}

// certificates checks the certificates this router holds and the one the
// web UI serves.
func (v *validator) certificates(c *Config, ifaces map[string]bool) {
	ids := map[string]bool{}
	for i, cert := range c.Certificates {
		path := fmt.Sprintf("certificates[%d]", i)
		if !certIDRe.MatchString(cert.ID) {
			v.add(path+".id", "%q must match %s; it is a directory name and a URL segment", cert.ID, certIDRe)
		} else if ids[cert.ID] {
			v.add(path+".id", "duplicate id %q", cert.ID)
		}
		ids[cert.ID] = true
		switch cert.Source {
		case SourceACME:
			v.acmeCertificate(path, cert, c, ifaces)
		case SourceUploaded:
			v.uploadedCertificate(path, cert)
		default:
			v.add(path+".source", "%q must be %s or %s", cert.Source, SourceACME, SourceUploaded)
		}
	}
	if id := c.System.Management.Certificate; id != "" && !ids[id] {
		v.add("system.management.certificate", "unknown certificate %q", id)
	}
}

func (v *validator) acmeCertificate(path string, cert Certificate, c *Config, ifaces map[string]bool) {
	wildcard, address := false, false
	if len(cert.Names) == 0 && len(cert.InterfaceAddresses) == 0 {
		v.add(path+".names", "say what this certificate should cover")
	}
	for j, name := range cert.Names {
		np := fmt.Sprintf("%s.names[%d]", path, j)
		n := strings.TrimSpace(name)
		switch {
		case strings.HasPrefix(n, "*."):
			wildcard = true
			v.domainName(np, strings.TrimPrefix(n, "*."))
		case isIPName(n):
			address = true
		default:
			v.domainName(np, n)
		}
	}
	for j, iface := range cert.InterfaceAddresses {
		ip := fmt.Sprintf("%s.interfaceAddresses[%d]", path, j)
		address = true
		in, ok := c.Interface(iface)
		switch {
		case !ifaces[iface] || !ok:
			v.add(ip, "unknown interface %q", iface)
		case !in.Enabled:
			v.add(ip, "interface %q is off, so it has no address to put in a certificate", iface)
		default:
			if z, ok := c.Zone(in.Zone); !ok || !z.External {
				v.add(ip, "interface %q is not in an external zone; a CA only issues for a public address", iface)
			}
		}
	}

	switch {
	case cert.Challenge == "":
		v.add(path+".challenge", "say how the CA should check this: %s or %s", ChallengeDNS, ChallengeHTTP)
	case !slices.Contains(Challenges, cert.Challenge):
		v.add(path+".challenge", "%q must be %s or %s", cert.Challenge, ChallengeDNS, ChallengeHTTP)
	case wildcard && cert.Challenge != ChallengeDNS:
		v.add(path+".challenge", "a wildcard is only issued over %s", ChallengeDNS)
	case address && cert.Challenge != ChallengeHTTP:
		v.add(path+".challenge", "an address is only issued over %s", ChallengeHTTP)
	}
	if cert.Challenge == ChallengeDNS {
		if cert.Provider == "" {
			v.add(path+".provider", "say which DNS provider writes the challenge record")
		} else if _, ok := c.DNSProvider(cert.Provider); !ok {
			v.add(path+".provider", "unknown DNS provider %q", cert.Provider)
		}
	} else if cert.Provider != "" {
		v.add(path+".provider", "only a %s challenge writes a DNS record", ChallengeDNS)
	}
	if cert.Enabled && cert.Challenge == ChallengeHTTP {
		if id, taken := forwardsPort80(c); taken {
			v.add(path+".challenge", "port 80 is forwarded to %s; http-01 cannot answer", id)
		}
	}

	if cert.Account == "" {
		v.add(path+".account", "say which account to order from")
	} else if _, ok := c.ACMEAccount(cert.Account); !ok {
		v.add(path+".account", "unknown ACME account %q", cert.Account)
	}
	if cert.KeyType != "" && !slices.Contains(KeyTypes, cert.KeyType) {
		v.add(path+".keyType", "%q must be one of %s", cert.KeyType, strings.Join(KeyTypes, ", "))
	}
	switch {
	case !slices.Contains(Profiles, cert.Profile):
		v.add(path+".profile", "%q is not a profile this build offers", cert.Profile)
	case address && cert.Profile != "" && cert.Profile != ProfileShortlived:
		v.add(path+".profile", "an address is only issued under %s", ProfileShortlived)
	}
	if cert.CertPEM != "" || cert.KeyPEM != "" {
		v.add(path+".certPem", "an issued certificate holds no PEM here; its files are on disk")
	}
}

func (v *validator) uploadedCertificate(path string, cert Certificate) {
	switch {
	case cert.CertPEM == "":
		v.add(path+".certPem", "paste the certificate, its chain under it")
	case cert.KeyPEM == "":
		v.add(path+".keyPem", "paste the private key")
	default:
		if _, err := tls.X509KeyPair([]byte(cert.CertPEM), []byte(cert.KeyPEM)); err != nil {
			v.add(path+".certPem", "%v", err)
		}
	}
	for field, set := range map[string]bool{
		"names":              len(cert.Names) > 0,
		"interfaceAddresses": len(cert.InterfaceAddresses) > 0,
		"account":            cert.Account != "",
		"challenge":          cert.Challenge != "",
		"provider":           cert.Provider != "",
		"keyType":            cert.KeyType != "",
		"profile":            cert.Profile != "",
	} {
		if set {
			v.add(path+"."+field, "an uploaded certificate is not ordered, so this has no effect")
		}
	}
}

// forwardsPort80 names an enabled forward that takes tcp 80 away from an
// external zone, which is where an http-01 challenge has to be answered.
func forwardsPort80(c *Config) (string, bool) {
	for _, f := range c.NAT.PortForwards {
		if !f.Enabled || (f.Protocol != ProtocolTCP && f.Protocol != ProtocolTCPUDP && f.Protocol != ProtocolAny) {
			continue
		}
		if z, ok := c.Zone(f.Zone); !ok || !z.External {
			continue
		}
		for _, p := range f.Ports {
			if r, err := ParsePortRange(p); err == nil && r.Lo <= 80 && 80 <= r.Hi {
				return f.ID, true
			}
		}
	}
	return "", false
}

func isIPName(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

func joinProviderKinds() string {
	out := make([]string, 0, len(ProviderKinds))
	for _, k := range ProviderKinds {
		out = append(out, k.Kind)
	}
	return strings.Join(out, ", ")
}

// checkPrivateKey accepts the PEM forms a CA account key comes in.
func checkPrivateKey(s string) error {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return fmt.Errorf("this is not a PEM private key")
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		_, err := x509.ParseECPrivateKey(block.Bytes)
		return err
	case "RSA PRIVATE KEY":
		_, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		return err
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return err
		}
		switch key.(type) {
		case *ecdsa.PrivateKey, *rsa.PrivateKey:
			return nil
		}
		return fmt.Errorf("a CA account key is EC or RSA, not %T", key)
	}
	return fmt.Errorf("%q is not a private key", block.Type)
}

func checkCertificates(s string) error {
	rest := []byte(s)
	found := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return err
		}
		found++
	}
	if found == 0 {
		return fmt.Errorf("this is not a PEM certificate")
	}
	return nil
}

// joinCronKinds names the kinds an operator may write, so a typo comes
// back with the list rather than just a refusal.
func joinCronKinds() string {
	out := make([]string, 0, len(CronKinds))
	for _, k := range CronKinds {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

func joinModes() string {
	out := make([]string, 0, len(UpdateModes))
	for _, m := range UpdateModes {
		out = append(out, string(m))
	}
	return strings.Join(out, ", ")
}

func joinLogLevels() string {
	out := make([]string, 0, len(LogLevels))
	for _, l := range LogLevels {
		out = append(out, string(l))
	}
	return strings.Join(out, ", ")
}

// aliasSelect checks the conditions that keep part of a fetched JSON list.
func (v *validator) aliasSelect(path string, a Alias) {
	if len(a.Select) == 0 {
		return
	}
	if !a.Selectable() {
		v.add(path+".select", "only a hosts alias with a URL can keep part of what it fetches")
		return
	}
	if len(a.Select) > MaxSelect {
		v.add(path+".select", "at most %d conditions", MaxSelect)
	}
	seen := map[string]bool{}
	fields := map[string]bool{}
	for j, s := range a.Select {
		at := fmt.Sprintf("%s.select[%d]", path, j)
		c, err := ParseCondition(s)
		if err != nil {
			v.add(at, "%v", err)
			continue
		}
		key := strings.ToLower(c.String())
		if seen[key] {
			v.add(at, "%s is listed twice", c)
		}
		seen[key] = true
		fields[strings.ToLower(c.Field)] = true
	}
	if len(fields) > MaxSelectFields {
		v.add(path+".select", "conditions on at most %d different fields", MaxSelectFields)
	}
}

func (v *validator) system(s *System) {
	for field, tmpl := range map[string]string{"geoIPv4Url": s.GeoIPv4URL, "geoIPv6Url": s.GeoIPv6URL} {
		if tmpl == "" {
			continue
		}
		if u, err := url.Parse(tmpl); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			v.add("system."+field, "%q must be an http or https URL", tmpl)
		} else if !strings.Contains(tmpl, "{country}") {
			v.add("system."+field, "the URL needs {country} in it, which is replaced with the code")
		}
	}
	for field, t := range map[string]struct{ tmpl, key string }{
		"asnUrl":      {s.ASNURL, "{asn}"},
		"asnNamesUrl": {s.ASNNamesURL, "{asns}"},
	} {
		if t.tmpl == "" {
			continue
		}
		if u, err := url.Parse(t.tmpl); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			v.add("system."+field, "%q must be an http or https URL", t.tmpl)
		} else if !strings.Contains(t.tmpl, t.key) {
			v.add("system."+field, "the URL needs %s in it, which is replaced with the number", t.key)
		}
	}
	for field, u := range map[string]string{"bogonV4Url": s.BogonV4URL, "bogonV6Url": s.BogonV6URL} {
		if u == "" {
			continue
		}
		if parsed, err := url.Parse(u); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			v.add("system."+field, "%q must be an http or https URL", u)
		}
	}
	if s.Hostname != "" && !hostnameRe.MatchString(s.Hostname) {
		v.add("system.hostname", "%q is not a valid hostname", s.Hostname)
	}
	if s.Timezone != "" && !timezone.Valid(s.Timezone) {
		v.add("system.timezone", "%q is not a timezone; use an IANA name such as Europe/Berlin", s.Timezone)
	}
	if s.KeepRevisions < 0 || s.KeepRevisions > MaxKeepRevisions {
		v.add("system.keepRevisions", "%d must be 0-%d (0 keeps %d)",
			s.KeepRevisions, MaxKeepRevisions, DefaultKeepRevisions)
	}
	if n := s.Management.FirewallLog.Entries; n < 0 || n > MaxFirewallLogEntries {
		v.add("system.management.firewallLog.entries", "%d must be 0-%d (0 means %d)",
			n, MaxFirewallLogEntries, DefaultFirewallLogEntries)
	}
	if s.Logging.MaxUseGB < 0 || s.Logging.MaxUseGB > journald.MaxMaxUseGB {
		v.add("system.logging.maxUseGB", "%d must be 0-%d (0 keeps %d)",
			s.Logging.MaxUseGB, journald.MaxMaxUseGB, journald.DefaultMaxUseGB)
	}
	if s.Logging.RetentionDays < 0 || s.Logging.RetentionDays > MaxRetentionDays {
		v.add("system.logging.retentionDays", "%d must be 0-%d (0 keeps %d)",
			s.Logging.RetentionDays, MaxRetentionDays, DefaultRetentionDays)
	}
	if l := s.Logging.Level; l != "" && !slices.Contains(LogLevels, l) {
		v.add("system.logging.level", "%q is not a log level (%s)", l, joinLogLevels())
	}
	if n := s.ConntrackMax; n != 0 && (n < sysctl.MinConntrackMax || n > sysctl.MaxConntrackMax) {
		v.add("system.conntrackMax", "%d must be %d-%d (0 leaves the kernel's own limit)",
			n, sysctl.MinConntrackMax, sysctl.MaxConntrackMax)
	}
	for i, d := range s.DNSServers {
		if _, err := ParseIP(d); err != nil {
			v.add(fmt.Sprintf("system.dnsServers[%d]", i), "%v", err)
		}
	}
}

// proxyID is id for the proxy's four lists, which share one set of names
// so that an event can name what it hit without saying what kind of thing
// it is. The refusal says which of them already has the name, because the
// one being checked is not always the one that was just renamed.
func (v *validator) proxyID(path, id, kind string, seen map[string]string) {
	if !idRe.MatchString(id) {
		v.add(path, "%q must match %s", id, idRe)
		return
	}
	if other, taken := seen[id]; taken {
		v.add(path, "%q is already the %s's; pools, sites, profiles and routes share one set of names", id, other)
		return
	}
	seen[id] = kind
}

func (v *validator) id(path, id string, seen map[string]bool) {
	if !idRe.MatchString(id) {
		v.add(path, "%q must match %s", id, idRe)
		return
	}
	if seen[id] {
		v.add(path, "duplicate id %q", id)
	}
	seen[id] = true
}

func (v *validator) addr4(path string, a IPv4) {
	switch a.Mode {
	case AddrNone, AddrDHCP, AddrPPP:
		if a.Address != "" {
			v.add(path+".address", "address is only valid in static mode")
		}
	case AddrStatic:
		v.prefix(path+".address", a.Address, true)
	default:
		v.add(path+".mode", "unknown IPv4 mode %q", a.Mode)
	}
	if a.Gateway != "" {
		if ip, err := ParseIP(a.Gateway); err != nil {
			v.add(path+".gateway", "%v", err)
		} else if !ip.Is4() {
			v.add(path+".gateway", "must be an IPv4 address")
		}
	}
}

func (v *validator) addr6(path string, a IPv6) {
	switch a.Mode {
	case AddrNone, AddrDHCP, AddrSLAAC, AddrDelegated, AddrPPP:
		if a.Address != "" {
			v.add(path+".address", "address is only valid in static mode")
		}
	case AddrStatic:
		v.prefix(path+".address", a.Address, false)
	default:
		v.add(path+".mode", "unknown IPv6 mode %q", a.Mode)
	}
	if a.PrefixHint != "" {
		p, err := netip.ParsePrefix(a.PrefixHint)
		switch {
		case err != nil:
			v.add(path+".prefixHint", "%v (write it like ::/56)", err)
		case p.Addr().Is4():
			v.add(path+".prefixHint", "a delegated prefix is IPv6, so write it like ::/56")
		case p.Bits() < 1 || p.Bits() > 64:
			v.add(path+".prefixHint", "/%d must be between /1 and /64", p.Bits())
		case a.Mode != AddrDHCP:
			v.add(path+".prefixHint", "asking for a prefix needs IPv6 mode dhcp, not %q", a.Mode)
		}
	}
	if a.SubnetID < 0 || a.SubnetID > 0xffff {
		v.add(path+".subnetId", "%d must be 0-65535", a.SubnetID)
	}
	if a.Mode != AddrDelegated && a.DelegatedFrom != "" {
		v.add(path+".delegatedFrom", "only an interface in delegated mode takes a prefix from another")
	}
	if a.Gateway != "" {
		if ip, err := ParseIP(a.Gateway); err != nil {
			v.add(path+".gateway", "%v", err)
		} else if !ip.Is6() {
			v.add(path+".gateway", "must be an IPv6 address")
		}
	}
}

func (v *validator) prefix(path, s string, want4 bool) {
	if s == "" {
		v.add(path, "address is required in static mode")
		return
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		v.add(path, "%q must be CIDR notation (address/prefix)", s)
		return
	}
	if p.Addr().Is4() != want4 {
		v.add(path, "wrong address family for %q", s)
	}
}

func (v *validator) endpoint(path string, e Endpoint, aliases map[string]AliasType, portsAllowed, isDest bool) {
	if len(e.Addresses) > 0 && e.Alias != "" {
		v.add(path, "addresses and alias are mutually exclusive")
	}
	for i, a := range e.Addresses {
		if _, err := ParseAddress(a); err != nil {
			v.add(fmt.Sprintf("%s.addresses[%d]", path, i), "%v", err)
		}
	}
	if e.Alias != "" {
		if t, ok := aliases[e.Alias]; !ok {
			v.add(path+".alias", "unknown alias %q", e.Alias)
		} else if !t.HoldsAddresses() {
			v.add(path+".alias", "alias %q holds ports, not addresses", e.Alias)
		}
	}
	if len(e.Ports) > 0 && e.PortAlias != "" {
		v.add(path, "ports and portAlias are mutually exclusive")
	}
	if !portsAllowed && (len(e.Ports) > 0 || e.PortAlias != "") {
		v.add(path, "ports require protocol tcp, udp, or tcp+udp")
	}
	for i, p := range e.Ports {
		if _, err := ParsePortRange(p); err != nil {
			v.add(fmt.Sprintf("%s.ports[%d]", path, i), "%v", err)
		}
	}
	if e.PortAlias != "" {
		if t, ok := aliases[e.PortAlias]; !ok {
			v.add(path+".portAlias", "unknown alias %q", e.PortAlias)
		} else if t != AliasPorts {
			v.add(path+".portAlias", "alias %q is not a ports alias", e.PortAlias)
		}
	}
	if e.Self {
		if !isDest {
			v.add(path+".self", "self is only valid as a destination")
		}
		if len(e.Addresses) > 0 || e.Alias != "" {
			v.add(path+".self", "self cannot be combined with addresses or an alias")
		}
	}
	// An inversion with nothing to invert would match everything, which is
	// never what someone ticking the box meant.
	if e.NotAddresses && len(e.Addresses) == 0 && e.Alias == "" && !e.Self {
		v.add(path+".notAddresses", "nothing to invert: give addresses, an alias, or self")
	}
	if e.NotPorts && len(e.Ports) == 0 && e.PortAlias == "" {
		v.add(path+".notPorts", "nothing to invert: give ports or a port alias")
	}
}

// holdRe is how long a source may be held: a number and a unit
// nftables counts timeouts in.
var holdRe = regexp.MustCompile(`^[0-9]+[smhd]$`)

// rateLimit checks one limit. A rate of zero is the one mistake worth
// spelling out: it reads as "allow none", and a rule that allows none is
// a drop rule somebody should have written instead.
func (v *validator) rateLimit(path string, l RateLimit) {
	switch {
	case l.Rate == 0:
		v.add(path+".rate", "a rate of zero allows nothing through; write a drop rule instead")
	case l.Rate < MinLimitRate || l.Rate > MaxLimitRate:
		v.add(path+".rate", "%d must be between %d and %d", l.Rate, MinLimitRate, MaxLimitRate)
	}
	if !l.Unit.Valid() {
		v.add(path+".unit", "unknown period %q (second, minute, or hour)", l.Unit)
	}
	if l.Burst < 0 || l.Burst > MaxLimitBurst {
		v.add(path+".burst", "%d must be between 0 and %d", l.Burst, MaxLimitBurst)
	}
}

// protection checks the edge defence: the zones it names have to exist,
// and each limit has to be a limit. A zone that faces the inside can be
// named deliberately — a guest network is a reasonable place to stop a
// flood — so unlike the connection tally there is no warning about it.
func (v *validator) protection(c *Config, zones map[string]bool) {
	p := c.Protection
	for i, z := range p.Zones {
		if !zones[z] {
			v.add(fmt.Sprintf("protection.zones[%d]", i), "unknown zone %q", z)
		}
	}
	if p.SynFlood != nil {
		v.rateLimit("protection.synFlood", *p.SynFlood)
	}
	if p.ICMPFlood != nil {
		v.rateLimit("protection.icmpFlood", *p.ICMPFlood)
	}
	if p.PortScan != nil {
		v.rateLimit("protection.portScan", p.PortScan.Limit())
		if !holdRe.MatchString(p.PortScan.HoldOr()) {
			v.add("protection.portScan.hold",
				"%q must be a number and a unit, like 10m, 1h, or 30s", p.PortScan.HoldOr())
		}
	}
	// A defence nobody can reach is worth saying out loud: it is almost
	// always a zone that was renamed or an interface that moved.
	if p.On() && len(c.ProtectedZones()) == 0 {
		v.add("protection", "nothing is defended: no external zone has an interface, "+
			"and protection.zones names none that does")
	}
}
