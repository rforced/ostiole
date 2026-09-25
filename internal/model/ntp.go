package model

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// NTP is where the router takes its time from and whether it passes the
// time on. The router always keeps time: certificates, DNSSEC, schedules
// and logs all go wrong without it, so nothing here turns that off.
type NTP struct {
	// Servers are asked for the time. Empty means DefaultNTPServers, so a
	// router that never chose its own follows the defaults as they change.
	Servers []NTPServer `json:"servers,omitempty"`
	// Serve answers time requests on the interfaces below and gives DHCP
	// clients this router as their time server.
	Serve bool `json:"serve,omitempty"`
	// Interfaces narrows where time is served. Empty means every interface
	// outside an external zone, as for DNS.
	Interfaces []string `json:"interfaces,omitempty"`
}

// NTPServer is one place the router asks for the time.
type NTPServer struct {
	// Host is a name or an address.
	Host string `json:"host"`
	// NTS asks for signed answers. It needs a name: the server's
	// certificate is checked against it.
	NTS bool `json:"nts,omitempty"`
	// Pool is one name for many servers, like pool.ntp.org, of which a few
	// are asked at a time.
	Pool bool `json:"pool,omitempty"`
}

// DefaultNTPServers is the NTP Pool, of which chronyd asks four servers.
// Of the pool's names only 2.pool.ntp.org also gives IPv6 addresses.
var DefaultNTPServers = []NTPServer{
	{Host: "2.pool.ntp.org", Pool: true},
}

// NTPServers is the list the router asks: its own, or the defaults.
func (c *Config) NTPServers() []NTPServer {
	if len(c.Services.NTP.Servers) > 0 {
		return c.Services.NTP.Servers
	}
	return slices.Clone(DefaultNTPServers)
}

// InsideInterfaces is where a LAN-side service answers: the interfaces
// named, or with none named every one outside an external zone. Either
// way a disabled interface is left out; nothing arrives on it to answer.
func (c *Config) InsideInterfaces(named []string) []string {
	var ifs []string
	if len(named) > 0 {
		for _, name := range named {
			if in, ok := c.Interface(name); ok && in.Enabled {
				ifs = append(ifs, name)
			}
		}
		return ifs
	}
	for _, in := range c.Interfaces {
		if !in.Enabled || in.Zone == "" {
			continue
		}
		if z, ok := c.Zone(in.Zone); ok && !z.External {
			ifs = append(ifs, in.Name)
		}
	}
	return ifs
}

// NTPInterfaces is where the router answers time requests, whether or not
// it is set to.
func (c *Config) NTPInterfaces() []string {
	return c.InsideInterfaces(c.Services.NTP.Interfaces)
}

// NTPServing reports whether the router answers time requests anywhere.
func (c *Config) NTPServing() bool {
	return c.Services.NTP.Serve && len(c.NTPInterfaces()) > 0
}

// ntp checks the time servers and where time is served. A name that is
// written down is checked whether or not serving is on, so a typo is not
// hidden by a switch.
func (v *validator) ntp(c *Config, ifaces map[string]bool) {
	n := c.Services.NTP
	seen := map[string]bool{}
	for i, s := range n.Servers {
		path := fmt.Sprintf("services.ntp.servers[%d]", i)
		host := strings.TrimSpace(s.Host)
		_, err := netip.ParseAddr(host)
		addr := err == nil
		switch {
		case host == "":
			v.add(path+".host", "a name or an address is required")
			continue
		case host != s.Host:
			v.add(path+".host", "%q has spaces around it", s.Host)
		case !addr && !hostnameRe.MatchString(host):
			v.add(path+".host", "%q is neither an address nor a hostname", s.Host)
		}
		if s.NTS && addr {
			v.add(path+".nts", "NTS needs a name rather than an address: the server's certificate is checked against it")
		}
		if s.Pool && addr {
			v.add(path+".pool", "a pool is a name that stands for many servers, not an address")
		}
		key := strings.ToLower(host)
		if seen[key] {
			v.add(path+".host", "%q is listed twice", host)
		}
		seen[key] = true
	}
	// A disabled interface, or none left to serve on, is not an error:
	// nothing arrives there to answer, and refusing would stop an apply
	// that only switched a link off.
	for i, name := range n.Interfaces {
		path := fmt.Sprintf("services.ntp.interfaces[%d]", i)
		in, known := c.Interface(name)
		switch {
		case !known || !ifaces[name]:
			v.add(path, "unknown interface %q", name)
		case !n.Serve:
		default:
			if z, ok := c.Zone(in.Zone); ok && z.External {
				v.add(path, "interface %q is in an external zone; time is served to the inside", name)
			}
		}
	}
}
