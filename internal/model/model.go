// Package model defines Ostiole's declarative configuration: zones,
// interfaces, firewall rules, NAT, and routes. The model is the source of
// truth; nftables and network configuration are rendered from it.
package model

// SchemaVersion is bumped when the on-disk JSON shape changes incompatibly.
const SchemaVersion = 1

// Action is a rule verdict.
type Action string

// Rule verdicts.
const (
	ActionAccept Action = "accept"
	ActionDrop   Action = "drop"
	ActionReject Action = "reject"
)

// Protocol selects the transport protocol a rule matches.
type Protocol string

// Supported protocols. ProtocolICMP covers both ICMP and ICMPv6.
const (
	ProtocolAny    Protocol = "any"
	ProtocolTCP    Protocol = "tcp"
	ProtocolUDP    Protocol = "udp"
	ProtocolTCPUDP Protocol = "tcp+udp"
	ProtocolICMP   Protocol = "icmp"
)

// AddrMode says how an interface obtains an address for one family.
type AddrMode string

// Address modes. SLAAC applies to IPv6 only.
const (
	AddrNone   AddrMode = "none"
	AddrStatic AddrMode = "static"
	AddrDHCP   AddrMode = "dhcp"
	AddrSLAAC  AddrMode = "slaac"
)

// AliasType is the kind of entries an alias holds.
type AliasType string

// Alias kinds.
const (
	AliasHosts AliasType = "hosts" // IP addresses and CIDR networks
	AliasPorts AliasType = "ports" // ports and port ranges
)

// OutboundMode controls outbound NAT.
type OutboundMode string

// Outbound NAT modes.
const (
	OutboundAutomatic OutboundMode = "automatic" // masquerade IPv4 leaving every external zone
	OutboundManual    OutboundMode = "manual"    // only the listed rules
	OutboundDisabled  OutboundMode = "disabled"
)

// Config is the complete appliance configuration.
type Config struct {
	Version    int           `json:"version"`
	System     System        `json:"system"`
	Zones      []Zone        `json:"zones"`
	Interfaces []Interface   `json:"interfaces"`
	Aliases    []Alias       `json:"aliases,omitempty"`
	Rules      []Rule        `json:"rules"`
	NAT        NAT           `json:"nat"`
	Routes     []StaticRoute `json:"routes,omitempty"`
}

// System holds box-level settings.
type System struct {
	Hostname   string     `json:"hostname,omitempty"`
	DNSServers []string   `json:"dnsServers,omitempty"`
	Management Management `json:"management"`
}

// Management describes how the box itself is administered. The ports feed
// the anti-lockout rule on zones that have AntiLockout set. A zero port
// disables that entry.
type Management struct {
	WebPort         uint16 `json:"webPort"`
	SSHPort         uint16 `json:"sshPort"`
	LogDefaultDrops bool   `json:"logDefaultDrops,omitempty"`
}

// Zone groups interfaces that share a security policy, like pfSense
// interface groups or firewalld zones.
type Zone struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// External marks WAN-like zones: automatic outbound NAT masquerades
	// IPv4 traffic leaving them.
	External bool `json:"external,omitempty"`
	// AntiLockout always allows the management ports from this zone so a
	// bad rule cannot lock the admin out.
	AntiLockout bool `json:"antiLockout,omitempty"`
	// LogDrops logs packets that reach the end of this zone's rules.
	LogDrops bool `json:"logDrops,omitempty"`
}

// Interface is a network interface Ostiole manages.
type Interface struct {
	Name        string `json:"name"`
	Zone        string `json:"zone,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	IPv4        IPv4   `json:"ipv4"`
	IPv6        IPv6   `json:"ipv6"`
	VLAN        *VLAN  `json:"vlan,omitempty"`
	MTU         int    `json:"mtu,omitempty"`
}

// IPv4 addressing for an interface. Address is CIDR notation.
type IPv4 struct {
	Mode    AddrMode `json:"mode"`
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
}

// IPv6 addressing for an interface. Address is CIDR notation.
type IPv6 struct {
	Mode    AddrMode `json:"mode"`
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
}

// VLAN makes the interface an 802.1Q sub-interface of Parent.
type VLAN struct {
	Parent string `json:"parent"`
	ID     uint16 `json:"id"`
}

// Alias is a named, reusable list of hosts/networks or ports. Host aliases
// become nftables sets; port aliases become inet_service sets.
type Alias struct {
	Name        string    `json:"name"`
	Type        AliasType `json:"type"`
	Description string    `json:"description,omitempty"`
	Entries     []string  `json:"entries"`
}

// Rule is a filter rule evaluated for traffic entering Zone, whether it is
// addressed to the firewall itself or forwarded through it (pfSense
// semantics). DestZone restricts a rule to forwarded traffic leaving that
// zone.
type Rule struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Zone        string   `json:"zone"`
	DestZone    string   `json:"destZone,omitempty"`
	Action      Action   `json:"action"`
	Protocol    Protocol `json:"protocol"`
	Source      Endpoint `json:"source"`
	Destination Endpoint `json:"destination"`
	Log         bool     `json:"log,omitempty"`
}

// Endpoint constrains one side of a rule. Empty means any. Addresses and
// Alias are mutually exclusive, as are Ports and PortAlias. Self matches
// the firewall's own addresses and is valid for destinations only.
type Endpoint struct {
	Addresses []string `json:"addresses,omitempty"`
	Alias     string   `json:"alias,omitempty"`
	Ports     []string `json:"ports,omitempty"`
	PortAlias string   `json:"portAlias,omitempty"`
	Self      bool     `json:"self,omitempty"`
}

// NAT holds outbound NAT and port forwards.
type NAT struct {
	Outbound     OutboundNAT   `json:"outbound"`
	PortForwards []PortForward `json:"portForwards,omitempty"`
}

// OutboundNAT configures source NAT for traffic leaving external zones.
type OutboundNAT struct {
	Mode  OutboundMode   `json:"mode"`
	Rules []OutboundRule `json:"rules,omitempty"`
}

// OutboundRule masquerades traffic leaving Zone, optionally only from the
// listed source addresses.
type OutboundRule struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Zone        string   `json:"zone"`
	Source      []string `json:"source,omitempty"`
}

// PortForward redirects traffic arriving in Zone on Ports to Target. An
// empty TargetPort keeps the original destination port.
type PortForward struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Zone        string   `json:"zone"`
	Protocol    Protocol `json:"protocol"`
	Ports       []string `json:"ports"`
	Target      string   `json:"target"`
	TargetPort  string   `json:"targetPort,omitempty"`
}

// StaticRoute sends Destination via Gateway, optionally pinned to Interface.
type StaticRoute struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface,omitempty"`
}

// Zone returns the zone with the given name.
func (c *Config) Zone(name string) (*Zone, bool) {
	for i := range c.Zones {
		if c.Zones[i].Name == name {
			return &c.Zones[i], true
		}
	}
	return nil, false
}

// Alias returns the alias with the given name.
func (c *Config) Alias(name string) (*Alias, bool) {
	for i := range c.Aliases {
		if c.Aliases[i].Name == name {
			return &c.Aliases[i], true
		}
	}
	return nil, false
}

// Interface returns the interface with the given name.
func (c *Config) Interface(name string) (*Interface, bool) {
	for i := range c.Interfaces {
		if c.Interfaces[i].Name == name {
			return &c.Interfaces[i], true
		}
	}
	return nil, false
}

// ZoneInterfaces returns the names of enabled interfaces assigned to zone,
// in configuration order.
func (c *Config) ZoneInterfaces(zone string) []string {
	var names []string
	for _, in := range c.Interfaces {
		if in.Enabled && in.Zone == zone {
			names = append(names, in.Name)
		}
	}
	return names
}
