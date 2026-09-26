package model

import (
	"slices"
	"sort"
	"strings"
)

// Proxy publishes what is behind the router. Sites are hostnames served
// over HTTPS with a certificate this router holds; routes pass a TCP or
// UDP port through to a pool of backends.
type Proxy struct {
	Enabled bool `json:"enabled"`
	// Zones the listeners are open on. Empty opens nothing: neither the
	// internet nor a guest network is a safe guess, so the operator names
	// the zones and validation says so when they have not.
	Zones []string `json:"zones,omitempty"`
	// HTTPPort and HTTPSPort are 80 and 443 when zero.
	HTTPPort  uint16 `json:"httpPort,omitempty"`
	HTTPSPort uint16 `json:"httpsPort,omitempty"`
	// HTTP3 answers over QUIC on the HTTPS port as well, which is UDP.
	HTTP3    bool         `json:"http3,omitempty"`
	Pools    []ProxyPool  `json:"pools,omitempty"`
	Sites    []ProxySite  `json:"sites,omitempty"`
	Profiles []WAFProfile `json:"wafProfiles,omitempty"`
	Routes   []L4Route    `json:"routes,omitempty"`
}

// ProxyUpstream is one backend, host:port.
type ProxyUpstream struct {
	Address string `json:"address"`
}

// ProxyPool is where a site's requests go, and how they are shared out.
type ProxyPool struct {
	ID          string          `json:"id"`
	Description string          `json:"description,omitempty"`
	Upstreams   []ProxyUpstream `json:"upstreams"`
	// Policy is round_robin (empty), least_conn, ip_hash, cookie or first.
	Policy string `json:"policy,omitempty"`
	// HealthPath is requested every HealthSeconds; a 2xx keeps the
	// upstream in the pool. Empty checks nothing.
	HealthPath    string `json:"healthPath,omitempty"`
	HealthSeconds int    `json:"healthSeconds,omitempty"` // 0 is 30
	// FailSeconds takes an upstream out for that long after a failed
	// request; 0 leaves it in.
	FailSeconds int `json:"failSeconds,omitempty"`
	// TLS connects to the upstreams over HTTPS. ServerName is what the
	// certificate must say; empty is the address. CAPEM trusts a private
	// issuer; Insecure trusts anything.
	TLS           bool   `json:"tls,omitempty"`
	TLSServerName string `json:"tlsServerName,omitempty"`
	TLSCAPEM      string `json:"tlsCaPem,omitempty"`
	TLSInsecure   bool   `json:"tlsInsecure,omitempty"`
	// ProxyProtocol sends the client's address ahead of the stream: v1, v2, or empty.
	ProxyProtocol string `json:"proxyProtocol,omitempty"`
}

// ProxyPath sends one path prefix of a site to another pool.
type ProxyPath struct {
	Prefix string `json:"prefix"`
	Pool   string `json:"pool"`
}

// ProxySite is one set of hostnames served over HTTPS.
type ProxySite struct {
	ID          string   `json:"id"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Hosts       []string `json:"hosts"`
	// Certificate is the ID of one this router holds; empty is the
	// built-in self-signed one.
	Certificate string      `json:"certificate,omitempty"`
	Pool        string      `json:"pool"`
	Paths       []ProxyPath `json:"paths,omitempty"`
	// PlainHTTP serves the site on the HTTP port as well instead of
	// redirecting to HTTPS.
	PlainHTTP bool `json:"plainHttp,omitempty"`
	// HostHeader is what the upstream sees: empty keeps the client's,
	// "upstream" sends the upstream's own address.
	HostHeader string `json:"hostHeader,omitempty"`
	// AllowFrom limits the site to these prefixes; empty is anyone.
	AllowFrom []string `json:"allowFrom,omitempty"`
	// WAF is the profile that inspects requests; empty inspects nothing.
	WAF string `json:"waf,omitempty"`
}

// WAFProfile is how the Core Rule Set is applied to a site.
type WAFProfile struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	// Mode is detect (empty) or block.
	Mode string `json:"mode,omitempty"`
	// Paranoia is 1 to 4; 0 is 1.
	Paranoia int `json:"paranoia,omitempty"`
	// InboundThreshold and OutboundThreshold are the anomaly scores a
	// request or a response is stopped at; 0 is 5 and 4.
	InboundThreshold  int `json:"inboundThreshold,omitempty"`
	OutboundThreshold int `json:"outboundThreshold,omitempty"`
	// Applications names the exclusion sets loaded: wordpress, nextcloud,
	// drupal, phpbb, phpmyadmin, dokuwiki, xenforo, cpanel.
	Applications []string `json:"applications,omitempty"`
	// BodyLimitMB is how much of a request body is inspected; 0 is 12.
	// The rest passes uninspected.
	BodyLimitMB int `json:"bodyLimitMB,omitempty"`
	// InspectResponses runs the outbound rules on text responses.
	InspectResponses bool           `json:"inspectResponses,omitempty"`
	Exclusions       []WAFExclusion `json:"exclusions,omitempty"`
}

// WAFExclusion switches a rule off, for one path or one variable when
// given. Rule is an ID or a range, "942100" or "942100-942199".
type WAFExclusion struct {
	Rule        string `json:"rule"`
	Path        string `json:"path,omitempty"`   // prefix
	Target      string `json:"target,omitempty"` // ARGS:password
	Description string `json:"description,omitempty"`
}

// L4Route passes one port through to a pool, whole or by server name.
type L4Route struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Protocol    string `json:"protocol"` // tcp | udp
	Port        uint16 `json:"port"`
	// SNI picks TLS connections by the name they ask for; empty takes
	// the whole port. TCP only.
	SNI       []string        `json:"sni,omitempty"`
	Upstreams []ProxyUpstream `json:"upstreams"`
	// Policy is round_robin (empty), least_conn, ip_hash, first or random.
	Policy string `json:"policy,omitempty"`
	// HealthSeconds connects to each upstream that often; 0 never. TCP only.
	HealthSeconds int      `json:"healthSeconds,omitempty"`
	ProxyProtocol string   `json:"proxyProtocol,omitempty"`
	AllowFrom     []string `json:"allowFrom,omitempty"`
}

// The ports a proxy listens on when it names none.
const (
	ProxyHTTPPort  = 80
	ProxyHTTPSPort = 443
)

// SelfCertificate is what a site names when it serves the built-in
// self-signed pair. The @ keeps it out of ValidCertificateID's alphabet,
// so it can never collide with a certificate the router holds.
const SelfCertificate = "@self"

// ProxyPolicies are how a site's requests are shared out over a pool.
var ProxyPolicies = []string{"", "round_robin", "least_conn", "ip_hash", "cookie", "first"}

// L4Policies are the same for a passed-through port.
var L4Policies = []string{"", "round_robin", "least_conn", "ip_hash", "first", "random"}

// WAFModes are what a profile does with a request that scores.
var WAFModes = []string{"", "detect", "block"}

// WAFApplications are the exclusion sets a profile can load, in the order
// the UI offers them.
var WAFApplications = []string{
	"wordpress", "nextcloud", "drupal", "phpbb", "phpmyadmin", "dokuwiki", "xenforo", "cpanel",
}

// ProxyProtocols send the client's address ahead of the stream.
var ProxyProtocols = []string{"", "v1", "v2"}

// WAF defaults, which are the Core Rule Set's own.
const (
	DefaultParanoia          = 1
	DefaultInboundThreshold  = 5
	DefaultOutboundThreshold = 4
	DefaultBodyLimitMB       = 12
	DefaultPoolHealthSeconds = 30
)

// HTTPPortOr is the port plain HTTP is served on.
func (p Proxy) HTTPPortOr() uint16 {
	if p.HTTPPort == 0 {
		return ProxyHTTPPort
	}
	return p.HTTPPort
}

// HTTPSPortOr is the port sites are served on.
func (p Proxy) HTTPSPortOr() uint16 {
	if p.HTTPSPort == 0 {
		return ProxyHTTPSPort
	}
	return p.HTTPSPort
}

// Pool returns the pool with the given id.
func (p *Proxy) Pool(id string) (*ProxyPool, bool) {
	for i := range p.Pools {
		if p.Pools[i].ID == id {
			return &p.Pools[i], true
		}
	}
	return nil, false
}

// Profile returns the WAF profile with the given id.
func (p *Proxy) Profile(id string) (*WAFProfile, bool) {
	for i := range p.Profiles {
		if p.Profiles[i].ID == id {
			return &p.Profiles[i], true
		}
	}
	return nil, false
}

// Site returns the site with the given id.
func (p *Proxy) Site(id string) (*ProxySite, bool) {
	for i := range p.Sites {
		if p.Sites[i].ID == id {
			return &p.Sites[i], true
		}
	}
	return nil, false
}

// ProxyZones names the zones the listeners are opened on, in
// configuration order. Nothing is opened until the operator ticks one.
func (p Proxy) ProxyZones(c *Config) []string {
	if len(p.Zones) == 0 {
		return nil
	}
	var out []string
	for _, z := range c.Zones {
		if slices.Contains(p.Zones, z.Name) {
			out = append(out, z.Name)
		}
	}
	return out
}

// ProxyEnabled reports whether the proxy has anything to serve. The daemon
// runs without one, so this is what the firewall and the backend ask.
func (c *Config) ProxyEnabled() bool {
	p := c.Services.Proxy
	if !p.Enabled {
		return false
	}
	for _, s := range p.Sites {
		if s.Enabled {
			return true
		}
	}
	for _, r := range p.Routes {
		if r.Enabled {
			return true
		}
	}
	return false
}

// ProxyPorts are the ports the proxy listens on, by protocol, sorted.
func (c *Config) ProxyPorts() (tcp, udp []uint16) {
	p := c.Services.Proxy
	if !c.ProxyEnabled() {
		return nil, nil
	}
	tcp = []uint16{p.HTTPPortOr(), p.HTTPSPortOr()}
	if p.HTTP3 {
		udp = append(udp, p.HTTPSPortOr())
	}
	for _, r := range p.Routes {
		if !r.Enabled {
			continue
		}
		if r.Protocol == "udp" {
			udp = append(udp, r.Port)
		} else {
			tcp = append(tcp, r.Port)
		}
	}
	return sortPorts(tcp), sortPorts(udp)
}

func sortPorts(in []uint16) []uint16 {
	slices.Sort(in)
	return slices.Compact(in)
}

// Certificates are the IDs the enabled sites name, the built-in pair
// included as SelfCertificate.
func (p Proxy) Certificates() []string {
	var out []string
	for _, s := range p.Sites {
		if !s.Enabled {
			continue
		}
		id := s.Certificate
		if id == "" {
			id = SelfCertificate
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// ProxyName is a name the router answers for an enabled site, with its
// own addresses on Interfaces: those that carry the proxy and get their
// DNS from this router.
type ProxyName struct {
	Name string `json:"name"`
	// Bare is the label alone, answered as well when Name is one label
	// under the local domain, as a host override's is.
	Bare       string   `json:"bare,omitempty"`
	Site       string   `json:"site"`
	Interfaces []string `json:"interfaces"`
}

// Names is the name in full and, when it has one, bare.
func (p ProxyName) Names() []string {
	if p.Bare == "" {
		return []string{p.Name}
	}
	return []string{p.Name, p.Bare}
}

// ProxyNames lists the names the DNS service answers for the reverse
// proxy, site by site, while both are on and share an interface. Wildcards
// are left out: they name no one host.
func (c *Config) ProxyNames() []ProxyName {
	if !c.ProxyEnabled() || !c.Services.DNS.Enabled {
		return nil
	}
	dns := c.InsideInterfaces(c.Services.DNS.Interfaces)
	var ifaces []string
	for _, z := range c.Services.Proxy.ProxyZones(c) {
		for _, name := range c.ZoneInterfaces(z) {
			if slices.Contains(dns, name) && !slices.Contains(ifaces, name) {
				ifaces = append(ifaces, name)
			}
		}
	}
	if len(ifaces) == 0 {
		return nil
	}
	local := NormalizeDomain(c.Services.DNS.Domain)
	var out []ProxyName
	for _, s := range c.Services.Proxy.Sites {
		if !s.Enabled {
			continue
		}
		for _, h := range s.Hosts {
			if strings.HasPrefix(h, "*.") {
				continue
			}
			name, bare := siteName(h, local)
			out = append(out, ProxyName{Name: name, Bare: bare, Site: s.ID, Interfaces: ifaces})
		}
	}
	return out
}

// siteName is the name a site host answers in full, and the label it
// answers alone as well when it has no domain or is one label under the
// local one.
func siteName(host, local string) (name, bare string) {
	host = NormalizeDomain(host)
	switch {
	case !strings.Contains(host, "."):
		if local != "" {
			return host + "." + local, host
		}
	case local != "" && strings.HasSuffix(host, "."+local):
		if label := strings.TrimSuffix(host, "."+local); !strings.Contains(label, ".") {
			return host, label
		}
	}
	return host, ""
}
