package model

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestProxyEventsDefaults(t *testing.T) {
	t.Parallel()
	var e ProxyEvents
	if e.Size() != DefaultProxyEventEntries {
		t.Errorf("defaults: %d events", e.Size())
	}
	e = ProxyEvents{Entries: 500}
	if e.Size() != 500 {
		t.Errorf("set: %d events", e.Size())
	}
}

// Every log in memory keeps an entry for the one setting under logging, a
// week unless it says otherwise.
func TestLoggingMemoryDays(t *testing.T) {
	t.Parallel()
	var l Logging
	if l.MemoryDays() != 7 || l.MemoryKeep() != 7*24*time.Hour {
		t.Errorf("defaults: %d days, %v", l.MemoryDays(), l.MemoryKeep())
	}
	l.Days = 30
	if l.MemoryDays() != 30 || l.MemoryKeep() != 30*24*time.Hour {
		t.Errorf("set: %d days, %v", l.MemoryDays(), l.MemoryKeep())
	}
	for _, days := range []int{-1, MaxLogDays + 1} {
		cfg := proxyStarter()
		cfg.System.Logging.Days = days
		if !hasIssue(t, cfg, "system.logging.days") {
			t.Errorf("%d days passed", days)
		}
	}
	cfg := proxyStarter()
	cfg.System.Logging.Days = MaxLogDays
	if err := cfg.Validate(); err != nil {
		t.Errorf("the ceiling was refused: %v", err)
	}
}

func TestValidateProxyEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		events ProxyEvents
		path   string
	}{
		{ProxyEvents{Entries: -1}, "services.proxy.events.entries"},
		{ProxyEvents{Entries: MaxProxyEventEntries + 1}, "services.proxy.events.entries"},
	} {
		cfg := proxyStarter()
		cfg.Services.Proxy = workingProxy()
		cfg.Services.Proxy.Events = tc.events
		if !hasIssue(t, cfg, tc.path) {
			t.Errorf("%+v passed", tc.events)
		}
	}
	cfg := proxyStarter()
	cfg.Services.Proxy = workingProxy()
	cfg.Services.Proxy.Events = ProxyEvents{Entries: MaxProxyEventEntries}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the ceilings were refused: %v", err)
	}
}

// The proxy's requests share the newer logs' setting, with their own
// ceiling of entries.
func TestValidateProxyRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		keep LogKeep
		path string
	}{
		{LogKeep{Entries: -1}, "services.proxy.requests.entries"},
		{LogKeep{Entries: MaxRequestEntries + 1}, "services.proxy.requests.entries"},
	} {
		cfg := proxyStarter()
		cfg.Services.Proxy = workingProxy()
		cfg.Services.Proxy.Requests = tc.keep
		if !hasIssue(t, cfg, tc.path) {
			t.Errorf("%+v passed", tc.keep)
		}
	}
	cfg := proxyStarter()
	cfg.Services.DHCP.Log = LogKeep{Entries: MaxDHCPLogEntries + 1}
	if !hasIssue(t, cfg, "services.dhcp.log.entries") {
		t.Error("a DHCP log past its ceiling passed")
	}
	cfg.Wireless.Log = LogKeep{Entries: -1}
	if !hasIssue(t, cfg, "wireless.log.entries") {
		t.Error("a wireless log with negative entries passed")
	}
	cfg.VPN.TailscaleLog = LogKeep{Entries: MaxPeerLogEntries + 1}
	if !hasIssue(t, cfg, "vpn.tailscaleLog.entries") {
		t.Error("a Tailscale log past its ceiling passed")
	}
	var k LogKeep
	if k.Size(7) != 7 || (LogKeep{Entries: 3}).Size(7) != 3 {
		t.Errorf("defaults: %d", k.Size(7))
	}
}

// A log that is off costs nothing, whatever it is set to.
func TestLogsFullBytes(t *testing.T) {
	t.Parallel()
	cfg := proxyStarter()
	cfg.System.Management.FirewallLog.Entries = 1000
	cfg.Services.DNS.QueryLog = QueryLog{Entries: 2000}
	cfg.Services.Proxy = workingProxy()
	cfg.Services.Proxy.Events.Entries = 3000
	want := int64(1000*FirewallLogBytes + 3000*ProxyEventBytes)
	if got := cfg.LogsFullBytes(); got != want {
		t.Errorf("DNS log off: %d, want %d", got, want)
	}
	cfg.Services.DNS.Enabled, cfg.Services.DNS.QueryLog.Enabled = true, true
	cfg.Services.Proxy.Enabled = false
	want = int64(1000*FirewallLogBytes + 2000*QueryLogBytes)
	if got := cfg.LogsFullBytes(); got != want {
		t.Errorf("proxy off: %d, want %d", got, want)
	}
	// The requests cost memory only at the levels that keep them.
	cfg.Services.Proxy.Enabled = true
	cfg.Services.Proxy.Requests.Entries = 4000
	cfg.System.Logging.Level = LogInfo
	want = int64(1000*FirewallLogBytes + 2000*QueryLogBytes + 3000*ProxyEventBytes + 4000*RequestBytes)
	if got := cfg.LogsFullBytes(); got != want {
		t.Errorf("requests kept: %d, want %d", got, want)
	}
}

// The pages quote what a log costs before it is applied, from a copy of
// these figures in web/src/lib/logs.js. The two must agree.
func TestTheLogsPageMirrorsTheModel(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../web/src/lib/logs.js")
	if err != nil {
		t.Fatal(err)
	}
	for log, want := range map[string][3]int{
		"firewall":     {DefaultFirewallLogEntries, MaxFirewallLogEntries, FirewallLogBytes},
		"queries":      {DefaultQueryLogEntries, MaxQueryLogEntries, QueryLogBytes},
		"events":       {DefaultProxyEventEntries, MaxProxyEventEntries, ProxyEventBytes},
		"destinations": {DefaultDestinationEntries, MaxDestinationEntries, DestinationBytes},
		"requests":     {DefaultRequestEntries, MaxRequestEntries, RequestBytes},
		"dhcp":         {DefaultDHCPLogEntries, MaxDHCPLogEntries, DHCPLogBytes},
		"wireless":     {DefaultWirelessLogEntries, MaxWirelessLogEntries, WirelessLogBytes},
		"wireguard":    {DefaultPeerLogEntries, MaxPeerLogEntries, PeerLogBytes},
		"tailscale":    {DefaultPeerLogEntries, MaxPeerLogEntries, PeerLogBytes},
		"discovery":    {DefaultDiscoveryLogEntries, MaxDiscoveryLogEntries, DiscoveryLogBytes},
	} {
		re := regexp.MustCompile(log + `: \{ entries: (\d+), max: (\d+), bytes: (\d+) \}`)
		m := re.FindStringSubmatch(string(raw))
		if m == nil {
			t.Errorf("no %s line in logs.js", log)
			continue
		}
		if got := fmt.Sprintf("%s %s %s", m[1], m[2], m[3]); got != fmt.Sprintf("%d %d %d", want[0], want[1], want[2]) {
			t.Errorf("%s: logs.js has %s, the model %v", log, got, want)
		}
	}
	days := fmt.Sprintf("DAYS = { default: %d, max: %d }", DefaultLogDays, MaxLogDays)
	if !strings.Contains(string(raw), days) {
		t.Errorf("logs.js does not say %s", days)
	}
	files := fmt.Sprintf("FILE_DAYS = { default: %d, max: %d }", DefaultLogFileDays, MaxLogFileDays)
	if !strings.Contains(string(raw), files) {
		t.Errorf("logs.js does not say %s", files)
	}
}

func TestLogFilesDefaults(t *testing.T) {
	t.Parallel()
	var f LogFiles
	if f.Interval() != 5*time.Minute || f.Days() != 30 || f.MaxUse() != 1<<30 {
		t.Errorf("defaults: every %v, %d days, %d bytes", f.Interval(), f.Days(), f.MaxUse())
	}
	f = LogFiles{WriteMinutes: 60, RetentionDays: 365, MaxUseGB: 3}
	if f.Interval() != time.Hour || f.Days() != 365 || f.MaxUse() != 3<<30 {
		t.Errorf("set: every %v, %d days, %d bytes", f.Interval(), f.Days(), f.MaxUse())
	}
}

func TestValidateLogFiles(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files LogFiles
		path  string
	}{
		{LogFiles{WriteMinutes: 2}, "system.logging.files.writeMinutes"},
		{LogFiles{WriteMinutes: -1}, "system.logging.files.writeMinutes"},
		{LogFiles{RetentionDays: -1}, "system.logging.files.retentionDays"},
		{LogFiles{RetentionDays: MaxLogFileDays + 1}, "system.logging.files.retentionDays"},
		{LogFiles{MaxUseGB: -1}, "system.logging.files.maxUseGB"},
		{LogFiles{MaxUseGB: MaxLogFilesGB + 1}, "system.logging.files.maxUseGB"},
	} {
		cfg := proxyStarter()
		cfg.System.Logging.Files = tc.files
		if !hasIssue(t, cfg, tc.path) {
			t.Errorf("%+v passed", tc.files)
		}
	}
	for _, minutes := range WriteEvery {
		cfg := proxyStarter()
		cfg.System.Logging.Files = LogFiles{Enabled: true, WriteMinutes: minutes, RetentionDays: MaxLogFileDays, MaxUseGB: MaxLogFilesGB}
		if err := cfg.Validate(); err != nil {
			t.Errorf("every %d minutes refused: %v", minutes, err)
		}
	}
	// Operators may switch them on: the files are no admin's alone.
	old, next := proxyStarter(), proxyStarter()
	next.System.Logging.Files = LogFiles{Enabled: true, WriteMinutes: 1}
	if got := AdminChanges(old, next); len(got) != 0 {
		t.Errorf("admin changes = %v", got)
	}
	// A configuration that never set them writes no block.
	raw, err := json.Marshal(old.System.Logging)
	if err != nil || strings.Contains(string(raw), "files") {
		t.Errorf("logging = %s, %v", raw, err)
	}
}

// Counting per device is off unless a configuration says otherwise, and a
// configuration that never said writes no block.
func TestTrafficIsOffByDefault(t *testing.T) {
	t.Parallel()
	cfg := proxyStarter()
	if cfg.Traffic.Devices {
		t.Error("a new router counts devices")
	}
	raw, err := json.Marshal(cfg)
	if err != nil || strings.Contains(string(raw), `"traffic"`) {
		t.Errorf("config = %s, %v", raw, err)
	}
	cfg.Traffic.Devices = true
	if err := cfg.Validate(); err != nil {
		t.Errorf("counting devices refused: %v", err)
	}
}

// Destinations belong to devices, and are bounded as the other logs are.
func TestValidateTrafficDestinations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		traffic Traffic
		path    string
	}{
		{Traffic{Destinations: TrafficDestinations{Enabled: true}}, "traffic.destinations.enabled"},
		{Traffic{Devices: true, Destinations: TrafficDestinations{Entries: -1}}, "traffic.destinations.entries"},
		{Traffic{Devices: true, Destinations: TrafficDestinations{Entries: MaxDestinationEntries + 1}}, "traffic.destinations.entries"},
	} {
		cfg := proxyStarter()
		cfg.Traffic = tc.traffic
		if !hasIssue(t, cfg, tc.path) {
			t.Errorf("%+v passed", tc.traffic)
		}
	}
	cfg := proxyStarter()
	cfg.Traffic = Traffic{Devices: true, Destinations: TrafficDestinations{Enabled: true, Entries: MaxDestinationEntries}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the ceilings were refused: %v", err)
	}
	if got, want := cfg.LogsFullBytes(), int64(DefaultFirewallLogEntries*FirewallLogBytes+MaxDestinationEntries*DestinationBytes); got != want {
		t.Errorf("logs full = %d, want %d", got, want)
	}
}
