package model

import (
	"fmt"
	"net/netip"
	"strings"
)

// DDNS is dynamic DNS: names kept at a DNS provider pointed at this
// router's own addresses.
type DDNS struct {
	Records []DDNSRecord `json:"records,omitempty"`
}

// DDNSRecord keeps a name's A record, its AAAA record, or both, at the
// provider holding the name's domain. Only the address is ever written:
// the TTL and anything else the provider keeps are left as they are, and
// nothing is deleted.
type DDNSRecord struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	// Name is the whole name, home.example.com.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Interface is whose public address is published. The name is not a
	// key: where the WAN has only a link-local address, the router's
	// global IPv6 address is on the LAN, so one name can take its A
	// record from one interface and its AAAA from another.
	Interface string `json:"interface"`
	// IPv4 keeps the A record, IPv6 the AAAA record.
	IPv4 bool `json:"ipv4,omitempty"`
	IPv6 bool `json:"ipv6,omitempty"`
}

// DDNSRecord returns the record with the given id.
func (c *Config) DDNSRecord(id string) (*DDNSRecord, bool) {
	for i := range c.Services.DDNS.Records {
		if c.Services.DDNS.Records[i].ID == id {
			return &c.Services.DDNS.Records[i], true
		}
	}
	return nil, false
}

// PublicAddress reports whether the internet could reach this router at
// ip: a global unicast address that is not private, unique local or
// carrier-grade NAT.
func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	switch {
	case !ip.IsGlobalUnicast(), ip.IsPrivate(), ip.IsLoopback(), ip.IsLinkLocalUnicast():
		return false
	case ip.Is4() && cgnat.Contains(ip):
		return false
	}
	return true
}

// cgnat is the space an ISP hands out behind its own NAT, which reaches
// nothing from outside.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// ddns checks the dynamic DNS records. A record on an interface that is
// off is not an error: it waits for an address, as a DHCP server waits
// for its interface.
func (v *validator) ddns(c *Config, ifaces map[string]bool) {
	ids := map[string]bool{}
	kept := map[string]string{}
	for i, r := range c.Services.DDNS.Records {
		path := fmt.Sprintf("services.ddns.records[%d]", i)
		v.id(path+".id", r.ID, ids)
		name := NormalizeDomain(r.Name)
		switch {
		case name == "":
			v.add(path+".name", "a name is needed here")
		case strings.Contains(name, "*"):
			v.add(path+".name", "%q is a wildcard; keep one name and point the others at it with a CNAME", r.Name)
		case strings.ContainsFunc(name, func(r rune) bool { return r > 127 }):
			v.add(path+".name", "%q has letters outside ASCII; write it in punycode (xn--)", r.Name)
		case len(name) > 253 || !domainRe.MatchString(name):
			v.add(path+".name", "%q is not a domain name", r.Name)
		default:
			v.ddnsProvider(path+".name", r.Name, c)
		}
		if !ifaces[r.Interface] {
			v.add(path+".interface", "unknown interface %q", r.Interface)
		}
		if !r.IPv4 && !r.IPv6 {
			v.add(path+".ipv4", "keep the A record, the AAAA record or both")
		}
		for _, f := range []struct {
			family string
			on     bool
		}{{"A", r.IPv4}, {"AAAA", r.IPv6}} {
			if !f.on || name == "" {
				continue
			}
			key := f.family + " " + name
			if other, taken := kept[key]; taken {
				v.add(path+".name", "%s already keeps the %s record for %s", other, f.family, r.Name)
			}
			kept[key] = r.ID
		}
	}
}

// ddnsProvider checks that some provider holds the name's domain and can
// keep a record there.
func (v *validator) ddnsProvider(path, name string, c *Config) {
	p, zone, ok := c.ProviderFor(name)
	if !ok {
		v.add(path, "no DNS provider holds %s; add its domain to one", name)
		return
	}
	if kind, known := ProviderKindOf(p.Kind); known && !kind.DynamicDNS {
		v.add(path, "%s holds %s, and %s cannot keep a dynamic DNS record (%s can)",
			p.ID, zone, kind.Label, DynamicDNSKinds())
	}
}
