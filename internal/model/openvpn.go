package model

import (
	"encoding/pem"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// OpenVPN dials an OpenVPN server as a client. The daemon creates the
// device and the server addresses it; the routes are typed here, never
// taken from the server unless PushedRoutes says so.
type OpenVPN struct {
	Remotes        []OpenVPNRemote `json:"remotes"`
	RemoteRandom   bool            `json:"remoteRandom,omitempty"`
	CAPEM          string          `json:"caPem"`
	CertPEM        string          `json:"certPem,omitempty"`        // with KeyPEM, or neither
	KeyPEM         string          `json:"keyPem,omitempty"`         // secret
	TLSCryptKeyPEM string          `json:"tlsCryptKeyPem,omitempty"` // secret
	TLSAuthKeyPEM  string          `json:"tlsAuthKeyPem,omitempty"`  // secret, with KeyDirection
	KeyDirection   *int            `json:"keyDirection,omitempty"`
	Username       string          `json:"username,omitempty"`
	Password       string          `json:"password,omitempty"` // secret
	// VerifyName is the server certificate's name: verify-x509-name.
	VerifyName string `json:"verifyName,omitempty"`
	// DataCiphers empty means the AEAD default.
	DataCiphers []string `json:"dataCiphers,omitempty"`
	// Routes are the networks reached through the tunnel.
	Routes []string `json:"routes,omitempty"`
	// PushedRoutes takes the routes the server sends as well.
	PushedRoutes bool `json:"pushedRoutes,omitempty"`
	// Keepalive is the seconds between pings; 0 means 10. The timeout is
	// six times it.
	Keepalive int `json:"keepalive,omitempty"`
}

// OpenVPNRemote is one server the tunnel may dial.
type OpenVPNRemote struct {
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Proto string `json:"proto,omitempty"` // udp when empty, or tcp
}

// DefaultOpenVPNKeepalive is the ping interval when none is set.
const DefaultOpenVPNKeepalive = 10

// openvpnNameRe is the device name an OpenVPN interface takes.
var openvpnNameRe = regexp.MustCompile(`^ovpn[0-9]+$`)

// OpenVPNDataCiphers are the data channel ciphers a tunnel may offer. The
// CBC ones keep the data channel in userspace.
var OpenVPNDataCiphers = []string{
	"AES-128-GCM", "AES-192-GCM", "AES-256-GCM", "CHACHA20-POLY1305", "AES-128-CBC", "AES-256-CBC",
}

// KeepaliveSeconds is the ping interval.
func (o *OpenVPN) KeepaliveSeconds() int {
	if o.Keepalive == 0 {
		return DefaultOpenVPNKeepalive
	}
	return o.Keepalive
}

// ProtoOrDefault is the remote's transport, udp when unset.
func (r OpenVPNRemote) ProtoOrDefault() string {
	if r.Proto == "" {
		return "udp"
	}
	return r.Proto
}

// Tunnel reports whether the interface is a tunnel a gateway can send
// rule traffic into.
func (i Interface) Tunnel() bool {
	return i.WireGuard != nil || i.OpenVPN != nil
}

// OpenVPNInterfaces lists the enabled OpenVPN interfaces in name order.
func (c *Config) OpenVPNInterfaces() []Interface {
	var out []Interface
	for _, in := range c.Interfaces {
		if in.Enabled && in.OpenVPN != nil {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b Interface) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// openvpnRoutes are the typed routes, each once, default routes left out.
func (i Interface) openvpnRoutes() []TunnelRoute {
	var out []TunnelRoute
	seen := map[netip.Prefix]bool{}
	for _, r := range i.OpenVPN.Routes {
		pre, err := ParseAddress(r)
		if err != nil || pre.Bits() == 0 || seen[pre] {
			continue
		}
		seen[pre] = true
		out = append(out, TunnelRoute{Prefix: pre})
	}
	slices.SortFunc(out, func(a, b TunnelRoute) int { return strings.Compare(a.Prefix.String(), b.Prefix.String()) })
	return out
}

// openvpn checks an OpenVPN interface. The server addresses the device,
// and every value lands in a config file, so nothing may break a line.
func (v *validator) openvpn(path string, in Interface) {
	o := in.OpenVPN
	opath := path + ".openvpn"
	if !openvpnNameRe.MatchString(in.Name) {
		v.add(path+".name", "%q must match %s", in.Name, openvpnNameRe)
	}
	if in.IPv4.Mode != AddrNone {
		v.add(path+".ipv4.mode", "the server addresses this interface; set it to none")
	}
	if in.IPv6.Mode != AddrNone {
		v.add(path+".ipv6.mode", "the server addresses this interface; set it to none")
	}
	if in.MTU >= 68 && in.MTU < 576 {
		v.add(path+".mtu", "MTU %d must be 576-65535 (or 0 for the server's)", in.MTU)
	}

	var routed []netip.Prefix
	for j, r := range o.Routes {
		rpath := fmt.Sprintf("%s.routes[%d]", opath, j)
		p, err := netip.ParsePrefix(r)
		switch {
		case err != nil:
			v.add(rpath, "%q is not a network", r)
		case p.Masked() != p:
			v.add(rpath, "%q has host bits set; write %s", r, p.Masked())
		case p.Bits() > 0:
			routed = append(routed, p)
		}
	}

	if len(o.Remotes) == 0 {
		v.add(opath+".remotes", "at least one server is required")
	}
	for j, r := range o.Remotes {
		rpath := fmt.Sprintf("%s.remotes[%d]", opath, j)
		addr, err := netip.ParseAddr(r.Host)
		switch {
		case err == nil:
			if p := slices.IndexFunc(routed, func(p netip.Prefix) bool { return p.Contains(addr.Unmap()) }); p >= 0 {
				v.add(rpath+".host", "%s is routed into this tunnel; the tunnel's own packets would be routed into it", r.Host)
			}
		case !hostnameRe.MatchString(r.Host):
			v.add(rpath+".host", "%q must be a host name or an IP address", r.Host)
		}
		if r.Port < 1 || r.Port > 65535 {
			v.add(rpath+".port", "%d must be 1-65535", r.Port)
		}
		if r.Proto != "" && r.Proto != "udp" && r.Proto != "tcp" {
			v.add(rpath+".proto", "%q must be udp or tcp", r.Proto)
		}
	}

	if o.CAPEM == "" {
		v.add(opath+".caPem", "the server's CA certificate is required")
	}
	for _, f := range []struct{ field, text string }{
		{"caPem", o.CAPEM}, {"certPem", o.CertPEM}, {"keyPem", o.KeyPEM},
		{"tlsCryptKeyPem", o.TLSCryptKeyPEM}, {"tlsAuthKeyPem", o.TLSAuthKeyPEM},
	} {
		if f.text != "" {
			v.pemText(opath+"."+f.field, f.text)
		}
	}
	switch {
	case o.CertPEM != "" && o.KeyPEM == "":
		v.add(opath+".keyPem", "a certificate needs its key")
	case o.CertPEM == "" && o.KeyPEM != "":
		v.add(opath+".certPem", "a key needs its certificate")
	}
	if o.TLSCryptKeyPEM != "" && o.TLSAuthKeyPEM != "" {
		v.add(opath+".tlsAuthKeyPem", "tls-crypt and tls-auth are exclusive; keep one")
	}
	switch d := o.KeyDirection; {
	case d != nil && o.TLSAuthKeyPEM == "":
		v.add(opath+".keyDirection", "a key direction goes with a tls-auth key only")
	case d != nil && *d != 0 && *d != 1:
		v.add(opath+".keyDirection", "%d must be 0 or 1", *d)
	case d == nil && o.TLSAuthKeyPEM != "":
		v.add(opath+".keyDirection", "a tls-auth key needs a key direction, 0 or 1")
	}

	if o.Password != "" && o.Username == "" {
		v.add(opath+".username", "a password needs a username")
	}
	if strings.ContainsFunc(o.Username, unicode.IsControl) {
		v.add(opath+".username", "%q cannot contain control characters", o.Username)
	}
	if strings.ContainsFunc(o.Password, unicode.IsControl) {
		v.add(opath+".password", "cannot contain control characters")
	}
	if strings.ContainsFunc(o.VerifyName, func(r rune) bool { return unicode.IsControl(r) || r == '"' || r == '\\' }) {
		v.add(opath+".verifyName", "%q cannot contain control characters, quotes or backslashes", o.VerifyName)
	}

	seen := map[string]bool{}
	for j, c := range o.DataCiphers {
		cpath := fmt.Sprintf("%s.dataCiphers[%d]", opath, j)
		switch {
		case !slices.Contains(OpenVPNDataCiphers, c):
			v.add(cpath, "%q is not a data cipher: want one of %s", c, strings.Join(OpenVPNDataCiphers, ", "))
		case seen[c]:
			v.add(cpath, "%q is listed twice", c)
		}
		seen[c] = true
	}
	if o.Keepalive < 0 || o.Keepalive > 3600 {
		v.add(opath+".keepalive", "%d must be 0-3600 seconds", o.Keepalive)
	}
}

// pemText checks a block written inline in the config: at least one PEM
// block, and nothing that could close the inline section early.
func (v *validator) pemText(path, text string) {
	switch block, _ := pem.Decode([]byte(text)); {
	case block == nil:
		v.add(path, "not PEM: want a -----BEGIN block")
	case strings.Contains(text, "<"):
		v.add(path, "a PEM block cannot contain \"<\"")
	}
}
