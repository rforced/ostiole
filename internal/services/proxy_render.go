package services

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/acme"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// The proxy's own JSON, in structs of ours rather than Caddy's: the
// daemon never links Caddy, and the sidecar's `validate` is what says
// whether a field name is right.
type (
	jsonMap = map[string]any

	caddyConfig struct {
		Admin   caddyAdmin   `json:"admin"`
		Logging caddyLogging `json:"logging"`
		Apps    caddyApps    `json:"apps"`
	}

	caddyAdmin struct {
		Listen string      `json:"listen"`
		Config adminConfig `json:"config"`
	}

	// adminConfig: Caddy keeps a copy of every configuration it loads in
	// its state directory unless told not to. Ours is rendered from the
	// model every time, so the copy is never read.
	adminConfig struct {
		Persist bool `json:"persist"`
	}

	caddyLogging struct {
		Logs map[string]caddyLog `json:"logs"`
	}

	caddyLog struct {
		Level   string            `json:"level"`
		Writer  map[string]string `json:"writer"`
		Encoder map[string]string `json:"encoder"`
	}

	caddyApps struct {
		TLS    *tlsApp    `json:"tls,omitempty"`
		HTTP   *httpApp   `json:"http,omitempty"`
		Layer4 *layer4App `json:"layer4,omitempty"`
	}

	tlsApp struct {
		Certificates tlsCertificates `json:"certificates"`
		// DisableOCSPStapling keeps the router from asking a certificate's
		// issuer about it on a schedule (ADR-0016).
		DisableOCSPStapling bool `json:"disable_ocsp_stapling"`
	}

	tlsCertificates struct {
		LoadFiles []certKeyPair `json:"load_files"`
	}

	certKeyPair struct {
		Certificate string   `json:"certificate"`
		Key         string   `json:"key"`
		Tags        []string `json:"tags,omitempty"`
	}

	httpApp struct {
		Servers map[string]*httpServer `json:"servers"`
		// Metrics is switched on for the app: per server is what Caddy
		// 2.11 warns about at every reload.
		Metrics *emptyObject `json:"metrics,omitempty"`
	}

	httpServer struct {
		Listen            []string     `json:"listen"`
		AutomaticHTTPS    autoHTTPS    `json:"automatic_https"`
		Protocols         []string     `json:"protocols,omitempty"`
		ReadHeaderTimeout string       `json:"read_header_timeout"`
		IdleTimeout       string       `json:"idle_timeout"`
		MaxHeaderBytes    int          `json:"max_header_bytes"`
		ListenerWrappers  []jsonMap    `json:"listener_wrappers,omitempty"`
		TLSConnPolicies   []jsonMap    `json:"tls_connection_policies,omitempty"`
		Logs              *emptyObject `json:"logs,omitempty"`
		Routes            []httpRoute  `json:"routes,omitempty"`
		Errors            *httpErrors  `json:"errors,omitempty"`
	}

	httpErrors struct {
		Routes []httpRoute `json:"routes"`
	}

	autoHTTPS struct {
		Disable bool `json:"disable"`
	}

	// emptyObject is a switch Caddy reads as present or absent.
	emptyObject struct{}

	httpRoute struct {
		Match    []jsonMap `json:"match,omitempty"`
		Handle   []jsonMap `json:"handle,omitempty"`
		Terminal bool      `json:"terminal,omitempty"`
	}

	layer4App struct {
		Servers map[string]*layer4Server `json:"servers"`
	}

	layer4Server struct {
		Listen []string      `json:"listen"`
		Routes []layer4Route `json:"routes,omitempty"`
	}

	layer4Route struct {
		Match  []jsonMap `json:"match,omitempty"`
		Handle []jsonMap `json:"handle,omitempty"`
	}
)

// challengeUpstream is where the http-01 solver binds while the proxy
// owns port 80.
var challengeUpstream = fmt.Sprintf("127.0.0.1:%d", acme.ChallengePort)

// Timeouts a listener on the internet needs. A slow request should not
// hold a connection open for as long as it likes.
const (
	proxyHeaderTimeout = "10s"
	proxyIdleTimeout   = "2m"
	proxyDialTimeout   = "10s"
	proxyHealthTimeout = "5s"
	proxyMaxHeader     = 65536
)

// render returns the files an enabled proxy needs: the configuration, the
// list of certificates an apply has to place, and the CA of any pool that
// speaks TLS to a private issuer.
func (p *Proxy) render(cfg *model.Config) network.Files {
	files := network.Files{}
	pr := cfg.Services.Proxy
	conf := caddyConfig{
		Admin:   caddyAdmin{Listen: "unix/" + ProxyAdminSocket, Config: adminConfig{Persist: false}},
		Logging: caddyLogging{Logs: map[string]caddyLog{"default": p.logConfig(cfg)}},
	}
	conf.Apps.TLS = p.tlsApp(pr)
	conf.Apps.HTTP = p.httpApp(cfg)
	conf.Apps.Layer4 = p.layer4App(pr)

	raw, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		// Every value here came out of a validated configuration.
		panic(err)
	}
	files[proxyConfName] = string(raw) + "\n"
	files[proxyCertList] = strings.Join(pr.Certificates(), "\n") + "\n"
	for _, pool := range pr.Pools {
		if pool.TLS && pool.TLSCAPEM != "" {
			files[poolCAName(pool.ID)] = pool.TLSCAPEM
		}
	}
	return files
}

// poolCAName is where a pool's private issuer is written. It is a public
// certificate, so it sits beside the rest rather than under certs/<id>.
func poolCAName(id string) string { return proxyCertsDir + "/pool-" + id + "-ca.pem" }

// logConfig follows system.logging.level. Caddy writes one JSON line per
// record to stderr, which the journal picks up. The level comes from
// EffectiveLevel, like every other daemon's: the proxy is not capped by a
// LogLevelMax drop-in, so this file is the only thing holding it to the
// level the rest of the router runs at.
func (p *Proxy) logConfig(cfg *model.Config) caddyLog {
	level := "WARN"
	switch cfg.System.Logging.EffectiveLevel() {
	case model.LogError:
		level = "ERROR"
	case model.LogInfo:
		level = "INFO"
	case model.LogDebug:
		level = "DEBUG"
	}
	return caddyLog{
		Level:   level,
		Writer:  map[string]string{"output": "stderr"},
		Encoder: map[string]string{"format": "json"},
	}
}

func (p *Proxy) tlsApp(pr model.Proxy) *tlsApp {
	var pairs []certKeyPair
	for _, id := range pr.Certificates() {
		// Every certificate the proxy serves is a copy under its own
		// directory, the built-in pair included: the originals are root's
		// and the sidecar runs as nobody in particular.
		dir := filepath.Join(p.Dir, proxyCertsDir, id)
		pairs = append(pairs, certKeyPair{
			Certificate: filepath.Join(dir, "fullchain.pem"),
			Key:         filepath.Join(dir, "key.pem"),
			Tags:        []string{id},
		})
	}
	if len(pairs) == 0 {
		return nil
	}
	return &tlsApp{Certificates: tlsCertificates{LoadFiles: pairs}, DisableOCSPStapling: true}
}

// serverHeaderRoute takes the Server header off every response, the
// upstream's included. Caddy sets its own before any handler runs, so
// the removal is deferred to the moment the response is written.
func serverHeaderRoute() httpRoute {
	return httpRoute{Handle: []jsonMap{{
		"handler":  "headers",
		"response": jsonMap{"deferred": true, "delete": []string{"Server"}},
	}}}
}

// errorRoutes answer a handler's error, the WAF's 403 among them. An
// error route has to write the response itself, and the header is taken
// off before it does.
func errorRoutes() *httpErrors {
	return &httpErrors{Routes: []httpRoute{{Handle: []jsonMap{
		{"handler": "headers", "response": jsonMap{"delete": []string{"Server"}}},
		{"handler": "static_response", "status_code": "{http.error.status_code}"},
	}}}}
}

func (p *Proxy) httpApp(cfg *model.Config) *httpApp {
	pr := cfg.Services.Proxy
	plain := &httpServer{
		Listen:            []string{fmt.Sprintf(":%d", pr.HTTPPortOr())},
		AutomaticHTTPS:    autoHTTPS{Disable: true},
		ReadHeaderTimeout: proxyHeaderTimeout,
		IdleTimeout:       proxyIdleTimeout,
		MaxHeaderBytes:    proxyMaxHeader,
	}
	secure := &httpServer{
		Listen:            []string{fmt.Sprintf(":%d", pr.HTTPSPortOr())},
		AutomaticHTTPS:    autoHTTPS{Disable: true},
		Protocols:         []string{"h1", "h2"},
		ReadHeaderTimeout: proxyHeaderTimeout,
		IdleTimeout:       proxyIdleTimeout,
		MaxHeaderBytes:    proxyMaxHeader,
	}
	if pr.HTTP3 {
		secure.Protocols = append(secure.Protocols, "h3")
	}
	// A request line per request is only worth the journal at the levels
	// that keep what the daemons say about their clients.
	if cfg.System.Logging.Records() {
		plain.Logs, secure.Logs = &emptyObject{}, &emptyObject{}
	}

	// Nothing this proxy sends names the software behind it, on the
	// ordinary path or on an error's.
	plain.Routes = append(plain.Routes, serverHeaderRoute())
	secure.Routes = append(secure.Routes, serverHeaderRoute())
	plain.Errors, secure.Errors = errorRoutes(), errorRoutes()

	// The challenge route comes next and always: with the proxy up, port
	// 80 is its, and the solver binds loopback behind it.
	plain.Routes = append(plain.Routes, httpRoute{
		Match: []jsonMap{{"path": []string{"/.well-known/acme-challenge/*"}}},
		Handle: []jsonMap{{
			"handler":   "reverse_proxy",
			"upstreams": []jsonMap{{"dial": challengeUpstream}},
		}},
		Terminal: true,
	})

	// {http.request.host} is the name without its port, so a proxy that is
	// not on 443 has to put its own back or the redirect lands nowhere.
	target := "https://{http.request.host}"
	if pr.HTTPSPortOr() != model.ProxyHTTPSPort {
		target += fmt.Sprintf(":%d", pr.HTTPSPortOr())
	}
	for _, site := range pr.Sites {
		if !site.Enabled {
			continue
		}
		handler := jsonMap{
			"handler": "static_response", "status_code": 308,
			"headers": jsonMap{"Location": []string{target + "{http.request.uri}"}},
		}
		if site.PlainHTTP {
			handler = p.siteSubroute(cfg, site)
		}
		plain.Routes = append(plain.Routes, httpRoute{
			Match: []jsonMap{{"host": site.Hosts}}, Handle: []jsonMap{handler}, Terminal: true,
		})
		secure.Routes = append(secure.Routes, httpRoute{
			Match: []jsonMap{{"host": site.Hosts}}, Handle: []jsonMap{p.siteSubroute(cfg, site)}, Terminal: true,
		})
		id := site.Certificate
		if id == "" {
			id = model.SelfCertificate
		}
		secure.TLSConnPolicies = append(secure.TLSConnPolicies, jsonMap{
			"match":                 jsonMap{"sni": site.Hosts},
			"certificate_selection": jsonMap{"any_tag": []string{id}},
		})
	}
	// A handshake for a name no site claims still needs a policy, or the
	// server has no certificate to answer with at all.
	if len(secure.TLSConnPolicies) > 0 {
		secure.TLSConnPolicies = append(secure.TLSConnPolicies, jsonMap{})
	}
	// A route on the HTTPS port is taken before TLS is terminated, so it
	// wraps the listener rather than sitting in the layer4 app.
	if routes := sniRoutes(pr); len(routes) > 0 {
		secure.ListenerWrappers = []jsonMap{
			{"wrapper": "layer4", "routes": routes},
			{"wrapper": "tls"},
		}
	}
	return &httpApp{Metrics: &emptyObject{}, Servers: map[string]*httpServer{"http": plain, "https": secure}}
}

// siteSubroute is what a matched site runs: the address check, the WAF,
// the per-path pools, and the site's own pool last.
func (p *Proxy) siteSubroute(cfg *model.Config, site model.ProxySite) jsonMap {
	pr := &cfg.Services.Proxy
	var routes []httpRoute
	if len(site.AllowFrom) > 0 {
		routes = append(routes, httpRoute{
			Match:    []jsonMap{{"not": []jsonMap{{"remote_ip": jsonMap{"ranges": site.AllowFrom}}}}},
			Handle:   []jsonMap{{"handler": "static_response", "status_code": 403}},
			Terminal: true,
		})
	}
	if profile, ok := pr.Profile(site.WAF); ok {
		routes = append(routes, httpRoute{Handle: []jsonMap{{
			"handler":        "waf",
			"load_owasp_crs": true,
			"directives":     wafDirectives(p.Dir, *profile, site.ID),
		}}})
	}
	for _, path := range site.Paths {
		pool, ok := pr.Pool(path.Pool)
		if !ok {
			continue
		}
		routes = append(routes, httpRoute{
			Match:    []jsonMap{{"path": []string{path.Prefix + "*"}}},
			Handle:   []jsonMap{p.reverseProxy(*pool, site)},
			Terminal: true,
		})
	}
	if pool, ok := pr.Pool(site.Pool); ok {
		routes = append(routes, httpRoute{Handle: []jsonMap{p.reverseProxy(*pool, site)}})
	}
	return jsonMap{"handler": "subroute", "routes": routes}
}

// reverseProxy is one pool as an HTTP handler.
func (p *Proxy) reverseProxy(pool model.ProxyPool, site model.ProxySite) jsonMap {
	ups := make([]jsonMap, 0, len(pool.Upstreams))
	for _, u := range pool.Upstreams {
		ups = append(ups, jsonMap{"dial": u.Address})
	}
	h := jsonMap{"handler": "reverse_proxy", "upstreams": ups}
	if pool.Policy != "" {
		h["load_balancing"] = jsonMap{"selection_policy": jsonMap{"policy": pool.Policy}}
	}
	checks := jsonMap{}
	if pool.HealthPath != "" {
		interval := pool.HealthSeconds
		if interval == 0 {
			interval = model.DefaultPoolHealthSeconds
		}
		checks["active"] = jsonMap{
			"path": pool.HealthPath, "interval": seconds(interval),
			"timeout": proxyHealthTimeout, "expect_status": 2,
		}
	}
	if pool.FailSeconds > 0 {
		checks["passive"] = jsonMap{"fail_duration": seconds(pool.FailSeconds), "max_fails": 1}
	}
	if len(checks) > 0 {
		h["health_checks"] = checks
	}
	transport := jsonMap{"protocol": "http", "dial_timeout": proxyDialTimeout}
	if pool.ProxyProtocol != "" {
		transport["proxy_protocol"] = pool.ProxyProtocol
	}
	if pool.TLS {
		tls := jsonMap{}
		if pool.TLSServerName != "" {
			tls["server_name"] = pool.TLSServerName
		}
		if pool.TLSInsecure {
			tls["insecure_skip_verify"] = true
		}
		if pool.TLSCAPEM != "" {
			tls["root_ca_pem_files"] = []string{filepath.Join(p.Dir, poolCAName(pool.ID))}
		}
		transport["tls"] = tls
	}
	h["transport"] = transport
	if site.HostHeader == "upstream" {
		h["headers"] = jsonMap{"request": jsonMap{
			"set": jsonMap{"Host": []string{"{http.reverse_proxy.upstream.hostport}"}},
		}}
	}
	return h
}

// sniRoutes are the layer 4 routes that share the HTTPS port with the
// terminated sites, picked out by the name the client asks for.
func sniRoutes(pr model.Proxy) []layer4Route {
	var out []layer4Route
	for _, r := range pr.Routes {
		if !r.Enabled || r.Protocol != "tcp" || r.Port != pr.HTTPSPortOr() {
			continue
		}
		out = append(out, layer4Route{Match: routeMatch(r), Handle: []jsonMap{layer4Proxy(r)}})
	}
	return out
}

func (p *Proxy) layer4App(pr model.Proxy) *layer4App {
	servers := map[string]*layer4Server{}
	whole := map[string]layer4Route{}
	for _, r := range pr.Routes {
		if !r.Enabled || (r.Protocol == "tcp" && r.Port == pr.HTTPSPortOr()) {
			continue
		}
		name := fmt.Sprintf("%s-%d", r.Protocol, r.Port)
		srv, ok := servers[name]
		if !ok {
			listen := fmt.Sprintf(":%d", r.Port)
			if r.Protocol == "udp" {
				listen = fmt.Sprintf("udp/:%d", r.Port)
			}
			srv = &layer4Server{Listen: []string{listen}}
			servers[name] = srv
		}
		route := layer4Route{Match: routeMatch(r), Handle: []jsonMap{layer4Proxy(r)}}
		if len(r.SNI) == 0 {
			// The route that takes the whole port goes last, or it would
			// swallow the connections a named route wanted.
			whole[name] = route
			continue
		}
		srv.Routes = append(srv.Routes, route)
	}
	if len(servers) == 0 {
		return nil
	}
	for name, route := range whole {
		servers[name].Routes = append(servers[name].Routes, route)
	}
	return &layer4App{Servers: servers}
}

func routeMatch(r model.L4Route) []jsonMap {
	m := jsonMap{}
	if len(r.SNI) > 0 {
		m["tls"] = jsonMap{"sni": r.SNI}
	}
	if len(r.AllowFrom) > 0 {
		m["remote_ip"] = jsonMap{"ranges": r.AllowFrom}
	}
	if len(m) == 0 {
		return nil
	}
	return []jsonMap{m}
}

func layer4Proxy(r model.L4Route) jsonMap {
	// One upstream per address. An upstream with several addresses is
	// dialled on all of them at once and the stream copied to each, which
	// is a tee, not a pool.
	ups := make([]jsonMap, 0, len(r.Upstreams))
	for _, u := range r.Upstreams {
		// A bare host:port is dialled over TCP, so a UDP route has to say
		// so: without it the datagram goes to the upstream's TCP port and
		// nothing ever answers.
		addr := u.Address
		if r.Protocol == "udp" {
			addr = "udp/" + addr
		}
		ups = append(ups, jsonMap{"dial": []string{addr}})
	}
	h := jsonMap{"handler": "proxy", "upstreams": ups}
	if r.Policy != "" {
		h["load_balancing"] = jsonMap{"selection": jsonMap{"policy": r.Policy}}
	}
	if r.HealthSeconds > 0 {
		h["health_checks"] = jsonMap{"active": jsonMap{
			"interval": seconds(r.HealthSeconds), "timeout": proxyHealthTimeout,
		}}
	}
	if r.ProxyProtocol != "" {
		h["proxy_protocol"] = r.ProxyProtocol
	}
	return h
}

func seconds(n int) string { return fmt.Sprintf("%ds", n) }
