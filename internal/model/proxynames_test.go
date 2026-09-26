package model

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// proxyOnLAN serves one site to the LAN from a router whose DNS answers
// the LAN under "lan".
func proxyOnLAN() *Config {
	cfg := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	cfg.System.Management.WebPort = 8443
	cfg.Services.Proxy = Proxy{
		Enabled: true,
		Zones:   []string{"lan", "wan"},
		Pools:   []ProxyPool{{ID: "media", Upstreams: []ProxyUpstream{{Address: "192.168.1.11:8096"}}}},
		Sites: []ProxySite{
			{ID: "watch", Enabled: true, Pool: "media",
				Hosts: []string{"watch.lan", "Watch.Example.com", "*.example.org", "media"}},
			{ID: "old", Pool: "media", Hosts: []string{"old.example.com"}},
		},
	}
	return cfg
}

func TestProxyNames(t *testing.T) {
	t.Parallel()
	cfg := proxyOnLAN()
	var got []string
	for _, pn := range cfg.ProxyNames() {
		got = append(got, pn.Names()...)
		// The WAN is external, so the DNS service does not answer there.
		if pn.Site != "watch" || !slices.Equal(pn.Interfaces, []string{"eth1"}) {
			t.Errorf("%s: site %q on %v, want watch on [eth1]", pn.Name, pn.Site, pn.Interfaces)
		}
	}
	// Full names first, bare labels for the local domain, the wildcard and
	// the switched-off site left out.
	want := []string{"watch.lan", "watch", "watch.example.com", "media.lan", "media"}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}

	for name, change := range map[string]func(*Config){
		"DNS off":           func(c *Config) { c.Services.DNS.Enabled = false },
		"proxy off":         func(c *Config) { c.Services.Proxy.Enabled = false },
		"proxy on WAN only": func(c *Config) { c.Services.Proxy.Zones = []string{"wan"} },
		"DNS elsewhere": func(c *Config) {
			c.Services.Proxy.Zones = []string{"lan"}
			c.Services.DNS.Interfaces = []string{"eth0"}
		},
	} {
		c := proxyOnLAN()
		change(c)
		if got := c.ProxyNames(); len(got) != 0 {
			t.Errorf("%s: names = %v, want none", name, got)
		}
	}
}

// A name a site answers cannot be an override or a static lease's name as
// well: the router answers it with its own address, so the other answer
// would never be heard.
func TestValidateNamesAProxySiteAnswers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*Config)
		path   string
	}{
		{"local override", func(c *Config) {
			c.Services.DNS.HostOverrides = []HostOverride{{Hostname: "watch", IP: "192.168.1.11"}}
		}, "services.dns.hostOverrides[0].hostname"},
		{"public override", func(c *Config) {
			c.Services.DNS.HostOverrides = []HostOverride{{Hostname: "watch", Domain: "example.com", IP: "192.168.1.11"}}
		}, "services.dns.hostOverrides[0].hostname"},
		{"alias", func(c *Config) {
			c.Services.DNS.HostOverrides = []HostOverride{{Hostname: "server", IP: "192.168.1.11", Aliases: []string{"media"}}}
		}, "services.dns.hostOverrides[0].aliases[0]"},
		{"static lease", func(c *Config) {
			c.Services.DHCP.StaticLeases = []StaticLease{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.11", Hostname: "watch"}}
		}, "services.dhcp.staticLeases[0].hostname"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := proxyOnLAN()
			tc.change(cfg)
			var ve *ValidationError
			if !errors.As(cfg.Validate(), &ve) {
				t.Fatal("a name the site answers passed")
			}
			found := false
			for _, i := range ve.Issues {
				if i.Path == tc.path && strings.Contains(i.Message, `reverse proxy site "watch"`) {
					found = true
				}
			}
			if !found {
				t.Errorf("issues = %v, want one at %s", ve.Issues, tc.path)
			}
		})
	}

	// Once the site stops answering on the LAN the names are free again.
	cfg := proxyOnLAN()
	cfg.Services.Proxy.Zones = []string{"wan"}
	cfg.Services.DNS.HostOverrides = []HostOverride{{Hostname: "watch", IP: "192.168.1.11"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("an override for a site served only outside was refused: %v", err)
	}
}

// The router answers a site's names itself, so no list blocks them.
func TestNeverBlockedCoversProxySiteNames(t *testing.T) {
	t.Parallel()
	got := proxyOnLAN().NeverBlocked()
	for _, want := range []string{"watch.lan", "watch", "watch.example.com", "media.lan", "media"} {
		if !slices.Contains(got, want) {
			t.Errorf("%q is not protected; have %v", want, got)
		}
	}
	if slices.Contains(got, "old.example.com") {
		t.Errorf("a switched-off site's name is protected: %v", got)
	}
}
