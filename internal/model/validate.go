package model

import (
	"fmt"
	"net/netip"
	"strings"
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

	ids := map[string]bool{}
	for i, r := range c.Rules {
		path := fmt.Sprintf("rules[%d]", i)
		v.id(path+".id", r.ID, ids)
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

	if len(v.issues) == 0 {
		return nil
	}
	return &ValidationError{Issues: v.issues}
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
