package install

import (
	"net/netip"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// An address the kernel marks permanent came from a file and becomes a
// static address with the default route behind it; one without that flag
// came from a lease and becomes DHCP, so the switch renews it rather than
// pinning somebody else's.
func TestSeedInterfaceFollowsTheAddressFlags(t *testing.T) {
	t.Parallel()
	lease := network.Link{
		Name: "eth0", Kind: "ethernet", MTU: 1500,
		Addresses:        []string{"203.0.113.10/24", "2001:db8::10/64", "fe80::1/64"},
		DynamicAddresses: []string{"203.0.113.10/24", "2001:db8::10/64"},
	}
	in, gws := seedInterface(lease, netip.MustParseAddr("203.0.113.1"), netip.Addr{})
	if in.IPv4.Mode != model.AddrDHCP || in.IPv6.Mode != model.AddrSLAAC {
		t.Errorf("leased interface = %+v", in)
	}
	if len(gws) != 0 {
		t.Errorf("a leased interface was given a gateway: %+v", gws)
	}
	if in.IPv4.Address != "" {
		t.Errorf("a lease was pinned as a static address: %q", in.IPv4.Address)
	}

	fixed := network.Link{
		Name: "enp1s0", Kind: "ethernet", MTU: 9000,
		Addresses: []string{"198.51.100.5/24"},
	}
	in, gws = seedInterface(fixed, netip.MustParseAddr("198.51.100.1"), netip.Addr{})
	if in.IPv4.Mode != model.AddrStatic || in.IPv4.Address != "198.51.100.5/24" {
		t.Errorf("fixed interface = %+v", in)
	}
	if in.IPv6.Mode != model.AddrNone {
		t.Errorf("an interface with no v6 address was given %s", in.IPv6.Mode)
	}
	if in.MTU != 9000 {
		t.Errorf("MTU = %d, want the one the link has now", in.MTU)
	}
	if len(gws) != 1 || gws[0].Address != "198.51.100.1" || gws[0].Interface != "enp1s0" {
		t.Fatalf("gateways = %+v", gws)
	}

	// Nothing global, nothing to render.
	if in, _ := seedInterface(network.Link{Name: "eth1", Addresses: []string{"fe80::2/64"}}, netip.Addr{}, netip.Addr{}); in != nil {
		t.Errorf("an interface with only a link-local address was seeded: %+v", in)
	}
}

// The seed is rendered by the networkd backend, which validates first, so
// whatever it builds has to be a configuration.
func TestSeedRendersAsAConfiguration(t *testing.T) {
	t.Parallel()
	cfg := model.Starter(model.StarterOptions{})
	in, gws := seedInterface(network.Link{
		Name: "eth0", Kind: "ethernet", Addresses: []string{"203.0.113.10/24"},
	}, netip.MustParseAddr("203.0.113.1"), netip.Addr{})
	cfg.Interfaces = append(cfg.Interfaces, *in)
	cfg.Gateways = append(cfg.Gateways, gws...)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the seeded configuration does not validate: %v", err)
	}
	files, err := network.NewNetworkd().Render(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, ok := files["00-ostiole-eth0.network"]; !ok {
		t.Errorf("no unit for eth0: %v", files.Names())
	}
}
