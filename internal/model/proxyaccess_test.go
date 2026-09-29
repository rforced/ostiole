package model

import (
	"slices"
	"testing"
)

func TestValidateProxyAccess(t *testing.T) {
	t.Parallel()
	good := func() *Config {
		cfg := proxyStarter()
		cfg.Aliases = []Alias{
			{Name: "home", Type: AliasHosts, Entries: []string{"198.51.100.0/24"}},
			{Name: "us", Type: AliasGeoIP, Entries: []string{"us"}},
			{Name: "web", Type: AliasPorts, Entries: []string{"443"}},
		}
		cfg.Services.Proxy = workingProxy()
		cfg.Services.Proxy.Access = append(cfg.Services.Proxy.Access,
			ProxyAccess{ID: "us-https", Enabled: true, Zone: "wan", Action: ActionAccept,
				Ports: []string{ProxyPortHTTPS}, Source: Endpoint{Alias: "us", NotAddresses: true}},
			ProxyAccess{ID: "home-imap", Enabled: true, Zone: "lan", Action: ActionReject, Log: true,
				Routes: []string{"imap"}, Source: Endpoint{Alias: "home"}},
		)
		return cfg
	}
	if err := good().Validate(); err != nil {
		t.Fatalf("a working access list was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(a *ProxyAccess)
		path string
	}{
		{"bad id", func(a *ProxyAccess) { a.ID = "has space" }, "services.proxy.access[1].id"},
		{"duplicate id", func(a *ProxyAccess) { a.ID = "wan" }, "services.proxy.access[1].id"},
		{"unknown zone", func(a *ProxyAccess) { a.Zone = "dmz" }, "services.proxy.access[1].zone"},
		{"nothing named", func(a *ProxyAccess) { a.Ports, a.Routes = nil, nil }, "services.proxy.access[1].ports"},
		{"unknown port", func(a *ProxyAccess) { a.Ports = []string{"8080"} }, "services.proxy.access[1].ports[0]"},
		{"port twice", func(a *ProxyAccess) { a.Ports = []string{"https", "https"} }, "services.proxy.access[1].ports[1]"},
		{"unknown route", func(a *ProxyAccess) { a.Routes = []string{"ghost"} }, "services.proxy.access[1].routes[0]"},
		{"route twice", func(a *ProxyAccess) { a.Routes = []string{"imap", "imap"} }, "services.proxy.access[1].routes[1]"},
		{"unknown action", func(a *ProxyAccess) { a.Action = "allow" }, "services.proxy.access[1].action"},
		{"source ports", func(a *ProxyAccess) { a.Source.Ports = []string{"1024"} }, "services.proxy.access[1].source"},
		{"source self", func(a *ProxyAccess) { a.Source = Endpoint{Self: true} }, "services.proxy.access[1].source.self"},
		{"ports alias", func(a *ProxyAccess) { a.Source.Alias = "web" }, "services.proxy.access[1].source.alias"},
		{"unknown alias", func(a *ProxyAccess) { a.Source.Alias = "ghost" }, "services.proxy.access[1].source.alias"},
		{"bad address", func(a *ProxyAccess) { a.Source = Endpoint{Addresses: []string{"nope"}} },
			"services.proxy.access[1].source.addresses[0]"},
		{"nothing to invert", func(a *ProxyAccess) { a.Source = Endpoint{NotAddresses: true} },
			"services.proxy.access[1].source.notAddresses"},
		{"unknown peer", func(a *ProxyAccess) { a.Source = Endpoint{Peer: "wg0/laptop"} },
			"services.proxy.access[1].source.peer"},
	} {
		cfg := good()
		tc.edit(&cfg.Services.Proxy.Access[1])
		if !hasIssue(t, cfg, tc.path) {
			t.Errorf("%s passed", tc.name)
		}
	}
}

func TestValidateAllowFromTakesSmallAliases(t *testing.T) {
	t.Parallel()
	cfg := proxyStarter()
	cfg.Aliases = []Alias{
		{Name: "office", Type: AliasHosts, Entries: []string{"198.51.100.0/24", "2001:db8::5"}},
		{Name: "feed", Type: AliasHosts, URL: "https://example.com/list.txt"},
		{Name: "us", Type: AliasGeoIP, Entries: []string{"us"}},
		{Name: "isp", Type: AliasASN, Entries: []string{"AS64500"}},
		{Name: "web", Type: AliasPorts, Entries: []string{"443"}},
	}
	cfg.Services.Proxy = workingProxy()
	cfg.Services.Proxy.Sites[0].AllowFrom = []string{"office", "192.168.0.0/16"}
	cfg.Services.Proxy.Routes[1].AllowFrom = []string{"office"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a hosts alias was refused: %v", err)
	}
	for _, name := range []string{"feed", "us", "isp", "web", "ghost", "not an address"} {
		c := proxyStarter()
		c.Aliases = cfg.Aliases
		c.Services.Proxy = workingProxy()
		c.Services.Proxy.Sites[0].AllowFrom = []string{name}
		if !hasIssue(t, c, "services.proxy.sites[0].allowFrom[0]") {
			t.Errorf("allowFrom %q passed", name)
		}
	}

	got := cfg.AllowFromRanges([]string{"office", "192.168.0.0/16", "ghost", " 10.0.0.1 "})
	want := []string{"198.51.100.0/24", "2001:db8::5", "192.168.0.0/16", "10.0.0.1"}
	if !slices.Equal(got, want) {
		t.Errorf("ranges = %v, want %v", got, want)
	}
	// An alias with nothing written in it lets nobody in rather than
	// everybody: the list stays empty, not nil.
	cfg.Aliases[0].Entries = nil
	if got := cfg.AllowFromRanges([]string{"office"}); got == nil || len(got) != 0 {
		t.Errorf("ranges = %#v, want an empty list", got)
	}
}

func TestAccessPortsFollowTheListeners(t *testing.T) {
	t.Parallel()
	cfg := proxyStarter()
	cfg.Services.Proxy = workingProxy()
	p := &cfg.Services.Proxy
	p.HTTPPort, p.HTTPSPort = 8080, 8443
	tcp, udp := cfg.AccessPorts(ProxyAccess{Ports: []string{ProxyPortHTTP, ProxyPortHTTPS}, Routes: []string{"imap", "dns"}})
	if !slices.Equal(tcp, []uint16{993, 8080, 8443}) || !slices.Equal(udp, []uint16{5353, 8443}) {
		t.Errorf("tcp %v udp %v", tcp, udp)
	}
	// A route that is off opens nothing, and HTTPS without HTTP/3 is TCP.
	p.HTTP3 = false
	p.Routes[1].Enabled = false
	tcp, udp = cfg.AccessPorts(ProxyAccess{Ports: []string{ProxyPortHTTPS}, Routes: []string{"imap"}})
	if !slices.Equal(tcp, []uint16{8443}) || udp != nil {
		t.Errorf("tcp %v udp %v", tcp, udp)
	}
	p.Access[0].Routes = []string{"imap", "dns"}
	if got := p.RouteZones(cfg, "dns"); !slices.Equal(got, []string{"wan"}) {
		t.Errorf("dns is reachable on %v", got)
	}
	if got := p.RouteZones(cfg, "mail"); got != nil {
		t.Errorf("mail is reachable on %v, want nowhere", got)
	}
}

func TestProxyAnswersChallenges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		edit    func(c *Config)
		answers bool
		limited bool
	}{
		{"open on wan", func(*Config) {}, true, false},
		{"lan only", func(c *Config) { c.Services.Proxy.Access = openOn("lan") }, false, false},
		{"https alone", func(c *Config) { c.Services.Proxy.Access[0].Ports = []string{ProxyPortHTTPS} }, false, false},
		{"another port", func(c *Config) { c.Services.Proxy.HTTPPort = 8080 }, false, false},
		{"line off", func(c *Config) { c.Services.Proxy.Access[0].Enabled = false }, false, false},
		{"drop line", func(c *Config) { c.Services.Proxy.Access[0].Action = ActionDrop }, false, false},
		{"from one country", func(c *Config) {
			c.Services.Proxy.Access[0].Source = Endpoint{Alias: "us"}
		}, true, true},
		{"one country and anywhere", func(c *Config) {
			c.Services.Proxy.Access[0].Source = Endpoint{Alias: "us"}
			c.Services.Proxy.Access = append(c.Services.Proxy.Access, openOn("wan")...)
			c.Services.Proxy.Access[1].ID = "wan-all"
		}, true, false},
		{"no certificate", func(c *Config) { c.Certificates = nil }, true, false},
	} {
		cfg := proxyStarter()
		cfg.Aliases = []Alias{{Name: "us", Type: AliasGeoIP, Entries: []string{"us"}}}
		cfg.ACME.Accounts = []ACMEAccount{{ID: "ca", Directory: ACMEDirectoryLetsEncryptStaging}}
		cfg.Certificates = []Certificate{{ID: "router", Enabled: true, Source: SourceACME,
			Names: []string{"router.example.com"}, Account: "ca", Challenge: ChallengeHTTP}}
		cfg.Services.Proxy = workingProxy()
		tc.edit(cfg)
		if got := cfg.ProxyAnswersChallenges(); got != tc.answers {
			t.Errorf("%s: answers challenges = %v, want %v", tc.name, got, tc.answers)
		}
		if got := cfg.ChallengesLimited(); got != tc.limited {
			t.Errorf("%s: limited = %v, want %v", tc.name, got, tc.limited)
		}
	}
}
