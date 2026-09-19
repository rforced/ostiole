package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
)

// Every route the server registers has to be described, or the OpenAPI
// document quietly stops matching the API. Adding a route without a line
// in routeDocs fails here.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/openapi.json", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("openapi: %d %s", resp.StatusCode, raw)
	}
	var doc struct {
		OpenAPI string                            `json:"openapi"`
		Paths   map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Errorf("openapi version = %q", doc.OpenAPI)
	}

	described := 0
	for path, ops := range doc.Paths {
		for method, op := range ops {
			described++
			m, _ := op.(map[string]any)
			summary, _ := m["summary"].(string)
			if summary == "" {
				t.Errorf("%s %s has no summary: add it to routeDocs", strings.ToUpper(method), path)
			}
		}
	}
	// The SPA fallback and the /api/ 404 are handlers, not API routes.
	if described < 40 {
		t.Errorf("described %d operations, which looks like routes went missing", described)
	}
}

// probe sends one request with a bearer token and nothing else, or with no
// credentials at all when token is empty, and reports the status.
func probe(t *testing.T, srv *httptest.Server, method, path, token string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// The CSRF guard sits in front of the gate and answers a write without
	// this header itself; it is the gate that is under test here.
	req.Header.Set(RequestHeader, RequestHeaderValue)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The description says who may call each route, and a client is written
// against it. The wrapper on the route and its line in routeDocs are two
// places, so this is what holds them together: every route is tried with
// no credentials, then with a token one role short of the one documented,
// and every read with exactly that role.
func TestEveryRouteEnforcesItsDocumentedRole(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	tokens := map[auth.Role]string{
		auth.RoleViewer:   mintToken(t, srv, "look", string(auth.RoleViewer)),
		auth.RoleOperator: mintToken(t, srv, "change", string(auth.RoleOperator)),
		auth.RoleAdmin:    mintToken(t, srv, "own", string(auth.RoleAdmin)),
	}
	short := map[auth.Role]auth.Role{auth.RoleOperator: auth.RoleViewer, auth.RoleAdmin: auth.RoleOperator}
	// Path parameters take any value: the gate comes before the lookup.
	params := strings.NewReplacer("{id}", "x", "{name}", "x")
	for pattern, doc := range routeDocs {
		if doc.public {
			continue
		}
		method, path, _ := strings.Cut(pattern, " ")
		path = params.Replace(path)
		if got := probe(t, srv, method, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s with no credentials: %d, want 401", pattern, got)
		}
		if doc.session {
			// About the session itself: a token is refused whatever its role,
			// and the description says only the cookie gets in.
			if got := probe(t, srv, method, path, tokens[auth.RoleAdmin]); got != http.StatusUnauthorized {
				t.Errorf("%s with a token: %d, want 401 (documented as session only)", pattern, got)
			}
			continue
		}
		if lower, ok := short[doc.role]; ok {
			if got := probe(t, srv, method, path, tokens[lower]); got != http.StatusForbidden {
				t.Errorf("%s with a %s token: %d, want 403 (documented as %s)", pattern, lower, got, doc.role)
			}
		}
		// Reads have no side effects, so the documented role is tried too:
		// it has to get past the gate, whatever the handler then says.
		if method == http.MethodGet {
			if got := probe(t, srv, method, path, tokens[doc.role]); got == http.StatusUnauthorized || got == http.StatusForbidden {
				t.Errorf("%s with a %s token: %d, which the description does not warn about", pattern, doc.role, got)
			}
		}
	}
}

// The roles in the description have to be the roles the server enforces,
// because that is what somebody writing a client will read.
// TestEveryRouteEnforcesItsDocumentedRole holds routeDocs to the wrappers;
// this checks the document carries what routeDocs says.
func TestOpenAPIStatesTheRequiredRole(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	_, raw := do(t, srv, http.MethodGet, "/api/v1/openapi.json", nil)

	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"/api/v1/status": "viewer",
		"/api/v1/apply":  "operator",
		"/api/v1/tokens": "admin",
	} {
		method := "get"
		if path == "/api/v1/apply" {
			method = "post"
		}
		op := doc.Paths[path][method]
		if op == nil {
			t.Errorf("%s %s missing", method, path)
			continue
		}
		if got, _ := op["x-required-role"].(string); got != want {
			t.Errorf("%s %s role = %q, want %q", method, path, got, want)
		}
	}
	// A route anyone may call says so by carrying no security at all.
	if sec, ok := doc.Paths["/api/v1/health"]["get"]["security"].([]any); !ok || len(sec) != 0 {
		t.Errorf("health security = %v, want none", doc.Paths["/api/v1/health"]["get"]["security"])
	}
}

func TestMetricsAreScrapableWithAToken(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	secret := mintToken(t, srv, "prometheus", "viewer")

	resp, raw := withToken(t, srv, http.MethodGet, "/metrics", secret)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics: %d %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q", ct)
	}
	body := string(raw)
	for _, want := range []string{
		"# TYPE ostiole_build_info gauge",
		"ostiole_build_info{",
		"ostiole_configured ",
		"ostiole_ruleset_loaded ",
		"# TYPE ostiole_interface_receive_bytes_total counter",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics are missing %q:\n%s", want, body)
		}
	}
	// Every HELP and TYPE line appears once however many samples follow.
	if n := strings.Count(body, "# TYPE ostiole_interface_up "); n != 1 {
		t.Errorf("TYPE for ostiole_interface_up appears %d times", n)
	}
}

func TestMetricsNeedAuthentication(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated scrape = %d, want 401", resp.StatusCode)
	}
}

func TestEscapeLabel(t *testing.T) {
	t.Parallel()
	if got := escapeLabel(`a"b\c` + "\n"); got != `a\"b\\c\n` {
		t.Errorf("escapeLabel = %q", got)
	}
}
