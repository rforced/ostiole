package model

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/rforced/ostiole/internal/timezone"
)

// StarterOptions parameterise Starter.
type StarterOptions struct {
	Hostname   string
	LAN        string // interface name, required
	LANAddress string // CIDR, e.g. 192.168.1.1/24
	WAN        string // interface name, optional
	// ManagementFromWAN also allows the management ports from the wan
	// zone, for routers administered over their public side. It is done
	// with ordinary rules rather than the zone's anti-lockout, so they show
	// up in the rule list and can be narrowed or deleted later.
	ManagementFromWAN bool
	// Services turns on DHCP and DNS for the LAN with a pool derived from
	// LANAddress and the given upstream resolvers.
	Services     bool
	DNSUpstreams []string
	// SSHPasswords allows a password at the SSH prompt. It starts as the
	// router has it, so writing a first configuration never changes how
	// the person writing it gets back in.
	SSHPasswords bool
}

// Starter returns a sane first configuration: a lan zone with anti-lockout
// and an allow-all rule, an external wan zone with DHCP, automatic outbound
// NAT, and management on 443/22. It is what `ostiole init` writes.
func Starter(o StarterOptions) *Config {
	cfg := &Config{
		Version: SchemaVersion,
		System: System{
			Hostname:   o.Hostname,
			Timezone:   timezone.Default,
			Management: Management{WebPort: 443, SSHPort: 22, SSHPasswords: o.SSHPasswords},
		},
		Zones: []Zone{
			{Name: "wan", Description: "Internet", External: true},
			{Name: "lan", Description: "Local network", AntiLockout: true},
		},
		Rules: []Rule{
			{
				ID:          "allow-lan",
				Description: "Allow LAN to any",
				Enabled:     true,
				Zone:        "lan",
				Action:      ActionAccept,
				Protocol:    ProtocolAny,
			},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
	}
	if o.LAN != "" {
		cfg.Interfaces = append(cfg.Interfaces, Interface{
			Name:    o.LAN,
			Zone:    "lan",
			Enabled: true,
			IPv4:    IPv4{Mode: AddrStatic, Address: o.LANAddress},
			IPv6:    IPv6{Mode: AddrNone},
		})
	}
	if o.ManagementFromWAN {
		cfg.Rules = append(cfg.Rules, ManagementRules("wan", cfg.System.Management)...)
	}
	if o.WAN != "" {
		cfg.Interfaces = append(cfg.Interfaces, Interface{
			Name:    o.WAN,
			Zone:    "wan",
			Enabled: true,
			IPv4:    IPv4{Mode: AddrDHCP},
			IPv6:    IPv6{Mode: AddrSLAAC},
		})
	}
	if o.Services && o.LAN != "" {
		if start, end, ok := DefaultPool(o.LANAddress); ok {
			cfg.Services.DHCP = DHCPService{Enabled: true, Servers: []DHCPServer{{
				Interface: o.LAN, Enabled: true, RangeStart: start, RangeEnd: end, LeaseTime: "12h",
			}}}
		}
		upstreams := o.DNSUpstreams
		if len(upstreams) == 0 {
			upstreams = []string{"1.1.1.1", "9.9.9.9"}
		}
		cfg.Services.DNS = DNSServer{Enabled: true, Upstreams: upstreams, Domain: "lan"}
	}
	return cfg
}

// DefaultPool picks a DHCP range inside the interface's subnet: hosts 100
// to 199 when the subnet is a /24 or larger, else the upper half minus the
// last address. It fails for subnets too small to be useful.
func DefaultPool(cidr string) (start, end string, ok bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || !p.Addr().Is4() || p.Bits() > 29 {
		return "", "", false
	}
	base := p.Masked().Addr().As4()
	size := uint32(1) << (32 - p.Bits())
	first := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	var lo, hi uint32
	if size >= 256 {
		lo, hi = first+100, first+199
	} else {
		lo, hi = first+size/2, first+size-2
	}
	self := p.Addr().As4()
	selfN := uint32(self[0])<<24 | uint32(self[1])<<16 | uint32(self[2])<<8 | uint32(self[3])
	if selfN >= lo && selfN <= hi {
		return "", "", false
	}
	toIP := func(n uint32) string {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], n)
		return netip.AddrFrom4(b).String()
	}
	return toIP(lo), toIP(hi), true
}

// ManagementRules are the rules that let the web UI and SSH in from a
// zone, one per port, as the setup wizard and the management page add
// them. They are ordinary rules on purpose: anti-lockout is a system rule
// nobody can edit, which is right for the LAN and wrong for the public
// side, where an operator may well want to narrow it to an address or take
// it away once a VPN is up.
func ManagementRules(zone string, m Management) []Rule {
	var rules []Rule
	add := func(id, what string, port uint16) {
		if port == 0 {
			return
		}
		rules = append(rules, Rule{
			ID:          id + "-from-" + zone,
			Description: what + " from " + zone,
			Enabled:     true,
			Zone:        zone,
			Action:      ActionAccept,
			Protocol:    ProtocolTCP,
			Destination: Endpoint{Self: true, Ports: []string{fmt.Sprint(port)}},
		})
	}
	add("web-ui", "Web UI", m.WebPort)
	if m.SSHPort != m.WebPort {
		add("ssh", "SSH", m.SSHPort)
	}
	return rules
}
