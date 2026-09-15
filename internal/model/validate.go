package model

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

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
	}

	ifaces := map[string]bool{}
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
		if in.WireGuard != nil {
			if in.VLAN != nil {
				v.add(path+".wireguard", "an interface is either a VLAN or a WireGuard tunnel, not both")
			}
			v.wireguard(path+".wireguard", in)
		}
	}

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
		default:
			v.add(path+".type", "unknown alias type %q", a.Type)
		}
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

	ids := map[string]bool{}
	for i, r := range c.Rules {
		path := fmt.Sprintf("rules[%d]", i)
		v.id(path+".id", r.ID, ids)
		if r.Schedule != "" && !schedules[r.Schedule] {
			v.add(path+".schedule", "unknown schedule %q", r.Schedule)
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

	switch c.NAT.Outbound.Mode {
	case OutboundAutomatic, OutboundManual, OutboundDisabled:
	default:
		v.add("nat.outbound.mode", "unknown mode %q", c.NAT.Outbound.Mode)
	}
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

	v.services(c, ifaces)

	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
}

var (
	macRe       = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
	leaseTimeRe = regexp.MustCompile(`^([0-9]+[smhdw]|infinite)$`)
	domainRe    = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
)

func (v *validator) services(c *Config, ifaces map[string]bool) {
	seen := map[string]bool{}
	for i, sc := range c.Services.DHCP.Scopes {
		path := fmt.Sprintf("services.dhcp.scopes[%d]", i)
		in, ok := c.Interface(sc.Interface)
		if !ok || !ifaces[sc.Interface] {
			v.add(path+".interface", "unknown interface %q", sc.Interface)
			continue
		}
		if seen[sc.Interface] {
			v.add(path+".interface", "interface %q already has a scope", sc.Interface)
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
			v.add(path+".leaseTime", "%q must look like 12h, 2d, or infinite", sc.LeaseTime)
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
	names := map[string]bool{}
	for i, h := range dns.HostOverrides {
		path := fmt.Sprintf("services.dns.hostOverrides[%d]", i)
		if !hostnameRe.MatchString(h.Hostname) {
			v.add(path+".hostname", "%q is not a valid hostname", h.Hostname)
		} else if names[strings.ToLower(h.Hostname)] {
			v.add(path+".hostname", "duplicate host %q", h.Hostname)
		}
		names[strings.ToLower(h.Hostname)] = true
		if _, err := ParseIP(h.IP); err != nil {
			v.add(path+".ip", "%v", err)
		}
	}
}

// dhcpv6 checks the router advertisement scopes. The prefix itself is not
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
			v.add(path+".leaseTime", "%q must look like 12h, 2d, or infinite", sc.LeaseTime)
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

func (v *validator) system(s *System) {
	if s.Hostname != "" && !hostnameRe.MatchString(s.Hostname) {
		v.add("system.hostname", "%q is not a valid hostname", s.Hostname)
	}
	for i, d := range s.DNSServers {
		if _, err := ParseIP(d); err != nil {
			v.add(fmt.Sprintf("system.dnsServers[%d]", i), "%v", err)
		}
	}
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
	case AddrNone, AddrDHCP:
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
	case AddrNone, AddrDHCP, AddrSLAAC:
		if a.Address != "" {
			v.add(path+".address", "address is only valid in static mode")
		}
	case AddrStatic:
		v.prefix(path+".address", a.Address, false)
	default:
		v.add(path+".mode", "unknown IPv6 mode %q", a.Mode)
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
		} else if t != AliasHosts {
			v.add(path+".alias", "alias %q is not a hosts alias", e.Alias)
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
}
