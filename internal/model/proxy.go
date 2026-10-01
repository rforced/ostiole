package model

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Proxy publishes what is behind the router. Sites are hostnames served
// over HTTPS with a certificate this router holds; routes pass a TCP or
// UDP port through to a pool of backends.
type Proxy struct {
	Enabled bool `json:"enabled"`
	// Access opens the listeners, zone by zone, read in order within each
	// zone. Empty opens nothing: neither the internet nor a guest network is
	// a safe guess, so the operator writes the list and validation says so
	// when there is none.
	Access []ProxyAccess `json:"access,omitempty"`
	// HTTPPort and HTTPSPort are 80 and 443 when zero.
	HTTPPort  uint16 `json:"httpPort,omitempty"`
	HTTPSPort uint16 `json:"httpsPort,omitempty"`
	// HTTP3 answers over QUIC on the HTTPS port as well, which is UDP.
	HTTP3    bool         `json:"http3,omitempty"`
	Pools    []ProxyPool  `json:"pools,omitempty"`
	Sites    []ProxySite  `json:"sites,omitempty"`
	Profiles []WAFProfile `json:"wafProfiles,omitempty"`
	Routes   []L4Route    `json:"routes,omitempty"`
	// Events is how much of what the WAF matched is kept in memory.
	Events ProxyEvents `json:"events,omitzero"`
	// Requests is how much of the line per request the proxy writes at the
	// Info and Debug levels is kept in memory.
	Requests LogKeep `json:"requests,omitzero"`
}

// Proxy request defaults and bounds. A request's line costs RequestBytes
// in memory, most of it the path and the user agent.
const (
	DefaultRequestEntries = 50_000
	MaxRequestEntries     = 10_000_000
	RequestBytes          = 400
)

// ProxyEvents is how many of the WAF's events are kept, and for how long.
type ProxyEvents struct {
	// Entries is the most events kept; zero keeps DefaultProxyEventEntries.
	Entries int `json:"entries,omitempty"`
	// Days is how long an event is kept; zero keeps DefaultLogDays.
	Days int `json:"days,omitempty"`
}

// WAF event defaults and bounds. An event costs ProxyEventBytes with the
// rules it matched (measured on a router: half of them under 600 bytes,
// one in ten over 2.5 KB), and the ring grows towards the ceiling as events
// arrive, so the ceiling is what a full log costs, about 1.5 GB. The page
// says what the chosen figure costs.
const (
	DefaultProxyEventEntries = 10_000
	MaxProxyEventEntries     = 1_000_000
	ProxyEventBytes          = 1536
)

// Size is how many events are kept, filling in the default.
func (e ProxyEvents) Size() int {
	if e.Entries > 0 {
		return e.Entries
	}
	return DefaultProxyEventEntries
}

// Retention is how long an event is kept, filling in the default.
func (e ProxyEvents) Retention() time.Duration {
	return logDays(e.Days)
}

// ProxyAccess is one line of the proxy's access list, read in order within
// its zone: the first that matches a connection decides it, and one that
// no line accepts goes on to the zone's end.
type ProxyAccess struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Zone    string `json:"zone"`
	// Ports names the listeners, ProxyPortHTTP and ProxyPortHTTPS, so
	// moving one changes no line. HTTPS covers HTTP/3's UDP port too.
	Ports []string `json:"ports,omitempty"`
	// Routes are passed-through ports, by route ID. A route no line names
	// is reachable nowhere.
	Routes []string `json:"routes,omitempty"`
	// Source is addresses, an alias or a WireGuard peer, inverted with
	// NotAddresses. Empty is anywhere.
	Source      Endpoint `json:"source"`
	Action      Action   `json:"action"`
	Log         bool     `json:"log,omitempty"`
	Description string   `json:"description,omitempty"`
}

// The listeners an access line names.
const (
	ProxyPortHTTP  = "http"
	ProxyPortHTTPS = "https"
)

// Opens reports whether the line lets something through: enabled and
// accepting.
func (a ProxyAccess) Opens() bool { return a.Enabled && a.Action == ActionAccept }

// Anywhere reports whether the line takes every source.
func (a ProxyAccess) Anywhere() bool {
	return len(a.Source.Addresses) == 0 && a.Source.Alias == "" && a.Source.Peer == ""
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
	// phpbb, xenforo, vaultwarden, jellyfin.
	Applications []string `json:"applications,omitempty"`
	// BodyLimitMB is the largest request body taken, all of it inspected;
	// 0 is 12. A larger one is refused.
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
var WAFApplications = []string{"wordpress", "nextcloud", "phpbb", "xenforo", "vaultwarden", "jellyfin"}

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

// Route returns the passed-through port with the given id.
func (p *Proxy) Route(id string) (*L4Route, bool) {
	for i := range p.Routes {
		if p.Routes[i].ID == id {
			return &p.Routes[i], true
		}
	}
	return nil, false
}

// ProxyZones names the zones an enabled line of the access list accepts
// something on, in configuration order. Nothing is opened until one does.
func (p Proxy) ProxyZones(c *Config) []string {
	var out []string
	for _, z := range c.Zones {
		if slices.ContainsFunc(p.Access, func(a ProxyAccess) bool { return a.Opens() && a.Zone == z.Name }) {
			out = append(out, z.Name)
		}
	}
	return out
}

// AccessPorts are the ports a line of the access list covers, by
// protocol, sorted: the listeners it names, HTTP/3 with HTTPS, and the
// routes it names that are enabled.
func (c *Config) AccessPorts(a ProxyAccess) (tcp, udp []uint16) {
	p := c.Services.Proxy
	for _, name := range a.Ports {
		switch name {
		case ProxyPortHTTP:
			tcp = append(tcp, p.HTTPPortOr())
		case ProxyPortHTTPS:
			tcp = append(tcp, p.HTTPSPortOr())
			if p.HTTP3 {
				udp = append(udp, p.HTTPSPortOr())
			}
		}
	}
	for _, id := range a.Routes {
		r, ok := p.Route(id)
		switch {
		case !ok || !r.Enabled:
		case r.Protocol == "udp":
			udp = append(udp, r.Port)
		default:
			tcp = append(tcp, r.Port)
		}
	}
	return sortPorts(tcp), sortPorts(udp)
}

// RouteZones names the zones where an enabled line accepts the route, in
// configuration order.
func (p Proxy) RouteZones(c *Config, route string) []string {
	var out []string
	for _, z := range c.Zones {
		if slices.ContainsFunc(p.Access, func(a ProxyAccess) bool {
			return a.Opens() && a.Zone == z.Name && slices.Contains(a.Routes, route)
		}) {
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

// ProxyAnswersChallenges reports whether a CA reaching port 80 from
// outside reaches the proxy: it is up, listening on 80, and a line of the
// access list accepts HTTP on an external zone. Otherwise, while the proxy
// is up, the firewall turns port 80 from outside to the solver itself, so
// a proxy open on 443 alone lets the CA reach the solver and nothing else.
func (c *Config) ProxyAnswersChallenges() bool {
	if !c.ProxyEnabled() || c.Services.Proxy.HTTPPortOr() != ProxyHTTPPort {
		return false
	}
	return slices.ContainsFunc(c.Services.Proxy.Access, func(a ProxyAccess) bool {
		return a.Opens() && slices.Contains(a.Ports, ProxyPortHTTP) && c.externalZone(a.Zone)
	})
}

// ChallengesLimited reports whether an http-01 certificate is checked
// through the proxy while every line accepting HTTP from outside names its
// sources. A CA checks from several places at once, so port 80 open to
// one country fails a renewal. A drop above an open line is not looked
// for: that is the operator's to write.
func (c *Config) ChallengesLimited() bool {
	if !c.HTTP01Certificates() || !c.ProxyAnswersChallenges() {
		return false
	}
	return !slices.ContainsFunc(c.Services.Proxy.Access, func(a ProxyAccess) bool {
		return a.Opens() && a.Anywhere() && slices.Contains(a.Ports, ProxyPortHTTP) && c.externalZone(a.Zone)
	})
}

func (c *Config) externalZone(name string) bool {
	z, ok := c.Zone(name)
	return ok && z.External
}

// AllowFromRanges is a site's or a route's Allow from as the proxy takes
// it: prefixes, with each hosts alias it names written out.
func (c *Config) AllowFromRanges(from []string) []string {
	out := []string{}
	for _, f := range from {
		if _, err := ParseAddress(f); err == nil {
			out = append(out, strings.TrimSpace(f))
			continue
		}
		a, ok := c.Alias(f)
		if !ok {
			continue
		}
		for _, e := range a.Entries {
			if pre, err := ParseAddress(e); err == nil {
				if pre.IsSingleIP() {
					out = append(out, pre.Addr().String())
				} else {
					out = append(out, pre.String())
				}
			}
		}
	}
	return out
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
