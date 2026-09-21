package gateway

import (
	"net"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

func detectConfig(gateways ...model.Gateway) *model.Config {
	return &model.Config{
		Version:  model.SchemaVersion,
		Zones:    []model.Zone{{Name: "wan", External: true}},
		Gateways: gateways,
	}
}

func TestCoveredBy(t *testing.T) {
	t.Parallel()
	cfg := detectConfig(
		model.Gateway{Name: "wan1", Enabled: true, Interface: "eth0"},
		model.Gateway{Name: "backup", Enabled: true, Interface: "eth1", Address: "198.51.100.1"},
	)
	cases := map[string]struct {
		in   Detected
		want string
	}{
		"a dynamic gateway covers whatever it learns": {
			Detected{Address: "203.0.113.1", Interface: "eth0"}, "wan1"},
		"an address that matches": {
			Detected{Address: "198.51.100.1", Interface: "eth1"}, "backup"},
		"the same address on another interface is another gateway": {
			Detected{Address: "198.51.100.1", Interface: "eth9"}, ""},
		"an address the configured one does not claim": {
			Detected{Address: "198.51.100.9", Interface: "eth1"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := coveredBy(cfg, tc.in); got != tc.want {
				t.Errorf("coveredBy = %q, want %q", got, tc.want)
			}
		})
	}
}

// Adopting a DHCP route must not pin the address it happens to have: the
// next lease would break it.
func TestSuggestLeavesDynamicAddressesAlone(t *testing.T) {
	t.Parallel()
	cfg := detectConfig()
	dynamic := Suggest(cfg, Detected{Address: "203.0.113.1", Interface: "eth0", Protocol: "dhcp"})
	if dynamic.Address != "" {
		t.Errorf("address = %q, want it left to DHCP", dynamic.Address)
	}
	if dynamic.Name != "gw_eth0" || dynamic.Interface != "eth0" || !dynamic.Enabled {
		t.Errorf("suggestion = %+v", dynamic)
	}

	static := Suggest(cfg, Detected{Address: "203.0.113.1", Interface: "eth0", Protocol: "static"})
	if static.Address != "203.0.113.1" {
		t.Errorf("a static route should keep its address, got %q", static.Address)
	}
	ra := Suggest(cfg, Detected{Address: "fe80::1", Interface: "eth0", Protocol: "ra"})
	if ra.Address != "" {
		t.Errorf("a route from an advertisement should not be pinned, got %q", ra.Address)
	}
}

func TestSuggestFindsAFreeName(t *testing.T) {
	t.Parallel()
	cfg := detectConfig(model.Gateway{Name: "gw_eth0", Interface: "eth0"})
	got := Suggest(cfg, Detected{Interface: "eth0", Protocol: "dhcp"})
	if got.Name != "gw_eth02" {
		t.Errorf("name = %q, want one that is free", got.Name)
	}
	// Interface names with dots and dashes still make a valid name.
	vlan := Suggest(detectConfig(), Detected{Interface: "enp8s0.101", Protocol: "dhcp"})
	if vlan.Name != "gw_enp8s0_101" {
		t.Errorf("name = %q", vlan.Name)
	}
	if err := (&model.Config{
		Version: model.SchemaVersion,
		Zones:   []model.Zone{{Name: "wan"}},
		Interfaces: []model.Interface{{Name: "enp8s0.101", Enabled: true,
			IPv4: model.IPv4{Mode: model.AddrDHCP}, IPv6: model.IPv6{Mode: model.AddrNone},
			VLAN: &model.VLAN{Parent: "enp8s0", ID: 101}}},
		Gateways: []model.Gateway{vlan},
		NAT:      model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundDisabled}},
	}).Validate(); err != nil {
		t.Errorf("the suggested gateway does not validate: %v", err)
	}
}

func TestRouteProtocolNames(t *testing.T) {
	t.Parallel()
	for proto, want := range map[int]string{16: "dhcp", 9: "ra", 3: "static", 2: "kernel"} {
		if got := routeProtocol(netlink.RouteProtocol(proto)); got != want {
			t.Errorf("protocol %d = %q, want %q", proto, got, want)
		}
	}
}

// Detect reads the real routing table, so it runs in a namespace where a
// default route can be added without touching the host.
func TestDetectReadsTheKernel(t *testing.T) {
	if os.Getenv("OSTIOLE_DETECT_NETNS") == "" {
		runInNamespace(t, "OSTIOLE_DETECT_NETNS", "TestDetectReadsTheKernel")
		return
	}

	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "wan0"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("add link: %v", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("up: %v", err)
	}
	addr, _ := netlink.ParseAddr("203.0.113.2/24")
	if err := netlink.AddrAdd(link, addr); err != nil {
		t.Fatalf("address: %v", err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP("203.0.113.1"),
		Priority:  100,
	}); err != nil {
		t.Fatalf("default route: %v", err)
	}

	// Nothing configured: the route is reported as unclaimed.
	got, err := Detect(detectConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("detected %+v, want the one default route", got)
	}
	d := got[0]
	if d.Address != "203.0.113.1" || d.Interface != "wan0" || d.Metric != 100 || d.Family != "IPv4" {
		t.Errorf("detected = %+v", d)
	}
	if d.Configured != "" {
		t.Errorf("an unconfigured route was reported as covered by %q", d.Configured)
	}

	// Once a gateway covers it, the page can stop offering it.
	covered, err := Detect(detectConfig(model.Gateway{Name: "wan1", Enabled: true, Interface: "wan0"}))
	if err != nil {
		t.Fatal(err)
	}
	if covered[0].Configured != "wan1" {
		t.Errorf("configured = %q, want wan1", covered[0].Configured)
	}
	// And what it suggests would actually apply.
	s := Suggest(detectConfig(), d)
	if s.Interface != "wan0" || !strings.HasPrefix(s.Name, "gw_") {
		t.Errorf("suggestion = %+v", s)
	}
}
