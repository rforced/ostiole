// Package model defines Ostiole's declarative configuration: zones,
// interfaces, firewall rules, NAT, and routes. The model is the source of
// truth; nftables and network configuration are rendered from it.
package model

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/timezone"
)

// SchemaVersion is bumped when the on-disk JSON shape changes incompatibly.
// Version 2 renamed a cron's "job" field to "kind"; version 3 renamed DHCP
// "scopes" to "servers"; version 4 split each update source's "schedule"
// into "checkSchedule" and "installSchedule". An older file needs each
// changed by hand before this build will load it.
const SchemaVersion = 5

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
	// AddrPPP takes the address from the other end of a dialled session.
	AddrPPP AddrMode = "ppp"
)

// AliasType is the kind of entries an alias holds.
type AliasType string

// Alias kinds.
const (
	AliasHosts AliasType = "hosts" // IP addresses and CIDR networks
	AliasPorts AliasType = "ports" // ports and port ranges
	// AliasGeoIP holds ISO country codes; the addresses behind them are
	// fetched, because nobody maintains that list by hand.
	AliasGeoIP AliasType = "geoip"
)

// OutboundMode controls outbound NAT.
type OutboundMode string

// Outbound NAT modes.
const (
	// OutboundAutomatic masquerades IPv4 leaving every external zone.
	OutboundAutomatic OutboundMode = "automatic"
	// OutboundHybrid applies the listed rules first and then the automatic
	// ones, which is how one host is given a different address, or kept
	// out of NAT, without writing out the rules for everything else.
	OutboundHybrid OutboundMode = "hybrid"
	// OutboundManual applies only the listed rules.
	OutboundManual OutboundMode = "manual"
	// OutboundDisabled translates nothing.
	OutboundDisabled OutboundMode = "disabled"
)

// OutboundModes lists them in the order the UI offers them.
var OutboundModes = []OutboundMode{OutboundAutomatic, OutboundHybrid, OutboundManual, OutboundDisabled}

// Config is the complete appliance configuration.
type Config struct {
	Version    int         `json:"version"`
	System     System      `json:"system"`
	Zones      []Zone      `json:"zones"`
	Interfaces []Interface `json:"interfaces"`
	Aliases    []Alias     `json:"aliases,omitempty"`
	Schedules  []Schedule  `json:"schedules,omitempty"`
	Rules      []Rule      `json:"rules"`
	// Protection is the edge defence: what a zone does about traffic that
	// is too much of itself rather than against the rules.
	Protection Protection `json:"protection,omitzero"`
	NAT        NAT        `json:"nat"`
	Gateways   []Gateway  `json:"gateways,omitempty"`
	// GatewayGroups combine gateways into one target rules can route
	// through, with failover between tiers.
	GatewayGroups []GatewayGroup `json:"gatewayGroups,omitempty"`
	Routes        []StaticRoute  `json:"routes,omitempty"`
	Services      Services       `json:"services"`
	// Blocking is DNS blocking: the lists of names this router refuses to
	// resolve, and what it does to stop a client going around it.
	Blocking Blocking `json:"blocking,omitempty"`
	// Crons are the work this router runs on a schedule of the operator's
	// choosing, alongside the work Ostiole does on its own account.
	Crons []Cron `json:"crons,omitempty"`
	// Updates is how this router keeps itself patched: the distro packages
	// underneath it and Ostiole's own releases.
	Updates Updates `json:"updates,omitempty"`
	// Wireless is the radios this router has. The networks they serve are
	// interfaces like any other.
	Wireless Wireless `json:"wireless,omitzero"`
}

// CronKind is what a scheduled cron does.
type CronKind string

// Cron kinds. Each one is something an appliance genuinely needs doing on
// a timer; Command is the escape hatch for everything else.
const (
	// CronBackup writes a configuration backup into a directory and keeps
	// the last few, which is the backup nobody remembers to take.
	CronBackup CronKind = "backup"
	// CronRefreshAliases fetches the address lists and country ranges now.
	CronRefreshAliases CronKind = "refresh-aliases"
	// CronRefreshBlocklists fetches the DNS blocklists now. It is separate
	// from the aliases: one is addresses for the firewall, the other names
	// for the resolver, and they come from different publishers on
	// different schedules.
	CronRefreshBlocklists CronKind = "refresh-blocklists"
	// CronRestartService restarts one of the services Ostiole runs.
	CronRestartService CronKind = "restart-service"
	// CronCommand runs a command line. It is as powerful as the router, which
	// is the point and also the warning.
	CronCommand CronKind = "command"
	// CronSystemUpdate checks the distro package manager and installs what
	// the update mode allows.
	CronSystemUpdate CronKind = "system-update"
	// CronOstioleUpdate does the same for Ostiole's own releases.
	CronOstioleUpdate CronKind = "ostiole-update"
	// CronSystemUpdateCheck asks the distro package manager what is
	// waiting and installs none of it, so the answer on the page is a day
	// old at worst whatever the mode is.
	CronSystemUpdateCheck CronKind = "system-update-check"
	// CronOstioleUpdateCheck does the same for Ostiole's own releases.
	CronOstioleUpdateCheck CronKind = "ostiole-update-check"
)

// CronKinds lists them in the order the UI offers them.
var CronKinds = []CronKind{
	CronBackup, CronRefreshAliases, CronRefreshBlocklists, CronRestartService, CronCommand,
}

// CronServices are the units a restart cron may name.
var CronServices = []string{"dnsmasq", "unbound", "miniupnpd", "tailscaled", "ostiole"}

// Cron is one piece of scheduled work.
type Cron struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Schedule is a five-field cron expression, or a shorthand like
	// @daily.
	Schedule string   `json:"schedule"`
	Kind     CronKind `json:"kind"`
	// Directory is where a backup cron writes.
	Directory string `json:"directory,omitempty"`
	// Keep is how many backups to leave behind; zero keeps ten.
	Keep int `json:"keep,omitempty"`
	// WithUsers includes the accounts in a backup.
	WithUsers bool `json:"withUsers,omitempty"`
	// Service is the unit a restart cron acts on.
	Service string `json:"service,omitempty"`
	// Command and Args are what a command cron runs. The command is not
	// passed through a shell, so there is nothing to quote and nothing to
	// inject.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// TimeoutSeconds bounds a command; zero means five minutes.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

// DefaultBackupsKept is how many backups a backup cron leaves behind.
const DefaultBackupsKept = 10

// Cron returns the cron with the given id. The crons derived from the
// update settings answer to their ids too, so "run it now" works for
// them without the operator having to write them out.
func (c *Config) Cron(id string) (*Cron, bool) {
	for i := range c.Crons {
		if c.Crons[i].ID == id {
			return &c.Crons[i], true
		}
	}
	derived := c.DerivedCrons()
	for i := range derived {
		if derived[i].ID == id {
			return &derived[i], true
		}
	}
	return nil, false
}

// UpdateMode says what an update cron is allowed to install.
type UpdateMode string

// Update modes.
const (
	// UpdateManual checks on the schedule and installs nothing, so the router
	// can say what is waiting without changing itself.
	UpdateManual UpdateMode = "manual"
	// UpdateSecurity installs only the updates the publisher marked as
	// security fixes.
	UpdateSecurity UpdateMode = "security"
	// UpdateAll installs everything that is newer.
	UpdateAll UpdateMode = "all"
)

// UpdateModes lists them in the order the UI offers them.
var UpdateModes = []UpdateMode{UpdateAll, UpdateSecurity, UpdateManual}

// DefaultUpdateCheckSchedule is when a router asks what is waiting if
// nobody says otherwise: every night, because "is there an update?" is
// worth no more than the last time anybody looked.
const DefaultUpdateCheckSchedule = "0 4 * * *"

// DefaultUpdateSchedule is when a router installs what the mode allows if
// nobody says otherwise: early on a Sunday, when a reboot hurts least, and
// half an hour after the check so the two never land in the same minute.
const DefaultUpdateSchedule = "30 4 * * 0"

// Update cron ids. They are reported as work Ostiole does on its own
// account rather than as crons the operator wrote.
const (
	CronIDSystemUpdate       = "system:os-updates"
	CronIDOstioleUpdate      = "system:ostiole-updates"
	CronIDSystemUpdateCheck  = "system:os-update-check"
	CronIDOstioleUpdateCheck = "system:ostiole-update-check"
)

// Update channels for Ostiole's own releases.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// Updates is how the router patches itself. The zero value means the
// defaults, so a configuration written before this existed gets weekly
// security updates rather than nothing.
type Updates struct {
	// System is the distro packages underneath Ostiole.
	System PackageUpdates `json:"system,omitempty"`
	// Ostiole is Ostiole's own releases.
	Ostiole SelfUpdates `json:"ostiole,omitempty"`
}

// PackageUpdates controls the distro package manager.
type PackageUpdates struct {
	// Mode is empty for the default, which is security.
	Mode UpdateMode `json:"mode,omitempty"`
	// CheckSchedule is when the router asks what is waiting; empty means
	// DefaultUpdateCheckSchedule.
	CheckSchedule string `json:"checkSchedule,omitempty"`
	// InstallSchedule is when it installs what the mode allows; empty means
	// DefaultUpdateSchedule. It does not run at all on manual.
	InstallSchedule string `json:"installSchedule,omitempty"`
	// Exclude names packages this router never upgrades, for the kernel a
	// driver is pinned to or anything else that must not move.
	Exclude []string `json:"exclude,omitempty"`
}

// SelfUpdates controls Ostiole's own releases.
type SelfUpdates struct {
	// Mode is empty for the default, which is security.
	Mode UpdateMode `json:"mode,omitempty"`
	// CheckSchedule is when the router asks what is waiting; empty means
	// DefaultUpdateCheckSchedule.
	CheckSchedule string `json:"checkSchedule,omitempty"`
	// InstallSchedule is when it installs what the mode allows; empty means
	// DefaultUpdateSchedule. It does not run at all on manual.
	InstallSchedule string `json:"installSchedule,omitempty"`
	// Channel is stable or beta; empty means stable.
	Channel string `json:"channel,omitempty"`
}

// SystemMode is the mode the distro packages update under.
func (u Updates) SystemMode() UpdateMode { return modeOr(u.System.Mode) }

// SystemCheckSchedule is when the distro packages are checked.
func (u Updates) SystemCheckSchedule() string { return checkScheduleOr(u.System.CheckSchedule) }

// SystemInstallSchedule is when the distro packages are installed.
func (u Updates) SystemInstallSchedule() string { return scheduleOr(u.System.InstallSchedule) }

// OstioleMode is the mode Ostiole's own releases update under.
func (u Updates) OstioleMode() UpdateMode { return modeOr(u.Ostiole.Mode) }

// OstioleCheckSchedule is when Ostiole looks for its own releases.
func (u Updates) OstioleCheckSchedule() string { return checkScheduleOr(u.Ostiole.CheckSchedule) }

// OstioleInstallSchedule is when Ostiole installs one.
func (u Updates) OstioleInstallSchedule() string { return scheduleOr(u.Ostiole.InstallSchedule) }

// OstioleChannel is the release channel in force.
func (u Updates) OstioleChannel() string {
	if u.Ostiole.Channel == "" {
		return ChannelStable
	}
	return u.Ostiole.Channel
}

func modeOr(m UpdateMode) UpdateMode {
	if m == "" {
		return UpdateSecurity
	}
	return m
}

func scheduleOr(s string) string {
	if s == "" {
		return DefaultUpdateSchedule
	}
	return s
}

func checkScheduleOr(s string) string {
	if s == "" {
		return DefaultUpdateCheckSchedule
	}
	return s
}

// DerivedCrons are the crons the update settings imply. They are not
// stored, so there is one place to change a mode and no way for the list
// of crons and the settings to disagree. Each source checks whatever the
// mode is, because a router that installs nothing still has to be able to
// say what is waiting; only the installing is turned off on manual.
func (c *Config) DerivedCrons() []Cron {
	return []Cron{
		{
			ID:          CronIDSystemUpdateCheck,
			Description: "Ask the distro package manager what updates are waiting",
			Enabled:     true,
			Schedule:    c.Updates.SystemCheckSchedule(),
			Kind:        CronSystemUpdateCheck,
		},
		{
			ID:          CronIDSystemUpdate,
			Description: "Install the distro updates the update mode allows",
			Enabled:     c.Updates.SystemMode() != UpdateManual,
			Schedule:    c.Updates.SystemInstallSchedule(),
			Kind:        CronSystemUpdate,
		},
		{
			ID:          CronIDOstioleUpdateCheck,
			Description: "Ask whether a newer Ostiole release has been published",
			Enabled:     true,
			Schedule:    c.Updates.OstioleCheckSchedule(),
			Kind:        CronOstioleUpdateCheck,
		},
		{
			ID:          CronIDOstioleUpdate,
			Description: "Install a newer Ostiole release when the update mode allows",
			Enabled:     c.Updates.OstioleMode() != UpdateManual,
			Schedule:    c.Updates.OstioleInstallSchedule(),
			Kind:        CronOstioleUpdate,
		},
	}
}

// System holds router-level settings.
type System struct {
	Hostname string `json:"hostname,omitempty"`
	// Timezone is the IANA zone the router reads its clock in, such as
	// "Europe/Berlin". Empty is UTC, which is what a router ships in.
	Timezone   string     `json:"timezone,omitempty"`
	DNSServers []string   `json:"dnsServers,omitempty"`
	Management Management `json:"management"`
	// KeepRevisions bounds the configuration history: every apply archives
	// the configuration it replaced, and the oldest are deleted once there
	// are more than this. Zero keeps DefaultKeepRevisions.
	KeepRevisions int `json:"keepRevisions,omitempty"`
	// GeoIPv4URL and GeoIPv6URL are where country address lists come from.
	// "{country}" is replaced with the lower-case ISO code. They are
	// settings so an air-gapped router can point at its own mirror; empty
	// uses the defaults.
	GeoIPv4URL string `json:"geoIPv4Url,omitempty"`
	GeoIPv6URL string `json:"geoIPv6Url,omitempty"`
	// BogonV4URL and BogonV6URL are where the list of unallocated
	// prefixes comes from; empty uses the defaults.
	BogonV4URL string `json:"bogonV4Url,omitempty"`
	BogonV6URL string `json:"bogonV6Url,omitempty"`
	// JournalMaxUseGB caps what the system journal keeps on disk, in
	// gigabytes; journald deletes the oldest entries beyond it. Zero keeps
	// the default.
	JournalMaxUseGB int `json:"journalMaxUseGB,omitempty"`
}

// JournalMaxUse is the journal's ceiling in gigabytes, the default when
// the setting says nothing.
func (s System) JournalMaxUse() int {
	if s.JournalMaxUseGB <= 0 {
		return journald.DefaultMaxUseGB
	}
	return s.JournalMaxUseGB
}

// Default sources for country address lists.
const (
	DefaultGeoIPv4URL = "https://www.ipdeny.com/ipblocks/data/aggregated/{country}-aggregated.zone"
	DefaultGeoIPv6URL = "https://www.ipdeny.com/ipv6/ipaddresses/aggregated/{country}-aggregated.zone"
)

// DefaultKeepRevisions is how much configuration history is kept when the
// setting says nothing. Enough to roll back through an afternoon's work,
// not so much that the directory becomes an archive nobody reads.
const DefaultKeepRevisions = 20

// MaxKeepRevisions bounds the setting.
const MaxKeepRevisions = 1000

// RevisionsKept is how many archived configurations to hold on to.
func (s System) RevisionsKept() int {
	if s.KeepRevisions <= 0 {
		return DefaultKeepRevisions
	}
	return s.KeepRevisions
}

// Zone is the timezone the router runs in, UTC when the setting says nothing.
func (s System) Zone() string {
	if s.Timezone == "" {
		return timezone.Default
	}
	return s.Timezone
}

// Location is Zone as a *time.Location, for the schedules that are read
// in the router's own time. A zone this build cannot resolve falls back to
// UTC: validation rejects those, and a cron an hour off still runs.
func (s System) Location() *time.Location { return timezone.Load(s.Zone()) }

// GeoIPTemplates returns the URLs country lists are fetched from.
func (s System) GeoIPTemplates() (v4, v6 string) {
	v4, v6 = s.GeoIPv4URL, s.GeoIPv6URL
	if v4 == "" && v6 == "" {
		return DefaultGeoIPv4URL, DefaultGeoIPv6URL
	}
	return v4, v6
}

// Management describes how the router itself is administered. The ports feed
// the anti-lockout rule on zones that have AntiLockout set. A zero port
// disables that entry.
type Management struct {
	WebPort uint16 `json:"webPort"`
	SSHPort uint16 `json:"sshPort"`
	// SSHPasswords lets a password in at the SSH prompt. Unset, sshd takes
	// keys only; nothing here checks that anybody has one.
	SSHPasswords    bool `json:"sshPasswords"`
	LogDefaultDrops bool `json:"logDefaultDrops,omitempty"`
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
	// Busy holds back a host on this zone that is opening a lot of
	// connections at once, which is what file sharing looks like from
	// outside. Unset leaves every host alone.
	Busy *BusyHosts `json:"busy,omitempty"`
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
	// PPPoE dials a session over an Ethernet link, which is how most DSL
	// and some fibre services are delivered.
	PPPoE     *PPPoE     `json:"pppoe,omitempty"`
	Tailscale *Tailscale `json:"tailscale,omitempty"`
	// Wireless makes this interface an SSID served by one of the radios.
	Wireless *WirelessNetwork `json:"wireless,omitempty"`
	MTU      int              `json:"mtu,omitempty"`

	// LogDrops overrides system.management.logDefaultDrops for traffic
	// arriving here: a WAN worth watching can log while a busy LAN stays
	// quiet. Unset follows the system setting.
	LogDrops *bool `json:"logDrops,omitempty"`
	// BlockPrivate drops traffic arriving here from addresses that cannot
	// legitimately come from the internet: RFC 1918, RFC 4193 unique local
	// addresses, and loopback. Turn it on for a WAN, and off if the
	// provider addresses that WAN privately.
	BlockPrivate bool `json:"blockPrivate,omitempty"`
	// BlockBogons drops traffic from prefixes IANA has not allocated,
	// which have no business being a source address. The list is fetched
	// and refreshed like a blocklist. It belongs on a WAN only.
	BlockBogons bool `json:"blockBogons,omitempty"`
	// Shaping holds the queue for this line on the router, given how fast
	// the line is. Unset leaves the interface as the kernel has it.
	Shaping *Shaping `json:"shaping,omitempty"`
}

// LogsDrops reports whether packets dropped by the default policy on this
// interface should be logged, given the system default.
func (i Interface) LogsDrops(def bool) bool {
	if i.LogDrops != nil {
		return *i.LogDrops
	}
	return def
}

// DHCPv6Client reports whether the interface may run a DHCPv6 client: it
// asks for an address, or it listens to router advertisements, which can
// tell it to ask. A prefix hint needs dhcp mode, so it is covered.
func (i Interface) DHCPv6Client() bool {
	return i.IPv6.Mode == AddrDHCP || i.IPv6.Mode == AddrSLAAC
}

// PrivateSources are the addresses that cannot legitimately be the source
// of traffic arriving from the internet: RFC 1918, RFC 4193, and
// loopback. Link-local is deliberately absent — IPv6 needs fe80::/10 for
// neighbour discovery and for the router advertisements that carry the
// default route, and blocking it would take the WAN down.
var PrivateSources = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8",
	"fc00::/7", "::1/128",
}

// Default sources for the bogon list: prefixes IANA has not allocated.
const (
	DefaultBogonV4URL = "https://www.team-cymru.org/Services/Bogons/fullbogons-ipv4.txt"
	DefaultBogonV6URL = "https://www.team-cymru.org/Services/Bogons/fullbogons-ipv6.txt"
)

// BogonTemplates returns the URLs the bogon list is fetched from.
func (s System) BogonTemplates() (v4, v6 string) {
	v4, v6 = s.BogonV4URL, s.BogonV6URL
	if v4 == "" && v6 == "" {
		return DefaultBogonV4URL, DefaultBogonV6URL
	}
	return v4, v6
}

// BlocksBogons reports whether any enabled interface asks for the bogon
// list, which is what decides whether it is worth fetching.
func (c *Config) BlocksBogons() bool {
	for _, in := range c.Interfaces {
		if in.Enabled && in.BlockBogons {
			return true
		}
	}
	return false
}

// PPPoE is a dialled session over Ethernet. The address, the default
// route, and the DNS servers all come from the other end, so there is
// nothing to configure but the credentials.
type PPPoE struct {
	// Parent is the Ethernet link the session runs over. It carries no
	// address of its own.
	Parent   string `json:"parent"`
	Username string `json:"username"`
	Password string `json:"password"`
	// ServiceName and ACName pick one concentrator when the line offers
	// more than one. Both are usually empty.
	ServiceName string `json:"serviceName,omitempty"`
	ACName      string `json:"acName,omitempty"`
	// IPv6 asks for an IPv6 session alongside the IPv4 one.
	IPv6 bool `json:"ipv6,omitempty"`
	// LCPInterval and LCPFailures decide how quickly a dead line is
	// noticed: pppd gives up after LCPFailures unanswered echoes.
	LCPInterval int `json:"lcpInterval,omitempty"`
	LCPFailures int `json:"lcpFailures,omitempty"`
}

// PPPoEMTU is what a PPPoE session leaves of an Ethernet frame: eight
// bytes of header come out of the usual 1500.
const PPPoEMTU = 1492

// Tailscale joins this router to a tailnet. tailscaled creates the
// interface and addresses it; the zone and the rules are the interface's.
type Tailscale struct {
	// Port peers dial; 0 lets the daemon pick one and opens nothing.
	Port uint16 `json:"port,omitempty"`
	// Hostname on the tailnet; empty takes the router's.
	Hostname string `json:"hostname,omitempty"`
	// LoginServer is the control server; empty is Tailscale's.
	LoginServer string `json:"loginServer,omitempty"`
	// AdvertiseRoutes are the networks offered to the tailnet.
	AdvertiseRoutes []string `json:"advertiseRoutes,omitempty"`
	// AdvertiseExitNode offers this router as the tailnet's way out.
	AdvertiseExitNode bool `json:"advertiseExitNode,omitempty"`
	// AcceptRoutes takes the networks other nodes advertise.
	AcceptRoutes bool `json:"acceptRoutes,omitempty"`
	// LogUploads sends the daemon's logs to Tailscale. Off keeps them here.
	LogUploads bool `json:"logUploads,omitempty"`
}

// TailscaleDevice is the only name a Tailscale interface may have: it is
// the daemon's default and what every tool on the router assumes.
const TailscaleDevice = "tailscale0"

// Wireless is the router's radios and the country they transmit in.
type Wireless struct {
	// Country is the ISO 3166-1 alpha-2 code every radio follows.
	Country string  `json:"country,omitempty"`
	Radios  []Radio `json:"radios,omitempty"`
}

// Radio is one wifi device, named after the interface the kernel gave
// it. That interface stays up and idle; the networks are interfaces of
// their own on the same device.
type Radio struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Band    Band   `json:"band"`
	// Channel 0 lets the daemon pick one.
	Channel  int      `json:"channel,omitempty"`
	Width    int      `json:"width"` // MHz
	Standard Standard `json:"standard"`
	// Power caps transmit power in dBm; 0 is the regulatory maximum.
	Power int `json:"power,omitempty"`
}

// WirelessNetwork makes an interface an SSID served by a radio. hostapd
// creates the device; the zone or the bridge is the interface's own.
type WirelessNetwork struct {
	Radio      string   `json:"radio"`
	SSID       string   `json:"ssid"`
	Security   Security `json:"security"`
	Passphrase string   `json:"passphrase,omitempty"`
	// Hidden leaves the name out of beacons.
	Hidden bool `json:"hidden,omitempty"`
	// Isolate stops clients reaching each other.
	Isolate bool `json:"isolate,omitempty"`
	// MaxClients 0 is the daemon's limit.
	MaxClients int `json:"maxClients,omitempty"`
}

// Band is the range of frequencies a radio transmits in.
type Band string

// Bands, in the order the UI offers them.
const (
	Band2G Band = "2g"
	Band5G Band = "5g"
	Band6G Band = "6g"
)

// Bands lists every band.
var Bands = []Band{Band2G, Band5G, Band6G}

// Standard is the 802.11 generation a radio serves.
type Standard string

// Standards, best first.
const (
	StandardAX     Standard = "ax"
	StandardAC     Standard = "ac"
	StandardN      Standard = "n"
	StandardLegacy Standard = "legacy"
)

// Standards lists every generation, best first.
var Standards = []Standard{StandardAX, StandardAC, StandardN, StandardLegacy}

// Security is how a network authenticates and encrypts.
type Security string

// Security choices, in the order the UI offers them.
const (
	// SecurityMixed takes WPA3 where the client has it and WPA2 where it
	// does not.
	SecurityMixed Security = "wpa2-wpa3"
	SecurityWPA3  Security = "wpa3"
	SecurityWPA2  Security = "wpa2"
	// SecurityOWE encrypts without a passphrase; a client that has never
	// heard of it joins an open network instead.
	SecurityOWE  Security = "owe"
	SecurityOpen Security = "open"
)

// Securities lists every choice, in the order the UI offers them.
var Securities = []Security{SecurityMixed, SecurityWPA3, SecurityWPA2, SecurityOWE, SecurityOpen}

// NeedsPassphrase reports whether this choice takes one.
func (s Security) NeedsPassphrase() bool {
	return s == SecurityMixed || s == SecurityWPA3 || s == SecurityWPA2
}

// RadarChannels are the 5 GHz channels a radar may be using, which a card
// may only transmit on after watching for one.
var RadarChannels = []int{52, 56, 60, 64, 100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144}

// Channels lists the channels of a band.
func Channels(b Band) []int {
	switch b {
	case Band2G:
		return []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	case Band5G:
		return []int{
			36, 40, 44, 48, 52, 56, 60, 64,
			100, 104, 108, 112, 116, 120, 124, 128, 132, 136, 140, 144,
			149, 153, 157, 161, 165, 169, 173, 177,
		}
	case Band6G:
		out := make([]int, 0, 59)
		for c := 1; c <= 233; c += 4 {
			out = append(out, c)
		}
		return out
	}
	return nil
}

// Widths lists the channel widths of a band, in MHz.
func Widths(b Band) []int {
	if b == Band2G {
		return []int{20, 40}
	}
	return []int{20, 40, 80, 160}
}

// Radio returns the radio with the given name.
func (c *Config) Radio(name string) (Radio, bool) {
	for _, r := range c.Wireless.Radios {
		if r.Name == name {
			return r, true
		}
	}
	return Radio{}, false
}

// NetworksOn returns the enabled networks a radio serves, sorted by
// interface name. The order is stable because the first of them is the
// one hostapd takes as its own interface.
func (c *Config) NetworksOn(radio string) []Interface {
	var out []Interface
	for _, in := range c.Interfaces {
		if in.Enabled && in.Wireless != nil && in.Wireless.Radio == radio {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b Interface) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// ActiveRadios returns the enabled radios that have a network to serve,
// in configuration order.
func (c *Config) ActiveRadios() []Radio {
	var out []Radio
	for _, r := range c.Wireless.Radios {
		if r.Enabled && len(c.NetworksOn(r.Name)) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// WirelessEnabled reports whether any radio is meant to be transmitting.
func (c *Config) WirelessEnabled() bool { return len(c.ActiveRadios()) > 0 }

// Kind names what an interface is made of, for the UI and for messages.
type Kind string

// Interface kinds.
const (
	KindPhysical  Kind = "physical"
	KindVLAN      Kind = "vlan"
	KindBridge    Kind = "bridge"
	KindBond      Kind = "bond"
	KindWireGuard Kind = "wireguard"
	KindPPPoE     Kind = "pppoe"
	KindTailscale Kind = "tailscale"
	KindWireless  Kind = "wireless"
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
	case i.PPPoE != nil:
		return KindPPPoE
	case i.Tailscale != nil:
		return KindTailscale
	case i.Wireless != nil:
		return KindWireless
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

// PPPoEParents maps each Ethernet link carrying a dialled session to the
// interface that dials it. Such a link is a port: the session holds the
// address, not the wire underneath it.
func (c *Config) PPPoEParents() map[string]string {
	out := map[string]string{}
	for _, in := range c.Interfaces {
		if in.PPPoE != nil && in.PPPoE.Parent != "" {
			out[in.PPPoE.Parent] = in.Name
		}
	}
	return out
}

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
	// Entries are the addresses or ports, written here. For a geoip alias
	// they are ISO country codes instead, and for a URL alias they are
	// extra entries kept alongside whatever is fetched.
	Entries []string `json:"entries"`
	// URL fetches the entries from a published list. The result is cached
	// on disk, so a router that boots without a working line still has the
	// list it had yesterday.
	URL string `json:"url,omitempty"`
	// RefreshHours is how often to fetch; zero means once a day. Nothing
	// is fetched more than once an hour.
	RefreshHours int `json:"refreshHours,omitempty"`
}

// Fetched reports whether this alias takes its contents from elsewhere.
func (a Alias) Fetched() bool { return a.URL != "" || a.Type == AliasGeoIP }

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
	// Priority puts the flows this rule admits in a tier, which decides
	// what yields to what on a shaped line. It follows the connection, so
	// the answers coming back are prioritised too.
	Priority Tier `json:"priority,omitempty"`
	// Limit holds what this rule admits to a rate. Over it, the packet
	// falls through to whatever comes next — which on a zone's last rule
	// is the drop at the end of it. It is only meaningful on an accept:
	// there is no sense in rationing a refusal.
	Limit *RateLimit `json:"limit,omitempty"`
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
	// NotAddresses inverts the address half: the rule matches every
	// address except the ones named. It needs something to invert, so it
	// is only valid alongside Addresses, Alias, or Self.
	//
	// Inverting covers both address families even when the addresses name
	// only one of them: every IPv6 packet is "not in this IPv4 set". The
	// renderer emits the other family in full rather than leaving it out,
	// which would quietly let half the internet past.
	NotAddresses bool `json:"notAddresses,omitempty"`
	// NotPorts inverts the port half in the same way.
	NotPorts bool `json:"notPorts,omitempty"`
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

// OutboundRule translates traffic leaving Zone. With nothing else set it
// masquerades, which is what most rules want; the rest of the fields are
// for the cases that need something particular.
type OutboundRule struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Zone        string   `json:"zone"`
	Source      []string `json:"source,omitempty"`
	// Destination restricts the rule to traffic headed somewhere in
	// particular; empty means anywhere.
	Destination []string `json:"destination,omitempty"`
	// Address sends the traffic out as this address instead of whichever
	// one the interface happens to have. It has to be an address the router
	// actually answers to.
	Address string `json:"address,omitempty"`
	// NoNAT leaves matching traffic alone. In hybrid mode this is how a
	// host is kept out of NAT that the automatic rules would otherwise
	// translate.
	NoNAT bool `json:"noNat,omitempty"`
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
	// Priority puts the forwarded flows in a tier. Only the first packet of
	// a connection is translated, which is exactly when it is classified.
	Priority Tier `json:"priority,omitempty"`
}

// Gateway is an upstream this firewall routes through. Several gateways
// make a multi-WAN router: the lowest priority that answers its monitor
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
	// which keeps a router working when its preferred line dies.
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

// Traffic shaping numbering. The tier occupies bits 24-26, beside policy
// routing's byte and clear of it, so a flow can be routed through one
// gateway and prioritised at the same time. Everything that writes either
// one masks the rest of the register rather than replacing it.
const (
	ShapeMarkShift = 24
	ShapeMarkMask  = 0x7 << ShapeMarkShift // 0x07000000; tier 1..4, 0 = unset
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

// reservedPolicyNumbers are numbers no target may take. Tailscale hardcodes
// 0x40000 for subnet routes and 0x80000 for its own bypass, and installs ip
// rules for them whatever its netfilter mode is.
var reservedPolicyNumbers = map[int]bool{4: true, 8: true}

// PolicyTargets lists every enabled gateway and gateway group, sorted by
// name and numbered from one, skipping the reserved numbers.
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
	if limit := MaxPolicyTargets - len(reservedPolicyNumbers); len(names) > limit {
		names = names[:limit]
	}
	out := make([]PolicyTarget, 0, len(names))
	n := 0
	for _, name := range names {
		n++
		for reservedPolicyNumbers[n] {
			n++
		}
		out = append(out, PolicyTarget{
			Name:  name,
			Group: group[name],
			Mark:  uint32(n) << PolicyMarkShift,
			Table: PolicyTableBase + n,
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

// CanFailover reports whether there is more than one enabled gateway, which
// is what failover needs: a router with one gateway has nowhere to move the
// default route to, so the monitor probes it but never touches its route.
func (c *Config) CanFailover() bool {
	n := 0
	for _, g := range c.Gateways {
		if !g.Enabled {
			continue
		}
		if n++; n > 1 {
			return true
		}
	}
	return false
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

// TailscaleInterface returns the interface that joins a tailnet. There is
// at most one.
func (c *Config) TailscaleInterface() (Interface, bool) {
	for _, in := range c.Interfaces {
		if in.Tailscale != nil {
			return in, true
		}
	}
	return Interface{}, false
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
	DHCP DHCPService `json:"dhcp"`
	DNS  DNSServer   `json:"dns"`
	UPnP UPnP        `json:"upnp"`
}

// UPnP lets a client on the LAN open a hole through the firewall for
// itself, which is how a console, a torrent client, or a video call
// reaches the outside without anyone writing a port forward. It is off by
// default: a host that can ask for a forward can ask for any forward.
//
// miniupnpd does the talking (ADR-0007); Ostiole renders its configuration
// and the chains it fills in.
type UPnP struct {
	Enabled bool `json:"enabled"`
	// IGD is the UPnP Internet Gateway Device protocol: consoles, Windows,
	// and most peer-to-peer clients.
	IGD bool `json:"igd,omitempty"`
	// PCP answers the Port Control Protocol and NAT-PMP, which is what
	// Apple devices and anything using Bonjour ask with.
	PCP bool `json:"pcp,omitempty"`
	// ExternalInterface is where the mapped port is opened, so it has to be
	// the one facing the internet.
	ExternalInterface string `json:"externalInterface,omitempty"`
	// Interfaces are where clients may ask from; empty means every enabled
	// interface that is not in an external zone.
	Interfaces []string `json:"interfaces,omitempty"`
	// DefaultDeny refuses anything the rules below do not allow. Without it
	// a client may map any port it likes.
	DefaultDeny bool `json:"defaultDeny,omitempty"`
	// ACL is read in order; the first entry that matches decides.
	ACL []UPnPRule `json:"acl,omitempty"`
}

// UPnPRule is one line of the access list: who may ask for which outside
// port, and which inside port it may point at.
type UPnPRule struct {
	Action        string `json:"action"`        // allow | deny
	ExternalPorts string `json:"externalPorts"` // "1024-65535"
	Source        string `json:"source"`        // "192.168.1.0/24"
	InternalPorts string `json:"internalPorts"`
	Description   string `json:"description,omitempty"`
}

// DHCPService hands out IPv4 addresses on selected interfaces, and with V6
// also advertises IPv6 prefixes and serves DHCPv6.
type DHCPService struct {
	Enabled      bool           `json:"enabled"`
	Servers      []DHCPServer   `json:"servers,omitempty"`
	StaticLeases []StaticLease  `json:"staticLeases,omitempty"`
	V6           []DHCPv6Server `json:"v6,omitempty"`
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

// DHCPv6Server serves IPv6 on one interface. The prefix is taken from the
// interface at run time, so it follows a static address, SLAAC, or a
// delegated prefix without being repeated here.
type DHCPv6Server struct {
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

// DefaultLeaseTime is the lease a pool hands out when it names none. A day
// keeps addresses steady across a night's sleep without holding them for
// long after a guest has gone.
const DefaultLeaseTime = "24h"

// DHCPServer is a pool on one interface, which must carry a static IPv4
// address. Empty Gateway and DNS default to this router's address on the
// interface (DNS only when the DNS service is enabled; otherwise the
// system DNS servers).
type DHCPServer struct {
	Interface  string   `json:"interface"`
	Enabled    bool     `json:"enabled"`
	RangeStart string   `json:"rangeStart"`
	RangeEnd   string   `json:"rangeEnd"`
	LeaseTime  string   `json:"leaseTime,omitempty"` // dnsmasq syntax: 24h, 2d, infinite; empty means DefaultLeaseTime
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
	Interfaces      []string         `json:"interfaces,omitempty"`
	Upstreams       []string         `json:"upstreams,omitempty"`
	Domain          string           `json:"domain,omitempty"`
	HostOverrides   []HostOverride   `json:"hostOverrides,omitempty"`
	DomainOverrides []DomainOverride `json:"domainOverrides,omitempty"`
	// Resolver decides who answers names this router does not know.
	Resolver ResolverMode `json:"resolver,omitempty"`
	// TLSUpstreams are the resolvers used in ResolverTLS mode.
	TLSUpstreams []TLSUpstream `json:"tlsUpstreams,omitempty"`
}

// ResolverMode selects how queries leave the router.
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

// HostOverride is a local name answered by this router.
type HostOverride struct {
	Hostname    string `json:"hostname"`
	IP          string `json:"ip"`
	Description string `json:"description,omitempty"`
}

// DomainOverride sends one domain, and everything under it, to resolvers of
// its own instead of the upstreams. That is how a split-horizon zone is
// reached: a tailnet answers its own ts.net names on 100.100.100.100, and
// no amount of asking the internet will find them. Servers may carry a
// port, written 10.0.0.1#5353.
type DomainOverride struct {
	Domain      string   `json:"domain"`
	Servers     []string `json:"servers"`
	Description string   `json:"description,omitempty"`
}
