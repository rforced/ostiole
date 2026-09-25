package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/version"
)

// router records every route as it is registered, so the OpenAPI
// description is generated from the server rather than written beside it
// and left to rot.
type router struct {
	mux    *http.ServeMux
	routes []route
}

type route struct {
	Method string
	Path   string
	Role   auth.Role
	Public bool
	// Session marks a route about the session itself, which a token
	// cannot stand in for.
	Session bool
	Summary string
}

// allowed lists the methods registered for a path, a {name} segment
// standing for any one segment, in the order they were registered.
func (r *router) allowed(path string) []string {
	var out []string
	want := strings.Split(strings.Trim(path, "/"), "/")
	for _, rt := range r.routes {
		have := strings.Split(strings.Trim(rt.Path, "/"), "/")
		if len(have) != len(want) {
			continue
		}
		match := true
		for i := range have {
			wildcard := strings.HasPrefix(have[i], "{") && strings.HasSuffix(have[i], "}")
			if have[i] != want[i] && !wildcard {
				match = false
				break
			}
		}
		if match {
			out = append(out, rt.Method)
		}
	}
	return out
}

func (r *router) HandleFunc(pattern string, h http.HandlerFunc) {
	method, path, hasMethod := strings.Cut(pattern, " ")
	if !hasMethod {
		// A catch-all like "/api/" is a fallback, not an endpoint.
		r.mux.HandleFunc(pattern, h)
		return
	}
	doc, documented := routeDocs[pattern]
	r.routes = append(r.routes, route{
		Method:  method,
		Path:    path,
		Role:    doc.role,
		Public:  doc.public,
		Session: doc.session,
		Summary: doc.summary,
	})
	if !documented {
		// A route with no entry still works; the test is what insists the
		// description stays complete.
		r.routes[len(r.routes)-1].Summary = ""
	}
	r.mux.HandleFunc(pattern, h)
}

func (r *router) Handle(pattern string, h http.Handler) { r.mux.Handle(pattern, h) }

type routeDoc struct {
	summary string
	role    auth.Role
	public  bool
	// session routes are about the cookie session: describing it, ending
	// it, changing its password. A token has none, so none is accepted.
	session bool
}

// routeDocs describes every endpoint. TestOpenAPICoversEveryRoute fails
// when a route is added without a line here, which is what keeps the
// description honest.
var routeDocs = map[string]routeDoc{
	"GET /api/v1/health":         {summary: "Report that the server is up, with its version.", public: true},
	"GET /api/v1/setup":          {summary: "Report whether the first account still has to be created.", public: true},
	"POST /api/v1/setup":         {summary: "Create the first administrator account.", public: true},
	"POST /api/v1/auth/login":    {summary: "Sign in and receive a session cookie.", public: true},
	"POST /api/v1/auth/logout":   {summary: "End the current session.", role: auth.RoleViewer, session: true},
	"GET /api/v1/auth/me":        {summary: "Describe the current session and the role it acts with.", role: auth.RoleViewer, session: true},
	"POST /api/v1/auth/password": {summary: "Change the signed-in account's password.", role: auth.RoleViewer, session: true},

	"GET /api/v1/status":                   {summary: "Whether a configuration is saved, loaded, and confirmed.", role: auth.RoleViewer},
	"GET /api/v1/system/stats":             {summary: "CPU, memory, swap, load, disk space and the connection table on this router.", role: auth.RoleViewer},
	"GET /api/v1/system/timezones":         {summary: "The timezones this router can be set to, and the one its clock reads now.", role: auth.RoleViewer},
	"GET /api/v1/overview":                 {summary: "Everything the dashboard shows, in one request.", role: auth.RoleViewer},
	"GET /api/v1/config":                   {summary: "The saved configuration. A viewer gets it without its secrets.", role: auth.RoleViewer},
	"GET /api/v1/config/revisions":         {summary: "List archived configurations.", role: auth.RoleViewer},
	"GET /api/v1/config/revisions/{id}":    {summary: "Read one archived configuration. A viewer gets it without its secrets.", role: auth.RoleViewer},
	"GET /api/v1/ruleset":                  {summary: "The nftables ruleset that is loaded.", role: auth.RoleViewer},
	"GET /api/v1/counters":                 {summary: "Per-rule packet and byte counters from the kernel.", role: auth.RoleViewer},
	"POST /api/v1/rules/system":            {summary: "The rules Ostiole adds on its own for the given configuration, in evaluation order.", role: auth.RoleViewer},
	"POST /api/v1/nat/system":              {summary: "The outbound NAT rules Ostiole writes on its own for the given configuration: the automatic masquerade.", role: auth.RoleViewer},
	"POST /api/v1/dns/system-hosts":        {summary: "The names a configuration makes the DNS server answer on its own: static leases with hostnames.", role: auth.RoleViewer},
	"DELETE /api/v1/dns/cache":             {summary: "Empty the DNS caches this router is running: dnsmasq, and the validating resolver when it is on.", role: auth.RoleOperator},
	"GET /api/v1/interfaces/live":          {summary: "Interfaces as the kernel has them now.", role: auth.RoleViewer},
	"POST /api/v1/interfaces/{name}/renew": {summary: "Ask for a fresh lease on a dynamic interface; release drops it first.", role: auth.RoleOperator},
	"GET /api/v1/services/status":          {summary: "Whether DHCP, DNS, the resolver, PPPoE, the mapping service, Tailscale, the proxy and the time service are set up and running.", role: auth.RoleViewer},
	"GET /api/v1/ntp/status":               {summary: "What the time service is doing: whether the clock is synchronised, the servers it asks, and what the LAN asked of it.", role: auth.RoleViewer},
	"GET /api/v1/dhcp/leases":              {summary: "Current DHCP leases, with the interface each was handed out on, the static lease pinning it, when it was last renewed and when its client last answered.", role: auth.RoleViewer},
	"GET /api/v1/upnp/mappings":            {summary: "Port mappings clients have opened over UPnP IGD, PCP, or NAT-PMP, read from the ruleset.", role: auth.RoleViewer},
	"POST /api/v1/wol/wake":                {summary: "Send the Wake on LAN packet for a MAC address onto an inside interface.", role: auth.RoleOperator},
	"GET /api/v1/tailscale/status":         {summary: "What the Tailscale node is doing: its state, its addresses, and the other nodes on the tailnet.", role: auth.RoleViewer},
	"GET /api/v1/proxy/status":             {summary: "Whether the reverse proxy is set up and running, the ports it answers on, and its upstreams.", role: auth.RoleViewer},
	"GET /api/v1/proxy/events":             {summary: "What the web application firewall matched, newest first.", role: auth.RoleViewer},
	"GET /api/v1/wireless/radios":          {summary: "The radios this router has, what each can do, and what it is transmitting now.", role: auth.RoleViewer},
	"GET /api/v1/wireless/clients":         {summary: "The clients connected to the wireless networks, with their signal and traffic.", role: auth.RoleViewer},
	"GET /api/v1/gateways":                 {summary: "Gateway health from the multi-WAN monitor.", role: auth.RoleViewer},
	"GET /api/v1/gateways/detected":        {summary: "Default routes the kernel already has, and the gateway each would become.", role: auth.RoleViewer},
	"GET /api/v1/policy":                   {summary: "Where policy-routed traffic is being sent.", role: auth.RoleViewer},
	"GET /api/v1/shaping":                  {summary: "Line speeds per interface and how the queues are behaving.", role: auth.RoleViewer},
	"GET /api/v1/log/recent":               {summary: "Recent firewall log entries, newest first.", role: auth.RoleViewer},
	"GET /api/v1/log/stream":               {summary: "Firewall log entries as they arrive (server-sent events).", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/journal":      {summary: "Read the system journal, newest entry first. A viewer reads Ostiole's own units only.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/states":       {summary: "The connections the kernel is tracking.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/neighbours":   {summary: "The ARP and NDP tables.", role: auth.RoleViewer},

	"GET /api/v1/diagnostics/drives":                     {summary: "Every drive in this router: identity, health, temperature, wear, attributes and logs.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/drives/{name}":              {summary: "What one drive says about itself.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/drives/{name}/report":       {summary: "One drive's full SMART report as text, serial number included.", role: auth.RoleViewer},
	"POST /api/v1/diagnostics/drives/{name}/self-test":   {summary: "Start a short, extended or conveyance self-test on a drive.", role: auth.RoleOperator},
	"DELETE /api/v1/diagnostics/drives/{name}/self-test": {summary: "Stop the self-test a drive is running.", role: auth.RoleOperator},
	"GET /api/v1/certificates":                           {summary: "Every certificate this router holds, the built-in self-signed pair, and the DNS providers this build can write to.", role: auth.RoleViewer},
	"GET /api/v1/update/status":                          {summary: "What the last check found about Ostiole's own releases, and the progress of an update that is running.", role: auth.RoleViewer},
	"GET /api/v1/system/updates":                         {summary: "What the distro package manager has waiting, and whether a reboot is.", role: auth.RoleViewer},
	"POST /api/v1/config/diff":                           {summary: "Compare two configurations.", role: auth.RoleViewer},

	"POST /api/v1/config/starter":               {summary: "Build a first configuration from the wizard's answers.", role: auth.RoleOperator},
	"POST /api/v1/check":                        {summary: "Validate a configuration and render it without applying. Changes only an administrator may apply are refused.", role: auth.RoleOperator},
	"POST /api/v1/apply":                        {summary: "Apply a configuration, optionally with a confirmation window. Command crons, backups that carry accounts, updates, the remote backup, notifications, management access and anti-lockout need an administrator.", role: auth.RoleOperator},
	"POST /api/v1/apply/confirm":                {summary: "Confirm the pending apply.", role: auth.RoleOperator},
	"POST /api/v1/apply/revert":                 {summary: "Undo the pending apply.", role: auth.RoleOperator},
	"POST /api/v1/config/restore":               {summary: "Read a backup file and report what it would change.", role: auth.RoleOperator},
	"GET /api/v1/config/backup/remote":          {summary: "The backups this router has in its bucket, newest first.", role: auth.RoleOperator},
	"POST /api/v1/config/backup/remote/restore": {summary: "Read one backup out of the bucket and report what it would change.", role: auth.RoleOperator},
	"POST /api/v1/wireguard/keys":               {summary: "Generate a WireGuard key pair or preshared key.", role: auth.RoleOperator},
	"POST /api/v1/certificates/keys":            {summary: "Generate a private key for an ACME account.", role: auth.RoleOperator},
	"POST /api/v1/tailscale/login":              {summary: "Log this router into its tailnet, with an auth key or a browser.", role: auth.RoleOperator},
	"GET /api/v1/diagnostics/modem":             {summary: "What the cable modem in 192.168.100.0/24 reports: provisioning, channels and levels.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/ping":             {summary: "Ping an address from this router.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/traceroute":       {summary: "Trace the route to an address.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/capture":          {summary: "Capture packets and return a pcap file.", role: auth.RoleOperator},
	"POST /api/v1/system/updates/check":         {summary: "Ask the distro package manager what is waiting.", role: auth.RoleOperator},

	"POST /api/v1/tailscale/logout":              {summary: "Drop this router's tailnet key. The node stays in the admin console.", role: auth.RoleAdmin},
	"POST /api/v1/config/backup":                 {summary: "Download the configuration as a backup file.", role: auth.RoleAdmin},
	"POST /api/v1/certificates/self-signed":      {summary: "Generate a new built-in self-signed certificate.", role: auth.RoleAdmin},
	"POST /api/v1/certificates/{id}/issue":       {summary: "Order a certificate now rather than waiting for the renewal cron.", role: auth.RoleAdmin},
	"GET /api/v1/certificates/{id}/files":        {summary: "The certificate, its chain and its key. Also open to a token restricted to this certificate.", role: auth.RoleAdmin},
	"GET /api/v1/certificates/{id}/files/{name}": {summary: "One PEM file of a certificate, as a download.", role: auth.RoleAdmin},
	"POST /api/v1/certificates/{id}/pkcs12":      {summary: "The certificate and its key as a password-protected PKCS#12 file.", role: auth.RoleAdmin},
	"GET /api/v1/tokens":                         {summary: "List API tokens.", role: auth.RoleAdmin},
	"POST /api/v1/tokens":                        {summary: "Create an API token; the secret is returned once.", role: auth.RoleAdmin},
	"DELETE /api/v1/tokens/{id}":                 {summary: "Delete an API token by id or name.", role: auth.RoleAdmin},
	"GET /api/v1/users":                          {summary: "List accounts and their roles.", role: auth.RoleAdmin},
	"POST /api/v1/users":                         {summary: "Create a local account with a password and a role.", role: auth.RoleAdmin},
	"DELETE /api/v1/users/{name}":                {summary: "Delete an account and end its sessions; not your own.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/role":             {summary: "Change what an account may do; not your own.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/password":         {summary: "Set another account's password, ending its sessions.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/username":         {summary: "Rename an account, keeping its role and sessions.", role: auth.RoleAdmin},
	"GET /api/v1/update/check":                   {summary: "Ask GitHub now whether a newer release exists, and cache the answer.", role: auth.RoleAdmin},
	"POST /api/v1/update/apply":                  {summary: "Download and install a release.", role: auth.RoleAdmin},
	"POST /api/v1/system/updates/apply":          {summary: "Install the distro packages the update mode allows.", role: auth.RoleAdmin},
	"POST /api/v1/system/reboot":                 {summary: "Reboot this router.", role: auth.RoleAdmin},

	"GET /api/v1/aliases/feeds":           {summary: "When each fetched alias was last updated, and what went wrong if it did.", role: auth.RoleViewer},
	"POST /api/v1/aliases/feeds/refresh":  {summary: "Fetch every address list, country list and AS list now.", role: auth.RoleOperator},
	"POST /api/v1/aliases/inspect":        {summary: "Read a list before an alias names it: how many addresses it holds and what it can be narrowed by. An operator's URL has to be on a public address unless the router already fetches it.", role: auth.RoleOperator},
	"POST /api/v1/aliases/{name}/refresh": {summary: "Fetch one alias now.", role: auth.RoleOperator},

	"GET /api/v1/blocking":                       {summary: "What DNS blocking is doing: the lists, how many names they hold, and when they were last fetched.", role: auth.RoleViewer},
	"GET /api/v1/blocking/catalog":               {summary: "The published blocklists Ostiole offers to subscribe to.", role: auth.RoleViewer},
	"GET /api/v1/blocking/lookup":                {summary: "Whether a name is blocked, and which list blocks it.", role: auth.RoleViewer},
	"POST /api/v1/blocking/refresh":              {summary: "Fetch every DNS blocklist now.", role: auth.RoleOperator},
	"POST /api/v1/blocking/lists/{name}/refresh": {summary: "Fetch one DNS blocklist now.", role: auth.RoleOperator},
	"POST /api/v1/blocking/lists/{name}/import":  {summary: "Load a DNS blocklist from the request body, for a router with no way out to the internet.", role: auth.RoleOperator},

	"GET /api/v1/dns/queries":         {summary: "What the DNS server answered, newest first, while the query log is on.", role: auth.RoleViewer},
	"GET /api/v1/dns/queries/stream":  {summary: "Answers from the DNS server as they are given (server-sent events).", role: auth.RoleViewer},
	"GET /api/v1/dns/queries/summary": {summary: "Totals for the query log, with the busiest names and clients.", role: auth.RoleViewer},
	"DELETE /api/v1/dns/queries":      {summary: "Empty the query log and its per-list counts.", role: auth.RoleOperator},

	"GET /api/v1/notifications":       {summary: "How mail and the webhook have done, and the notices sent lately.", role: auth.RoleViewer},
	"POST /api/v1/notifications/test": {summary: "Send a test notice to the targets the settings in the body switch on.", role: auth.RoleAdmin},

	"GET /api/v1/crons":           {summary: "The operator's crons, and the work Ostiole does on its own account.", role: auth.RoleViewer},
	"POST /api/v1/crons/{id}/run": {summary: "Run a cron now. Command and update crons need an administrator.", role: auth.RoleOperator},

	"GET /api/v1/host":               {summary: "What this router is: distribution, kernel, Ostiole's units, the daemons it has, who owns the addresses, and leftover rulesets.", role: auth.RoleViewer},
	"POST /api/v1/host/legacy/flush": {summary: "Clear the rulesets an older firewall left in the kernel.", role: auth.RoleAdmin},

	"GET /metrics":             {summary: "Metrics in the Prometheus text format.", role: auth.RoleViewer},
	"GET /api/v1/openapi.json": {summary: "This description.", public: true},
}

func (a *api) registerOpenAPI(mux *router) {
	mux.HandleFunc("GET /api/v1/openapi.json", a.public(func(w http.ResponseWriter, r *http.Request) error {
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		writeJSON(w, http.StatusOK, a.openAPI(scheme+"://"+r.Host))
		return nil
	}))
}

// OpenAPIDocument renders the description without running a server, for
// anyone generating a client or checking it into a repository. The
// handlers are built only so the routes register themselves.
func OpenAPIDocument(baseURL string) ([]byte, error) {
	a := &api{}
	mux := &router{mux: http.NewServeMux()}
	a.routes = mux
	mux.HandleFunc("GET /api/v1/health", a.health)
	a.register(mux)
	return marshalOpenAPI(a.openAPI(baseURL))
}

// openAPI builds the description from the routes that were registered.
func (a *api) openAPI(baseURL string) map[string]any {
	paths := map[string]any{}
	sorted := append([]route{}, a.routes.routes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		return sorted[i].Method < sorted[j].Method
	})
	for _, rt := range sorted {
		item, _ := paths[rt.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[rt.Path] = item
		}
		op := map[string]any{
			"summary":     rt.Summary,
			"operationId": operationID(rt),
			"responses": map[string]any{
				"200": map[string]any{"description": "Success"},
				"401": map[string]any{"description": "Authentication required"},
				"403": map[string]any{"description": "The caller's role does not allow this"},
			},
		}
		switch {
		case rt.Public:
			op["security"] = []any{}
		case rt.Session:
			op["security"] = []any{map[string]any{"session": []string{}}}
			op["x-required-role"] = string(rt.Role)
		default:
			op["security"] = []any{
				map[string]any{"session": []string{}},
				map[string]any{"token": []string{}},
			}
			op["x-required-role"] = string(rt.Role)
		}
		if params := pathParams(rt.Path); len(params) > 0 {
			op["parameters"] = params
		}
		item[strings.ToLower(rt.Method)] = op
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Ostiole",
			"version": version.Version,
			"description": "The API behind the Ostiole web UI. Every endpoint takes either the " +
				"session cookie the UI uses or an API token in an Authorization header, " +
				"except the three under /auth, which are about the session itself and take " +
				"only the cookie. Roles are admin, operator, and viewer; each endpoint says " +
				"the least it needs.",
			"license": map[string]any{"name": "AGPL-3.0-or-later"},
		},
		"servers": []any{map[string]any{"url": baseURL}},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"session": map[string]any{
					"type": "apiKey", "in": "cookie", "name": SessionCookie,
					"description": "Set by POST /api/v1/auth/login. Mutating requests with a " +
						"session also need the " + RequestHeader + " header.",
				},
				"token": map[string]any{
					"type": "http", "scheme": "bearer",
					"description": "An API token from POST /api/v1/tokens, sent as " +
						"Authorization: Bearer ost_…",
				},
			},
		},
		"paths": paths,
	}
}

func operationID(rt route) string {
	id := strings.ToLower(rt.Method) + strings.NewReplacer("/", "_", "{", "", "}", "", ".", "_", "-", "_").
		Replace(strings.TrimPrefix(rt.Path, "/api/v1"))
	return strings.Trim(id, "_")
}

func pathParams(path string) []any {
	var out []any
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			out = append(out, map[string]any{
				"name":     strings.Trim(part, "{}"),
				"in":       "path",
				"required": true,
				"schema":   map[string]any{"type": "string"},
			})
		}
	}
	return out
}

// marshalOpenAPI is used by the CLI to write the description to a file.
func marshalOpenAPI(doc map[string]any) ([]byte, error) {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
