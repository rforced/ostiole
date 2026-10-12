package model

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// testPEM is a block that parses; nothing reads what is in it.
func testPEM(kind string) string {
	return "-----BEGIN " + kind + "-----\nTGFiIGJsb2NrIGZvciBhIHRlc3Q=\n-----END " + kind + "-----\n"
}

// ovpnConfig is a starter router with ovpn0 dialling a made-up server, in
// an external zone, and a tunnel gateway on it.
func ovpnConfig(t *testing.T) *Config {
	t.Helper()
	cfg := Starter(StarterOptions{Hostname: "fw", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0"})
	cfg.Zones = append(cfg.Zones, Zone{Name: "vpnout", External: true})
	cfg.Interfaces = append(cfg.Interfaces, Interface{
		Name:    "ovpn0",
		Zone:    "vpnout",
		Enabled: true,
		IPv4:    IPv4{Mode: AddrNone},
		IPv6:    IPv6{Mode: AddrNone},
		OpenVPN: &OpenVPN{
			Remotes:        []OpenVPNRemote{{Host: "vpn.example", Port: 1194, Proto: "udp"}, {Host: "198.51.100.9", Port: 443, Proto: "tcp"}},
			CAPEM:          testPEM("CERTIFICATE"),
			CertPEM:        testPEM("CERTIFICATE"),
			KeyPEM:         testPEM("PRIVATE KEY"),
			TLSCryptKeyPEM: testPEM("OpenVPN Static key V1"),
			Username:       "lab-user",
			Password:       "made-up",
			VerifyName:     "lab-server",
			DataCiphers:    []string{"AES-256-GCM", "CHACHA20-POLY1305"},
			Routes:         []string{"10.8.0.0/24", "0.0.0.0/0", "fd00:8::/64", "10.8.0.0/24"},
			Keepalive:      15,
		},
	})
	cfg.Gateways = []Gateway{
		{Name: "wan", Enabled: true, Interface: "eth0", Monitor: "9.9.9.9"},
		{Name: "vpn", Enabled: true, Interface: "ovpn0", Monitor: "10.8.0.1"},
	}
	return cfg
}

func TestAnOpenVPNTunnelValidates(t *testing.T) {
	t.Parallel()
	cfg := ovpnConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	in := tunnel(cfg)
	if in.Kind() != KindOpenVPN || !in.Tunnel() {
		t.Errorf("kind = %q, tunnel = %v; want openvpn, true", in.Kind(), in.Tunnel())
	}
	vpn, _ := cfg.Gateway("vpn")
	if !cfg.TunnelGateway(*vpn) {
		t.Error("a gateway on an OpenVPN tunnel is not a tunnel gateway")
	}
	if v4, v6 := cfg.TunnelFamilies(*vpn); !v4 || v6 {
		t.Errorf("families = %v, %v; want IPv4 alone", v4, v6)
	}
	var got []netip.Prefix
	for _, r := range in.TunnelRoutes() {
		got = append(got, r.Prefix)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.8.0.0/24"), netip.MustParsePrefix("fd00:8::/64")}
	if !slices.Equal(got, want) {
		t.Errorf("TunnelRoutes = %v, want %v", got, want)
	}
	if names := cfg.OpenVPNInterfaces(); len(names) != 1 || names[0].Name != "ovpn0" {
		t.Errorf("OpenVPNInterfaces = %v, want ovpn0", names)
	}
	in.Enabled = false
	if names := cfg.OpenVPNInterfaces(); len(names) != 0 {
		t.Errorf("OpenVPNInterfaces lists a disabled tunnel: %v", names)
	}
}

func TestOpenVPNDefaults(t *testing.T) {
	t.Parallel()
	if got := (&OpenVPN{}).KeepaliveSeconds(); got != 10 {
		t.Errorf("KeepaliveSeconds = %d, want 10", got)
	}
	if got := (&OpenVPN{Keepalive: 25}).KeepaliveSeconds(); got != 25 {
		t.Errorf("KeepaliveSeconds = %d, want 25", got)
	}
	if got := (OpenVPNRemote{}).ProtoOrDefault(); got != "udp" {
		t.Errorf("ProtoOrDefault = %q, want udp", got)
	}
	if got := (OpenVPNRemote{Proto: "tcp"}).ProtoOrDefault(); got != "tcp" {
		t.Errorf("ProtoOrDefault = %q, want tcp", got)
	}
}

func TestValidateCatchesOpenVPNMistakes(t *testing.T) {
	t.Parallel()
	const at = "interfaces[2]"
	const ov = at + ".openvpn"
	o := func(c *Config) *OpenVPN { return tunnel(c).OpenVPN }
	one := 1
	two := 2
	for _, tc := range []struct {
		name string
		edit func(*Config)
		want string // the path of the one issue
		msg  string // part of what it says
	}{
		{"a name that is not ovpnN", func(c *Config) {
			tunnel(c).Name = "vpn0"
			c.Gateways[1].Interface = "vpn0"
		}, at + ".name", "must match"},
		{"an IPv4 mode", func(c *Config) { tunnel(c).IPv4 = IPv4{Mode: AddrDHCP} }, at + ".ipv4.mode", "set it to none"},
		{"an IPv6 mode", func(c *Config) { tunnel(c).IPv6 = IPv6{Mode: AddrSLAAC} }, at + ".ipv6.mode", "set it to none"},
		{"a small MTU", func(c *Config) { tunnel(c).MTU = 500 }, at + ".mtu", "576-65535"},
		{"no remotes", func(c *Config) { o(c).Remotes = nil }, ov + ".remotes", "at least one server"},
		{"a remote that is not a host", func(c *Config) { o(c).Remotes[0].Host = "vpn example" }, ov + ".remotes[0].host", "host name or an IP"},
		{"a remote inside a route", func(c *Config) { o(c).Remotes[1].Host = "10.8.0.9" }, ov + ".remotes[1].host", "routed into this tunnel"},
		{"a port of 0", func(c *Config) { o(c).Remotes[0].Port = 0 }, ov + ".remotes[0].port", "1-65535"},
		{"a port too high", func(c *Config) { o(c).Remotes[0].Port = 70000 }, ov + ".remotes[0].port", "1-65535"},
		{"an unknown proto", func(c *Config) { o(c).Remotes[0].Proto = "udp6" }, ov + ".remotes[0].proto", "udp or tcp"},
		{"no CA", func(c *Config) { o(c).CAPEM = "" }, ov + ".caPem", "CA certificate is required"},
		{"a CA that is not PEM", func(c *Config) { o(c).CAPEM = "ca.crt" }, ov + ".caPem", "not PEM"},
		{"a block that closes the inline section", func(c *Config) { o(c).CertPEM += "</cert>\nup /bin/sh\n" }, ov + ".certPem", "cannot contain"},
		{"a key that is not PEM", func(c *Config) { o(c).KeyPEM = "secret" }, ov + ".keyPem", "not PEM"},
		{"a tls-crypt key that is not PEM", func(c *Config) { o(c).TLSCryptKeyPEM = "x" }, ov + ".tlsCryptKeyPem", "not PEM"},
		{"a certificate without its key", func(c *Config) { o(c).KeyPEM = "" }, ov + ".keyPem", "needs its key"},
		{"a key without its certificate", func(c *Config) { o(c).CertPEM = "" }, ov + ".certPem", "needs its certificate"},
		{"tls-crypt and tls-auth", func(c *Config) {
			o(c).TLSAuthKeyPEM = testPEM("OpenVPN Static key V1")
			o(c).KeyDirection = &one
		}, ov + ".tlsAuthKeyPem", "exclusive"},
		{"a key direction without tls-auth", func(c *Config) { o(c).KeyDirection = &one }, ov + ".keyDirection", "tls-auth key only"},
		{"tls-auth without a key direction", func(c *Config) {
			o(c).TLSCryptKeyPEM = ""
			o(c).TLSAuthKeyPEM = testPEM("OpenVPN Static key V1")
		}, ov + ".keyDirection", "needs a key direction"},
		{"a key direction of 2", func(c *Config) {
			o(c).TLSCryptKeyPEM = ""
			o(c).TLSAuthKeyPEM = testPEM("OpenVPN Static key V1")
			o(c).KeyDirection = &two
		}, ov + ".keyDirection", "must be 0 or 1"},
		{"a password without a username", func(c *Config) { o(c).Username = "" }, ov + ".username", "needs a username"},
		{"a line break in the username", func(c *Config) { o(c).Username = "lab\nuser" }, ov + ".username", "control characters"},
		{"a line break in the password", func(c *Config) { o(c).Password = "made\nup" }, ov + ".password", "control characters"},
		{"a quote in the verify name", func(c *Config) { o(c).VerifyName = `lab"server` }, ov + ".verifyName", "quotes"},
		{"an unknown cipher", func(c *Config) { o(c).DataCiphers = []string{"BF-CBC"} }, ov + ".dataCiphers[0]", "not a data cipher"},
		{"a cipher twice", func(c *Config) { o(c).DataCiphers = []string{"AES-256-GCM", "AES-256-GCM"} }, ov + ".dataCiphers[1]", "listed twice"},
		{"a route that is not a network", func(c *Config) { o(c).Routes = []string{"10.8.0.1"} }, ov + ".routes[0]", "not a network"},
		{"a route with host bits", func(c *Config) { o(c).Routes = []string{"10.8.0.1/24"} }, ov + ".routes[0]", "write 10.8.0.0/24"},
		{"a route on the LAN", func(c *Config) { o(c).Routes = []string{"192.168.1.0/25"} }, ov + ".routes[0]", "also on eth1"},
		{"a negative keepalive", func(c *Config) { o(c).Keepalive = -1 }, ov + ".keepalive", "0-3600"},
		{"a keepalive too long", func(c *Config) { o(c).Keepalive = 3601 }, ov + ".keepalive", "0-3600"},
		{"a WireGuard block as well", func(c *Config) {
			tunnel(c).WireGuard = &WireGuard{PrivateKey: newKey(t)}
			tunnel(c).Name = "ovpn0"
		}, at + ".kind", "an OpenVPN tunnel"},
		{"a bridge member", func(c *Config) {
			tunnel(c).Zone = ""
			c.Interfaces = append(c.Interfaces, Interface{Name: "br0", Enabled: true, Bridge: &Bridge{Members: []string{"ovpn0"}},
				IPv4: IPv4{Mode: AddrNone}, IPv6: IPv6{Mode: AddrNone}})
			c.Gateways = c.Gateways[:1]
		}, "interfaces[3].bridge.members[0]", "an OpenVPN tunnel carries routed traffic"},
		{"a tunnel gateway in an internal zone", func(c *Config) { c.Zones[len(c.Zones)-1].External = false },
			"gateways[1].interface", "has to be in an external zone"},
		{"lookups through a tunnel dialled by name", func(c *Config) { c.Services.DNS.Via = "vpn" },
			"services.dns.via", "ovpn0 dials vpn.example by name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := ovpnConfig(t)
			tc.edit(cfg)
			err := cfg.Validate()
			var ve *ValidationError
			if !errors.As(err, &ve) || len(ve.Issues) != 1 || ve.Issues[0].Path != tc.want {
				t.Fatalf("err = %v, want one issue at %s", err, tc.want)
			}
			if msg := ve.Issues[0].Message; !strings.Contains(msg, tc.msg) {
				t.Errorf("message = %q, want it to hold %q", msg, tc.msg)
			}
		})
	}
}

func TestLookupsThroughAnOpenVPNTunnelByAddress(t *testing.T) {
	t.Parallel()
	cfg := ovpnConfig(t)
	cfg.Services.DNS.Via = "vpn"
	tunnel(cfg).OpenVPN.Remotes = tunnel(cfg).OpenVPN.Remotes[1:]
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRedactedBlanksOpenVPNSecrets(t *testing.T) {
	t.Parallel()
	o := ovpnConfig(t).Redacted().Interfaces[2].OpenVPN
	if o.KeyPEM != "" || o.TLSCryptKeyPEM != "" || o.Password != "" {
		t.Errorf("Redacted left a secret: %+v", o)
	}
	if o.CAPEM == "" || o.CertPEM == "" || o.Username == "" {
		t.Error("Redacted blanked what is not a secret")
	}
}
