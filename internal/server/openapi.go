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
	Method  string
	Path    string
	Role    auth.Role
	Public  bool
	Summary string
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
}

// routeDocs describes every endpoint. TestOpenAPICoversEveryRoute fails
// when a route is added without a line here, which is what keeps the
// description honest.
var routeDocs = map[string]routeDoc{
	"GET /api/v1/health":         {summary: "Report that the server is up, with its version.", public: true},
	"GET /api/v1/setup":          {summary: "Report whether the first account still has to be created.", public: true},
	"POST /api/v1/setup":         {summary: "Create the first administrator account.", public: true},
	"POST /api/v1/auth/login":    {summary: "Sign in and receive a session cookie.", public: true},
	"POST /api/v1/auth/logout":   {summary: "End the current session.", role: auth.RoleViewer},
	"GET /api/v1/auth/me":        {summary: "Describe the current session.", role: auth.RoleViewer},
	"POST /api/v1/auth/password": {summary: "Change the signed-in account's password.", role: auth.RoleViewer},

	"GET /api/v1/status":                 {summary: "Whether a configuration is saved, loaded, and confirmed.", role: auth.RoleViewer},
	"GET /api/v1/system/stats":           {summary: "CPU, memory, swap, load and disk space on this router.", role: auth.RoleViewer},
	"GET /api/v1/system/timezones":       {summary: "The timezones this router can be set to, and the one its clock reads now.", role: auth.RoleViewer},
	"GET /api/v1/overview":               {summary: "Everything the dashboard shows, in one request.", role: auth.RoleViewer},
	"GET /api/v1/config":                 {summary: "The saved configuration.", role: auth.RoleViewer},
	"GET /api/v1/config/revisions":       {summary: "List archived configurations.", role: auth.RoleViewer},
	"GET /api/v1/config/revisions/{id}":  {summary: "Read one archived configuration.", role: auth.RoleViewer},
	"GET /api/v1/ruleset":                {summary: "The nftables ruleset that is loaded.", role: auth.RoleViewer},
	"GET /api/v1/counters":               {summary: "Per-rule packet and byte counters from the kernel.", role: auth.RoleViewer},
	"POST /api/v1/rules/system":          {summary: "The rules Ostiole adds on its own for the given configuration, in evaluation order.", role: auth.RoleViewer},
	"GET /api/v1/interfaces/live":        {summary: "Interfaces as the kernel has them now.", role: auth.RoleViewer},
	"GET /api/v1/services/status":        {summary: "Whether DHCP, DNS, the resolver, PPPoE, and the mapping service are set up and running.", role: auth.RoleViewer},
	"GET /api/v1/dhcp/leases":            {summary: "Current DHCP leases.", role: auth.RoleViewer},
	"GET /api/v1/upnp/mappings":          {summary: "Port mappings clients have opened over UPnP IGD, PCP, or NAT-PMP, read from the ruleset.", role: auth.RoleViewer},
	"GET /api/v1/gateways":               {summary: "Gateway health from the multi-WAN monitor.", role: auth.RoleViewer},
	"GET /api/v1/gateways/detected":      {summary: "Default routes the kernel already has, and the gateway each would become.", role: auth.RoleViewer},
	"GET /api/v1/policy":                 {summary: "Where policy-routed traffic is being sent.", role: auth.RoleViewer},
	"GET /api/v1/shaping":                {summary: "Line speeds per interface and how the queues are behaving.", role: auth.RoleViewer},
	"GET /api/v1/log/recent":             {summary: "Recent firewall log entries.", role: auth.RoleViewer},
	"GET /api/v1/log/stream":             {summary: "Firewall log entries as they arrive (server-sent events).", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/journal":    {summary: "Read the system journal.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/states":     {summary: "The connections the kernel is tracking.", role: auth.RoleViewer},
	"GET /api/v1/diagnostics/neighbours": {summary: "The ARP and NDP tables.", role: auth.RoleViewer},
	"GET /api/v1/certificate":            {summary: "Describe the certificate the web UI serves.", role: auth.RoleViewer},
	"GET /api/v1/update/status":          {summary: "What the last check found about Ostiole's own releases, and the progress of an update that is running.", role: auth.RoleViewer},
	"GET /api/v1/system/updates":         {summary: "What the distro package manager has waiting, and whether a reboot is.", role: auth.RoleViewer},
	"POST /api/v1/config/diff":           {summary: "Compare two configurations.", role: auth.RoleViewer},

	"POST /api/v1/config/starter":         {summary: "Build a first configuration from the wizard's answers.", role: auth.RoleOperator},
	"POST /api/v1/check":                  {summary: "Validate a configuration and render it without applying.", role: auth.RoleOperator},
	"POST /api/v1/apply":                  {summary: "Apply a configuration, optionally with a confirmation window.", role: auth.RoleOperator},
	"POST /api/v1/apply/confirm":          {summary: "Confirm the pending apply.", role: auth.RoleOperator},
	"POST /api/v1/apply/revert":           {summary: "Undo the pending apply.", role: auth.RoleOperator},
	"POST /api/v1/config/restore":         {summary: "Read a backup file and report what it would change.", role: auth.RoleOperator},
	"POST /api/v1/wireguard/keys":         {summary: "Generate a WireGuard key pair or preshared key.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/ping":       {summary: "Ping an address from this router.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/traceroute": {summary: "Trace the route to an address.", role: auth.RoleOperator},
	"POST /api/v1/diagnostics/capture":    {summary: "Capture packets and return a pcap file.", role: auth.RoleOperator},
	"POST /api/v1/system/updates/check":   {summary: "Ask the distro package manager what is waiting.", role: auth.RoleOperator},

	"GET /api/v1/config/backup":            {summary: "Download the configuration as a backup file.", role: auth.RoleAdmin},
	"POST /api/v1/certificate":             {summary: "Install a certificate and its private key.", role: auth.RoleAdmin},
	"POST /api/v1/certificate/self-signed": {summary: "Generate a new self-signed certificate.", role: auth.RoleAdmin},
	"GET /api/v1/tokens":                   {summary: "List API tokens.", role: auth.RoleAdmin},
	"POST /api/v1/tokens":                  {summary: "Create an API token; the secret is returned once.", role: auth.RoleAdmin},
	"DELETE /api/v1/tokens/{id}":           {summary: "Delete an API token by id or name.", role: auth.RoleAdmin},
	"GET /api/v1/users":                    {summary: "List accounts and their roles.", role: auth.RoleAdmin},
	"POST /api/v1/users":                   {summary: "Create a local account with a password and a role.", role: auth.RoleAdmin},
	"DELETE /api/v1/users/{name}":          {summary: "Delete an account and end its sessions; not your own.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/role":       {summary: "Change what an account may do; not your own.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/password":   {summary: "Set another account's password, ending its sessions.", role: auth.RoleAdmin},
	"POST /api/v1/users/{name}/username":   {summary: "Rename an account, keeping its role and sessions.", role: auth.RoleAdmin},
	"GET /api/v1/update/check":             {summary: "Ask GitHub now whether a newer release exists, and cache the answer.", role: auth.RoleAdmin},
	"POST /api/v1/update/apply":            {summary: "Download and install a release.", role: auth.RoleAdmin},
	"POST /api/v1/system/updates/apply":    {summary: "Install the distro packages the update mode allows.", role: auth.RoleAdmin},
	"POST /api/v1/system/reboot":           {summary: "Reboot this router.", role: auth.RoleAdmin},

	"GET /api/v1/aliases/feeds":           {summary: "When each fetched alias was last updated, and what went wrong if it did.", role: auth.RoleViewer},
	"POST /api/v1/aliases/feeds/refresh":  {summary: "Fetch every address list and country list now.", role: auth.RoleOperator},
	"POST /api/v1/aliases/{name}/refresh": {summary: "Fetch one alias now.", role: auth.RoleOperator},

	"GET /api/v1/blocking":                       {summary: "What DNS blocking is doing: the lists, how many names they hold, and when they were last fetched.", role: auth.RoleViewer},
	"GET /api/v1/blocking/catalog":               {summary: "The published blocklists Ostiole offers to subscribe to.", role: auth.RoleViewer},
	"GET /api/v1/blocking/lookup":                {summary: "Whether a name is blocked, and which list blocks it.", role: auth.RoleViewer},
	"POST /api/v1/blocking/refresh":              {summary: "Fetch every DNS blocklist now.", role: auth.RoleOperator},
	"POST /api/v1/blocking/lists/{name}/refresh": {summary: "Fetch one DNS blocklist now.", role: auth.RoleOperator},
	"POST /api/v1/blocking/lists/{name}/import":  {summary: "Load a DNS blocklist from the request body, for a router with no way out to the internet.", role: auth.RoleOperator},

	"GET /api/v1/crons":           {summary: "The operator's crons, and the work Ostiole does on its own account.", role: auth.RoleViewer},
	"POST /api/v1/crons/{id}/run": {summary: "Run a cron now.", role: auth.RoleOperator},

	"GET /api/v1/host":                  {summary: "What this router still needs: packages, competing services, leftover rulesets, and who owns the addresses.", role: auth.RoleViewer},
	"POST /api/v1/host/prepare":         {summary: "Set up the components the configuration needs, retire the old firewall once a ruleset is loaded, and clear the leftovers, in that order.", role: auth.RoleAdmin},
	"POST /api/v1/host/setup":           {summary: "Install the named components and write their units.", role: auth.RoleAdmin},
	"POST /api/v1/host/extras/remove":   {summary: "Remove what a router has no use for (other updaters, snapd, desktop daemons), or preview what removing it would take.", role: auth.RoleAdmin},
	"POST /api/v1/host/ssh":             {summary: "Turn password logins over SSH off (keys only) or back on.", role: auth.RoleAdmin},
	"POST /api/v1/host/takeover":        {summary: "Stop, disable and mask every competing firewall service.", role: auth.RoleAdmin},
	"POST /api/v1/host/packages/remove": {summary: "Remove a retired competitor's packages, or preview what removing them would take.", role: auth.RoleAdmin},
	"POST /api/v1/host/legacy/flush":    {summary: "Clear the rulesets an older firewall left in the kernel.", role: auth.RoleAdmin},
	"POST /api/v1/host/network":         {summary: "Hand addressing to systemd-networkd, or confirm or revert a handover that is waiting.", role: auth.RoleAdmin},
	"POST /api/v1/host/steps/{step}":    {summary: "Leave a preparation step alone, or take that back.", role: auth.RoleAdmin},

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
	mux.HandleFunc("GET /api/v1/health", handleHealth)
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
		if rt.Public {
			op["security"] = []any{}
		} else {
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
				"session cookie the UI uses or an API token in an Authorization header. " +
				"Roles are admin, operator, and viewer; each endpoint says the least it needs.",
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
