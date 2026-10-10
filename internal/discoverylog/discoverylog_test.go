package discoverylog

import (
	"reflect"
	"testing"

	"ostiole/internal/discovery"
	"ostiole/internal/logsearch"
	"ostiole/internal/model"
)

func relayConfig() *model.Config {
	cfg := model.Starter(model.StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "things"})
	cfg.Interfaces = append(cfg.Interfaces,
		model.Interface{Name: "eth1.30", Zone: "things", Enabled: true, VLAN: &model.VLAN{Parent: "eth1", ID: 30},
			IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.30.1/24"}, IPv6: model.IPv6{Mode: model.AddrNone}},
		model.Interface{Name: "eth2", Zone: "things", IPv4: model.IPv4{Mode: model.AddrNone}, IPv6: model.IPv6{Mode: model.AddrNone}})
	cfg.Services.Discovery = model.Discovery{Enabled: true, MDNS: true, Services: []string{"_printer._tcp"},
		Interfaces: []model.DiscoveryInterface{
			{Interface: "eth1", Asks: true},
			{Interface: "eth2", Answers: true},
			{Interface: "eth1.30", Answers: true},
		}}
	return cfg
}

// The relay runs what the configuration asks, on the enabled links, and
// is handed nothing at all once discovery has no work, so it closes every
// socket.
func TestRelayConfig(t *testing.T) {
	t.Parallel()
	cfg := relayConfig()
	got := RelayConfig(cfg)
	want := discovery.Config{
		MDNS: true, Services: []string{"_printer._tcp"},
		Links:      []discovery.Link{{Name: "eth1", Asks: true}, {Name: "eth1.30", Answers: true}},
		ReplyPorts: [2]int{model.DiscoveryReplyPortFirst, model.DiscoveryReplyPortLast},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("on: %+v", got)
	}
	for name, off := range map[string]func(*model.Config){
		"disabled":        func(c *model.Config) { c.Services.Discovery.Enabled = false },
		"no protocol":     func(c *model.Config) { c.Services.Discovery.MDNS = false },
		"nothing asks":    func(c *model.Config) { c.Services.Discovery.Interfaces[0].Asks = false },
		"answerer is off": func(c *model.Config) { c.Interfaces[len(c.Interfaces)-2].Enabled = false },
	} {
		c := relayConfig()
		off(c)
		if got := RelayConfig(c); !reflect.DeepEqual(got, discovery.Config{}) {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if got := RelayConfig(nil); !reflect.DeepEqual(got, discovery.Config{}) {
		t.Errorf("nil: %+v", got)
	}
}

type configs []discovery.Config

func (c *configs) Configure(d discovery.Config) { *c = append(*c, d) }

// The follower hands the relay each configuration it reads and sizes the
// log to it.
func TestFollowerConfiguresTheRelayAndSizesTheLog(t *testing.T) {
	t.Parallel()
	cfg := relayConfig()
	cfg.Services.Discovery.Log.Entries = 3
	var got configs
	l := New()
	f := &Follower{Relay: &got, Log: l, Source: func() *model.Config { return cfg }}
	f.Tick()
	if len(got) != 1 || len(got[0].Links) != 2 || l.Size() != 3 {
		t.Fatalf("configured %+v, size %d", got, l.Size())
	}
	cfg = nil
	f.Tick()
	if len(got) != 2 || !reflect.DeepEqual(got[1], discovery.Config{}) {
		t.Errorf("with no configuration: %+v", got)
	}
}

// A search finds a packet by any interface it went to and by why it was
// dropped.
func TestMatcherSearchesWhatTheLogShows(t *testing.T) {
	t.Parallel()
	relayed := Event{Protocol: "mdns", Kind: discovery.KindQuery, From: "eth1", To: []string{"eth1.30", "eth3"},
		Name: "_printer._tcp.local.", Source: "192.168.1.20"}
	dropped := Event{Protocol: "ssdp", Kind: discovery.KindSearch, From: "eth1", Source: "192.168.1.21", Dropped: "duplicate"}
	for q, want := range map[string][2]bool{
		"":          {true, true},
		"eth3":      {true, false},
		"duplicate": {false, true},
		"ssdp":      {false, true},
		"_printer":  {true, false},
		"eth1":      {true, true},
	} {
		m := Matcher(logsearch.Parse(q))
		if m(&relayed) != want[0] || m(&dropped) != want[1] {
			t.Errorf("%q: relayed %v, dropped %v, want %v", q, m(&relayed), m(&dropped), want)
		}
	}
}
