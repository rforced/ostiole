package model

import "strings"

// Blocking is DNS blocking, the job pfBlockerNG and Pi-hole do: subscribe to
// published lists of names, refuse to resolve what is on them, and make it
// awkward for a client to ask someone else instead.
//
// Only the definitions live here. The names themselves are fetched, cached
// beside the configuration and rendered into a dnsmasq include file, so a
// revision of this configuration never carries a quarter of a million
// domains (ADR-0005).
type Blocking struct {
	// Enabled merges the subscribed lists in, and decides nothing else: the
	// deny list, the Firefox canary and enforcement are the operator's own
	// words, and apply whenever the DNS server is on.
	Enabled bool `json:"enabled,omitempty"`
	// Mode decides what a blocked name is answered with.
	Mode BlockMode `json:"mode,omitempty"`
	// Lists are the published lists this router subscribes to.
	Lists []BlockList `json:"lists,omitempty"`
	// Allow is never blocked, whatever a list says. A name here covers
	// everything under it, so allowing example.com allows its subdomains.
	Allow []string `json:"allow,omitempty"`
	// Deny is blocked whether a list names it or not, subdomains included.
	Deny []string `json:"deny,omitempty"`
	// MaxDomains is the most names the merged list may come to; zero means
	// the default. It is a memory budget rather than a policy: dnsmasq
	// holds roughly 90 MB per million names, so raising it is worth doing
	// deliberately on a router that has the memory, and worth refusing on one
	// that does not.
	MaxDomains int `json:"maxDomains,omitempty"`
	// Enforce keeps clients on this resolver. Blocking a name achieves
	// nothing if the client simply asks 8.8.8.8 instead, and keeping clients
	// here is worth having without any lists at all, for local names,
	// domain overrides and validation.
	Enforce DNSEnforce `json:"enforce,omitempty"`
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
// operator typed or uploaded, which is how an air-gapped router gets one.
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

// DNSEnforce keeps clients on this router's resolver. Without it, blocking is
// advisory: anything that ships its own resolver address ignores it. It does
// not wait for the lists. The drops apply whenever they are ticked; the
// redirect and the canary need the DNS server on, because both send clients
// to a resolver here.
type DNSEnforce struct {
	// RedirectDNS sends plain DNS from internal zones to this router, whoever
	// the client meant to ask. It needs the DNS server on: redirected to a
	// router that is not answering, every client would lose DNS.
	RedirectDNS bool `json:"redirectDns,omitempty"`
	// BlockDoT drops DNS over TLS on its own port, which is the easy half
	// of stopping encrypted DNS.
	BlockDoT bool `json:"blockDot,omitempty"`
	// DoHAlias names a host alias holding the addresses of DNS over HTTPS
	// servers; traffic to them is dropped. DoH hides on port 443, so only
	// an address list can catch it.
	DoHAlias string `json:"dohAlias,omitempty"`
	// FirefoxCanary answers use-application-dns.net with NXDOMAIN, which is
	// how Firefox is asked not to turn DoH on by itself. It is a dnsmasq
	// entry, so it does nothing while the DNS server is off.
	FirefoxCanary bool `json:"firefoxCanary,omitempty"`
	// ExemptAlias names a host alias of clients left alone by all of the
	// above: the one machine that is allowed to resolve for itself.
	ExemptAlias string `json:"exemptAlias,omitempty"`
}

// MaxBlockedDomains is the most the merged blocklist may ever come to,
// whatever the configuration asks for. It matches dnsblock.MaxDomains,
// which cannot be imported here: dnsblock is the one that imports model.
const MaxBlockedDomains = 25_000_000

// ListsActive reports whether the subscribed lists are being applied: they
// are switched on, and so is the DNS server that would refuse the names.
func (c *Config) ListsActive() bool {
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

// NeverBlocked are the names this router refuses to block whatever a list says:
// its own domain, its own hostname, and every name it answers for locally.
// A list that blocked one of these would take the UI away from whoever is
// using it by name.
func (c *Config) NeverBlocked() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = NormalizeDomain(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	domain := NormalizeDomain(c.Services.DNS.Domain)
	// A bare name is also answered with the local domain on the end:
	// dnsmasq is told expand-hosts, so "printer" resolves as "printer.lan"
	// too, and blocking either one takes the host away.
	addBoth := func(name string) {
		add(name)
		if domain != "" && !strings.Contains(strings.TrimSpace(name), ".") {
			add(name + "." + domain)
		}
	}
	add(domain)
	addBoth(c.System.Hostname)
	for _, o := range c.Services.DNS.HostOverrides {
		for _, n := range o.Names(domain) {
			add(n)
		}
	}
	for _, l := range c.Services.DHCP.StaticLeases {
		addBoth(l.Hostname)
	}
	return out
}

// DelegatedDomains are the domains handed to resolvers of their own. They
// are not blocked either: the operator has said who answers them, and a
// list that took one away would break a delegation that was asked for.
func (c *Config) DelegatedDomains() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range c.Services.DNS.DomainOverrides {
		name := NormalizeDomain(d.Domain)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// CoversName reports whether blocking parent would also block name, which is
// what makes blocking a parent of this router's own name dangerous: blocking is
// by subtree.
func CoversName(parent, name string) bool {
	parent = strings.ToLower(strings.Trim(strings.TrimSpace(parent), "."))
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "."))
	return parent != "" && (parent == name || strings.HasSuffix(name, "."+parent))
}
