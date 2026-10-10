package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The relay takes the same links a wake does, and says to add the bridge
// rather than wake on it.
func TestCheckDiscoveryInterface(t *testing.T) {
	t.Parallel()
	cfg := wakeConfig()
	for name, want := range map[string]string{
		"eth1":       "",
		"eth1.20":    "",
		"br0":        "",
		"eth0":       "external zone",
		"eth2":       `part of "br0", so add "br0"`,
		"eth3":       `carries the session "ppp0"`,
		"ppp0":       "dialled session",
		"wg0":        "tunnel",
		"tailscale0": "tunnel",
		"eth4":       "has no zone",
		"eth9":       "unknown interface",
	} {
		err := cfg.CheckDiscoveryInterface(name)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
}

func discoveryConfig() *Config {
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "things"})
	cfg.Interfaces = append(cfg.Interfaces,
		Interface{Name: "eth1.30", Zone: "things", Enabled: true, VLAN: &VLAN{Parent: "eth1", ID: 30},
			IPv4: IPv4{Mode: AddrStatic, Address: "192.168.30.1/24"}, IPv6: IPv6{Mode: AddrNone}},
		Interface{Name: "eth2", Zone: "things", IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}})
	cfg.Services.Discovery = Discovery{Enabled: true, MDNS: new(true), SSDP: new(true), Interfaces: []DiscoveryInterface{
		{Interface: "eth1", Asks: true},
		{Interface: "eth1.30", Answers: true},
	}}
	return cfg
}

func TestDiscoveryLinksAndActive(t *testing.T) {
	t.Parallel()
	cfg := discoveryConfig()
	cfg.Services.Discovery.Interfaces = append(cfg.Services.Discovery.Interfaces,
		DiscoveryInterface{Interface: "eth2", Asks: true, Answers: true})
	links := cfg.DiscoveryLinks()
	if len(links) != 2 || links[0].Interface != "eth1" || links[1].Interface != "eth1.30" {
		t.Errorf("links = %+v, want eth1 and eth1.30 without eth2, which is off", links)
	}
	if !cfg.DiscoveryActive() {
		t.Error("an asker and an answerer should make the relay active")
	}

	for name, change := range map[string]func(*Discovery){
		"off":          func(d *Discovery) { d.Enabled = false },
		"no protocol":  func(d *Discovery) { d.MDNS, d.SSDP = new(false), new(false) },
		"no asker":     func(d *Discovery) { d.Interfaces[0].Asks = false },
		"no answerer":  func(d *Discovery) { d.Interfaces[1].Answers = false },
		"answerer off": func(*Discovery) {},
	} {
		c := discoveryConfig()
		change(&c.Services.Discovery)
		if name == "answerer off" {
			in, _ := c.Interface("eth1.30")
			in.Enabled = false
		}
		if c.DiscoveryActive() {
			t.Errorf("%s: relay active", name)
		}
		if name == "off" || name == "no protocol" {
			if got := c.DiscoveryLinks(); got != nil {
				t.Errorf("%s: links = %+v, want none", name, got)
			}
		}
	}
}

func TestValidateDiscovery(t *testing.T) {
	t.Parallel()
	cfg := discoveryConfig()
	cfg.Services.Discovery.Interfaces = append(cfg.Services.Discovery.Interfaces,
		DiscoveryInterface{Interface: "eth2", Asks: true})
	cfg.Services.Discovery.Services = []string{
		"_zigate-zigbee-gateway._tcp", "_Volumio._tcp", "_philipstv_s_rpc._tcp", "_googlecast._tcp",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("good relay rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		change func(*Discovery)
		path   string
	}{
		{"unknown", func(d *Discovery) { d.Interfaces[0].Interface = "ghost" }, "services.discovery.interfaces[0].interface"},
		{"unknown while off", func(d *Discovery) {
			d.Enabled = false
			d.Interfaces[1].Interface = "ghost"
		}, "services.discovery.interfaces[1].interface"},
		{"external", func(d *Discovery) { d.Interfaces[1].Interface = "eth0" }, "services.discovery.interfaces[1].interface"},
		{"duplicate", func(d *Discovery) {
			d.Interfaces = append(d.Interfaces, DiscoveryInterface{Interface: "eth1", Answers: true})
		}, "services.discovery.interfaces[2].interface"},
		{"no role", func(d *Discovery) {
			d.Interfaces = append(d.Interfaces, DiscoveryInterface{Interface: "eth2"})
		}, "services.discovery.interfaces[2].interface"},
		{"one interface", func(d *Discovery) {
			d.Interfaces = []DiscoveryInterface{{Interface: "eth1", Asks: true, Answers: true}}
		}, "services.discovery.interfaces"},
		{"no asker", func(d *Discovery) { d.Interfaces[0] = DiscoveryInterface{Interface: "eth1", Answers: true} }, "services.discovery.interfaces"},
		{"no answerer", func(d *Discovery) { d.Interfaces[1] = DiscoveryInterface{Interface: "eth1.30", Asks: true} }, "services.discovery.interfaces"},
		{"no protocol", func(d *Discovery) { d.MDNS, d.SSDP = new(false), new(false) }, "services.discovery.mdns"},
		{"no underscore", func(d *Discovery) { d.Services = []string{"googlecast._tcp"} }, "services.discovery.services[0]"},
		{"sctp", func(d *Discovery) { d.Services = []string{"_ok._tcp", "_x._sctp"} }, "services.discovery.services[1]"},
		{"empty", func(d *Discovery) { d.Services = []string{""} }, "services.discovery.services[0]"},
		{"same type", func(d *Discovery) { d.Services = []string{"_hap._tcp", "_HAP._tcp"} }, "services.discovery.services[1]"},
		{"log", func(d *Discovery) { d.Log.Entries = MaxDiscoveryLogEntries + 1 }, "services.discovery.log.entries"},
	} {
		c := discoveryConfig()
		tc.change(&c.Services.Discovery)
		var ve *ValidationError
		if !errors.As(c.Validate(), &ve) || !hasPath(ve, tc.path) {
			t.Errorf("%s: want an issue at %s, got %v", tc.name, tc.path, c.Validate())
		}
	}
}

// A relay never set up writes nothing, so configurations from before it
// read back the same.
func TestDiscoveryAbsentByDefault(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Services{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "discovery") {
		t.Errorf("an unset relay was written: %s", raw)
	}
}

// A router without the block relays both protocols, as the page shows it,
// and a protocol switched off is written, not dropped with the block.
func TestDiscoveryProtocolsSurviveASave(t *testing.T) {
	t.Parallel()
	var none Services
	if err := json.Unmarshal([]byte(`{}`), &none); err != nil {
		t.Fatal(err)
	}
	if d := none.Discovery; !d.RelaysMDNS() || !d.RelaysSSDP() {
		t.Errorf("without a block: mdns %v, ssdp %v, want both on", d.RelaysMDNS(), d.RelaysSSDP())
	}
	for _, c := range []struct{ in, want string }{
		{`{"enabled":false,"mdns":false,"ssdp":false}`, `"discovery":{"enabled":false,"mdns":false,"ssdp":false}`},
		{`{"enabled":false,"mdns":true,"ssdp":false}`, `"discovery":{"enabled":false,"mdns":true,"ssdp":false}`},
		{`{"enabled":false,"mdns":true,"ssdp":true}`, `"discovery":{"enabled":false,"mdns":true,"ssdp":true}`},
	} {
		var s Services
		if err := json.Unmarshal([]byte(`{"discovery":`+c.in+`}`), &s); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), c.want) {
			t.Errorf("%s was written as %s", c.in, raw)
		}
	}
}
