package model

import "time"

// How long a log in memory keeps an entry, in days: the firewall log, the
// DNS query log and the WAF events alike.
const (
	DefaultLogDays = 7
	MaxLogDays     = 365
)

// logDays is a log's days as a duration, filling in the default.
func logDays(days int) time.Duration {
	if days <= 0 {
		days = DefaultLogDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// LogKeep is how much of a log is kept in memory: the most entries and the
// most days. The newer logs share it; each has its own default and ceiling
// of entries.
type LogKeep struct {
	// Entries is the most entries kept; zero keeps the log's default.
	Entries int `json:"entries,omitempty"`
	// Days is how long an entry is kept; zero keeps DefaultLogDays.
	Days int `json:"days,omitempty"`
}

// Size is how many entries are kept, def when the setting says nothing.
func (k LogKeep) Size(def int) int {
	if k.Entries > 0 {
		return k.Entries
	}
	return def
}

// Retention is how long an entry is kept, filling in the default.
func (k LogKeep) Retention() time.Duration { return logDays(k.Days) }

// LogFiles writes the logs Ostiole keeps in memory to files as well, and
// reads them back when the daemon starts. Off by default: the files hold
// client addresses.
type LogFiles struct {
	Enabled bool `json:"enabled,omitempty"`
	// WriteMinutes is the longest an entry waits to be written; zero
	// keeps DefaultWriteMinutes.
	WriteMinutes int `json:"writeMinutes,omitempty"`
	// RetentionDays is how long the files keep an entry, or the log's own
	// days where those are fewer; zero keeps DefaultLogFileDays.
	RetentionDays int `json:"retentionDays,omitempty"`
	// MaxUseGB caps the files; zero keeps DefaultLogFilesGB.
	MaxUseGB int `json:"maxUseGB,omitempty"`
}

// Log file defaults and bounds. A month is the longest window the
// Traffic page shows.
const (
	DefaultWriteMinutes = 5
	DefaultLogFileDays  = 31
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

// Days is the longest the files keep an entry; a log that keeps fewer
// days keeps fewer in its files too.
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
