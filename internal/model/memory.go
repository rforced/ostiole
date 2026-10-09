package model

import "fmt"

// What the router runs besides its logs, in bytes; a megabyte here is
// 1,000,000 bytes.
const (
	// ReserveSystem is the kernel, systemd, journald, udevd, logind,
	// networkd, sshd and the slab.
	ReserveSystem = 300_000_000
	// ReserveServices is unbound, chronyd, hostapd and miniupnpd.
	ReserveServices = 65_000_000
	// ReserveDNSMasq is dnsmasq without a blocklist.
	ReserveDNSMasq = 10_000_000
	// ReserveBlockedName is what dnsmasq holds for each name of the
	// blocking ceiling while the lists are on.
	ReserveBlockedName = 115
	// ReserveProxy is the proxy and its WAF while it serves.
	ReserveProxy = 100_000_000
	// ReserveJournalReader is one journalctl the daemon reads.
	ReserveJournalReader = 22_000_000
	// ReserveDaemon is the daemon without its logs.
	ReserveDaemon = 150_000_000
)

// DefaultBlockedDomains is the blocking ceiling when the configuration
// names none. It matches dnsblock.DefaultMaxDomains, which cannot be
// imported here: dnsblock is the one that imports model.
const DefaultBlockedDomains = 1_000_000

// PeakFactor is how much more than its full size a log costs at its
// largest, while it is read back from its files.
const PeakFactor = 1.5

// MinLogCeiling is the lowest a log's ceiling goes, so that a router short
// of memory still keeps something rather than refusing every setting.
const MinLogCeiling = 1_000

const firewallLogEntries = "system.management.firewallLog.entries"

// MemoryBudget shares a router's memory between what it runs and its logs.
type MemoryBudget struct {
	// Total is MemTotal in bytes; zero means unknown, and refuses nothing.
	Total int64
}

// Reserve is what the router runs besides its logs, as c has it.
func (MemoryBudget) Reserve(c *Config) int64 {
	r := int64(ReserveSystem + ReserveServices + ReserveDNSMasq + ReserveDaemon)
	if c.ListsActive() {
		n := c.Blocking.MaxDomains
		if n <= 0 {
			n = DefaultBlockedDomains
		}
		r += ReserveBlockedName * int64(n)
	}
	if c.ProxyEnabled() {
		r += ReserveProxy + ReserveJournalReader
	}
	if c.Services.DHCP.Enabled && c.System.Logging.Records() {
		r += ReserveJournalReader
	}
	if c.WirelessEnabled() && c.System.Logging.Records() {
		r += ReserveJournalReader
	}
	return r
}

// Logs is what the logs may cost at their largest: Total less the
// reserve, never below zero.
func (b MemoryBudget) Logs(c *Config) int64 {
	return max(b.Total-b.Reserve(c), 0)
}

// Ceiling is the most entries of bytesPerEntry a log may keep here: what
// fits Logs at the peak, at most most, and at least MinLogCeiling even on
// a router with nothing left for its logs. With Total unknown it is most.
func (b MemoryBudget) Ceiling(c *Config, bytesPerEntry int64, most int) int {
	if b.Total <= 0 {
		return most
	}
	fit := int(float64(b.Logs(c)) / (float64(bytesPerEntry) * PeakFactor))
	return min(most, max(fit, MinLogCeiling))
}

// LogCeilings are the most entries each log setting may ask for here.
type LogCeilings struct {
	QueryLog     int `json:"queryLog"`
	FirewallLog  int `json:"firewallLog"`
	ProxyEvents  int `json:"proxyEvents"`
	Requests     int `json:"requests"`
	Destinations int `json:"destinations"`
	DHCPLog      int `json:"dhcpLog"`
	WirelessLog  int `json:"wirelessLog"`
	PeerLog      int `json:"peerLog"`
}

// Ceilings are each log's ceiling here.
func (b MemoryBudget) Ceilings(c *Config) LogCeilings {
	return LogCeilings{
		QueryLog:     b.Ceiling(c, QueryLogBytes, MaxQueryLogEntries),
		FirewallLog:  b.Ceiling(c, FirewallLogBytes, MaxFirewallLogEntries),
		ProxyEvents:  b.Ceiling(c, ProxyEventBytes, MaxProxyEventEntries),
		Requests:     b.Ceiling(c, RequestBytes, MaxRequestEntries),
		Destinations: b.Ceiling(c, DestinationBytes, MaxDestinationEntries),
		DHCPLog:      b.Ceiling(c, DHCPLogBytes, MaxDHCPLogEntries),
		WirelessLog:  b.Ceiling(c, WirelessLogBytes, MaxWirelessLogEntries),
		PeerLog:      b.Ceiling(c, PeerLogBytes, MaxPeerLogEntries),
	}
}

// LogSizes is how many entries each log keeps on this machine: its
// setting, or its ceiling when that is lower.
type LogSizes struct {
	QueryLog     int
	FirewallLog  int
	ProxyEvents  int
	Requests     int
	Destinations int
	DHCPLog      int
	WirelessLog  int
	WireGuardLog int
	TailscaleLog int
}

// Sizes clamps every log's setting to its ceiling.
func (b MemoryBudget) Sizes(c *Config) LogSizes {
	ce := b.Ceilings(c)
	q, p := c.Services.DNS.QueryLog, c.Services.Proxy
	return LogSizes{
		QueryLog:     min(q.Size(), ce.QueryLog),
		FirewallLog:  min(c.System.Management.FirewallLog.Size(), ce.FirewallLog),
		ProxyEvents:  min(p.Events.Size(), ce.ProxyEvents),
		Requests:     min(p.Requests.Size(DefaultRequestEntries), ce.Requests),
		Destinations: min(c.Traffic.Destinations.Size(), ce.Destinations),
		DHCPLog:      min(c.Services.DHCP.Log.Size(DefaultDHCPLogEntries), ce.DHCPLog),
		WirelessLog:  min(c.Wireless.Log.Size(DefaultWirelessLogEntries), ce.WirelessLog),
		WireGuardLog: min(c.VPN.WireGuardLog.Size(DefaultPeerLogEntries), ce.PeerLog),
		TailscaleLog: min(c.VPN.TailscaleLog.Size(DefaultPeerLogEntries), ce.PeerLog),
	}
}

// LogsFullBytes is what the logs that are on cost full at the sizes this machine keeps.
func (b MemoryBudget) LogsFullBytes(c *Config) int64 {
	var total int64
	for _, l := range c.memoryLogs() {
		if l.on {
			total += int64(min(l.size, b.Ceiling(c, l.bytes, l.most))) * l.bytes
		}
	}
	return total
}

// Check refuses the logs that are on where this router's memory cannot
// hold them at their largest: each above its ceiling, and all of them
// together above Logs, on every log set above its default or on the
// firewall log's when none is. With Total unknown it refuses nothing.
func (b MemoryBudget) Check(c *Config) []Issue {
	if b.Total <= 0 {
		return nil
	}
	v := &validator{}
	logs := c.memoryLogs()
	var full int64
	for _, l := range logs {
		if !l.on {
			continue
		}
		full += int64(l.size) * l.bytes
		if l.size <= l.def {
			continue
		}
		if most := b.Ceiling(c, l.bytes, l.most); l.size > most {
			v.add(l.path, "this router allows up to %d", most)
		}
	}
	peak, room := int64(float64(full)*PeakFactor), b.Logs(c)
	if peak <= room {
		return v.issues
	}
	msg := fmt.Sprintf("the logs come to %s at their largest; this router has %s for them",
		bytesText(peak), bytesText(room))
	for _, l := range logs {
		if l.on && l.size > l.def {
			v.add(l.path, "%s", msg)
		}
	}
	return v.issues
}

// bytesText writes a size the way the pages do: decimal units, with one
// decimal below 100.
func bytesText(n int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	v, i := float64(n), 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if i == 0 || v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
