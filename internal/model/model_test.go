package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestStarterValidates(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("starter config invalid: %v", err)
	}
	if got := cfg.ZoneInterfaces("lan"); len(got) != 1 || got[0] != "eth1" {
		t.Errorf("ZoneInterfaces(lan) = %v", got)
	}
	// Every router starts in UTC, and says so rather than leaving it to be
	// inferred from an empty field.
	if cfg.System.Timezone != "UTC" {
		t.Errorf("starter timezone = %q, want UTC", cfg.System.Timezone)
	}
}

func TestSystemZone(t *testing.T) {
	t.Parallel()
	// A configuration written before the setting existed runs in UTC.
	if got := (System{}).Zone(); got != "UTC" {
		t.Errorf("Zone() with nothing set = %q", got)
	}
	if got := (System{}).Location(); got != time.UTC {
		t.Errorf("Location() with nothing set = %v", got)
	}
	s := System{Timezone: "Europe/Berlin"}
	if got := s.Zone(); got != "Europe/Berlin" {
		t.Errorf("Zone() = %q", got)
	}
	if got := s.Location().String(); got != "Europe/Berlin" {
		t.Errorf("Location() = %q", got)
	}
}

func TestValidateTimezone(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24"})
	cfg.System.Timezone = "Mars/Olympus"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("want an error for a zone that does not exist")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || !hasPath(ve, "system.timezone") {
		t.Errorf("issues = %v", err)
	}
	// Empty is the default rather than a mistake.
	cfg.System.Timezone = ""
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty timezone rejected: %v", err)
	}
}

func hasPath(ve *ValidationError, path string) bool {
	for _, i := range ve.Issues {
		if i.Path == path {
			return true
		}
	}
	return false
}

func TestParsePortRange(t *testing.T) {
	t.Parallel()
	good := []struct {
		in   string
		want PortRange
	}{
		{"443", PortRange{443, 443}},
		{" 8000-8100 ", PortRange{8000, 8100}},
		{"1-65535", PortRange{1, 65535}},
	}
	for _, tc := range good {
		got, err := ParsePortRange(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParsePortRange(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "0", "65536", "80-70", "a", "80-", "-80", "1-2-3"} {
		if _, err := ParsePortRange(in); err == nil {
			t.Errorf("ParsePortRange(%q) succeeded, want error", in)
		}
	}
}

func TestParseAddress(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"10.0.0.1":          "10.0.0.1/32",
		"10.0.0.7/8":        "10.0.0.0/8",
		"2001:db8::1":       "2001:db8::1/128",
		"2001:db8::ffff/32": "2001:db8::/32",
	}
	for in, want := range cases {
		got, err := ParseAddress(in)
		if err != nil || got.String() != want {
			t.Errorf("ParseAddress(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "10.0.0.256", "10.0.0.0/33", "host.example", "10.0.0.1/-1"} {
		if _, err := ParseAddress(in); err == nil {
			t.Errorf("ParseAddress(%q) succeeded, want error", in)
		}
	}
}

func TestValidateCatchesEverything(t *testing.T) {
	t.Parallel()
	cfg := &Config{
		Version: 99,
		System:  System{Hostname: "bad host", DNSServers: []string{"nope"}, KeepRevisions: -1},
		Zones:   []Zone{{Name: "lan"}, {Name: "lan"}, {Name: "Bad-Zone"}},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "nozone", IPv4: IPv4{Mode: AddrStatic}, IPv6: IPv6{Mode: "magic"}},
			{Name: "eth0", IPv4: IPv4{Mode: AddrDHCP, Address: "1.2.3.4/24", Gateway: "::1"}, IPv6: IPv6{Mode: AddrStatic, Address: "10.0.0.1/24"}},
			{Name: "this-name-is-way-too-long", IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}, VLAN: &VLAN{Parent: "this-name-is-way-too-long", ID: 5000}, MTU: 10},
		},
		Aliases: []Alias{
			{Name: "hosts", Type: AliasHosts, Entries: []string{"1.2.3.4", "garbage"}},
			{Name: "ports", Type: AliasPorts, Entries: []string{"80", "99999"}},
			{Name: "hosts", Type: "weird"},
		},
		Rules: []Rule{
			{ID: "r1", Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny,
				Source:      Endpoint{Self: true, Ports: []string{"80"}},
				Destination: Endpoint{Addresses: []string{"1.1.1.1"}, Alias: "hosts", Ports: []string{"1"}, PortAlias: "ports"}},
			{ID: "r1", Zone: "missing", DestZone: "missing", Action: "explode", Protocol: "sctp",
				Source: Endpoint{Alias: "ports"}, Destination: Endpoint{PortAlias: "hosts", Self: true, Alias: "hosts"}},
			{ID: "bad id!", Zone: "lan", Action: ActionDrop, Protocol: ProtocolTCP, Destination: Endpoint{Ports: []string{"x"}}},
		},
		NAT: NAT{
			Outbound: OutboundNAT{Mode: "sometimes", Rules: []OutboundRule{{ID: "o1", Zone: "nope", Source: []string{"bad"}}}},
			PortForwards: []PortForward{
				{ID: "pf1", Zone: "nope", Protocol: ProtocolICMP, Target: "not-an-ip", TargetPort: "1-2"},
				{ID: "pf1", Zone: "lan", Protocol: ProtocolTCP, Ports: []string{"bad"}, Target: "10.0.0.1", TargetPort: "x"},
			},
		},
		Routes: []StaticRoute{
			{ID: "rt1", Destination: "10.0.0.0/8", Gateway: "2001:db8::1", Interface: "ghost"},
			{ID: "rt1", Destination: "bad", Gateway: "bad"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	wantPaths := []string{
		"version",
		"system.hostname",
		"system.dnsServers[0]",
		"system.keepRevisions",
		"zones[1].name",
		"zones[2].name",
		"interfaces[0].zone",
		"interfaces[0].ipv4.address",
		"interfaces[0].ipv6.mode",
		"interfaces[1].name",
		"interfaces[1].ipv4.address",
		"interfaces[1].ipv4.gateway",
		"interfaces[1].ipv6.address",
		"interfaces[2].name",
		"interfaces[2].vlan.parent",
		"interfaces[2].vlan.id",
		"interfaces[2].mtu",
		"aliases[0].entries[1]",
		"aliases[1].entries[1]",
		"aliases[2].name",
		"aliases[2].type",
		"rules[0].source.self",
		"rules[0].source",
		"rules[0].destination",
		"rules[1].id",
		"rules[1].zone",
		"rules[1].destZone",
		"rules[1].action",
		"rules[1].protocol",
		"rules[1].source.alias",
		"rules[1].destination.portAlias",
		"rules[1].destination.self",
		"rules[2].id",
		"rules[2].destination.ports[0]",
		"nat.outbound.mode",
		"nat.outbound.rules[0].zone",
		"nat.outbound.rules[0].source[0]",
		"nat.portForwards[0].zone",
		"nat.portForwards[0].protocol",
		"nat.portForwards[0].ports",
		"nat.portForwards[0].target",
		"nat.portForwards[0].targetPort",
		"nat.portForwards[1].id",
		"nat.portForwards[1].ports[0]",
		"nat.portForwards[1].targetPort",
		"routes[0].gateway",
		"routes[0].interface",
		"routes[1].id",
		"routes[1].destination",
		"routes[1].gateway",
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range wantPaths {
		if !got[p] {
			t.Errorf("missing issue at %s", p)
		}
	}
	if !strings.Contains(err.Error(), "issue(s)") {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestValidateRejectsNoZones(t *testing.T) {
	t.Parallel()
	cfg := &Config{Version: SchemaVersion, NAT: NAT{Outbound: OutboundNAT{Mode: OutboundDisabled}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "zones: at least one zone") {
		t.Fatalf("err = %v", err)
	}
}

func TestStarterServicesAndDefaultPool(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	if err := cfg.Validate(); err != nil {
		t.Fatalf("starter with services invalid: %v", err)
	}
	sc := cfg.Services.DHCP.Servers
	if len(sc) != 1 || sc[0].RangeStart != "192.168.1.100" || sc[0].RangeEnd != "192.168.1.199" {
		t.Errorf("server = %+v", sc)
	}
	if !cfg.Services.DNS.Enabled || len(cfg.Services.DNS.Upstreams) != 2 {
		t.Errorf("dns = %+v", cfg.Services.DNS)
	}
	cases := map[string][2]string{
		"10.0.0.1/16":      {"10.0.0.100", "10.0.0.199"},
		"192.168.5.1/26":   {"192.168.5.32", "192.168.5.62"},
		"192.168.5.1/28":   {"192.168.5.8", "192.168.5.14"},
		"192.168.5.150/24": {"", ""}, // the router sits inside the pool
		"192.168.5.1/30":   {"", ""},
		"2001:db8::1/64":   {"", ""},
	}
	for in, want := range cases {
		s, e, ok := DefaultPool(in)
		if ok != (want[0] != "") || s != want[0] || e != want[1] {
			t.Errorf("DefaultPool(%q) = %q, %q, %v; want %v", in, s, e, ok, want)
		}
	}
}

func TestValidateServices(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Services = Services{
		DHCP: DHCPService{Enabled: true,
			Servers: []DHCPServer{
				{Interface: "eth1", Enabled: true, RangeStart: "192.168.2.10", RangeEnd: "192.168.1.5", LeaseTime: "soon", Gateway: "10.0.0.1", DNS: []string{"bad"}, Domain: "-x"},
				{Interface: "eth0", Enabled: true, RangeStart: "1.1.1.1", RangeEnd: "1.1.1.2"},
				{Interface: "ghost", Enabled: true},
			},
			StaticLeases: []StaticLease{{MAC: "nope", IP: "2001:db8::1", Hostname: "bad host"}, {MAC: "AA:bb:cc:dd:ee:ff", IP: "192.168.1.9"}, {MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}},
		},
		DNS: DNSServer{Enabled: true, Interfaces: []string{"nope"}, Upstreams: []string{"x"}, Domain: "bad domain", HostOverrides: []HostOverride{{Hostname: "a", IP: "1.1.1.1"}, {Hostname: "A", IP: "x"}, {Hostname: "-", IP: "1.1.1.1"}}},
	}
	err := cfg.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{
		"services.dhcp.servers[0].rangeStart", "services.dhcp.servers[0].leaseTime", "services.dhcp.servers[0].gateway",
		"services.dhcp.servers[0].dns[0]", "services.dhcp.servers[0].domain",
		"services.dhcp.servers[1].interface", "services.dhcp.servers[2].interface",
		"services.dhcp.staticLeases[0].mac", "services.dhcp.staticLeases[0].ip", "services.dhcp.staticLeases[0].hostname",
		"services.dhcp.staticLeases[2].mac",
		"services.dns.interfaces[0]", "services.dns.upstreams[0]", "services.dns.domain",
		"services.dns.hostOverrides[1].hostname", "services.dns.hostOverrides[1].ip", "services.dns.hostOverrides[2].hostname",
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}
	cfg.Services.DNS = DNSServer{Enabled: true}
	cfg.Services.DHCP = DHCPService{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("dns without upstreams accepted: %v", err)
	}
}

func TestValidateDomainOverrides(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Services.DNS = DNSServer{Enabled: true, Upstreams: []string{"1.1.1.1"}, Domain: "lan",
		DomainOverrides: []DomainOverride{
			{Domain: "ts.net", Servers: []string{"100.100.100.100"}},
			{Domain: "TS.net.", Servers: []string{"100.100.100.100"}}, // the same domain, written another way
			{Domain: "lan", Servers: []string{"10.0.0.53"}},           // ours to answer
			{Domain: "corp.example", Servers: []string{"10.0.0.53#0"}},
			{Domain: "no domain at all", Servers: nil},
		}}
	err := cfg.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v", err)
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{
		"services.dns.domainOverrides[1].domain",
		"services.dns.domainOverrides[2].domain",
		"services.dns.domainOverrides[3].servers[0]",
		"services.dns.domainOverrides[4].domain",
		"services.dns.domainOverrides[4].servers",
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}

	// What a tailnet needs, a resolver on another port, and a delegation of
	// one name under the local domain are all fine.
	cfg.Services.DNS.DomainOverrides = []DomainOverride{
		{Domain: "ts.net", Servers: []string{"100.100.100.100"}},
		{Domain: "corp.example", Servers: []string{"10.0.0.53#5353", "2001:db8::53"}},
		{Domain: "sub.lan", Servers: []string{"10.0.0.53"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("good overrides were refused: %v", err)
	}
}

func TestValidateDHCPv6(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Interfaces[0].IPv6 = IPv6{Mode: AddrStatic, Address: "2001:db8::1/64"}
	cfg.Services.DHCP = DHCPService{Enabled: true, V6: []DHCPv6Server{
		{Interface: "eth1", Enabled: true, Mode: RAManaged, RangeStart: "::1ff", RangeEnd: "::100", LeaseTime: "soon", DNS: []string{"192.168.1.1"}, Domain: "-x"},
		{Interface: "eth1", Enabled: true, Mode: RASLAAC, RangeStart: "::100"},
		{Interface: "eth0", Enabled: true, Mode: RASLAAC},
		{Interface: "ghost", Enabled: true, Mode: RASLAAC},
		{Interface: "eth1", Enabled: true, Mode: "wat"},
	}}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{
		"services.dhcp.v6[0].rangeEnd", "services.dhcp.v6[0].leaseTime", "services.dhcp.v6[0].dns[0]",
		"services.dhcp.v6[0].domain", "services.dhcp.v6[1].interface", "services.dhcp.v6[1].mode",
		"services.dhcp.v6[3].interface", "services.dhcp.v6[4].mode",
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}

	// eth0 (the WAN, SLAAC) raised nothing: advertising there is legal, if
	// unusual. A single sane server must validate too.
	cfg.Services.DHCP.V6 = []DHCPv6Server{{Interface: "eth1", Enabled: true, Mode: RAManaged, RangeStart: "::100", RangeEnd: "::1ff"}}
	cfg.Services.DNS = DNSServer{Enabled: true, Upstreams: []string{"1.1.1.1"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid IPv6 server rejected: %v", err)
	}
}

func TestValidateStaticLeaseAddresses(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24"})
	cfg.Services.DHCP = DHCPService{Enabled: true, StaticLeases: []StaticLease{
		{MAC: "aa:bb:cc:dd:ee:01"},
		{MAC: "aa:bb:cc:dd:ee:02", IPv6: "::20"},
		{MAC: "aa:bb:cc:dd:ee:03", IPv6: "192.168.1.9"},
		{MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.1.9", IPv6: "2001:db8::9"},
	}}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	if !got["services.dhcp.staticLeases[0].ip"] || !got["services.dhcp.staticLeases[2].ipv6"] {
		t.Errorf("issues = %v", ve.Issues)
	}
	if got["services.dhcp.staticLeases[1].ipv6"] || got["services.dhcp.staticLeases[3].ipv6"] {
		t.Errorf("valid leases rejected: %v", ve.Issues)
	}
}

func TestValidateUPnP(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Services.UPnP = UPnP{
		Enabled:           true,
		ExternalInterface: "eth1", // the LAN: a port opened there reaches nothing
		Interfaces:        []string{"ghost", "eth1"},
		ACL: []UPnPRule{
			{Action: "maybe", ExternalPorts: "nope", Source: "2001:db8::/32", InternalPorts: "70000"},
			{Action: "allow", ExternalPorts: "1024-65535", Source: "192.168.1.0/24", InternalPorts: "1024-65535"},
		},
	}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{
		"services.upnp.enabled",           // neither protocol switched on
		"services.upnp.externalInterface", // not in an external zone
		"services.upnp.interfaces[0]",     // unknown
		"services.upnp.interfaces[1]",     // is the external interface
		"services.upnp.acl[0].action", "services.upnp.acl[0].externalPorts",
		"services.upnp.acl[0].internalPorts", "services.upnp.acl[0].source",
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}
	if got["services.upnp.acl[1].source"] {
		t.Errorf("a valid access list entry was rejected: %v", ve.Issues)
	}

	// The same configuration made right.
	cfg.Services.UPnP = UPnP{
		Enabled: true, IGD: true, PCP: true, ExternalInterface: "eth0",
		Interfaces: []string{"eth1"}, DefaultDeny: true,
		ACL: []UPnPRule{{Action: "allow", ExternalPorts: "1024-65535", Source: "192.168.1.0/24", InternalPorts: "1024-65535"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid mapping service rejected: %v", err)
	}

	// A typo in an interface name is caught whether or not the service is
	// switched on, so turning it on later does not fail on something that
	// was written down long before.
	cfg.Services.UPnP = UPnP{ExternalInterface: "ghost"}
	if !errors.As(cfg.Validate(), &ve) {
		t.Error("an unknown external interface passed while the service was off")
	}

	// Off and unwritten is the default, and it has to validate.
	cfg.Services.UPnP = UPnP{}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the default (off) was rejected: %v", err)
	}
}

func TestValidateSchedulesAndOneToOne(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Schedules = []Schedule{
		{Name: "work", Days: []string{"monday", "caturday"}, Start: "08:00", End: "17:00"},
		{Name: "work", Start: "25:00", End: "17:60"},
		{Name: "same", Start: "09:00", End: "09:00"},
	}
	cfg.Rules = append(cfg.Rules, Rule{
		ID: "scheduled", Enabled: true, Zone: "lan", Action: ActionDrop,
		Protocol: ProtocolAny, Schedule: "ghost",
	})
	cfg.NAT.OneToOne = []OneToOneNAT{
		{ID: "one", Enabled: true, Zone: "lan", External: "203.0.113.5", Internal: "10.0.0.5"},
		{ID: "two", Enabled: true, Zone: "ghost", External: "nope", Internal: "10.0.0.6"},
		{ID: "three", Enabled: true, Zone: "wan", External: "203.0.113.7", Internal: "2001:db8::7"},
	}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{
		"schedules[0].days[1]", "schedules[1].name", "schedules[1].start", "schedules[1].end",
		"schedules[2].end", "rules[1].schedule",
		"nat.oneToOne[0].zone", "nat.oneToOne[1].zone", "nat.oneToOne[1].external",
		"nat.oneToOne[2].internal",
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}

	// A sane schedule, a rule using it, and a 1:1 mapping on the WAN.
	cfg.Schedules = []Schedule{{Name: "work", Days: []string{"monday"}, Start: "08:00", End: "17:00"}}
	cfg.Rules[len(cfg.Rules)-1].Schedule = "work"
	cfg.NAT.OneToOne = []OneToOneNAT{{ID: "one", Enabled: true, Zone: "wan", External: "203.0.113.5", Internal: "10.0.0.5"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid schedule and mapping rejected: %v", err)
	}
}

func TestParseClock(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"00:00": 0, "08:30": 510, "23:59": 1439} {
		got, err := ParseClock(in)
		if err != nil || got != want {
			t.Errorf("ParseClock(%q) = %d, %v; want %d", in, got, err, want)
		}
		if back := Clock(want); back != in {
			t.Errorf("Clock(%d) = %q, want %q", want, back, in)
		}
	}
	for _, in := range []string{"", "8", "24:00", "08:60", "-1:00", "aa:bb"} {
		if _, err := ParseClock(in); err == nil {
			t.Errorf("ParseClock(%q) succeeded", in)
		}
	}
	if d, ok := Weekday("  MONDAY "); !ok || d != "Monday" {
		t.Errorf("Weekday = %q, %v", d, ok)
	}
	if _, ok := Weekday("caturday"); ok {
		t.Error("Weekday accepted a nonsense day")
	}
}

func policyConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "lan"}, {Name: "wan", External: true}},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}, IPv6: IPv6{Mode: AddrNone}},
			{Name: "eth1", Zone: "lan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"}, IPv6: IPv6{Mode: AddrNone}},
		},
		Gateways: []Gateway{{Name: "wan1", Enabled: true, Interface: "eth0"}},
		GatewayGroups: []GatewayGroup{
			{Name: "both", Enabled: true, Members: []GatewayMember{{Gateway: "wan1"}}},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
	}
}

func TestValidateInvertedEndpoints(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Aliases = []Alias{
		{Name: "office", Type: AliasHosts, Entries: []string{"203.0.113.0/24"}},
		{Name: "dns_ports", Type: AliasPorts, Entries: []string{"53", "853"}},
	}
	cfg.Rules = []Rule{
		{
			ID: "not-office", Enabled: true, Zone: "wan", Action: ActionDrop, Protocol: ProtocolAny,
			Source: Endpoint{Alias: "office", NotAddresses: true},
		},
		{
			// The rule that forces LAN clients onto this resolver.
			ID: "local-dns", Enabled: true, Zone: "lan", Action: ActionReject, Protocol: ProtocolTCPUDP,
			Destination: Endpoint{Self: true, NotAddresses: true, PortAlias: "dns_ports"},
		},
		{
			ID: "not-ports", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolTCP,
			Destination: Endpoint{Ports: []string{"25"}, NotPorts: true},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("inverted rules rejected: %v", err)
	}

	// An inversion with nothing behind it would quietly match everything.
	cfg.Rules = []Rule{
		{
			ID: "empty-addr", Enabled: true, Zone: "wan", Action: ActionDrop, Protocol: ProtocolAny,
			Source: Endpoint{NotAddresses: true},
		},
		{
			ID: "empty-ports", Enabled: true, Zone: "wan", Action: ActionDrop, Protocol: ProtocolTCP,
			Destination: Endpoint{NotPorts: true},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an inversion with nothing to invert was accepted")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]bool{}
	for _, i := range ve.Issues {
		got[i.Path] = true
	}
	for _, p := range []string{"rules[0].source.notAddresses", "rules[1].destination.notPorts"} {
		if !got[p] {
			t.Errorf("missing issue for %s; got %v", p, ve.Issues)
		}
	}
}

func TestValidateAcceptsPolicyRouting(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Rules = []Rule{
		{ID: "r1", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny, Gateway: "wan1"},
		{ID: "r2", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny, Gateway: "both"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("policy routing config rejected: %v", err)
	}
}

func TestValidateCatchesPolicyRoutingMistakes(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Rules = []Rule{
		{ID: "unknown", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny, Gateway: "ghost"},
		{ID: "dropped", Enabled: true, Zone: "lan", Action: ActionDrop, Protocol: ProtocolAny, Gateway: "wan1"},
		{ID: "destzone", Enabled: true, Zone: "lan", DestZone: "wan", Action: ActionAccept, Protocol: ProtocolAny, Gateway: "wan1"},
	}
	cfg.GatewayGroups = append(cfg.GatewayGroups,
		GatewayGroup{Name: "both", Enabled: true, Members: []GatewayMember{{Gateway: "wan1"}, {Gateway: "wan1"}}},
		GatewayGroup{Name: "wan1", Members: []GatewayMember{{Gateway: "ghost", Tier: 999}}},
		GatewayGroup{Name: "empty", OnDown: "maybe"},
	)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"rules[0].gateway",
		"rules[1].gateway",
		"rules[2].gateway",
		"gatewayGroups[1].name",
		"gatewayGroups[1].members[1].gateway",
		"gatewayGroups[2].name",
		"gatewayGroups[2].members[0].gateway",
		"gatewayGroups[2].members[0].tier",
		"gatewayGroups[3].members",
		"gatewayGroups[3].onDown",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s", p)
		}
	}
	if msg := got["rules[1].gateway"]; !strings.Contains(msg, "accept") {
		t.Errorf("drop rule message = %q, want it to mention accept", msg)
	}
}

func tailscaleConfig() *Config {
	cfg := policyConfig()
	cfg.Zones = append(cfg.Zones, Zone{Name: "tailnet"})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name: TailscaleDevice, Zone: "tailnet", Enabled: true,
		IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
		Tailscale: &Tailscale{
			Port:              41641,
			AdvertiseRoutes:   []string{"192.168.1.0/24"},
			AdvertiseExitNode: true,
		},
	})
	return cfg
}

func TestValidateAcceptsATailscaleNode(t *testing.T) {
	t.Parallel()
	cfg := tailscaleConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("tailscale config rejected: %v", err)
	}
	in, ok := cfg.TailscaleInterface()
	if !ok || in.Name != TailscaleDevice || in.Kind() != KindTailscale {
		t.Errorf("TailscaleInterface() = %+v, %v", in, ok)
	}
}

func TestValidateCatchesTailscaleMistakes(t *testing.T) {
	t.Parallel()
	cfg := tailscaleConfig()
	in := &cfg.Interfaces[len(cfg.Interfaces)-1]
	in.Name = "ts0"
	in.IPv4 = IPv4{Mode: AddrStatic, Address: "10.0.0.1/24"}
	in.MTU = 1280
	in.Tailscale.Port = 80
	in.Tailscale.Hostname = "Not A Label"
	in.Tailscale.LoginServer = "http://control.example"
	in.Tailscale.AdvertiseRoutes = []string{
		"192.168.1.1/24", "100.64.0.0/10", "fd7a:115c:a1e0::/48", "nonsense",
	}
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name: "tailscale1", Zone: "tailnet", Enabled: true,
		IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}, Tailscale: &Tailscale{},
	})

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"interfaces[2].name",
		"interfaces[2].ipv4.mode",
		"interfaces[2].mtu",
		"interfaces[2].tailscale.port",
		"interfaces[2].tailscale.hostname",
		"interfaces[2].tailscale.loginServer",
		"interfaces[2].tailscale.advertiseRoutes[0]",
		"interfaces[2].tailscale.advertiseRoutes[1]",
		"interfaces[2].tailscale.advertiseRoutes[2]",
		"interfaces[2].tailscale.advertiseRoutes[3]",
		"interfaces[3].tailscale",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s; got %v", p, ve.Issues)
		}
	}
	if msg := got["interfaces[2].tailscale.advertiseRoutes[0]"]; !strings.Contains(msg, "192.168.1.0/24") {
		t.Errorf("host-bits message = %q, want the masked prefix", msg)
	}
}

// wirelessConfig has one radio serving two networks: one bridged into the
// LAN and one with an address of its own in a guest zone.
func wirelessConfig() *Config {
	cfg := policyConfig()
	cfg.Zones = append(cfg.Zones, Zone{Name: "guest"})
	cfg.Interfaces[1] = Interface{Name: "eth1", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}}
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{
			Name: "br-lan", Zone: "lan", Enabled: true,
			IPv4:   IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"},
			IPv6:   IPv6{Mode: AddrNone},
			Bridge: &Bridge{Members: []string{"eth1", "ap0"}},
		},
		Interface{
			Name: "ap0", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Wireless: &WirelessNetwork{
				Radio: "wlp3s0", SSID: "ostiole-lan", Security: SecurityMixed, Passphrase: "correct horse battery",
			},
		},
		Interface{
			Name: "ap1", Zone: "guest", Enabled: true,
			IPv4: IPv4{Mode: AddrStatic, Address: "10.99.0.1/24"},
			IPv6: IPv6{Mode: AddrNone},
			Wireless: &WirelessNetwork{
				Radio: "wlp3s0", SSID: "ostiole-guest", Security: SecurityWPA3,
				Passphrase: "guests get their own", Isolate: true,
			},
		},
	)
	cfg.Wireless = Wireless{
		Country: "US",
		Radios: []Radio{
			{Name: "wlp3s0", Enabled: true, Band: Band5G, Channel: 36, Width: 80, Standard: StandardAX},
		},
	}
	return cfg
}

func TestValidateAcceptsWirelessNetworks(t *testing.T) {
	t.Parallel()
	cfg := wirelessConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("wireless config rejected: %v", err)
	}
	if k := cfg.Interfaces[3].Kind(); k != KindWireless {
		t.Errorf("Kind() = %q, want %q", k, KindWireless)
	}
	nets := cfg.NetworksOn("wlp3s0")
	if len(nets) != 2 || nets[0].Name != "ap0" || nets[1].Name != "ap1" {
		t.Errorf("NetworksOn = %+v, want ap0 then ap1", nets)
	}
	if got := cfg.ActiveRadios(); len(got) != 1 || !cfg.WirelessEnabled() {
		t.Errorf("ActiveRadios = %+v", got)
	}
	cfg.Interfaces[3].Enabled = false
	cfg.Interfaces[4].Enabled = false
	if cfg.WirelessEnabled() {
		t.Error("a radio with no network is still active")
	}
}

func TestValidateWantsACountryBeforeTransmitting(t *testing.T) {
	t.Parallel()
	cfg := wirelessConfig()
	cfg.Wireless.Country = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	if !strings.Contains(err.Error(), "wireless.country") {
		t.Errorf("error = %v, want the country", err)
	}
}

func TestValidateCatchesWirelessMistakes(t *testing.T) {
	t.Parallel()
	cfg := wirelessConfig()
	cfg.Wireless.Country = "usa"
	cfg.Wireless.Radios = append(cfg.Wireless.Radios,
		Radio{Name: "wlp3s0", Enabled: true, Band: Band5G, Width: 80, Standard: StandardAX},
		Radio{Name: "eth0", Enabled: true, Band: Band2G, Width: 20, Standard: StandardAC},
		Radio{Name: "wlp4s0", Enabled: true, Band: Band5G, Channel: 52, Width: 160, Standard: StandardAX, Power: 40},
		Radio{Name: "wlp5s0", Enabled: true, Band: Band2G, Channel: 20, Width: 160, Standard: StandardN},
		Radio{Name: "wlp6s0", Enabled: true, Band: "7g", Width: 20, Standard: StandardN},
		Radio{Name: "wlp7s0", Enabled: true, Band: Band6G, Channel: 5, Width: 80, Standard: StandardAC},
	)
	cfg.Interfaces[3].Wireless.SSID = strings.Repeat("x", 33)
	cfg.Interfaces[3].Wireless.Passphrase = "short"
	cfg.Interfaces[3].Wireless.MaxClients = 5000
	cfg.Interfaces[4].Wireless.Security = "wep"
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{
			Name: "ap2", Zone: "guest", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Wireless: &WirelessNetwork{
				Radio: "nosuch", SSID: "orphan", Security: SecurityOpen, Passphrase: "not here",
			},
		},
		Interface{
			Name: "wlp4s0", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Wireless: &WirelessNetwork{Radio: "wlp4s0", SSID: "on the anchor", Security: SecurityOpen},
		},
		Interface{
			Name: "ap3", Zone: "guest", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Wireless: &WirelessNetwork{
				Radio: "wlp7s0", SSID: "six\x01ghz", Security: SecurityMixed, Passphrase: "correct horse battery",
			},
		},
	)

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"wireless.country",
		"wireless.radios[1].name",
		"wireless.radios[2].name",
		"wireless.radios[2].standard",
		"wireless.radios[3].name",
		"wireless.radios[3].channel",
		"wireless.radios[3].power",
		"wireless.radios[4].channel",
		"wireless.radios[4].width",
		"wireless.radios[5].band",
		"wireless.radios[6].standard",
		"interfaces[3].wireless.ssid",
		"interfaces[3].wireless.passphrase",
		"interfaces[3].wireless.maxClients",
		"interfaces[4].wireless.security",
		"interfaces[5].wireless.radio",
		"interfaces[5].wireless.passphrase",
		"interfaces[6].name",
		"interfaces[7].wireless.ssid",
		"interfaces[7].wireless.security",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s; got %v", p, ve.Issues)
		}
	}
	if msg := got["wireless.radios[3].channel"]; !strings.Contains(msg, "radar") {
		t.Errorf("channel 52 message = %q, want it to mention radar", msg)
	}
}

func TestPolicyTargetsAreStableAndSkipDisabled(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Gateways = append(cfg.Gateways, Gateway{Name: "alpha", Enabled: true, Interface: "eth0"},
		Gateway{Name: "zulu", Interface: "eth0"})

	targets := cfg.PolicyTargets()
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Name)
	}
	if strings.Join(names, ",") != "alpha,both,wan1" {
		t.Fatalf("targets = %v, want the enabled ones sorted by name", names)
	}
	for i, tg := range targets {
		if want := uint32(i+1) << PolicyMarkShift; tg.Mark != want {
			t.Errorf("%s mark = 0x%x, want 0x%x", tg.Name, tg.Mark, want)
		}
		if want := PolicyTableBase + i + 1; tg.Table != want {
			t.Errorf("%s table = %d, want %d", tg.Name, tg.Table, want)
		}
		if tg.Mark&uint32(PolicyMarkMask) != tg.Mark {
			t.Errorf("%s mark 0x%x escapes the mask", tg.Name, tg.Mark)
		}
	}
	if tg, ok := cfg.PolicyTarget("both"); !ok || !tg.Group {
		t.Errorf("PolicyTarget(both) = %+v, %v; want a group", tg, ok)
	}
	if _, ok := cfg.PolicyTarget("zulu"); ok {
		t.Error("a disabled gateway must not get a mark")
	}
}

func TestPolicyTargetsSkipTheReservedNumbers(t *testing.T) {
	t.Parallel()
	cfg := &Config{Version: SchemaVersion}
	for i := range 10 {
		cfg.Gateways = append(cfg.Gateways, Gateway{
			Name: fmt.Sprintf("gw%02d", i), Enabled: true, Interface: "eth0",
		})
	}

	want := []int{1, 2, 3, 5, 6, 7, 9, 10, 11, 12}
	for i, tg := range cfg.PolicyTargets() {
		if got := int(tg.Mark >> PolicyMarkShift); got != want[i] {
			t.Errorf("%s number = %d, want %d", tg.Name, got, want[i])
		}
		if tg.Table != PolicyTableBase+want[i] {
			t.Errorf("%s table = %d, want %d", tg.Name, tg.Table, PolicyTableBase+want[i])
		}
	}
}

func aggregateConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "lan"}},
		Interfaces: []Interface{
			{Name: "br0", Zone: "lan", Enabled: true,
				IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"}, IPv6: IPv6{Mode: AddrNone},
				Bridge: &Bridge{Members: []string{"eth2", "eth3"}}},
			{Name: "bond0", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
				Bond: &Bond{Members: []string{"eth0", "eth1"}, Mode: BondActiveBackup, Primary: "eth0", MIIMonitorMS: 100}},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundDisabled}},
	}
}

func TestValidateAcceptsBridgesAndBonds(t *testing.T) {
	t.Parallel()
	cfg := aggregateConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("bridge and bond rejected: %v", err)
	}
	br, _ := cfg.Interface("br0")
	if br.Kind() != KindBridge {
		t.Errorf("br0 kind = %q", br.Kind())
	}
	if got := cfg.MasterOf(); got["eth2"] != "br0" || got["eth0"] != "bond0" {
		t.Errorf("MasterOf = %v", got)
	}
}

// A bond inside a bridge is the normal way to build a resilient LAN, so
// that combination must keep working.
func TestValidateAllowsABondInsideABridge(t *testing.T) {
	t.Parallel()
	cfg := aggregateConfig()
	cfg.Interfaces[0].Bridge.Members = []string{"bond0", "eth2"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("bond inside a bridge rejected: %v", err)
	}
}

func TestValidateCatchesAggregationMistakes(t *testing.T) {
	t.Parallel()
	cfg := aggregateConfig()
	cfg.Interfaces = append(cfg.Interfaces,
		// A port cannot have a zone or an address of its own.
		Interface{Name: "eth2", Zone: "lan", Enabled: true,
			IPv4: IPv4{Mode: AddrStatic, Address: "10.0.0.1/24"}, IPv6: IPv6{Mode: AddrNone}},
		// Two masters cannot claim the same port.
		Interface{Name: "br1", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Bridge: &Bridge{Members: []string{"eth3", "eth3", "br1", "br0"}}},
		// A bond needs members, a known mode, and sane knobs.
		Interface{Name: "bond9", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Bond: &Bond{Mode: "round-robin-ish", MIIMonitorMS: 99999,
				TransmitHashPolicy: "layer9", LACPRate: "medium", Primary: "eth7"}},
		// One interface, two kinds.
		Interface{Name: "muddle", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Bridge: &Bridge{Members: []string{"eth8"}}, VLAN: &VLAN{Parent: "eth9", ID: 5}},
	)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"interfaces[2].zone",
		"interfaces[2].ipv4.mode",
		"interfaces[3].bridge.members[1]",
		"interfaces[3].bridge.members[2]",
		"interfaces[3].bridge.members[3]",
		"interfaces[4].bond.members",
		"interfaces[4].bond.mode",
		"interfaces[4].bond.miiMonitorMs",
		"interfaces[4].bond.transmitHashPolicy",
		"interfaces[4].bond.lacpRate",
		"interfaces[4].bond.primary",
		"interfaces[5].kind",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
	if msg := got["interfaces[3].bridge.members[3]"]; !strings.Contains(msg, "bridge") {
		t.Errorf("nesting a bridge = %q", msg)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func delegationConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "wan", External: true}, {Name: "lan"}},
		Interfaces: []Interface{
			{Name: "wan0", Zone: "wan", Enabled: true,
				IPv4: IPv4{Mode: AddrDHCP}, IPv6: IPv6{Mode: AddrDHCP, PrefixHint: "::/56"}},
			{Name: "lan0", Zone: "lan", Enabled: true,
				IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"},
				IPv6: IPv6{Mode: AddrDelegated, DelegatedFrom: "wan0", SubnetID: 1}},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
	}
}

func TestValidateAcceptsPrefixDelegation(t *testing.T) {
	t.Parallel()
	if err := delegationConfig().Validate(); err != nil {
		t.Fatalf("prefix delegation rejected: %v", err)
	}
}

func TestValidateCatchesDelegationMistakes(t *testing.T) {
	t.Parallel()
	cfg := delegationConfig()
	// Two interfaces cannot take the same piece of one prefix, and subnet
	// 500 does not exist in a /56.
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{Name: "lan1", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrDelegated, DelegatedFrom: "wan0", SubnetID: 1}},
		Interface{Name: "lan2", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrDelegated, DelegatedFrom: "wan0", SubnetID: 500}},
		// Nothing upstream is asking for a prefix.
		Interface{Name: "lan3", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrDelegated, DelegatedFrom: "lan0"}},
		Interface{Name: "lan4", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrDelegated}},
		// A hint on an interface that is not asking anyone for anything.
		Interface{Name: "lan5", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrStatic, Address: "2001:db8::1/64", PrefixHint: "::/56"}},
		// A hint that is not a prefix at all.
		Interface{Name: "lan6", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrDHCP, PrefixHint: "10.0.0.0/8"}},
		// Delegation settings on an interface that is not delegated.
		Interface{Name: "lan7", Enabled: true, IPv4: IPv4{Mode: AddrNone},
			IPv6: IPv6{Mode: AddrSLAAC, DelegatedFrom: "wan0"}},
	)
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"interfaces[2].ipv6.subnetId",
		"interfaces[3].ipv6.subnetId",
		"interfaces[4].ipv6.delegatedFrom",
		"interfaces[5].ipv6.delegatedFrom",
		"interfaces[6].ipv6.prefixHint",
		"interfaces[7].ipv6.prefixHint",
		"interfaces[8].ipv6.delegatedFrom",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
	if msg := got["interfaces[3].ipv6.subnetId"]; !strings.Contains(msg, "256") {
		t.Errorf("subnet out of range = %q, want the count of subnets a /56 has", msg)
	}
}

func TestUpdateDefaults(t *testing.T) {
	t.Parallel()
	// A configuration written before updates existed has to come out
	// checking nightly and patching itself weekly, not doing nothing.
	var u Updates
	if got := u.SystemMode(); got != UpdateSecurity {
		t.Errorf("SystemMode = %q, want %q", got, UpdateSecurity)
	}
	if got := u.OstioleMode(); got != UpdateSecurity {
		t.Errorf("OstioleMode = %q, want %q", got, UpdateSecurity)
	}
	if got := u.SystemCheckSchedule(); got != DefaultUpdateCheckSchedule {
		t.Errorf("SystemCheckSchedule = %q, want %q", got, DefaultUpdateCheckSchedule)
	}
	if got := u.OstioleCheckSchedule(); got != DefaultUpdateCheckSchedule {
		t.Errorf("OstioleCheckSchedule = %q, want %q", got, DefaultUpdateCheckSchedule)
	}
	if got := u.SystemInstallSchedule(); got != DefaultUpdateSchedule {
		t.Errorf("SystemInstallSchedule = %q, want %q", got, DefaultUpdateSchedule)
	}
	if got := u.OstioleInstallSchedule(); got != DefaultUpdateSchedule {
		t.Errorf("OstioleInstallSchedule = %q, want %q", got, DefaultUpdateSchedule)
	}
	// The two defaults are different minutes on purpose: a check and an
	// install that collide leave one of them reporting a busy router.
	if DefaultUpdateCheckSchedule == DefaultUpdateSchedule {
		t.Error("the check and the install would land in the same minute")
	}
	if got := u.OstioleChannel(); got != ChannelStable {
		t.Errorf("OstioleChannel = %q, want %q", got, ChannelStable)
	}
	u.System.Mode = UpdateManual
	u.System.CheckSchedule = "@daily"
	u.System.InstallSchedule = "@weekly"
	if got := u.SystemMode(); got != UpdateManual {
		t.Errorf("SystemMode = %q, want %q", got, UpdateManual)
	}
	if got := u.SystemCheckSchedule(); got != "@daily" {
		t.Errorf("SystemCheckSchedule = %q, want @daily", got)
	}
	if got := u.SystemInstallSchedule(); got != "@weekly" {
		t.Errorf("SystemInstallSchedule = %q, want @weekly", got)
	}
}

func TestDerivedCronsFollowTheSettings(t *testing.T) {
	t.Parallel()
	cfg := &Config{Updates: Updates{
		System:  PackageUpdates{CheckSchedule: "30 3 * * *"},
		Ostiole: SelfUpdates{Mode: UpdateManual},
	}}
	derived := cfg.DerivedCrons()
	if len(derived) != 4 {
		t.Fatalf("derived %d crons, want 4", len(derived))
	}
	byID := map[string]Cron{}
	for _, c := range derived {
		byID[c.ID] = c
	}
	for _, want := range []struct {
		id       string
		kind     CronKind
		schedule string
		enabled  bool
	}{
		{CronIDSystemUpdateCheck, CronSystemUpdateCheck, "30 3 * * *", true},
		{CronIDSystemUpdate, CronSystemUpdate, DefaultUpdateSchedule, true},
		// Ostiole is on manual: it still asks what is out, and installs
		// none of it.
		{CronIDOstioleUpdateCheck, CronOstioleUpdateCheck, DefaultUpdateCheckSchedule, true},
		{CronIDOstioleUpdate, CronOstioleUpdate, DefaultUpdateSchedule, false},
	} {
		c, ok := byID[want.id]
		if !ok {
			t.Fatalf("%s is not derived from the settings", want.id)
		}
		if c.Kind != want.kind || c.Schedule != want.schedule || c.Enabled != want.enabled {
			t.Errorf("%s = %+v, want kind %q, schedule %q, enabled %v",
				want.id, c, want.kind, want.schedule, want.enabled)
		}
		found, ok := cfg.Cron(want.id)
		if !ok {
			t.Fatalf("Cron(%q) not found; run-it-now would not work", want.id)
		}
		if found.Kind != want.kind {
			t.Errorf("Cron(%q) = %+v", want.id, found)
		}
	}
}

func TestValidateCrons(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	// Every kind the UI offers has to be accepted here, or a cron that
	// can be created cannot be applied.
	for _, kind := range CronKinds {
		c := Cron{ID: "c1", Enabled: true, Schedule: "@daily", Kind: kind}
		switch kind {
		case CronBackup:
			c.Directory = "/var/backups/ostiole"
		case CronRestartService:
			c.Service = "dnsmasq"
		case CronCommand:
			c.Command = "/usr/bin/true"
		}
		cfg.Crons = []Cron{c}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s rejected: %v", kind, err)
		}
	}

	cfg.Crons = []Cron{
		{ID: "c1", Schedule: "every other tuesday", Kind: "invented"},
		{ID: "c1", Schedule: "@daily", Kind: CronBackup, Directory: "backups", TimeoutSeconds: -1},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"crons[0].schedule",
		"crons[0].kind",
		"crons[1].id",
		"crons[1].directory",
		"crons[1].timeoutSeconds",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
	// The refusal names the kinds that do exist.
	if msg := got["crons[0].kind"]; !strings.Contains(msg, string(CronRefreshBlocklists)) {
		t.Errorf("unknown kind message = %q, want the known kinds listed", msg)
	}
}

func TestValidateUpdates(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Updates = Updates{
		System: PackageUpdates{
			Mode:            "sometimes",
			CheckSchedule:   "every other tuesday",
			InstallSchedule: "@fortnightly",
			Exclude:         []string{"kernel", "--assume-yes", "kernel*", "gcc-c++"},
		},
		Ostiole: SelfUpdates{Channel: "nightly"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"updates.system.mode",
		"updates.system.checkSchedule",
		"updates.system.installSchedule",
		"updates.system.exclude[1]",
		"updates.ostiole.channel",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (got %v)", p, keysOf(got))
		}
	}
	// A glob is a legitimate entry — "kernel*" is how a router holds back
	// every kernel package — and so is a name with a plus in it.
	for _, p := range []string{"updates.system.exclude[2]", "updates.system.exclude[3]"} {
		if msg, ok := got[p]; ok {
			t.Errorf("%s rejected: %s", p, msg)
		}
	}
	// The empty settings are the defaults, not a mistake.
	cfg.Updates = Updates{}
	if err := cfg.Validate(); err != nil {
		t.Errorf("empty update settings rejected: %v", err)
	}
}

// shapingConfig is a router with a line and a network behind it, ready to
// be given speeds.
func shapingConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "lan"}, {Name: "wan", External: true}},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}, IPv6: IPv6{Mode: AddrNone}},
			{Name: "eth1", Zone: "lan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"}, IPv6: IPv6{Mode: AddrNone}},
		},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
	}
}

func TestValidateAcceptsShaping(t *testing.T) {
	t.Parallel()
	cfg := shapingConfig()
	cfg.Interfaces[0].Shaping = &Shaping{Download: 200_000_000, Upload: 20_000_000, Link: LinkPPPoEPTM}
	cfg.Interfaces[1].Shaping = &Shaping{Download: 50_000_000}
	cfg.Rules = []Rule{
		{ID: "voip", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolUDP, Priority: TierRealtime},
		// A rule may pick a gateway and a priority at once: they write
		// different bits of the same mark.
		{ID: "both", Enabled: true, Zone: "lan", DestZone: "wan", Action: ActionAccept, Protocol: ProtocolAny, Priority: TierBulk},
	}
	cfg.NAT.PortForwards = []PortForward{
		{ID: "pf", Enabled: true, Zone: "wan", Protocol: ProtocolTCP, Ports: []string{"443"},
			Target: "192.168.1.10", Priority: TierHigh},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("shaping config rejected: %v", err)
	}
	if got, want := len(cfg.ShapedInterfaces()), 2; got != want {
		t.Errorf("%d shaped interfaces, want %d", got, want)
	}
	if !cfg.ShapesTraffic() {
		t.Error("rules set a priority, yet nothing is said to be classified")
	}
}

// An interface that is shaped but turned off carries no traffic, and a
// priority on a rule that is turned off classifies nothing.
func TestShapingIgnoresWhatIsTurnedOff(t *testing.T) {
	t.Parallel()
	cfg := shapingConfig()
	cfg.Interfaces[0].Enabled = false
	cfg.Interfaces[0].Shaping = &Shaping{Download: 200_000_000}
	cfg.Rules = []Rule{{ID: "off", Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny, Priority: TierHigh}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config rejected: %v", err)
	}
	if got := cfg.ShapedInterfaces(); len(got) != 0 {
		t.Errorf("a disabled interface is shaped: %v", got)
	}
	if cfg.ShapesTraffic() {
		t.Error("a disabled rule was taken to classify traffic")
	}
}

func TestValidateCatchesShapingMistakes(t *testing.T) {
	t.Parallel()
	cfg := shapingConfig()
	cfg.Zones = append(cfg.Zones, Zone{Name: "dmz"})
	cfg.Interfaces = append(cfg.Interfaces,
		// Speeds nobody can shape to, and a line type that does not exist.
		Interface{Name: "eth2", Zone: "dmz", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Shaping: &Shaping{Download: 10, Upload: MaxRate + 1, Link: "carrier-pigeon"}},
		// Neither direction given: the interface is listed as shaped and
		// nothing is shaped.
		Interface{Name: "eth3", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Shaping: &Shaping{}},
		// A bridge member carries its master's traffic, not its own.
		Interface{Name: "br0", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Bridge: &Bridge{Members: []string{"eth4"}}},
		Interface{Name: "eth4", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Shaping: &Shaping{Download: 1_000_000}},
		// Two names too long to fit in a device name that happen to be cut
		// and hashed to the same one.
		Interface{Name: "enp0s3aaaah9", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Shaping: &Shaping{Download: 1_000_000}},
		Interface{Name: "enp0s3aaac3f", Enabled: true, IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone},
			Shaping: &Shaping{Download: 1_000_000}},
	)
	cfg.Rules = []Rule{
		{ID: "dropped", Enabled: true, Zone: "lan", Action: ActionDrop, Protocol: ProtocolAny, Priority: TierHigh},
		{ID: "unknown", Enabled: true, Zone: "lan", Action: ActionAccept, Protocol: ProtocolAny, Priority: "urgent"},
	}
	cfg.NAT.PortForwards = []PortForward{
		{ID: "pf", Enabled: true, Zone: "wan", Protocol: ProtocolTCP, Ports: []string{"443"},
			Target: "192.168.1.10", Priority: "urgent"},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"interfaces[2].shaping.download",
		"interfaces[2].shaping.upload",
		"interfaces[2].shaping.link",
		"interfaces[3].shaping",
		"interfaces[5].shaping",
		"interfaces[7].shaping",
		"rules[0].priority",
		"rules[1].priority",
		"nat.portForwards[0].priority",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (have %v)", p, got)
		}
	}
	if msg := got["rules[0].priority"]; !strings.Contains(msg, "accept") {
		t.Errorf("drop rule message = %q, want it to mention accept", msg)
	}
	if msg := got["interfaces[5].shaping"]; !strings.Contains(msg, "br0") {
		t.Errorf("member message = %q, want it to name the master", msg)
	}
	if msg := got["interfaces[7].shaping"]; !strings.Contains(msg, "same helper device") {
		t.Errorf("collision message = %q", msg)
	}
}

// The marks are what the firewall writes and the queue reads, so the four
// tiers have to come out as one, two, three and four in the tier bits and
// nothing else may touch the byte policy routing keeps beside them.
func TestTierMarks(t *testing.T) {
	t.Parallel()
	for i, tier := range Tiers {
		mark, ok := tier.Mark()
		if !ok {
			t.Fatalf("%s has no mark", tier)
		}
		if want := uint32(i+1) << ShapeMarkShift; mark != want {
			t.Errorf("%s mark = 0x%08x, want 0x%08x", tier, mark, want)
		}
		if mark&^uint32(ShapeMarkMask) != 0 {
			t.Errorf("%s mark 0x%08x reaches outside its own bits", tier, mark)
		}
		if mark&PolicyMarkMask != 0 {
			t.Errorf("%s mark 0x%08x overlaps policy routing", tier, mark)
		}
	}
	if _, ok := Tier("").Mark(); ok {
		t.Error("the empty tier was given a mark; unclassified traffic is the queue's own business")
	}
	if !Tier("").Valid() || Tier("urgent").Valid() {
		t.Error("Valid does not agree with the four tiers and the empty one")
	}
}

// Helper device names have to fit in the kernel's fifteen characters and
// stay the same across runs, or an apply would leave the last one behind.
func TestIFBNames(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"eth0":        "ifb-eth0",
		"enp1s0":      "ifb-enp1s0",
		"eleven-char": "ifb-eleven-char",
	} {
		if got := IFBName(name); got != want {
			t.Errorf("IFBName(%q) = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"enp0s31f6.4000", "a-very-long-interface-name", "wg0.vlan.1234"} {
		got := IFBName(name)
		if len(got) != 15 {
			t.Errorf("IFBName(%q) = %q, which is %d characters", name, got, len(got))
		}
		if got != IFBName(name) {
			t.Errorf("IFBName(%q) is not stable", name)
		}
	}
}

func TestValidateAcceptsBusyHosts(t *testing.T) {
	t.Parallel()
	cfg := shapingConfig()
	cfg.Interfaces[1].Shaping = &Shaping{Download: 50_000_000}
	cfg.Zones[0].Busy = &BusyHosts{Connections: DefaultBusyConnections, Priority: TierBulk}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("busy host config rejected: %v", err)
	}
	if got := cfg.BusyZones(); len(got) != 1 || got[0].Name != "lan" {
		t.Errorf("busy zones = %v", got)
	}
	// Holding a busy host back classifies traffic no rule mentions, so the
	// ruleset still needs the rules that carry a tier between packets.
	if !cfg.ShapesTraffic() {
		t.Error("a zone that holds back a busy host does not count as classifying traffic")
	}
}

func TestValidateCatchesBusyHostMistakes(t *testing.T) {
	t.Parallel()
	cfg := shapingConfig()
	cfg.Zones[0].Busy = &BusyHosts{Connections: 2}                                      // too few, no tier
	cfg.Zones[1].Busy = &BusyHosts{Connections: MaxBusyConnections + 1, Priority: "sl"} // and facing the internet

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation errors")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for _, p := range []string{
		"zones[0].busy.connections",
		"zones[0].busy.priority",
		"zones[1].busy.connections",
		"zones[1].busy.priority",
		"zones[1].busy",
	} {
		if _, ok := got[p]; !ok {
			t.Errorf("missing issue at %s (have %v)", p, got)
		}
	}
	// The tally is per source address, so it only means anything where the
	// sources are a known set of hosts.
	if msg := got["zones[1].busy"]; !strings.Contains(msg, "internet") {
		t.Errorf("external zone message = %q", msg)
	}
}
