package model

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func exitNodeConfig() *Config {
	cfg := Starter(StarterOptions{Hostname: "gateway", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "tailnet"})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name:      TailscaleDevice,
		Zone:      "tailnet",
		Enabled:   true,
		IPv4:      IPv4{Mode: AddrNone},
		IPv6:      IPv6{Mode: AddrNone},
		Tailscale: &Tailscale{Port: 41641},
	})
	cfg.Gateways = append(cfg.Gateways, Gateway{
		Name:      "amsterdam",
		Enabled:   true,
		Interface: TailscaleDevice,
		ExitNode:  "ams-1",
	})
	return cfg
}

func TestExitNodeGatewayValidatesAndActsAsTunnel(t *testing.T) {
	t.Parallel()
	cfg := exitNodeConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid exit-node config failed: %v", err)
	}
	gw, ok := cfg.ExitNode()
	if !ok || gw.Name != "amsterdam" || gw.ExitNode != "ams-1" {
		t.Fatalf("ExitNode() = %+v, %v", gw, ok)
	}
	if !cfg.TunnelGateway(gw) {
		t.Error("exit-node gateway must count as a tunnel gateway")
	}
	if v4, v6 := cfg.TunnelFamilies(gw); !v4 || !v6 {
		t.Errorf("TunnelFamilies = (%v, %v), want (true, true)", v4, v6)
	}
	if cfg.CanFailover() {
		t.Error("an exit-node gateway must not trigger default-route failover")
	}
	if !slices.ContainsFunc(cfg.BlockingTargets(), func(b BlockingTarget) bool { return b.Name == "amsterdam" }) {
		t.Error("a lone exit-node gateway must be a blocking target")
	}
	// Dotted MagicDNS names and tailnet IPv4/IPv6 addresses are accepted, as is
	// an optional IPv4 monitor and routing DNS lookups through it.
	for _, node := range []string{"ams-1.tail1234.ts.net", "ams-1.tail1234.ts.net.", "100.64.0.9", "fd7a:115c:a1e0::9"} {
		c := exitNodeConfig()
		c.Gateways[len(c.Gateways)-1].ExitNode = node
		c.Gateways[len(c.Gateways)-1].Monitor = "1.1.1.1"
		c.Services.DNS.Enabled = true
		c.Services.DNS.Upstreams = []string{"9.9.9.9"}
		c.Services.DNS.Via = "amsterdam"
		if err := c.Validate(); err != nil {
			t.Errorf("exitNode %q failed validation: %v", node, err)
		}
	}
	// A second disabled exit-node gateway is allowed so the operator can swap.
	c := exitNodeConfig()
	c.Gateways = append(c.Gateways, Gateway{
		Name: "tokyo", Enabled: false, Interface: TailscaleDevice, ExitNode: "tok-1",
	})
	if err := c.Validate(); err != nil {
		t.Errorf("second disabled exit-node gateway failed: %v", err)
	}
}

func TestValidateExitNodeGatewayRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(*Config)
		wantPath string
		wantSub  string
	}{
		{
			name: "missing exitNode on tailscale interface",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].ExitNode = ""
			},
			wantPath: "exitNode",
			wantSub:  "name the exit node",
		},
		{
			name: "address set on exit-node gateway",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].Address = "100.64.0.1"
			},
			wantPath: "address",
			wantSub:  "leave it empty",
		},
		{
			name: "priority set on exit-node gateway",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].Priority = 10
			},
			wantPath: "priority",
			wantSub:  "leave it at 0",
		},
		{
			name: "IPv6 monitor on exit-node gateway",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].Monitor = "2606:4700::1111"
			},
			wantPath: "monitor",
			wantSub:  "IPv4",
		},
		{
			name: "non-tailnet IPv4 as exitNode",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].ExitNode = "1.1.1.1"
			},
			wantPath: "exitNode",
			wantSub:  "not a tailnet address",
		},
		{
			name: "non-tailnet IPv6 as exitNode",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].ExitNode = "2001:db8::1"
			},
			wantPath: "exitNode",
			wantSub:  "not a tailnet address",
		},
		{
			name: "invalid hostname as exitNode",
			mutate: func(c *Config) {
				c.Gateways[len(c.Gateways)-1].ExitNode = "bad host!"
			},
			wantPath: "exitNode",
			wantSub:  "tailnet node name",
		},
		{
			name: "exitNode on non-Tailscale interface",
			mutate: func(c *Config) {
				c.Gateways = append(c.Gateways, Gateway{
					Name: "badwan", Enabled: true, Interface: "eth0", ExitNode: "ams-1",
				})
			},
			wantPath: "exitNode",
			wantSub:  "only a Tailscale interface",
		},
		{
			name: "second enabled exit-node gateway",
			mutate: func(c *Config) {
				c.Gateways = append(c.Gateways, Gateway{
					Name: "tokyo", Enabled: true, Interface: TailscaleDevice, ExitNode: "tok-1",
				})
			},
			wantPath: "exitNode",
			wantSub:  "one exit node at a time",
		},
		{
			name: "advertiseExitNode conflicts with enabled exit-node gateway",
			mutate: func(c *Config) {
				in, _ := c.TailscaleInterface()
				in.Tailscale.AdvertiseExitNode = true
			},
			wantPath: ".tailscale.advertiseExitNode",
			wantSub:  `"amsterdam"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := exitNodeConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			var verr *ValidationError
			if errors.As(err, &verr) {
				for _, e := range verr.Issues {
					if strings.Contains(e.Path, tc.wantPath) && strings.Contains(e.Message, tc.wantSub) {
						return
					}
				}
			}
			t.Errorf("Validate = %v, want error on %q containing %q", err, tc.wantPath, tc.wantSub)
		})
	}
}
