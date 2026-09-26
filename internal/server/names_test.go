package server

import (
	"slices"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/services"
)

// The router of the watch.jd diagnosis: a site the proxy serves on the
// LAN, an override, a static lease, and devices that sent names.
func namesConfig() *model.Config {
	cfg := model.Starter(model.StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "10.0.0.1/24", WAN: "eth0", Services: true})
	cfg.System.Management.WebPort = 8443
	cfg.Services.DNS.Domain = "jd"
	cfg.Services.DNS.HostOverrides = []model.HostOverride{{Hostname: "printer", IP: "10.0.0.30"}}
	cfg.Services.DHCP.Servers[0].DNSRegistration = true
	for i := range cfg.Interfaces {
		if cfg.Interfaces[i].Name == "eth1" {
			cfg.Interfaces[i].IPv6 = model.IPv6{Mode: model.AddrStatic, Address: "2001:db8:1::1/64"}
		}
	}
	cfg.Services.DHCP.V6 = []model.DHCPv6Server{{Interface: "eth1", Enabled: true, Mode: model.RAManaged,
		RangeStart: "::100", RangeEnd: "::1ff", DNSRegistration: true}}
	cfg.Services.DHCP.StaticLeases = []model.StaticLease{{MAC: "aa:bb:cc:00:00:11", IP: "10.0.0.11", Hostname: "server"}}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true,
		Zones:   []string{"lan"},
		Pools:   []model.ProxyPool{{ID: "media", Upstreams: []model.ProxyUpstream{{Address: "10.0.0.11:8096"}}}},
		Sites:   []model.ProxySite{{ID: "watch", Enabled: true, Pool: "media", Hosts: []string{"watch.jd"}}},
	}
	return cfg
}

func TestDNSNames(t *testing.T) {
	t.Parallel()
	cfg := namesConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	expires := time.Unix(1790471918, 0).UTC()
	file := []services.Lease{
		{MAC: "3a:8a:33:e0:8f:75", IP: "10.0.0.185", Hostname: "Watch", Family: 4, Expires: expires},
		{MAC: "aa:bb:cc:00:00:11", IP: "10.0.0.11", Hostname: "server", Family: 4},
		{MAC: "aa:bb:cc:00:00:30", IP: "10.0.0.30", Hostname: "printer", Family: 4},
		{MAC: "aa:bb:cc:00:00:31", IP: "10.0.0.31", Hostname: "printer", Family: 4},
		{MAC: "aa:bb:cc:00:00:40", IP: "10.0.0.40", Family: 4},
		{IP: "2001:db8:1::150", Hostname: "phone", Family: 6},
		{IP: "2001:db8:9::150", Hostname: "tablet", Family: 6},
	}
	links := []network.Link{
		{Name: "eth1", Addresses: []string{"10.0.0.1/24", "fe80::1/64", "2001:db8:1::1/64"}},
		{Name: "eth9", Addresses: []string{"2001:db8:9::1/64"}},
	}
	rows := dnsNames(cfg, file, links)
	type row struct{ name, source, held string }
	var got []row
	for _, r := range rows {
		held := ""
		if r.HeldBy != nil {
			held = r.HeldBy.Source + ":" + r.HeldBy.Name
		}
		got = append(got, row{r.Name, r.Source, held})
	}
	want := []row{
		{"server.jd", "static", ""},
		{"watch.jd", "proxy", ""},
		// The site answers watch.jd with the router's address, so the
		// device keeps its address and loses the name.
		{"Watch.jd", "device", "proxy:watch"},
		// The override answers printer with the device's own address.
		{"printer.jd", "device", ""},
		{"printer.jd", "device", "override:printer.jd"},
		{"phone.jd", "device", ""},
	}
	if !slices.Equal(got, want) {
		t.Errorf("rows =\n%v\nwant\n%v", got, want)
	}
	proxy := rows[1]
	if !slices.Equal(proxy.Addresses, []string{"10.0.0.1", "2001:db8:1::1"}) || proxy.Bare != "watch" ||
		!slices.Equal(proxy.Interfaces, []string{"eth1"}) {
		t.Errorf("proxy row = %+v", proxy)
	}
	if rows[2].Bare != "Watch" || !rows[2].Expires.Equal(expires) || rows[2].MAC != "3a:8a:33:e0:8f:75" {
		t.Errorf("device row = %+v", rows[2])
	}

	// A server that does not register lists none of its devices.
	cfg.Services.DHCP.Servers[0].DNSRegistration = false
	cfg.Services.DHCP.V6[0].DNSRegistration = false
	for _, r := range dnsNames(cfg, file, links) {
		if r.Source == "device" {
			t.Errorf("a device on a server that does not register: %+v", r)
		}
	}
}
