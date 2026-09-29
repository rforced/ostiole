package model

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestParseWakeMAC(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want, err string
	}{
		{in: "aa:bb:cc:dd:ee:ff", want: "aa:bb:cc:dd:ee:ff"},
		{in: "AA:BB:CC:00:11:22", want: "aa:bb:cc:00:11:22"},
		{in: "aa-bb-cc-dd-ee-ff", err: "not a MAC address"},
		{in: "aabb.ccdd.eeff", err: "not a MAC address"},
		{in: "aa:bb:cc:dd:ee:ff:00:11", err: "not a MAC address"},
		{in: "", err: "not a MAC address"},
		{in: "ff:ff:ff:ff:ff:ff", err: "group address"},
		{in: "01:00:5e:00:00:fb", err: "group address"},
		{in: "00:00:00:00:00:00", err: "no machine's"},
	} {
		hw, err := ParseWakeMAC(tc.in)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%q: err = %v, want %q", tc.in, err, tc.err)
		case tc.err == "" && err != nil:
			t.Errorf("%q: %v", tc.in, err)
		case tc.err == "" && hw.String() != tc.want:
			t.Errorf("%q = %s, want %s", tc.in, hw, tc.want)
		}
	}
}

// wakeConfig has one interface of every sort a wake could be asked for.
func wakeConfig() *Config {
	return &Config{
		Version: SchemaVersion,
		Zones:   []Zone{{Name: "wan", External: true}, {Name: "lan"}, {Name: "vpn"}},
		Interfaces: []Interface{
			{Name: "eth0", Zone: "wan", Enabled: true, IPv4: IPv4{Mode: AddrDHCP}},
			{Name: "eth1", Zone: "lan", Enabled: true, IPv4: IPv4{Mode: AddrStatic, Address: "192.168.1.1/24"}},
			{Name: "eth1.20", Zone: "lan", Enabled: true, VLAN: &VLAN{Parent: "eth1", ID: 20},
				IPv4: IPv4{Mode: AddrStatic, Address: "10.20.0.1/24"}},
			{Name: "br0", Zone: "lan", Enabled: true, Bridge: &Bridge{Members: []string{"eth2"}},
				IPv4: IPv4{Mode: AddrStatic, Address: "10.30.0.1/24"}},
			{Name: "eth2", Enabled: true},
			{Name: "eth3", Enabled: true},
			{Name: "ppp0", Zone: "wan", Enabled: true, PPPoE: &PPPoE{Parent: "eth3"}},
			{Name: "wg0", Zone: "vpn", Enabled: true, WireGuard: &WireGuard{}},
			{Name: "tailscale0", Zone: "vpn", Enabled: true, Tailscale: &Tailscale{}},
			{Name: "eth4", Enabled: true},
			{Name: "eth5", Zone: "lan", IPv4: IPv4{Mode: AddrStatic, Address: "10.50.0.1/24"}},
		},
	}
}

// A wake is an Ethernet frame for the inside: a tunnel or a dialled
// session has no Ethernet, a member is reached through its bridge, and
// the WAN side is the provider's network.
func TestCheckWake(t *testing.T) {
	t.Parallel()
	cfg := wakeConfig()
	for name, want := range map[string]string{
		"eth1":       "",
		"eth1.20":    "",
		"br0":        "",
		"eth5":       "", // off, but the caller decides what that means
		"eth0":       "external zone",
		"eth2":       `part of "br0", so wake on "br0"`,
		"eth3":       `carries the session "ppp0"`,
		"ppp0":       "dialled session",
		"wg0":        "tunnel",
		"tailscale0": "tunnel",
		"eth4":       "has no zone",
		"eth9":       "unknown interface",
	} {
		err := cfg.CheckWake(name)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

// A wake about to be sent also needs the interface on.
func TestWakeTarget(t *testing.T) {
	t.Parallel()
	cfg := wakeConfig()
	if hw, err := cfg.WakeTarget("eth1", "AA:BB:CC:00:00:01"); err != nil || hw.String() != "aa:bb:cc:00:00:01" {
		t.Errorf("eth1: %s %v", hw, err)
	}
	for _, tc := range []struct{ iface, mac, want string }{
		{"eth5", "aa:bb:cc:00:00:01", `"eth5" is off`},
		{"eth0", "aa:bb:cc:00:00:01", "external zone"},
		{"eth1", "ff:ff:ff:ff:ff:ff", "group address"},
	} {
		if _, err := cfg.WakeTarget(tc.iface, tc.mac); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: err = %v, want %q", tc.iface, tc.mac, err, tc.want)
		}
	}
}

func TestValidateWoL(t *testing.T) {
	t.Parallel()
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{Name: "eth2", Zone: "lan", IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}})
	cfg.Services.WoL.Devices = []WoLDevice{
		{ID: "nas", Interface: "eth1", MAC: "aa:bb:cc:00:00:01", Description: "NAS"},
		// Off is allowed: the device waits for the interface to come back.
		{ID: "lab", Interface: "eth2", MAC: "aa:bb:cc:00:00:02"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("good devices rejected: %v", err)
	}

	cfg.Services.WoL.Devices = []WoLDevice{
		{ID: "nas", Interface: "eth1", MAC: "aa:bb:cc:00:00:01"},
		{ID: "nas", Interface: "eth1", MAC: "AA:BB:CC:00:00:01"},
		{ID: "bad id", Interface: "eth0", MAC: "aa:bb:cc:00:00:03"},
		{ID: "group", Interface: "ghost", MAC: "ff:ff:ff:ff:ff:ff"},
	}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for path, want := range map[string]string{
		"services.wol.devices[1].id":        "duplicate id",
		"services.wol.devices[1].mac":       "already listed",
		"services.wol.devices[2].id":        "must match",
		"services.wol.devices[2].interface": "external zone",
		"services.wol.devices[3].mac":       "group address",
		"services.wol.devices[3].interface": "unknown interface",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s = %q, want %q", path, got[path], want)
		}
	}
	for _, path := range []string{"services.wol.devices[0].id", "services.wol.devices[0].mac", "services.wol.devices[0].interface"} {
		if msg, ok := got[path]; ok {
			t.Errorf("%s: %s", path, msg)
		}
	}
}

// A wake cron names a device by id, and only a wake cron names one.
func TestValidateWakeCrons(t *testing.T) {
	t.Parallel()
	cfg := policyConfig()
	cfg.Services.WoL.Devices = []WoLDevice{{ID: "nas", Interface: "eth1", MAC: "aa:bb:cc:00:00:01"}}
	cfg.Crons = []Cron{
		{ID: "a", Schedule: "@daily", Kind: CronWake},
		{ID: "b", Schedule: "@daily", Kind: CronWake, Device: "gone"},
		{ID: "c", Schedule: "@daily", Kind: CronRefreshAliases, Device: "nas"},
		{ID: "d", Schedule: "@daily", Kind: CronWake, Device: "nas"},
	}
	var ve *ValidationError
	if !errors.As(cfg.Validate(), &ve) {
		t.Fatal("want validation issues")
	}
	got := map[string]string{}
	for _, i := range ve.Issues {
		got[i.Path] = i.Message
	}
	for path, want := range map[string]string{
		"crons[0].device": "say which device",
		"crons[1].device": `"gone"`,
		"crons[2].device": "only a wake cron",
	} {
		if !strings.Contains(got[path], want) {
			t.Errorf("%s = %q, want %q", path, got[path], want)
		}
	}
	if msg, ok := got["crons[3].device"]; ok {
		t.Errorf("a good wake cron: %s", msg)
	}
}

func TestInterfaceFor(t *testing.T) {
	t.Parallel()
	cfg := wakeConfig()
	for addr, want := range map[string]string{
		"192.168.1.40":        "eth1",
		"10.20.0.9":           "eth1.20",
		"10.30.0.200":         "br0",
		"::ffff:192.168.1.40": "eth1",
		"10.50.0.2":           "", // eth5 is off
		"203.0.113.9":         "",
		"2001:db8::1":         "",
	} {
		got, ok := cfg.InterfaceFor(netip.MustParseAddr(addr))
		if got != want || ok != (want != "") {
			t.Errorf("%s = %q %v, want %q", addr, got, ok, want)
		}
	}
}
