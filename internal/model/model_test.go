package model

import (
	"errors"
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
