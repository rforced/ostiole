package model

import "strings"

// Blocking is DNS blocking, the job pfBlockerNG and Pi-hole do: subscribe to
// published lists of names, refuse to resolve what is on them, and make it
// awkward for a client to ask someone else instead.
//
// Only the definitions live here. The names themselves are fetched, cached
// under /var/lib/ostiole and rendered into a dnsmasq include file, so a
// revision of this configuration never carries a quarter of a million
// domains (ADR-0005).
type Blocking struct {
	Enabled bool `json:"enabled,omitempty"`
	// Mode decides what a blocked name is answered with.
	Mode BlockMode `json:"mode,omitempty"`
	// Lists are the published lists this box subscribes to.
	Lists []BlockList `json:"lists,omitempty"`
	// Allow is never blocked, whatever a list says. A name here covers
	// everything under it, so allowing example.com allows its subdomains.
	Allow []string `json:"allow,omitempty"`
	// Deny is blocked whether a list names it or not, subdomains included.
	Deny []string `json:"deny,omitempty"`
	// Enforce keeps clients on this resolver. Blocking a name achieves
	// nothing if the client simply asks 8.8.8.8 instead.
	Enforce DNSEnforce `json:"enforce,omitempty"`
	// QueryLog records what was asked and what was refused.
	QueryLog QueryLog `json:"queryLog,omitempty"`
}

// BlockMode is the answer a blocked name gets.
type BlockMode string

// Block modes.
const (
	// BlockNXDomain answers "no such name", which is what a client handles
	// best: it fails immediately instead of trying to connect to nowhere.
	BlockNXDomain BlockMode = "nxdomain"
	// BlockNull answers 0.0.0.0 and ::, which is easier to spot in a packet
	// capture and is what hosts-file blocking has always done.
	BlockNull BlockMode = "null"
)

// BlockModes lists them in the order the UI offers them.
var BlockModes = []BlockMode{BlockNXDomain, BlockNull}

// ListFormat is how a published list is written. Nearly every list is one of
// these, and most declare themselves clearly enough to be recognised.
type ListFormat string

// List formats.
const (
	// FormatAuto works it out from the first lines that parse.
	FormatAuto ListFormat = "auto"
	// FormatHosts is "0.0.0.0 example.com", the Steven Black shape.
	FormatHosts ListFormat = "hosts"
	// FormatDomains is one name per line, the OISD and HaGeZi shape.
	FormatDomains ListFormat = "domains"
	// FormatAdblock is "||example.com^", the uBlock Origin shape. Only the
	// rules that name a whole domain are usable; the rest are for a browser.
	FormatAdblock ListFormat = "adblock"
	// FormatDnsmasq is "address=/example.com/0.0.0.0" or "local=/example.com/".
	FormatDnsmasq ListFormat = "dnsmasq"
	// FormatUnbound is `local-zone: "example.com." always_nxdomain`.
	FormatUnbound ListFormat = "unbound"
)

// ListFormats lists them in the order the UI offers them.
var ListFormats = []ListFormat{FormatAuto, FormatHosts, FormatDomains, FormatAdblock, FormatDnsmasq, FormatUnbound}

// BlockList is one subscription. A list with no URL holds only what the
// operator typed or uploaded, which is how an air-gapped box gets one.
type BlockList struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	URL         string `json:"url,omitempty"`
	// Format is how to read it; empty means work it out.
	Format ListFormat `json:"format,omitempty"`
	// RefreshHours is how often to fetch; zero means once a day. Nothing is
	// fetched more than once an hour.
	RefreshHours int `json:"refreshHours,omitempty"`
}

// DNSEnforce keeps clients on this box's resolver. Without it, blocking is
// advisory: anything that ships its own resolver address ignores it.
type DNSEnforce struct {
	// RedirectDNS sends plain DNS from internal zones to this box, whoever
	// the client meant to ask.
	RedirectDNS bool `json:"redirectDns,omitempty"`
	// BlockDoT drops DNS over TLS on its own port, which is the easy half
	// of stopping encrypted DNS.
	BlockDoT bool `json:"blockDot,omitempty"`
	// DoHAlias names a host alias holding the addresses of DNS over HTTPS
	// servers; traffic to them is dropped. DoH hides on port 443, so only
	// an address list can catch it.
	DoHAlias string `json:"dohAlias,omitempty"`
	// FirefoxCanary answers use-application-dns.net with NXDOMAIN, which is
	// how Firefox is asked not to turn DoH on by itself.
	FirefoxCanary bool `json:"firefoxCanary,omitempty"`
	// ExemptAlias names a host alias of clients left alone by all of the
	// above: the one machine that is allowed to resolve for itself.
	ExemptAlias string `json:"exemptAlias,omitempty"`
}

// QueryLog records what clients asked for. It is off by default and kept in
// memory only: a DNS query log is the most revealing thing this box could
// write down.
type QueryLog struct {
	Enabled bool `json:"enabled,omitempty"`
	// Entries is how many recent queries to keep; zero means the default.
	Entries int `json:"entries,omitempty"`
}

// DefaultQueryLogEntries is the ring size when none is given.
const DefaultQueryLogEntries = 2000

// BlockingActive reports whether names are actually being blocked: the
// feature is on, and so is the DNS server that would enforce it.
func (c *Config) BlockingActive() bool {
	return c.Blocking.Enabled && c.Services.DNS.Enabled
}

// BlockMode is the mode in force, filling in the default.
func (b Blocking) BlockMode() BlockMode {
	if b.Mode == BlockNull {
		return BlockNull
	}
	return BlockNXDomain
}

// EnabledLists are the lists that are switched on.
func (b Blocking) EnabledLists() []BlockList {
	var out []BlockList
	for _, l := range b.Lists {
		if l.Enabled {
			out = append(out, l)
		}
	}
	return out
}

// List returns the list with the given name.
func (b *Blocking) List(name string) (*BlockList, bool) {
	for i := range b.Lists {
		if b.Lists[i].Name == name {
			return &b.Lists[i], true
		}
	}
	return nil, false
}

// FormatOrAuto is how to read this list, filling in the default.
func (l BlockList) FormatOrAuto() ListFormat {
	if l.Format == "" {
		return FormatAuto
	}
	return l.Format
}

// NeverBlocked are the names this box refuses to block whatever a list says:
// its own domain, its own hostname, and every name it answers for locally.
// A list that blocked one of these would take the UI away from whoever is
// using it by name.
func (c *Config) NeverBlocked() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	add(c.Services.DNS.Domain)
	add(c.System.Hostname)
	if c.System.Hostname != "" && c.Services.DNS.Domain != "" {
		add(c.System.Hostname + "." + c.Services.DNS.Domain)
	}
	for _, o := range c.Services.DNS.HostOverrides {
		add(o.Hostname)
	}
	for _, l := range c.Services.DHCP.StaticLeases {
		add(l.Hostname)
	}
	return out
}
