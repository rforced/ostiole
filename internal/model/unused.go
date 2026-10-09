package model

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
)

// Unused is an item nothing in the configuration uses (ADR-0042). Takes
// names what goes with it when it is removed.
type Unused struct {
	Kind  string   `json:"kind"`
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Path  string   `json:"path"`
	Why   string   `json:"why"`
	Takes []string `json:"takes,omitempty"`
}

// Disabled is an item switched off. It is kept on purpose: listed beside
// the unused ones, never removed with them.
type Disabled struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// UnusedKey names an unused item by its kind and what the kind is keyed by.
type UnusedKey struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// candidate is an item of a kind that can be unused, and whether
// something uses it.
type candidate struct {
	Unused
	used bool
}

// Unused lists what nothing in the configuration uses and what is switched
// off, each sorted by kind and then name.
func (c *Config) Unused() ([]Unused, []Disabled) {
	var out []Unused
	for _, cd := range c.candidates() {
		if !cd.used {
			out = append(out, cd.Unused)
		}
	}
	slices.SortFunc(out, func(a, b Unused) int { return byKindName(a.Kind, a.Name, a.ID, b.Kind, b.Name, b.ID) })
	return out, c.disabled()
}

// RemoveUnused removes the unused items the keys name, and with a zone
// what is written against it. A key that names no unused item is refused
// before anything is removed.
func (c *Config) RemoveUnused(keys []UnusedKey) error {
	found := map[UnusedKey]bool{}
	for _, cd := range c.candidates() {
		found[UnusedKey{cd.Kind, cd.ID}] = cd.used
	}
	off := map[UnusedKey]bool{}
	for _, d := range c.disabled() {
		off[UnusedKey{d.Kind, d.ID}] = true
	}
	for _, k := range keys {
		used, ok := found[k]
		switch {
		case ok && used:
			return fmt.Errorf("%s %q is in use", k.Kind, k.ID)
		case off[k]:
			return fmt.Errorf("%s %q is switched off, and switched-off items are kept", k.Kind, k.ID)
		case !ok:
			return fmt.Errorf("no unused %s %q", k.Kind, k.ID)
		}
	}
	for _, k := range keys {
		c.removeUnused(k)
	}
	return nil
}

func (c *Config) candidates() []candidate {
	var out []candidate
	add := func(kind, id, name, path, why string, used bool) {
		out = append(out, candidate{Unused{Kind: kind, ID: id, Name: name, Path: path, Why: why}, used})
	}
	p := &c.Services.Proxy

	aliases, schedules, routed := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, r := range c.Rules {
		for _, e := range []Endpoint{r.Source, r.Destination} {
			aliases[e.Alias], aliases[e.PortAlias] = true, true
		}
		schedules[r.Schedule] = true
		routed[r.Gateway] = true
	}
	for _, a := range p.Access {
		aliases[a.Source.Alias] = true
	}
	for _, from := range c.allowFroms() {
		for _, f := range from {
			if _, err := ParseAddress(f); err != nil {
				aliases[f] = true
			}
		}
	}
	enforce := c.Blocking.Enforce
	aliases[enforce.DoHAlias], aliases[enforce.ExemptClients], aliases[enforce.ExemptDestinations] = true, true, true
	for _, a := range c.Aliases {
		add("alias", a.Name, a.Name, diffPath("aliases", a.Name), "Nothing names it", aliases[a.Name])
	}
	for _, s := range c.Schedules {
		add("schedule", s.Name, s.Name, diffPath("schedules", s.Name), "No rule names it", schedules[s.Name])
	}

	pools, profiles, certs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, s := range p.Sites {
		pools[s.Pool], profiles[s.WAF], certs[s.Certificate] = true, true, true
		for _, pa := range s.Paths {
			pools[pa.Pool] = true
		}
	}
	certs[c.System.Management.Certificate] = true
	for _, pl := range p.Pools {
		add("pool", pl.ID, pl.ID, diffPath("services.proxy.pools", pl.ID), "No site uses it", pools[pl.ID])
	}
	for _, w := range p.Profiles {
		add("waf-profile", w.ID, w.ID, diffPath("services.proxy.wafProfiles", w.ID), "No site uses it", profiles[w.ID])
	}
	for _, cert := range c.Certificates {
		if cert.Enabled {
			add("certificate", cert.ID, cert.ID, diffPath("certificates", cert.ID),
				"No site or the web UI uses it", certs[cert.ID])
		}
	}

	accounts, providers := map[string]bool{}, c.providersUsed()
	for _, cert := range c.Certificates {
		accounts[cert.Account] = true
	}
	for _, a := range c.ACME.Accounts {
		add("acme-account", a.ID, a.ID, diffPath("acme.accounts", a.ID), "No certificate uses it", accounts[a.ID])
	}
	for _, pr := range c.DNSProviders {
		add("dns-provider", pr.ID, pr.ID, diffPath("dnsProviders", pr.ID),
			"No certificate or dynamic DNS record uses it", providers[pr.ID])
	}

	routed[c.Services.DNS.Via] = true
	for _, g := range c.GatewayGroups {
		for _, m := range g.Members {
			routed[m.Gateway] = true
		}
	}
	for _, g := range c.GatewayGroups {
		if g.Enabled {
			add("gateway-group", g.Name, g.Name, diffPath("gatewayGroups", g.Name), "No rule routes through it", routed[g.Name])
		}
	}
	for _, g := range c.Gateways {
		if g.Enabled && c.TunnelGateway(g) {
			add("gateway", g.Name, g.Name, diffPath("gateways", g.Name), "No rule routes through it", routed[g.Name])
		}
	}

	zoned, radios := map[string]bool{}, map[string]bool{}
	for _, in := range c.Interfaces {
		zoned[in.Zone] = true
		if in.Wireless != nil {
			radios[in.Wireless.Radio] = true
		}
	}
	kept := c.keptZones(zoned)
	for _, z := range c.Zones {
		add("zone", z.Name, z.Name, diffPath("zones", z.Name), "No interface is in it", zoned[z.Name] || kept[z.Name])
		if cd := &out[len(out)-1]; !cd.used {
			cd.Takes = c.zoneTakes(z.Name)
		}
	}
	for _, r := range c.Wireless.Radios {
		if r.Enabled {
			add("radio", r.Name, r.Name, diffPath("wireless.radios", r.Name), "No network is on it", radios[r.Name])
		}
	}
	for _, l := range c.Services.DHCP.StaticLeases {
		add("static-lease", l.MAC, cmp.Or(l.Hostname, l.MAC), diffPath("services.dhcp.staticLeases", l.MAC),
			"Outside every DHCP network", c.leaseServed(l))
	}
	return out
}

// allowFroms are the proxy sites' and routes' Allow from lists.
func (c *Config) allowFroms() [][]string {
	var out [][]string
	for _, s := range c.Services.Proxy.Sites {
		out = append(out, s.AllowFrom)
	}
	for _, r := range c.Services.Proxy.Routes {
		out = append(out, r.AllowFrom)
	}
	return out
}

// providersUsed are the DNS providers a certificate's challenge or a
// dynamic DNS record is written through.
func (c *Config) providersUsed() map[string]bool {
	used := map[string]bool{}
	for _, cert := range c.Certificates {
		switch {
		case cert.Provider != "":
			used[cert.Provider] = true
		case cert.Source != SourceACME || cert.Challenge != ChallengeDNS:
		default:
			if p, err := c.CertificateProvider(cert); err == nil {
				used[p.ID] = true
				continue
			}
			for _, n := range cert.Names {
				if p, _, ok := c.ProviderFor(n); ok {
					used[p.ID] = true
				}
			}
		}
	}
	for _, r := range c.Services.DDNS.Records {
		if p, _, ok := c.ProviderFor(r.Name); ok {
			used[p.ID] = true
		}
	}
	return used
}

// keptZones are the empty zones that cannot go without the rest failing
// validation or changing what it means: all of them when no zone has an
// interface, those whose access lines are the only ones letting anything
// reach the proxy, and those protection names when none of its zones has
// an interface, since an empty list means every external zone.
func (c *Config) keptZones(zoned map[string]bool) map[string]bool {
	kept := map[string]bool{}
	if !slices.ContainsFunc(c.Zones, func(z Zone) bool { return zoned[z.Name] }) {
		for _, z := range c.Zones {
			kept[z.Name] = true
		}
		return kept
	}
	access := c.Services.Proxy.Access
	if c.ProxyEnabled() && !slices.ContainsFunc(access, func(a ProxyAccess) bool { return a.Opens() && zoned[a.Zone] }) {
		for _, a := range access {
			if a.Opens() {
				kept[a.Zone] = true
			}
		}
	}
	if protected := c.Protection.Zones; !slices.ContainsFunc(protected, func(z string) bool { return zoned[z] }) {
		for _, z := range protected {
			kept[z] = true
		}
	}
	return kept
}

// zoneTakes names what is written against a zone, which goes with it.
func (c *Config) zoneTakes(zone string) []string {
	var out []string
	for _, r := range c.Rules {
		if r.Zone == zone || r.DestZone == zone {
			out = append(out, "rule "+r.ID)
		}
	}
	for _, r := range c.NAT.Outbound.Rules {
		if r.Zone == zone {
			out = append(out, "outbound NAT "+r.ID)
		}
	}
	for _, pf := range c.NAT.PortForwards {
		if pf.Zone == zone {
			out = append(out, "port forward "+pf.ID)
		}
	}
	for _, o := range c.NAT.OneToOne {
		if o.Zone == zone {
			out = append(out, "1:1 NAT "+o.ID)
		}
	}
	if slices.Contains(c.Protection.Zones, zone) {
		out = append(out, "protection")
	}
	for _, a := range c.Services.Proxy.Access {
		if a.Zone == zone {
			out = append(out, "proxy access rule "+cmp.Or(a.Description, a.ID))
		}
	}
	return out
}

// leaseServed reports whether a static lease has an address in a network
// a DHCP server is written for, switched on or not. An IPv6 host part like
// ::20, or any IPv6 address on an interface whose prefix is learned, is
// taken to be: the prefix is only known at run time.
func (c *Config) leaseServed(l StaticLease) bool {
	if a, ok := leaseAddr(l, netip.IPv4Unspecified()); ok {
		for _, sc := range c.Services.DHCP.Servers {
			if in, found := c.Interface(sc.Interface); found && inNetwork(in.IPv4.Address, a) {
				return true
			}
		}
	}
	if l.IPv6 == "" {
		return false
	}
	a, full := leaseAddr(l, netip.IPv6Unspecified())
	for _, sc := range c.Services.DHCP.V6 {
		in, found := c.Interface(sc.Interface)
		switch {
		case !found:
		case !full || in.IPv6.Mode != AddrStatic, inNetwork(in.IPv6.Address, a):
			return true
		}
	}
	return false
}

// inNetwork reports whether a is in the network of an interface address
// written as a prefix, as a DHCP pool's range is checked.
func inNetwork(address string, a netip.Addr) bool {
	p, err := netip.ParsePrefix(address)
	return err == nil && p.Masked().Contains(a)
}

func (c *Config) disabled() []Disabled {
	var out []Disabled
	add := func(on bool, kind, id, name, path string) {
		if !on {
			out = append(out, Disabled{Kind: kind, ID: id, Name: name, Path: path})
		}
	}
	for _, r := range c.Rules {
		add(r.Enabled, "rule", r.ID, cmp.Or(r.Description, r.ID), diffPath("rules", r.ID))
	}
	for _, cr := range c.Crons {
		add(cr.Enabled, "cron", cr.ID, cmp.Or(cr.Description, cr.ID), diffPath("crons", cr.ID))
	}
	for _, in := range c.Interfaces {
		add(in.Enabled, "interface", in.Name, in.Name, diffPath("interfaces", in.Name))
		if in.WireGuard == nil {
			continue
		}
		for _, pr := range in.WireGuard.Peers {
			ref := PeerRef(in.Name, pr.Name)
			add(pr.Enabled, "wireguard-peer", ref, ref, diffPath(diffPath("interfaces", in.Name)+".wireguard.peers", pr.Name))
		}
	}
	for _, r := range c.Wireless.Radios {
		add(r.Enabled, "radio", r.Name, r.Name, diffPath("wireless.radios", r.Name))
	}
	for _, o := range c.NAT.OneToOne {
		add(o.Enabled, "one-to-one-nat", o.ID, cmp.Or(o.Description, o.ID), diffPath("nat.oneToOne", o.ID))
	}
	for _, r := range c.NAT.Outbound.Rules {
		add(r.Enabled, "outbound-nat", r.ID, cmp.Or(r.Description, r.ID), diffPath("nat.outbound.rules", r.ID))
	}
	for _, pf := range c.NAT.PortForwards {
		add(pf.Enabled, "port-forward", pf.ID, cmp.Or(pf.Description, pf.ID), diffPath("nat.portForwards", pf.ID))
	}
	for _, g := range c.Gateways {
		add(g.Enabled, "gateway", g.Name, g.Name, diffPath("gateways", g.Name))
	}
	for _, g := range c.GatewayGroups {
		add(g.Enabled, "gateway-group", g.Name, g.Name, diffPath("gatewayGroups", g.Name))
	}
	for _, r := range c.Routes {
		add(r.Enabled, "static-route", r.ID, cmp.Or(r.Description, r.ID), diffPath("routes", r.ID))
	}
	p := c.Services.Proxy
	for _, a := range p.Access {
		add(a.Enabled, "proxy-access", a.ID, cmp.Or(a.Description, a.ID), diffPath("services.proxy.access", a.ID))
	}
	for _, s := range p.Sites {
		add(s.Enabled, "proxy-site", s.ID, s.ID, diffPath("services.proxy.sites", s.ID))
	}
	for _, r := range p.Routes {
		add(r.Enabled, "proxy-route", r.ID, r.ID, diffPath("services.proxy.routes", r.ID))
	}
	for _, cert := range c.Certificates {
		add(cert.Enabled, "certificate", cert.ID, cert.ID, diffPath("certificates", cert.ID))
	}
	for _, r := range c.Services.DDNS.Records {
		add(r.Enabled, "ddns-record", r.ID, r.Name, diffPath("services.ddns.records", r.ID))
	}
	for _, l := range c.Blocking.Lists {
		add(l.Enabled, "block-list", l.Name, l.Name, diffPath("blocking.lists", l.Name))
	}
	slices.SortFunc(out, func(a, b Disabled) int { return byKindName(a.Kind, a.Name, a.ID, b.Kind, b.Name, b.ID) })
	return out
}

func byKindName(ak, an, ai, bk, bn, bi string) int {
	return cmp.Or(cmp.Compare(ak, bk), cmp.Compare(an, bn), cmp.Compare(ai, bi))
}

// diffPath is an entry's path as internal/diff writes it: the list's path
// and the entry's identity in brackets.
func diffPath(list, key string) string { return list + "[" + key + "]" }

func (c *Config) removeUnused(k UnusedKey) {
	id := k.ID
	switch k.Kind {
	case "alias":
		c.Aliases = without(c.Aliases, func(a Alias) bool { return a.Name == id })
	case "schedule":
		c.Schedules = without(c.Schedules, func(s Schedule) bool { return s.Name == id })
	case "pool":
		c.Services.Proxy.Pools = without(c.Services.Proxy.Pools, func(p ProxyPool) bool { return p.ID == id })
	case "waf-profile":
		c.Services.Proxy.Profiles = without(c.Services.Proxy.Profiles, func(w WAFProfile) bool { return w.ID == id })
	case "certificate":
		c.Certificates = without(c.Certificates, func(cert Certificate) bool { return cert.ID == id })
	case "acme-account":
		c.ACME.Accounts = without(c.ACME.Accounts, func(a ACMEAccount) bool { return a.ID == id })
	case "dns-provider":
		c.DNSProviders = without(c.DNSProviders, func(p DNSProvider) bool { return p.ID == id })
	case "gateway-group":
		c.GatewayGroups = without(c.GatewayGroups, func(g GatewayGroup) bool { return g.Name == id })
	case "gateway":
		c.Gateways = without(c.Gateways, func(g Gateway) bool { return g.Name == id })
	case "radio":
		c.Wireless.Radios = without(c.Wireless.Radios, func(r Radio) bool { return r.Name == id })
	case "static-lease":
		c.Services.DHCP.StaticLeases = without(c.Services.DHCP.StaticLeases, func(l StaticLease) bool { return l.MAC == id })
	case "zone":
		c.Zones = without(c.Zones, func(z Zone) bool { return z.Name == id })
		c.Rules = without(c.Rules, func(r Rule) bool { return r.Zone == id || r.DestZone == id })
		n := &c.NAT
		n.Outbound.Rules = without(n.Outbound.Rules, func(r OutboundRule) bool { return r.Zone == id })
		n.PortForwards = without(n.PortForwards, func(pf PortForward) bool { return pf.Zone == id })
		n.OneToOne = without(n.OneToOne, func(o OneToOneNAT) bool { return o.Zone == id })
		c.Protection.Zones = without(c.Protection.Zones, func(z string) bool { return z == id })
		c.Services.Proxy.Access = without(c.Services.Proxy.Access, func(a ProxyAccess) bool { return a.Zone == id })
	}
}

// without returns list less what drop matches, in a new slice so that a
// copy of the configuration taken before keeps its own.
func without[T any](list []T, drop func(T) bool) []T {
	if !slices.ContainsFunc(list, drop) {
		return list
	}
	out := make([]T, 0, len(list))
	for _, x := range list {
		if !drop(x) {
			out = append(out, x)
		}
	}
	return out
}
