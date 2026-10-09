package model

import (
	"slices"
	"testing"
)

const oneGB = 950_000_000

func memoryStarter() *Config {
	return Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
}

func serving() Proxy {
	return Proxy{Enabled: true, Sites: []ProxySite{{ID: "shop", Enabled: true}}}
}

func TestReserve(t *testing.T) {
	t.Parallel()
	var b MemoryBudget
	bare := &Config{}
	if got := b.Reserve(bare); got != 525_000_000 {
		t.Errorf("bare: %d", got)
	}

	blocking := memoryStarter()
	blocking.Blocking.Enabled = true
	if got := b.Reserve(blocking); got != 525_000_000+115*DefaultBlockedDomains {
		t.Errorf("blocking at the default ceiling: %d", got)
	}
	blocking.Blocking.MaxDomains = 2_000_000
	if got := b.Reserve(blocking); got != 525_000_000+230_000_000 {
		t.Errorf("blocking at 2,000,000: %d", got)
	}
	blocking.Services.DNS.Enabled = false
	if got := b.Reserve(blocking); got != 525_000_000 {
		t.Errorf("blocking without the DNS server: %d", got)
	}

	proxy := &Config{}
	proxy.Services.Proxy = serving()
	if got := b.Reserve(proxy); got != 525_000_000+100_000_000+22_000_000 {
		t.Errorf("proxy: %d", got)
	}

	dhcp := &Config{}
	dhcp.Services.DHCP.Enabled = true
	if got := b.Reserve(dhcp); got != 525_000_000 {
		t.Errorf("DHCP without records: %d", got)
	}
	dhcp.System.Logging.Level = LogInfo
	if got := b.Reserve(dhcp); got != 525_000_000+22_000_000 {
		t.Errorf("DHCP with records: %d", got)
	}
}

func TestLogsFloorsAtZero(t *testing.T) {
	t.Parallel()
	if got := (MemoryBudget{Total: 100_000_000}).Logs(&Config{}); got != 0 {
		t.Errorf("tiny: %d", got)
	}
	if got := (MemoryBudget{Total: oneGB}).Logs(&Config{}); got != 425_000_000 {
		t.Errorf("1 GB: %d", got)
	}
}

func TestCeiling(t *testing.T) {
	t.Parallel()
	c := &Config{}
	if got := (MemoryBudget{Total: 100_000_000}).Ceiling(c, QueryLogBytes, MaxQueryLogEntries); got != MinLogCeiling {
		t.Errorf("tiny: %d", got)
	}
	if got := (MemoryBudget{Total: 100_000_000}).Ceiling(c, QueryLogBytes, 10); got != 10 {
		t.Errorf("below the floor: %d", got)
	}
	if got := (MemoryBudget{Total: 1 << 50}).Ceiling(c, QueryLogBytes, MaxQueryLogEntries); got != MaxQueryLogEntries {
		t.Errorf("huge: %d", got)
	}
	if got := (MemoryBudget{}).Ceiling(c, QueryLogBytes, MaxQueryLogEntries); got != MaxQueryLogEntries {
		t.Errorf("unknown: %d", got)
	}
	if got := (MemoryBudget{Total: oneGB}).Ceiling(c, QueryLogBytes, MaxQueryLogEntries); got != 1_888_888 {
		t.Errorf("1 GB: %d", got)
	}
}

func TestCeilings(t *testing.T) {
	t.Parallel()
	got := MemoryBudget{Total: oneGB}.Ceilings(memoryStarter())
	if got.QueryLog != 1_888_888 || got.FirewallLog != 809_523 || got.PeerLog != MaxPeerLogEntries {
		t.Errorf("1 GB: %+v", got)
	}
}

func TestMemoryCheckRefusesALogAboveItsCeiling(t *testing.T) {
	t.Parallel()
	c := memoryStarter()
	c.Blocking.Enabled = true
	c.Services.Proxy = serving()
	c.Services.DNS.QueryLog = QueryLog{Enabled: true, Entries: 1_000_000}
	issues := MemoryBudget{Total: oneGB}.Check(c)
	want := Issue{Path: "services.dns.queryLog.entries", Message: "this router allows up to 835555"}
	if !slices.Contains(issues, want) {
		t.Errorf("issues = %v", issues)
	}
	if issues := (MemoryBudget{}).Check(c); issues != nil {
		t.Errorf("unknown memory: %v", issues)
	}
}

func TestMemoryCheckRefusesTheLogsTogether(t *testing.T) {
	t.Parallel()
	c := memoryStarter()
	c.Services.DNS.QueryLog = QueryLog{Enabled: true, Entries: 1_000_000}
	c.System.Management.FirewallLog.Entries = 400_000
	b := MemoryBudget{Total: oneGB}
	msg := "the logs come to 435 MB at their largest; this router has 425 MB for them"
	want := []Issue{{Path: "system.management.firewallLog.entries", Message: msg}, {Path: "services.dns.queryLog.entries", Message: msg}}
	if got := b.Check(c); !slices.Equal(got, want) {
		t.Errorf("issues = %v", got)
	}
	c.System.Management.FirewallLog.Entries = 300_000
	if got := b.Check(c); got != nil {
		t.Errorf("fitting: %v", got)
	}
}

func TestMemoryCheckFallsBackToTheFirewallLog(t *testing.T) {
	t.Parallel()
	c := &Config{}
	c.System.Management.FirewallLog.Entries = 40_000
	got := MemoryBudget{Total: 530_000_000}.Check(c)
	want := []Issue{
		{Path: "system.management.firewallLog.entries", Message: "this router allows up to 9523"},
		{Path: "system.management.firewallLog.entries", Message: "the logs come to 21.0 MB at their largest; this router has 5.0 MB for them"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("issues = %v", got)
	}
}

func TestLogsFullBytesCountsTheQueryLogReadBack(t *testing.T) {
	t.Parallel()
	c := &Config{}
	c.Services.DNS.QueryLog = QueryLog{Enabled: true, Entries: 2000}
	fw := int64(DefaultFirewallLogEntries * FirewallLogBytes)
	if got := c.LogsFullBytes(); got != fw {
		t.Errorf("DNS and files off: %d", got)
	}
	c.System.Logging.Files.Enabled = true
	if got := c.LogsFullBytes(); got != fw+2000*QueryLogBytes {
		t.Errorf("files on: %d", got)
	}
}
