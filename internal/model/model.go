// Package model defines Ostiole's declarative configuration: zones,
// interfaces, firewall rules, NAT, and routes. The model is the source of
// truth; nftables and network configuration are rendered from it.
package model

import "sort"

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

// Address modes. SLAAC and delegated apply to IPv6 only.
const (
	AddrNone   AddrMode = "none"
	AddrStatic AddrMode = "static"
	AddrDHCP   AddrMode = "dhcp"
	AddrSLAAC  AddrMode = "slaac"
	// AddrDelegated takes a /64 out of a prefix another interface was
	// delegated, which is how an ISP hands out addressable space for the
	// networks behind the router.
	AddrDelegated AddrMode = "delegated"
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
	Version    int         `json:"version"`
	System     System      `json:"system"`
	Zones      []Zone      `json:"zones"`
	Interfaces []Interface `json:"interfaces"`
	Aliases    []Alias     `json:"aliases,omitempty"`
	Schedules  []Schedule  `json:"schedules,omitempty"`
	Rules      []Rule      `json:"rules"`
	NAT        NAT         `json:"nat"`
	Gateways   []Gateway   `json:"gateways,omitempty"`
	// GatewayGroups combine gateways into one target rules can route
	// through, with failover between tiers.
	GatewayGroups []GatewayGroup `json:"gatewayGroups,omitempty"`
	Routes        []StaticRoute  `json:"routes,omitempty"`
	Services      Services       `json:"services"`
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
	// WireGuard turns this interface into a VPN tunnel. It is exclusive
	// with VLAN, and the interface is created by Ostiole rather than found
	// on the hardware.
	WireGuard *WireGuard `json:"wireguard,omitempty"`
	// Bridge makes this interface a software switch over its members.
	Bridge *Bridge `json:"bridge,omitempty"`
	// Bond joins several links into one.
	Bond *Bond `json:"bond,omitempty"`
	MTU  int   `json:"mtu,omitempty"`
}

// Kind names what an interface is made of, for the UI and for messages.
type Kind string

// Interface kinds.
const (
	KindPhysical  Kind = "physical"
	KindVLAN      Kind = "vlan"
	KindBridge    Kind = "bridge"
	KindBond      Kind = "bond"
	KindWireGuard Kind = "wireguard"
)

// Kind reports what this interface is.
func (i Interface) Kind() Kind {
	switch {
	case i.VLAN != nil:
		return KindVLAN
	case i.Bridge != nil:
		return KindBridge
	case i.Bond != nil:
		return KindBond
	case i.WireGuard != nil:
		return KindWireGuard
	}
	return KindPhysical
}

// Members lists the interfaces this one is built from, if any.
func (i Interface) Members() []string {
	switch {
	case i.Bridge != nil:
		return i.Bridge.Members
	case i.Bond != nil:
		return i.Bond.Members
	}
	return nil
}

// Bridge turns the interface into a software switch. Its members carry no
// addresses of their own: the bridge holds them for the whole segment.
type Bridge struct {
	Members []string `json:"members"`
	// STP stops a cabling loop from taking the network down. It costs a
	// few seconds of silence whenever a port comes up, so it is off by
	// default on an appliance where the ports are known.
	STP bool `json:"stp,omitempty"`
	// VLANFiltering lets the bridge keep VLANs apart rather than flooding
	// every tagged frame to every port.
	VLANFiltering bool `json:"vlanFiltering,omitempty"`
}

// BondMode is how a bond spreads traffic over its members.
type BondMode string

// Bond modes, named as the kernel names them.
const (
	// BondActiveBackup uses one member and keeps the rest in reserve. It
	// needs nothing from the switch, which makes it the safe choice.
	BondActiveBackup BondMode = "active-backup"
	// BondLACP negotiates with the switch (802.3ad) and needs it configured
	// to match.
	BondLACP       BondMode = "802.3ad"
	BondRoundRobin BondMode = "balance-rr"
	BondXOR        BondMode = "balance-xor"
	BondBroadcast  BondMode = "broadcast"
	BondTLB        BondMode = "balance-tlb"
	BondALB        BondMode = "balance-alb"
)

// BondModes lists every mode, in the order the UI offers them.
var BondModes = []BondMode{
	BondActiveBackup, BondLACP, BondRoundRobin, BondXOR, BondBroadcast, BondTLB, BondALB,
}

// Bond joins several links into one, for throughput or for redundancy.
type Bond struct {
	Members []string `json:"members"`
	Mode    BondMode `json:"mode"`
	// MIIMonitorMS is how often member links are checked for carrier.
	// Zero leaves the kernel default, which is no monitoring at all, so
	// the UI suggests 100.
	MIIMonitorMS int `json:"miiMonitorMs,omitempty"`
	// TransmitHashPolicy decides which member a flow takes in the
	// balancing modes; it is ignored by the others.
	TransmitHashPolicy string `json:"transmitHashPolicy,omitempty"`
	// Primary is the member active-backup prefers while it is up.
	Primary string `json:"primary,omitempty"`
	// LACPRate is "slow" or "fast" and only applies to 802.3ad.
	LACPRate string `json:"lacpRate,omitempty"`
}

// HashPolicies are the transmit hash policies networkd accepts.
var HashPolicies = []string{"layer2", "layer2+3", "layer3+4", "encap2+3", "encap3+4"}

// MasterOf maps each enslaved interface to the bridge or bond that owns
// it. A member belongs to at most one, which validation enforces.
func (c *Config) MasterOf() map[string]string {
	out := map[string]string{}
	for _, in := range c.Interfaces {
		for _, m := range in.Members() {
			out[m] = in.Name
		}
	}
	return out
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
	// PrefixHint asks the upstream to delegate a prefix of this size,
	// written like "::/56". It belongs on a WAN in dhcp mode; the
	// interfaces behind it then take their own /64 out of it.
	PrefixHint string `json:"prefixHint,omitempty"`
	// DelegatedFrom names the interface that requested the prefix this one
	// takes a subnet of. It is required in delegated mode.
	DelegatedFrom string `json:"delegatedFrom,omitempty"`
	// SubnetID picks which /64 of the delegated prefix to use. Each
	// interface behind one upstream needs its own.
	SubnetID int `json:"subnetId,omitempty"`
}

// VLAN makes the interface an 802.1Q sub-interface of Parent.
type VLAN struct {
	Parent string `json:"parent"`
	ID     uint16 `json:"id"`
}

// WireGuard configures a tunnel interface. Its addresses, zone, and
// firewall rules come from the interface it belongs to, like any other
// link.
type WireGuard struct {
	// PrivateKey is this firewall's key, base64 as wg(8) prints it.
	PrivateKey string `json:"privateKey"`
	// PublicKey is what peers must configure. It is derived from the
	// private key and stored so the UI can show it.
	PublicKey string `json:"publicKey,omitempty"`
	// ListenPort accepts incoming tunnels; 0 picks a random source port
	// and accepts nothing, which suits a client-only tunnel.
	ListenPort uint16          `json:"listenPort,omitempty"`
	Peers      []WireGuardPeer `json:"peers,omitempty"`
}

// WireGuardPeer is one other end of a tunnel.
type WireGuardPeer struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	PublicKey   string `json:"publicKey"`
	// PresharedKey adds a symmetric layer, which is what keeps the tunnel
	// safe from a future quantum attacker.
	PresharedKey string `json:"presharedKey,omitempty"`
	// AllowedIPs are the addresses this peer may use and that are routed
	// to it.
	AllowedIPs []string `json:"allowedIps"`
	// Endpoint is host:port for peers this firewall dials; leave it empty
	// for peers that connect inwards.
	Endpoint string `json:"endpoint,omitempty"`
	// Keepalive in seconds keeps a NAT binding open, usually 25.
	Keepalive int `json:"keepalive,omitempty"`
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
	// Schedule names a Schedule; outside it the rule does not match and
	// evaluation carries on with the next one.
	Schedule string `json:"schedule,omitempty"`
	// Gateway sends matching traffic through a named gateway or gateway
	// group instead of the default route. It only makes sense on an accept
	// rule, and cannot be combined with DestZone: the outgoing interface is
	// not known yet when the routing decision is made.
	Gateway string `json:"gateway,omitempty"`
}

// Schedule is a recurring window in the firewall's local time. Rules that
// name it only match inside it.
type Schedule struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Days of the week, lower case ("monday"); empty means every day.
	Days []string `json:"days,omitempty"`
	// Start and End are "HH:MM". An End before Start means the window runs
	// over midnight.
	Start string `json:"start"`
	End   string `json:"end"`
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

// NAT holds outbound NAT, port forwards, and 1:1 mappings.
type NAT struct {
	Outbound     OutboundNAT   `json:"outbound"`
	PortForwards []PortForward `json:"portForwards,omitempty"`
	OneToOne     []OneToOneNAT `json:"oneToOne,omitempty"`
}

// OneToOneNAT maps one external address onto one internal host, both
// directions: traffic to External is translated to Internal, and traffic
// the host sends out of Zone leaves as External.
type OneToOneNAT struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Zone is the external zone the address belongs to.
	Zone     string `json:"zone"`
	External string `json:"external"`
	Internal string `json:"internal"`
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
	// Reflection forwards connections that internal hosts make to the
	// firewall's own outside address, so one name works from both sides.
	Reflection bool `json:"reflection,omitempty"`
}

// Gateway is an upstream this firewall routes through. Several gateways
// make a multi-WAN box: the lowest priority that answers its monitor
// carries the default route, and the rest wait.
type Gateway struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Interface   string `json:"interface"`
	// Address is the next hop. Empty means whatever DHCP or a router
	// advertisement gives the interface, which is the usual WAN case.
	Address string `json:"address,omitempty"`
	// Priority orders failover, lowest first. Equal priorities are left
	// to the kernel, which spreads traffic over them.
	Priority int `json:"priority,omitempty"`
	// Monitor is the address probed to decide whether this gateway works.
	// Empty means the gateway address itself, which only tells you the
	// first hop is alive.
	Monitor string `json:"monitor,omitempty"`
}

// GatewayMetric turns a priority into a route metric. The gaps leave room
// to demote a gateway that fails its monitor without colliding with the
// next one.
func (g Gateway) GatewayMetric() int {
	p := g.Priority
	if p < 0 {
		p = 0
	}
	return 10 + p*10
}

// GatewayGroup is several gateways used as one policy routing target. The
// lowest tier that has an online member carries the traffic; members that
// share a tier are used together and the kernel spreads connections over
// them.
type GatewayGroup struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Enabled     bool            `json:"enabled"`
	Members     []GatewayMember `json:"members"`
	// OnDown decides what happens when no member answers its monitor.
	OnDown OnDownMode `json:"onDown,omitempty"`
}

// GatewayMember is one gateway in a group.
type GatewayMember struct {
	Gateway string `json:"gateway"`
	// Tier orders failover, lowest first.
	Tier int `json:"tier,omitempty"`
}

// OnDownMode is what a group does when every member is down.
type OnDownMode string

// Group behaviour when nothing is online.
const (
	// OnDownFallback sends the traffic out the ordinary default route,
	// which keeps a box working when its preferred line dies.
	OnDownFallback OnDownMode = "fallback"
	// OnDownBlock drops it instead, so a tunnel that is meant to carry
	// everything cannot leak onto the WAN while it is down.
	OnDownBlock OnDownMode = "block"
)

// Policy routing numbering. Marks occupy bits 16-23, clear of the low
// values other software puts in the packet mark, and table ids sit above
// the classic 8-bit ones.
const (
	PolicyMarkShift = 16
	PolicyMarkMask  = 0xff << PolicyMarkShift
	PolicyTableBase = 2200
	// MaxPolicyTargets is how many gateways and groups can be marked; the
	// mark has one byte for them.
	MaxPolicyTargets = 255
)

// PolicyTarget is a gateway or gateway group that rules can route through.
// Its mark and routing table are derived from the configuration, so the
// firewall and the routing tables agree without sharing state.
type PolicyTarget struct {
	Name  string `json:"name"`
	Group bool   `json:"group,omitempty"`
	Mark  uint32 `json:"mark"`
	Table int    `json:"table"`
}

// PolicyTargets lists every enabled gateway and gateway group, sorted by
// name and numbered from one.
func (c *Config) PolicyTargets() []PolicyTarget {
	names := make([]string, 0, len(c.Gateways)+len(c.GatewayGroups))
	group := make(map[string]bool, len(c.GatewayGroups))
	for _, g := range c.Gateways {
		if g.Enabled {
			names = append(names, g.Name)
		}
	}
	for _, g := range c.GatewayGroups {
		if g.Enabled {
			names = append(names, g.Name)
			group[g.Name] = true
		}
	}
	sort.Strings(names)
	if len(names) > MaxPolicyTargets {
		names = names[:MaxPolicyTargets]
	}
	out := make([]PolicyTarget, 0, len(names))
	for i, name := range names {
		out = append(out, PolicyTarget{
			Name:  name,
			Group: group[name],
			Mark:  uint32(i+1) << PolicyMarkShift,
			Table: PolicyTableBase + i + 1,
		})
	}
	return out
}

// PolicyTarget returns the marking assigned to a gateway or group name.
func (c *Config) PolicyTarget(name string) (PolicyTarget, bool) {
	for _, t := range c.PolicyTargets() {
		if t.Name == name {
			return t, true
		}
	}
	return PolicyTarget{}, false
}

// Gateway returns the gateway with the given name.
func (c *Config) Gateway(name string) (*Gateway, bool) {
	for i := range c.Gateways {
		if c.Gateways[i].Name == name {
			return &c.Gateways[i], true
		}
	}
	return nil, false
}

// GatewayGroup returns the group with the given name.
func (c *Config) GatewayGroup(name string) (*GatewayGroup, bool) {
	for i := range c.GatewayGroups {
		if c.GatewayGroups[i].Name == name {
			return &c.GatewayGroups[i], true
		}
	}
	return nil, false
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

// Schedule returns the schedule with the given name.
func (c *Config) Schedule(name string) (*Schedule, bool) {
	for i := range c.Schedules {
		if c.Schedules[i].Name == name {
			return &c.Schedules[i], true
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

// Services are the LAN-side services Ostiole runs through dnsmasq.
type Services struct {
	DHCP DHCPServer `json:"dhcp"`
	DNS  DNSServer  `json:"dns"`
}

// DHCPServer hands out IPv4 addresses on selected interfaces, and with V6
// also advertises IPv6 prefixes and serves DHCPv6.
type DHCPServer struct {
	Enabled      bool          `json:"enabled"`
	Scopes       []DHCPScope   `json:"scopes,omitempty"`
	StaticLeases []StaticLease `json:"staticLeases,omitempty"`
	V6           []DHCPv6Scope `json:"v6,omitempty"`
}

// RAMode says how hosts on an interface configure IPv6.
type RAMode string

// Router advertisement modes.
const (
	// RASLAAC advertises the prefix and lets hosts pick their own address.
	RASLAAC RAMode = "slaac"
	// RAStateless keeps SLAAC addressing but answers DHCPv6 information
	// requests, which is how clients learn DNS servers and the domain.
	RAStateless RAMode = "stateless"
	// RAManaged hands out addresses from a pool over DHCPv6.
	RAManaged RAMode = "managed"
)

// DHCPv6Scope serves IPv6 on one interface. The prefix is taken from the
// interface at run time, so it follows a static address, SLAAC, or a
// delegated prefix without being repeated here.
type DHCPv6Scope struct {
	Interface string `json:"interface"`
	Enabled   bool   `json:"enabled"`
	Mode      RAMode `json:"mode"`
	// RangeStart and RangeEnd are host parts of that prefix, written like
	// ::100 and ::1ff. Required when Mode is managed, ignored otherwise.
	RangeStart string `json:"rangeStart,omitempty"`
	RangeEnd   string `json:"rangeEnd,omitempty"`
	// LeaseTime is also the lifetime advertised in router advertisements.
	LeaseTime string   `json:"leaseTime,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	Domain    string   `json:"domain,omitempty"`
}

// DHCPScope is a pool on one interface, which must carry a static IPv4
// address. Empty Gateway and DNS default to this box's address on the
// interface (DNS only when the DNS service is enabled; otherwise the
// system DNS servers).
type DHCPScope struct {
	Interface  string   `json:"interface"`
	Enabled    bool     `json:"enabled"`
	RangeStart string   `json:"rangeStart"`
	RangeEnd   string   `json:"rangeEnd"`
	LeaseTime  string   `json:"leaseTime,omitempty"` // dnsmasq syntax: 12h, 2d, infinite
	Gateway    string   `json:"gateway,omitempty"`
	DNS        []string `json:"dns,omitempty"`
	Domain     string   `json:"domain,omitempty"`
}

// StaticLease pins addresses to a MAC. IPv6 may be a host part of the
// interface's prefix, written like ::20, and only reaches clients whose
// DHCPv6 identifier the server can tie to the MAC.
type StaticLease struct {
	MAC         string `json:"mac"`
	IP          string `json:"ip,omitempty"`
	IPv6        string `json:"ipv6,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Description string `json:"description,omitempty"`
}

// DNSServer answers local names and forwards the rest upstream.
type DNSServer struct {
	Enabled bool `json:"enabled"`
	// Interfaces to listen on; empty means every interface that is not in
	// an external zone.
	Interfaces    []string       `json:"interfaces,omitempty"`
	Upstreams     []string       `json:"upstreams,omitempty"`
	Domain        string         `json:"domain,omitempty"`
	HostOverrides []HostOverride `json:"hostOverrides,omitempty"`
	// Resolver decides who answers names this box does not know.
	Resolver ResolverMode `json:"resolver,omitempty"`
	// TLSUpstreams are the resolvers used in ResolverTLS mode.
	TLSUpstreams []TLSUpstream `json:"tlsUpstreams,omitempty"`
}

// ResolverMode selects how queries leave the box.
type ResolverMode string

// Resolver modes. Anything but ResolverForward runs unbound as a
// validating resolver behind dnsmasq.
const (
	// ResolverForward sends queries straight to Upstreams, in the clear.
	// It is the default because it needs nothing but dnsmasq.
	ResolverForward ResolverMode = "forward"
	// ResolverValidate resolves from the root servers and checks DNSSEC.
	ResolverValidate ResolverMode = "validate"
	// ResolverTLS forwards to TLSUpstreams over DNS over TLS and checks
	// DNSSEC.
	ResolverTLS ResolverMode = "tls"
)

// TLSUpstream is one DNS over TLS server. Hostname is the name its
// certificate must carry, without which the connection is not private.
type TLSUpstream struct {
	Address  string `json:"address"`
	Hostname string `json:"hostname"`
}

// HostOverride is a local name answered by this box.
type HostOverride struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Description string `json:"description,omitempty"`
}
