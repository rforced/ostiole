package model

import (
	"strings"
	"testing"
)

// protectedConfig is the smallest valid router with an outside and an
// inside, for the checks that are about the defence rather than the rest.
func protectedConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		System:  System{Hostname: "fw", Management: Management{WebPort: 443, SSHPort: 22}},
		Zones: []Zone{
			{Name: "wan", External: true},
			{Name: "lan", AntiLockout: true},
		},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}, IPv6: IPv6{Mode: AddrNone}},
			{Name: "eth1", Zone: "lan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"},
				IPv6: IPv6{Mode: AddrNone}},
		},
		Rules: []Rule{{
			ID: "allow-lan", Enabled: true, Zone: "lan", Action: ActionAccept,
			Protocol: ProtocolAny,
		}},
		NAT: NAT{Outbound: OutboundNAT{Mode: OutboundAutomatic}},
	}
}

func TestProtectionDefaultsToTheOutside(t *testing.T) {
	t.Parallel()
	cfg := protectedConfig()
	if zones := cfg.ProtectedZones(); len(zones) != 0 {
		t.Errorf("a router with no protection defends %v", zones)
	}
	cfg.Protection.SynFlood = &DefaultSynFlood
	if got := cfg.ProtectedZones(); len(got) != 1 || got[0] != "wan" {
		t.Errorf("defended zones = %v, want the external one", got)
	}
	// Naming a zone replaces the default rather than adding to it: a
	// guest network is a reasonable place to stop a flood, and somebody
	// who says so means only there.
	cfg.Protection.Zones = []string{"lan"}
	if got := cfg.ProtectedZones(); len(got) != 1 || got[0] != "lan" {
		t.Errorf("defended zones = %v, want the one named", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a named zone was rejected: %v", err)
	}
}

// A zone with no interfaces defends nothing, and a defence that defends
// nothing is almost always a zone that was renamed.
func TestProtectionWithNothingToDefend(t *testing.T) {
	t.Parallel()
	cfg := protectedConfig()
	cfg.Protection.SynFlood = &DefaultSynFlood
	cfg.Protection.Zones = []string{"nowhere"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	if !strings.Contains(err.Error(), "unknown zone") {
		t.Errorf("err = %v, want it to name the unknown zone", err)
	}

	// An external zone with no interface on it is the subtler case.
	cfg = protectedConfig()
	cfg.Protection.ICMPFlood = &DefaultICMPFlood
	cfg.Interfaces = cfg.Interfaces[1:] // the WAN is gone
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "nothing is defended") {
		t.Errorf("err = %v, want it to say nothing is defended", err)
	}
}

func TestProtectionRejectsALimitThatIsNotOne(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		change func(*Config)
		want   string
	}{
		"a rate of zero": {
			change: func(c *Config) { c.Protection.SynFlood = &RateLimit{Rate: 0} },
			want:   "allows nothing through",
		},
		"a rate past the ceiling": {
			change: func(c *Config) { c.Protection.SynFlood = &RateLimit{Rate: MaxLimitRate + 1} },
			want:   "must be between",
		},
		"a period nftables cannot count in": {
			change: func(c *Config) { c.Protection.ICMPFlood = &RateLimit{Rate: 5, Unit: "fortnight"} },
			want:   "unknown period",
		},
		"a negative burst": {
			change: func(c *Config) { c.Protection.ICMPFlood = &RateLimit{Rate: 5, Burst: -1} },
			want:   "must be between 0 and",
		},
		"a hold that is not a duration": {
			change: func(c *Config) { c.Protection.PortScan = &PortScan{Rate: 5, Hold: "a while"} },
			want:   "number and a unit",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := protectedConfig()
			tc.change(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Rationing a refusal is not a thing: the rule already refuses.
func TestRuleLimitOnlyOnAnAccept(t *testing.T) {
	t.Parallel()
	cfg := protectedConfig()
	cfg.Rules[0].Action = ActionDrop
	cfg.Rules[0].Limit = &DefaultRuleLimit
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "only an accept rule") {
		t.Errorf("err = %v, want it to refuse a limit on a drop rule", err)
	}

	cfg.Rules[0].Action = ActionAccept
	if err := cfg.Validate(); err != nil {
		t.Errorf("a limit on an accept rule was rejected: %v", err)
	}
}
