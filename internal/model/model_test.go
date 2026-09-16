package model

import (
	"errors"
	"sort"
	"strings"
	"testing"
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
		System:  System{Hostname: "bad host", DNSServers: []string{"nope"}},
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
	sc := cfg.Services.DHCP.Scopes
	if len(sc) != 1 || sc[0].RangeStart != "192.168.1.100" || sc[0].RangeEnd != "192.168.1.199" {
		t.Errorf("scope = %+v", sc)
	}
	if !cfg.Services.DNS.Enabled || len(cfg.Services.DNS.Upstreams) != 2 {
		t.Errorf("dns = %+v", cfg.Services.DNS)
	}
	cases := map[string][2]string{
		"10.0.0.1/16":      {"10.0.0.100", "10.0.0.199"},
		"192.168.5.1/26":   {"192.168.5.32", "192.168.5.62"},
		"192.168.5.1/28":   {"192.168.5.8", "192.168.5.14"},
		"192.168.5.150/24": {"", ""}, // the box sits inside the pool
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
		DHCP: DHCPServer{Enabled: true,
			Scopes: []DHCPScope{
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
		"services.dhcp.scopes[0].rangeStart", "services.dhcp.scopes[0].leaseTime", "services.dhcp.scopes[0].gateway",
		"services.dhcp.scopes[0].dns[0]", "services.dhcp.scopes[0].domain",
		"services.dhcp.scopes[1].interface", "services.dhcp.scopes[2].interface",
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
	cfg.Services.DHCP = DHCPServer{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "upstream") {
		t.Errorf("dns without upstreams accepted: %v", err)
	}
}

func TestValidateDHCPv6(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Interfaces[0].IPv6 = IPv6{Mode: AddrStatic, Address: "2001:db8::1/64"}
	cfg.Services.DHCP = DHCPServer{Enabled: true, V6: []DHCPv6Scope{
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
	// unusual. A single sane scope must validate too.
	cfg.Services.DHCP.V6 = []DHCPv6Scope{{Interface: "eth1", Enabled: true, Mode: RAManaged, RangeStart: "::100", RangeEnd: "::1ff"}}
	cfg.Services.DNS = DNSServer{Enabled: true, Upstreams: []string{"1.1.1.1"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid IPv6 scope rejected: %v", err)
	}
}

func TestValidateStaticLeaseAddresses(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24"})
	cfg.Services.DHCP = DHCPServer{Enabled: true, StaticLeases: []StaticLease{
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
