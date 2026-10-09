package model

import "time"

// How many days an entry stays in memory, in every log kept there:
// Logging.Days's default and ceiling.
const (
	DefaultLogDays = 7
	MaxLogDays     = 365
)

// MemoryDays is how many days an entry stays in memory, the default when
// the setting says nothing.
func (l Logging) MemoryDays() int {
	if l.Days <= 0 {
		return DefaultLogDays
	}
	return l.Days
}

// MemoryKeep is how long an entry stays in memory.
func (l Logging) MemoryKeep() time.Duration {
	return time.Duration(l.MemoryDays()) * 24 * time.Hour
}

// LogKeep is how many entries of a log are kept in memory. The newer logs
// share it; each has its own default and ceiling.
type LogKeep struct {
	// Entries is the most entries kept; zero keeps the log's default.
	Entries int `json:"entries,omitempty"`
}

// Size is how many entries are kept, def when the setting says nothing.
func (k LogKeep) Size(def int) int {
	if k.Entries > 0 {
		return k.Entries
	}
	return def
}

// LogFiles writes the logs Ostiole keeps in memory to files as well, and
// reads them back when the daemon starts. Off by default: the files hold
// client addresses.
type LogFiles struct {
	Enabled bool `json:"enabled,omitempty"`
	// WriteMinutes is the longest an entry waits to be written; zero
	// keeps DefaultWriteMinutes.
	WriteMinutes int `json:"writeMinutes,omitempty"`
	// RetentionDays is how long the files keep an entry, in every log and
	// whatever memory keeps; zero keeps DefaultLogFileDays.
	RetentionDays int `json:"retentionDays,omitempty"`
	// MaxUseGB caps the files; zero keeps DefaultLogFilesGB.
	MaxUseGB int `json:"maxUseGB,omitempty"`
}

// Log file defaults and bounds: the files keep a month unless set.
const (
	DefaultWriteMinutes = 5
	DefaultLogFileDays  = 30
	MaxLogFileDays      = 365
	DefaultLogFilesGB   = 1
	MaxLogFilesGB       = 1024
)

// WriteEvery are the intervals the files may be written at, in minutes.
var WriteEvery = []int{1, 5, 15, 60}

// Interval is the longest an entry waits to be written.
func (f LogFiles) Interval() time.Duration {
	if f.WriteMinutes <= 0 {
		return DefaultWriteMinutes * time.Minute
	}
	return time.Duration(f.WriteMinutes) * time.Minute
}

// Days is how many days the files keep an entry, the default when the
// setting says nothing.
func (f LogFiles) Days() int {
	if f.RetentionDays <= 0 {
		return DefaultLogFileDays
	}
	return f.RetentionDays
}

// MaxUse caps the files, in bytes.
func (f LogFiles) MaxUse() int64 {
	gb := f.MaxUseGB
	if gb <= 0 {
		gb = DefaultLogFilesGB
	}
	return int64(gb) << 30
}

// LogsFullBytes is what the logs kept in memory cost once full, counting
// only those that are on: the firewall log always, the query log while it
// is on and the DNS server or its files fill it, the WAF events while the
// proxy serves, Traffic's destinations while they are recorded, and the
// proxy's requests, the DHCP log, the wireless clients and the VPN peers
// while their service is on and the level keeps them.
func (c *Config) LogsFullBytes() int64 {
	var total int64
	for _, l := range c.memoryLogs() {
		if l.on {
			total += int64(l.size) * l.bytes
		}
	}
	return total
}

// memoryLog is one log kept in memory: its setting's path, whether it is
// on, its size and default, its ceiling and what an entry costs.
type memoryLog struct {
	path      string
	on        bool
	size, def int
	most      int
	bytes     int64
}

func (c *Config) memoryLogs() []memoryLog {
	records := c.System.Logging.Records()
	q, p := c.Services.DNS.QueryLog, c.Services.Proxy
	return []memoryLog{
		{firewallLogEntries, true, c.System.Management.FirewallLog.Size(),
			DefaultFirewallLogEntries, MaxFirewallLogEntries, FirewallLogBytes},
		{"services.dns.queryLog.entries", q.Enabled && (c.Services.DNS.Enabled || c.System.Logging.Files.Enabled),
			q.Size(), DefaultQueryLogEntries, MaxQueryLogEntries, QueryLogBytes},
		{"services.proxy.events.entries", c.ProxyEnabled(), p.Events.Size(),
			DefaultProxyEventEntries, MaxProxyEventEntries, ProxyEventBytes},
		{"traffic.destinations.entries", c.Traffic.DestinationsOn(), c.Traffic.Destinations.Size(),
			DefaultDestinationEntries, MaxDestinationEntries, DestinationBytes},
		{"services.proxy.requests.entries", c.ProxyEnabled() && records, p.Requests.Size(DefaultRequestEntries),
			DefaultRequestEntries, MaxRequestEntries, RequestBytes},
		{"services.dhcp.log.entries", c.Services.DHCP.Enabled && records, c.Services.DHCP.Log.Size(DefaultDHCPLogEntries),
			DefaultDHCPLogEntries, MaxDHCPLogEntries, DHCPLogBytes},
		{"wireless.log.entries", c.WirelessEnabled() && records, c.Wireless.Log.Size(DefaultWirelessLogEntries),
			DefaultWirelessLogEntries, MaxWirelessLogEntries, WirelessLogBytes},
		{"vpn.wireguardLog.entries", c.WireGuardEnabled() && records, c.VPN.WireGuardLog.Size(DefaultPeerLogEntries),
			DefaultPeerLogEntries, MaxPeerLogEntries, PeerLogBytes},
		{"vpn.tailscaleLog.entries", c.TailscaleEnabled() && records, c.VPN.TailscaleLog.Size(DefaultPeerLogEntries),
			DefaultPeerLogEntries, MaxPeerLogEntries, PeerLogBytes},
	}
}
