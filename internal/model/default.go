package model

// StarterOptions parameterise Starter.
type StarterOptions struct {
	Hostname   string
	LAN        string // interface name, required
	LANAddress string // CIDR, e.g. 192.168.1.1/24
	WAN        string // interface name, optional
}

// Starter returns a sane first configuration: a lan zone with anti-lockout
// and an allow-all rule, an external wan zone with DHCP, automatic outbound
// NAT, and management on 443/22. It is what `ostiole init` writes.
func Starter(o StarterOptions) *Config {
	cfg := &Config{
		Version: SchemaVersion,
		System: System{
			Hostname:   o.Hostname,
			Management: Management{WebPort: 443, SSHPort: 22},
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
	if o.WAN != "" {
		cfg.Interfaces = append(cfg.Interfaces, Interface{
			Name:    o.WAN,
			Zone:    "wan",
			Enabled: true,
			IPv4:    IPv4{Mode: AddrDHCP},
			IPv6:    IPv6{Mode: AddrSLAAC},
		})
	}
	return cfg
}
