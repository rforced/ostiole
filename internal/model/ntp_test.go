package model

import (
	"errors"
	"slices"
	"testing"
)

func TestNTPServersFallBackToTheDefaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	got := cfg.NTPServers()
	if !slices.Equal(got, DefaultNTPServers) {
		t.Fatalf("servers = %+v, want the defaults", got)
	}
	for _, s := range got {
		if !s.NTS {
			t.Errorf("default %s is not asked to sign its answers", s.Host)
		}
	}
	// What a caller does with the list is its own business.
	got[0].Host = "changed.example"
	if DefaultNTPServers[0].Host == "changed.example" {
		t.Error("the defaults were handed out to be changed")
	}

	own := []NTPServer{{Host: "time.example.lan"}}
	cfg.Services.NTP.Servers = own
	if got := cfg.NTPServers(); !slices.Equal(got, own) {
		t.Errorf("servers = %+v, want the router's own", got)
	}
}

func TestNTPInterfaces(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "lab"})
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{Name: "eth2", Zone: "lab", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "10.30.0.1/24"}},
		Interface{Name: "eth3", Zone: "lan", IPv4: IPv4{Mode: AddrNone}},
	)
	// None named: every enabled interface outside an external zone.
	if got := cfg.NTPInterfaces(); !slices.Equal(got, []string{"eth1", "eth2"}) {
		t.Errorf("interfaces = %v, want eth1 and eth2", got)
	}
	if cfg.NTPServing() {
		t.Error("serving without being asked to")
	}
	cfg.Services.NTP = NTP{Serve: true, Interfaces: []string{"eth2", "eth3"}}
	// Named: those, less the disabled one.
	if got := cfg.NTPInterfaces(); !slices.Equal(got, []string{"eth2"}) {
		t.Errorf("interfaces = %v, want eth2", got)
	}
	if !cfg.NTPServing() {
		t.Error("not serving on eth2")
	}
}

func TestValidateNTP(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Interfaces = append(cfg.Interfaces, Interface{Name: "eth2", Zone: "lan", IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}})
	cfg.Services.NTP = NTP{
		Serve: true,
		Servers: []NTPServer{
			{Host: ""},
			{Host: " time.example.lan"},
			{Host: "not a name"},
			{Host: "192.0.2.1", NTS: true},
			{Host: "2001:db8::1", Pool: true},
			{Host: "Pool.NTP.org", Pool: true},
			{Host: "pool.ntp.org", Pool: true},
			{Host: "nts.netnod.se", NTS: true},
			{Host: "192.0.2.2"},
		},
		Interfaces: []string{"ghost", "eth0", "eth2", "eth1"},
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
		"services.ntp.servers[0].host", // empty
		"services.ntp.servers[1].host", // spaces around it
		"services.ntp.servers[2].host", // not a name
		"services.ntp.servers[3].nts",  // NTS needs a name
		"services.ntp.servers[4].pool", // a pool is a name
		"services.ntp.servers[6].host", // the same pool again, in other case
		"services.ntp.interfaces[0]",   // unknown
		"services.ntp.interfaces[1]",   // external
	} {
		if !got[p] {
			t.Errorf("missing issue at %s (have %v)", p, ve.Issues)
		}
	}
	for _, p := range []string{
		"services.ntp.servers[5].host", "services.ntp.servers[7].host", "services.ntp.servers[7].nts",
		"services.ntp.servers[8].host", "services.ntp.interfaces[2]", "services.ntp.interfaces[3]",
		"services.ntp.serve",
	} {
		if got[p] {
			t.Errorf("a valid entry was rejected at %s: %v", p, ve.Issues)
		}
	}

	// A typo in an interface name is caught with serving off too.
	cfg.Services.NTP = NTP{Interfaces: []string{"ghost"}}
	if !hasIssue(t, cfg, "services.ntp.interfaces[0]") {
		t.Error("an unknown interface passed while serving was off")
	}

	// Switching the only served link off must not stop the apply that
	// does it: nothing arrives there to answer.
	cfg.Services.NTP = NTP{Serve: true, Interfaces: []string{"eth2"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("serving on a disabled interface was refused: %v", err)
	}
	if cfg.NTPServing() {
		t.Error("serving on a disabled interface")
	}

	cfg.Services.NTP = NTP{}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the default was rejected: %v", err)
	}
}

// A new router answers its LAN's time requests; one set up without the
// LAN services does not.
func TestStarterServesTime(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	if !cfg.Services.NTP.Serve || len(cfg.Services.NTP.Servers) != 0 {
		t.Errorf("ntp = %+v, want serving on the default servers", cfg.Services.NTP)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("starter does not validate: %v", err)
	}
	if bare := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"}); bare.Services.NTP.Serve {
		t.Error("serving time without the LAN services")
	}
}
