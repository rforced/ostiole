package model

import (
	"encoding/binary"
	"net/netip"

	"github.com/rforced/ostiole/internal/timezone"
)

// StarterOptions parameterise Starter.
type StarterOptions struct {
	Hostname   string
	LAN        string // interface name, required
	LANAddress string // CIDR, e.g. 192.168.1.1/24
	WAN        string // interface name, optional
	// ManagementFromWAN turns the wan zone's anti-lockout on, for a router
	// administered over its public side. It is the same switch the zone
	// carries, so it is taken back the same way: untick anti-lockout on
	// the zone.
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
			{Name: "wan", Description: "Internet", External: true, AntiLockout: o.ManagementFromWAN},
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
				Interface: o.LAN, Enabled: true, RangeStart: start, RangeEnd: end, LeaseTime: DefaultLeaseTime,
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
