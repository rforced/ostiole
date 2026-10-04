package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/requestlog"
	"github.com/rforced/ostiole/internal/testenv"
	"github.com/rforced/ostiole/internal/wafevent"
)

// A name no site claims gets nothing from the proxy: a 403 over HTTP, and
// no handshake over HTTPS, whatever the loaded certificates cover. What
// Caddy makes of the configuration is the point, so the sidecar is asked.
func TestProxyRefusesNamesNoSiteClaims(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled:   true,
		HTTPPort:  freePort(t),
		HTTPSPort: freePort(t),
		Pools:     []model.ProxyPool{{ID: "web", Upstreams: []model.ProxyUpstream{{Address: answer(t, "site")}}}},
		Sites: []model.ProxySite{
			// The built-in pair lists every address the router had.
			{ID: "status", Enabled: true, Hosts: []string{"status.example.com"}, Pool: "web"},
			// A wildcard covers names the site does not serve.
			{ID: "shop", Enabled: true, Hosts: []string{"shop.example.com"}, Certificate: "wild", Pool: "web"},
		},
	}
	plain, secure, _ := runSidecar(t, bin, cfg, answer(t, "solver"), map[string][]string{
		model.SelfCertificate: {"gateway.example.net", "127.0.0.1"},
		"wild":                {"*.example.com"},
	})

	for _, tc := range []struct {
		name, sni, url string
		status         int
		body           string
	}{
		{"the address", "", "http://127.0.0.1/", http.StatusForbidden, ""},
		{"a name no site claims", "", "http://nothing.example.com/", http.StatusForbidden, ""},
		{"the challenge under any name", "", "http://nothing.example.com/.well-known/acme-challenge/x", http.StatusOK, "solver"},
		{"a site", "", "http://status.example.com/", http.StatusPermanentRedirect, ""},
		{"a site's name, then a host no site claims", "status.example.com", "https://nothing.example.com/", http.StatusForbidden, ""},
		{"a site on the built-in pair", "status.example.com", "https://status.example.com/", http.StatusOK, "site"},
		{"a site on the wildcard", "shop.example.com", "https://shop.example.com/", http.StatusOK, "site"},
	} {
		addr := plain
		if tc.sni != "" {
			addr = secure
		}
		if status, body := fetch(t, addr, tc.sni, tc.url); status != tc.status || body != tc.body {
			t.Errorf("%s: %d %q, want %d %q", tc.name, status, body, tc.status, tc.body)
		}
	}

	// A browser names nothing for an address; the other name is one the
	// wildcard covers.
	for _, sni := range []string{"", "other.example.com"} {
		if names, err := handshake(secure, sni); !refused(err) {
			t.Errorf("a handshake naming %q got a certificate for %v (%v); want it refused", sni, names, err)
		}
	}
}

// With routes and no site the HTTPS port still speaks TLS: a route takes
// its name and any other handshake is refused, rather than read as HTTP.
func TestProxyRefusesNamesNoRouteClaims(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	mail := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "mail")
	}))
	t.Cleanup(mail.Close)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t)}
	cfg.Services.Proxy.Routes = []model.L4Route{{
		ID: "mail", Enabled: true, Protocol: "tcp", Port: cfg.Services.Proxy.HTTPSPort,
		SNI: []string{"mail.example.com"}, Upstreams: []model.ProxyUpstream{{Address: mail.Listener.Addr().String()}},
	}}
	_, secure, _ := runSidecar(t, bin, cfg, "", nil)

	if status, body := fetch(t, secure, "mail.example.com", "https://mail.example.com/"); status != http.StatusOK || body != "mail" {
		t.Errorf("the route: %d %q, want 200 %q", status, body, "mail")
	}
	for _, sni := range []string{"", "other.example.com"} {
		if names, err := handshake(secure, sni); !refused(err) {
			t.Errorf("a handshake naming %q got a certificate for %v (%v); want it refused", sni, names, err)
		}
	}
}

// An event says what the client got and why: the WAF's 403 to a request
// or a response it stopped, and the site's own answer to one it only
// flagged. The rules that inspect responses log like the rest.
func TestProxyEventsSayWhatTheClientGot(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	// A database error in a page is a leak the outbound rules stop.
	leaky := answer(t, "You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version")
	const attack = "/?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E"
	// A made-up JSON web token, which no line the proxy writes may hold.
	const token = "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2lnbmF0dXJl"
	cases := []struct {
		site, profile, pool, uri string
		status                   int
		verdict                  string
		rules                    []int
	}{
		{"guard", "block", "missing", attack, http.StatusForbidden, wafevent.VerdictBlocked, []int{941100, 949110}},
		{"watch", "detect", "missing", attack, http.StatusNotFound, wafevent.VerdictWouldBlock, []int{941100, 949110}},
		{"leak", "block", "leaky", "/", http.StatusForbidden, wafevent.VerdictBlocked, []int{951230, 959100}},
		{"spill", "detect", "leaky", "/", http.StatusOK, wafevent.VerdictWouldBlock, []int{951230, 959100}},
		{"vault", "block", "missing", attack + "&access_token=" + token, http.StatusForbidden, wafevent.VerdictBlocked, []int{941100, 949110}},
	}
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled:   true,
		HTTPPort:  freePort(t),
		HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{
			{ID: "missing", Upstreams: []model.ProxyUpstream{{Address: missing.Listener.Addr().String()}}},
			{ID: "leaky", Upstreams: []model.ProxyUpstream{{Address: leaky}}},
		},
		Profiles: []model.WAFProfile{
			{ID: "block", Mode: "block", InspectResponses: true},
			{ID: "detect", Mode: "detect", InspectResponses: true},
		},
	}
	for _, tc := range cases {
		cfg.Services.Proxy.Sites = append(cfg.Services.Proxy.Sites, model.ProxySite{
			ID: tc.site, Enabled: true, Hosts: []string{tc.site + ".example.com"}, Pool: tc.pool, PlainHTTP: true, WAF: tc.profile,
		})
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	for _, tc := range cases {
		if status, _ := fetch(t, plain, "", "http://"+tc.site+".example.com"+tc.uri); status != tc.status {
			t.Errorf("%s: the client got %d, want %d", tc.site, status, tc.status)
		}
		ev := wafEvent(t, out, tc.site)
		if ev.Status != tc.status || ev.Verdict != tc.verdict {
			t.Errorf("%s: event %d %s, want %d %s", tc.site, ev.Status, ev.Verdict, tc.status, tc.verdict)
		}
		for _, id := range tc.rules {
			if !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID == id }) {
				t.Errorf("%s: rules %v, want %d among them", tc.site, ev.Rules, id)
			}
		}
		if tc.site == "vault" && !strings.HasSuffix(ev.URI, "&access_token="+wafevent.Redacted) {
			t.Errorf("vault: the event's URI is %s", ev.URI)
		}
	}
	if strings.Contains(out.String(), token) {
		t.Errorf("the sidecar wrote the token:\n%s", out)
	}
}

// At Info a request's line names its site and user agent and keeps no
// query string and no other header, so a token in either stays out of the
// journal.
func TestProxyRequestLinesAreCutDown(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Sites: []model.ProxySite{{ID: "vault", Enabled: true, Hosts: []string{"vault.example.com"}, Pool: "app", PlainHTTP: true}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://vault.example.com/hub?access_token=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "probe/1")
	req.Header.Set("X-Api-Key", "hidden")
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, plain)
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	deadline := time.Now().Add(10 * time.Second)
	for {
		for l := range strings.SplitSeq(out.String(), "\n") {
			r, ok := requestlog.Parse(l)
			if !ok || r.Site != "vault" {
				continue
			}
			if r.Path != "/hub" || r.Agent != "probe/1" || r.Status != http.StatusOK || r.Method != http.MethodGet ||
				strings.Contains(l, "secret") || strings.Contains(l, "hidden") {
				t.Errorf("request line %s read as %+v", l, r)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no request line:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// An upstream that is gone is logged at the error level, and the request
// the line names is cut down as any request line is: no query string and
// no headers, where Caddy itself hides only the four credential headers.
func TestProxyErrorLinesAreCutDown(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	gone := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(freePort(t))))
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "gone", Upstreams: []model.ProxyUpstream{{Address: gone}}}},
		Sites: []model.ProxySite{{ID: "watch", Enabled: true, Hosts: []string{"watch.example.com"}, Pool: "gone", PlainHTTP: true}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://watch.example.com/Items?api_key=secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Token="hidden"`)
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, plain)
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		for l := range strings.SplitSeq(out.String(), "\n") {
			if !strings.Contains(l, `"status":502`) {
				continue
			}
			if !strings.Contains(l, `"uri":"/Items"`) || strings.Contains(l, "secret") || strings.Contains(l, "hidden") {
				t.Errorf("error line %s", l)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no error line:\n%s", out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A JSON body is read as JSON: a harmless one passes even at paranoia 4,
// where read as a form its quotes and braces were an attack, and an attack
// in one of its values is caught by that value's name, even where another
// key flattens to the same name or differs only in case. One that does not
// parse, or nests too deep, is refused rather than let through unread, and
// an empty one is not read at all.
func TestProxyReadsJSONBodiesAsJSON(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		// CRS takes no +json type unless one is let in, as here.
		Profiles: []model.WAFProfile{{ID: "strict", Mode: "block", Paranoia: 4,
			Exclusions: []model.WAFExclusion{{Rule: "920420", Path: "/event"}}}},
		Sites: []model.ProxySite{{ID: "api", Enabled: true, Hosts: []string{"api.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "strict"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://api.example.com"
	for _, c := range []struct {
		path, contentType, body string
		status                  int
	}{
		{"/user-key-id", "application/json; charset=utf-8", `{"userKeyId":"9f86d081884c7d659a2feaa0c55ad015"}`, http.StatusOK},
		{"/event", "application/cloudevents+json", `{"specversion":"1.0","type":"a.b","data":{"tags":["a","b"]}}`, http.StatusOK},
		{"/empty", "application/json", "", http.StatusOK},
		{"/xss", "application/json", `{"name":"<script>alert(1)</script>"}`, http.StatusForbidden},
		// Both flatten to json.a.b, where Coraza 3.7 kept only the last.
		{"/collision", "application/json", `{"a":{"b":"<script>alert(1)</script>"},"a.b":"x"}`, http.StatusForbidden},
		{"/broken", "application/json", `{"name": `, http.StatusForbidden},
	} {
		if got := post(t, plain, site+c.path, c.contentType, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	// Names that differ only in case are one argument, where Coraza 3.8.0
	// kept one of the two values, at random.
	for range 20 {
		if got := post(t, plain, site+"/case", "application/json", `{"a":{"b":"<script>alert(1)</script>","B":"x"}}`); got != http.StatusForbidden {
			t.Fatalf("/case: status %d, want 403", got)
		}
	}
	// Nested ten million deep behind a thousand values: Coraza 3.8.0 stopped
	// reading at the limit, then checked the rest by recursion, and the stack
	// overflow killed the proxy.
	deep := `{"a":[` + strings.Repeat("1,", 1000) + strings.Repeat("[", 10_000_000) + `]}`
	if got := post(t, plain, site+"/deep", "application/json", deep); got != http.StatusForbidden {
		t.Errorf("/deep: status %d, want 403", got)
	}
	event := func(path string) func() (wafevent.Event, bool) {
		return func() (wafevent.Event, bool) {
			return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
		}
	}
	waitFor(t, "the events", func() bool {
		_, xss := event("/xss")()
		_, broken := event("/broken")()
		_, deep := event("/deep")()
		return xss && broken && deep
	})
	if ev, _ := event("/xss")(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
		return h.ID == 941100 && strings.Contains(h.Data, "found within ARGS:json.name:")
	}) {
		t.Errorf("the attack in a value was caught as %+v", ev.Rules)
	}
	for _, path := range []string{"/broken", "/deep"} {
		if ev, _ := event(path)(); ev.Status != http.StatusForbidden || !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
			return h.Message == "Failed to parse request body"
		}) {
			t.Errorf("%s was refused with %d by %+v", path, ev.Status, ev.Rules)
		}
	}
	for _, path := range []string{"/user-key-id", "/event", "/empty"} {
		if ev, ok := event(path)(); ok {
			t.Errorf("%s matched %+v", path, ev.Rules)
		}
	}
}

// An XML body is read as XML, SOAP's and any other +xml type included: a
// harmless one passes even at paranoia 4, where read as a form its angle
// brackets and quotes were an attack, XML-RPC's <param> among them, and an
// attack in its text or in an attribute is caught there. One that does not
// parse is refused, and an empty one is not read at all.
func TestProxyReadsXMLBodiesAsXML(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		// CRS takes no +xml type but SOAP's unless one is let in, as here.
		Profiles: []model.WAFProfile{{ID: "strict", Mode: "block", Paranoia: 4,
			Exclusions: []model.WAFExclusion{{Rule: "920420", Path: "/feed"}}}},
		Sites: []model.ProxySite{{ID: "api", Enabled: true, Hosts: []string{"api.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "strict"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://api.example.com"
	for _, c := range []struct {
		path, contentType, body string
		status                  int
	}{
		{"/order", "text/xml; charset=utf-8",
			`<?xml version="1.0" encoding="UTF-8"?><order id="17"><item sku="A100">Tea</item><note>Leave it at the door</note></order>`, http.StatusOK},
		{"/price", "application/soap+xml; charset=utf-8",
			`<Envelope><Body><GetPrice><Item>Tea</Item></GetPrice></Body></Envelope>`, http.StatusOK},
		{"/feed", "application/atom+xml", `<feed><title>News</title></feed>`, http.StatusOK},
		// HTML's empty elements are elements like any other.
		{"/xmlrpc", "text/xml",
			`<?xml version="1.0"?><methodCall><methodName>demo.echo</methodName><params><param><value><string>Tea</string></value></param></params></methodCall>`, http.StatusOK},
		{"/empty", "application/xml", "", http.StatusOK},
		{"/xss", "application/xml", `<note><body><![CDATA[<script>alert(1)</script>]]></body></note>`, http.StatusForbidden},
		{"/link", "application/xml", `<note><link href="javascript:alert(1)">Tea</link></note>`, http.StatusForbidden},
		{"/broken", "application/xml", `<<order>`, http.StatusForbidden},
	} {
		if got := post(t, plain, site+c.path, c.contentType, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	event := func(path string) func() (wafevent.Event, bool) {
		return func() (wafevent.Event, bool) {
			return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
		}
	}
	waitFor(t, "the events", func() bool {
		_, xss := event("/xss")()
		_, link := event("/link")()
		_, broken := event("/broken")()
		return xss && link && broken
	})
	if ev, _ := event("/xss")(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
		return h.ID == 941100 && strings.Contains(h.Data, "found within REQUEST_XML:/*:")
	}) {
		t.Errorf("the attack in the text was caught as %+v", ev.Rules)
	}
	if ev, _ := event("/link")(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
		return strings.Contains(h.Data, "found within REQUEST_XML://@*:")
	}) {
		t.Errorf("the attack in an attribute was caught as %+v", ev.Rules)
	}
	if ev, _ := event("/broken")(); ev.Status != http.StatusForbidden || !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
		return h.Message == "Failed to parse request body"
	}) {
		t.Errorf("the broken body was refused with %d by %+v", ev.Status, ev.Rules)
	}
	for _, path := range []string{"/order", "/price", "/feed", "/xmlrpc", "/empty"} {
		if ev, ok := event(path)(); ok {
			t.Errorf("%s matched %+v", path, ev.Rules)
		}
	}
}

// Coraza reads the first thousand arguments and drops the rest, so an
// attack behind a thousand harmless ones went unread. A request with more
// is refused, whether they come in the query string, a form or JSON, and
// one with a thousand passes.
func TestProxyRefusesArgumentsPastTheLimit(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Paranoia: 1}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://web.example.com"
	args := func(n int) string {
		a := make([]string, n)
		for i := range a {
			a[i] = fmt.Sprintf("a%d=1", i)
		}
		return strings.Join(a, "&")
	}
	const xss = "q=%3Cscript%3Ealert(1)%3C%2Fscript%3E"
	const form = "application/x-www-form-urlencoded"
	for _, c := range []struct {
		path, query, contentType, body string
		status                         int
	}{
		{"/search", args(1000), "", "", http.StatusOK},
		{"/login", "", form, args(1000), http.StatusOK},
		// The array's length is no argument, where Coraza 3.8.0 counted it.
		{"/api", "", "application/json", `{"a":[` + strings.Repeat("1,", 999) + `1]}`, http.StatusOK},
		{"/search-flood", args(1000) + "&" + xss, "", "", http.StatusForbidden},
		{"/login-flood", "", form, args(1000) + "&" + xss, http.StatusForbidden},
		{"/api-flood", "", "application/json", `{"a":[` + strings.Repeat("1,", 1000) + `"<script>alert(1)</script>"]}`, http.StatusForbidden},
	} {
		target := site + c.path
		if c.query != "" {
			target += "?" + c.query
		}
		if got := post(t, plain, target, c.contentType, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	event := func(path string) func() (wafevent.Event, bool) {
		return func() (wafevent.Event, bool) {
			return findEvent(out, func(ev wafevent.Event) bool {
				p, _, _ := strings.Cut(ev.URI, "?")
				return p == path
			})
		}
	}
	refused := map[string]string{
		"/search-flood": "Argument limit reached (GET/PATH args)",
		"/login-flood":  "Argument limit reached (POST args)",
		"/api-flood":    "Argument limit reached (POST args)",
	}
	waitFor(t, "the events", func() bool {
		for path := range refused {
			if _, ok := event(path)(); !ok {
				return false
			}
		}
		return true
	})
	for path, message := range refused {
		if ev, _ := event(path)(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.Message == message }) {
			t.Errorf("%s was refused by %+v", path, ev.Rules)
		}
	}
	for _, path := range []string{"/search", "/login", "/api"} {
		if ev, ok := event(path)(); ok {
			t.Errorf("%s matched %+v", path, ev.Rules)
		}
	}
}

// A multipart body that repeats a part's header or a parameter, quotes an
// extended filename or continues one in numbered pieces, or never closes is
// refused, since backends settle those differently. So is any body past the
// limit, an upload or padding in front of an attack the WAF would otherwise
// never read.
func TestProxyRefusesMalformedMultipartBodies(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Paranoia: 1, BodyLimitMB: 1}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://web.example.com"
	part := func(disposition, data string) string {
		return "--XyZ\r\nContent-Disposition: " + disposition + "\r\n\r\n" + data + "\r\n"
	}
	const end = "--XyZ--\r\n"
	photo := strings.Repeat("A", 10_000)
	for _, c := range []struct {
		path, body string
		status     int
	}{
		{"/upload", part(`form-data; name="f"; filename="photo.jpg"`, photo) + part(`form-data; name="title"`, "Holiday") + end, http.StatusOK},
		{"/long", part(`form-data; name="f"; filename="photo.jpg"`, strings.Repeat("A", 2<<20)) + end, http.StatusForbidden},
		{"/two-names", part(`form-data; name="f"; filename="photo.jpg"; filename="photo.png"`, photo) + end, http.StatusForbidden},
		{"/two-headers", "--XyZ\r\nContent-Disposition: form-data; name=\"f\"; filename=\"photo.jpg\"\r\n" +
			"Content-Disposition: form-data; name=\"f\"; filename=\"photo.png\"\r\n\r\n" + photo + "\r\n" + end, http.StatusForbidden},
		{"/quoted", part(`form-data; name="f"; filename*="utf-8''photo.jpg"`, photo) + end, http.StatusForbidden},
		// Werkzeug takes the piece, which Coraza 3.8.0 let through unread
		// beside the extended filename.
		{"/continued", part(`form-data; name="f"; filename="photo.jpg"; filename*=utf-8''photo.png; filename*0*=utf-8''photo.php`, photo) + end, http.StatusForbidden},
		{"/unclosed", part(`form-data; name="f"; filename="photo.jpg"`, photo), http.StatusForbidden},
	} {
		if got := post(t, plain, site+c.path, "multipart/form-data; boundary=XyZ", c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	event := func(path string) func() (wafevent.Event, bool) {
		return func() (wafevent.Event, bool) {
			return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
		}
	}
	padded := `{"pad": "` + strings.Repeat("A", 2<<20) + `", "q": "' or 1=1 union select password from users--"}`
	if got := post(t, plain, site+"/padded", "application/json", padded); got != http.StatusForbidden {
		t.Errorf("/padded: status %d, want 403", got)
	}
	refused := []string{"/two-names", "/two-headers", "/quoted", "/continued", "/unclosed", "/long", "/padded"}
	waitFor(t, "the events", func() bool {
		for _, path := range refused {
			if _, ok := event(path)(); !ok {
				return false
			}
		}
		return true
	})
	for path, data := range map[string]string{
		"/two-names":   "MULTIPART_DUPLICATE_PART_HEADER=1",
		"/two-headers": "MULTIPART_DUPLICATE_PART_HEADER=1",
		"/quoted":      "MULTIPART_INVALID_QUOTING=1",
		"/continued":   "MULTIPART_DUPLICATE_PART_HEADER=0, MULTIPART_INVALID_QUOTING=0",
		"/unclosed":    "MULTIPART_DUPLICATE_PART_HEADER=0, MULTIPART_INVALID_QUOTING=0",
	} {
		if ev, _ := event(path)(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
			return h.Message == "Multipart request body failed strict validation" && strings.Contains(h.Data, data)
		}) {
			t.Errorf("%s was refused by %+v", path, ev.Rules)
		}
	}
	for _, path := range []string{"/long", "/padded"} {
		if ev, _ := event(path)(); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool {
			return h.Message == "Request body larger than the limit"
		}) {
			t.Errorf("%s was refused by %+v", path, ev.Rules)
		}
	}
	if ev, ok := event("/upload")(); ok {
		t.Errorf("/upload matched %+v", ev.Rules)
	}
}

// A request may name only the charsets CRS allows, however its header
// spells the parameter, and a multipart body only those too, however many
// times it names one. CRS 4.29 read the parameter in lower case alone and
// the first _charset_ alone, so CHARSET=utf-7, or a second _charset_, went
// through.
func TestProxyHoldsRequestsToTheAllowedCharsets(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Paranoia: 1}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://web.example.com"
	const form, multipart = "application/x-www-form-urlencoded", "multipart/form-data; boundary=XyZ"
	part := func(name, value string) string {
		return "--XyZ\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
	}
	const end = "--XyZ--\r\n"
	for _, c := range []struct {
		path, contentType, body string
		status                  int
	}{
		{"/form", form + "; charset=utf-8", "q=hello", http.StatusOK},
		{"/form-utf7", form + "; charset=utf-7", "q=hello", http.StatusForbidden},
		{"/form-shouted", form + "; CHARSET=utf-7", "q=hello", http.StatusForbidden},
		{"/upload", multipart, part("_charset_", "utf-8") + part("q", "hello") + end, http.StatusOK},
		{"/upload-second", multipart, part("_charset_", "utf-8") + part("_charset_", "utf-7") + part("q", "hello") + end, http.StatusForbidden},
	} {
		if got := post(t, plain, site+c.path, c.contentType, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	refusedBy := map[string]int{"/form-utf7": 920480, "/form-shouted": 920480, "/upload-second": 922100}
	event := func(path string) (wafevent.Event, bool) {
		return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
	}
	waitFor(t, "the events", func() bool {
		for path := range refusedBy {
			if _, ok := event(path); !ok {
				return false
			}
		}
		return true
	})
	for path, id := range refusedBy {
		if ev, _ := event(path); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID == id }) {
			t.Errorf("%s was refused by %+v, want %d", path, ev.Rules, id)
		}
	}
}

// A command in the path is read as one in an argument is. CRS 4.29's
// command rules never read the path, so a site that hands a path segment
// to a shell was open at every paranoia level.
func TestProxyReadsCommandsInThePath(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Paranoia: 1}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://web.example.com"
	refused := []string{"/files?f=x%26%26whoami", "/files/x%26%26whoami", "/files/x%7Cuname%20-a", "/files/%24(id)"}
	if status, _ := fetch(t, plain, "", site+"/files/report.pdf"); status != http.StatusOK {
		t.Errorf("/files/report.pdf: status %d, want 200", status)
	}
	for _, uri := range refused {
		if status, _ := fetch(t, plain, "", site+uri); status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", uri, status)
		}
	}
	event := func(uri string) (wafevent.Event, bool) {
		return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == uri })
	}
	waitFor(t, "the events", func() bool {
		for _, uri := range refused {
			if _, ok := event(uri); !ok {
				return false
			}
		}
		return true
	})
	for _, uri := range refused {
		if ev, _ := event(uri); !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID/1000 == 932 }) {
			t.Errorf("%s was refused by %+v, want a command rule", uri, ev.Rules)
		}
	}
}

// A profile that passes larger bodies lets an upload past the limit
// through with its first part inspected, and a body cut at the limit is
// not refused as one that does not parse. That padding hides what comes
// after it is the price, and what a request carries up to the limit is
// still read.
func TestAProfileThatPassesLargeBodiesLetsThemThrough(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Paranoia: 1, BodyLimitMB: 1, PassLargeBodies: true}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const site = "http://web.example.com"
	upload := "--XyZ\r\nContent-Disposition: form-data; name=\"f\"; filename=\"photo.jpg\"\r\n\r\n" +
		strings.Repeat("A", 2<<20) + "\r\n--XyZ--\r\n"
	padded := `{"pad": "` + strings.Repeat("A", 2<<20) + `", "q": "' or 1=1 union select password from users--"}`
	attack := `{"q": "' or 1=1 union select password from users--"}`
	for _, c := range []struct {
		path, kind, body string
		status           int
	}{
		{"/upload", "multipart/form-data; boundary=XyZ", upload, http.StatusOK},
		{"/padded", "application/json", padded, http.StatusOK},
		{"/attack", "application/json", attack, http.StatusForbidden},
	} {
		if got := post(t, plain, site+c.path, c.kind, c.body); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
	}
	event := func(path string) func() (wafevent.Event, bool) {
		return func() (wafevent.Event, bool) {
			return findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
		}
	}
	waitFor(t, "the attack's event", func() bool { _, ok := event("/attack")(); return ok })
	for _, path := range []string{"/upload", "/padded"} {
		if ev, ok := event(path)(); ok {
			t.Errorf("%s matched %+v", path, ev.Rules)
		}
	}
}

// coraza-caddy keeps a WAF, and the logger it was built with, across
// reloads while the WAF's directives stay the same. A proxy that built its
// WAFs before their logger was left out rebuilds them at the reload that
// leaves it out, so the rule-by-rule lines, which carry the whole URI,
// stop there.
func TestAReloadLeavesTheWAFsOwnLoggerOut(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	missing := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(missing.Close)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "missing", Upstreams: []model.ProxyUpstream{{Address: missing.Listener.Addr().String()}}}},
		Profiles: []model.WAFProfile{{ID: "block", Mode: "block"}},
		Sites: []model.ProxySite{{ID: "guard", Enabled: true, Hosts: []string{"guard.example.com"}, Pool: "missing",
			PlainHTTP: true, WAF: "block"}},
	}
	var current string
	excluded := regexp.MustCompile(`,\s*"exclude":\s*\[\s*"http\.handlers\.waf"\s*\]`)
	older := func(conf string) string {
		current = conf
		return excluded.ReplaceAllString(strings.ReplaceAll(conf, `SecDebugLogLevel 0\n`, ""), "")
	}
	s := runSidecarWith(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}}, older)
	plain, out, path := s.plain, s.out, s.path
	if b, err := os.ReadFile(path); err != nil || current == "" || strings.Contains(string(b), "http.handlers.waf") {
		t.Fatalf("the older configuration still leaves the WAF's logger out (%v)", err)
	}
	const attack = "?q=%3Cscript%3Ealert(1)%3C%2Fscript%3E"
	ruleLines := func(marker string) int {
		n := 0
		for l := range strings.SplitSeq(out.String(), "\n") {
			if strings.Contains(l, `"logger":"http.handlers.waf"`) && strings.Contains(l, marker) {
				n++
			}
		}
		return n
	}
	fetch(t, plain, "", "http://guard.example.com/before"+attack)
	waitFor(t, "the WAF's own lines", func() bool { return ruleLines("/before") > 0 })

	if err := os.WriteFile(path, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	reloadSidecar(t, bin, path)
	fetch(t, plain, "", "http://guard.example.com/after"+attack)
	waitFor(t, "the audit line", func() bool {
		ev, ok := findEvent(out, func(ev wafevent.Event) bool { return strings.HasPrefix(ev.URI, "/after") })
		return ok && ev.Verdict == wafevent.VerdictBlocked
	})
	if n := ruleLines("/after"); n != 0 {
		t.Errorf("after the reload the WAF wrote %d lines of its own:\n%s", n, out)
	}
}

// coraza-caddy keeps a WAF across reloads while its directives read the
// same, whatever the files they include hold. A set that changed changes
// its hash in the directives, and the reload an apply does reads it.
func TestAReloadReadsAChangedSet(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "jellyfin", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{{ID: "watch", Mode: "block", Paranoia: 4, Applications: []string{"jellyfin"}}},
		Sites: []model.ProxySite{{ID: "watch", Enabled: true, Hosts: []string{"watch.example.com"}, Pool: "jellyfin",
			PlainHTTP: true, WAF: "watch"}},
	}
	raw, err := crsPlugins.ReadFile("crs/plugins/jellyfin-before.conf")
	if err != nil {
		t.Fatal(err)
	}
	now, empty := fmt.Sprintf("%x", sha256.Sum256(raw)), fmt.Sprintf("%x", sha256.Sum256(nil))
	include := regexp.MustCompile(`Include ([^\\"]+/jellyfin-before\.conf)`)
	var current, set string
	// The set as an older release wrote it, empty, and the directives that
	// release rendered for it.
	older := func(conf string) string {
		current = conf
		m := include.FindStringSubmatch(conf)
		if m == nil || !strings.Contains(conf, now) {
			t.Fatalf("the directives do not name the set and its hash:\n%s", conf)
		}
		set = m[1]
		if err := os.WriteFile(set, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(conf, now, empty)
	}
	s := runSidecarWith(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}}, older)
	g := jf{vw{rand.New(rand.NewPCG(5, 6))}}
	r := jfRequest{"GET", "/UserViews?userId=" + g.uuid(), "", "", false, g.client(jfClients - 1)}
	if status := jfSend(t, s.plain, r); status != http.StatusForbidden {
		t.Fatalf("with the set empty the client got %d, want 403", status)
	}

	// The new set on disk and the old directives: the WAF stays as it was.
	if err := os.WriteFile(set, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	reloadSidecar(t, bin, s.path)
	if status := jfSend(t, s.plain, r); status != http.StatusForbidden {
		t.Fatalf("a reload with the same directives read the new set (%d): coraza-caddy no longer keeps its WAFs", status)
	}

	// The directives the new set renders.
	if err := os.WriteFile(s.path, []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	reloadSidecar(t, bin, s.path)
	if status := jfSend(t, s.plain, r); status != http.StatusOK {
		t.Errorf("after the reload the client got %d, want 200\n%s", status, events(s.out))
	}
}

// cloudflareCookie is a cookie Cloudflare sets, with a random value of its
// shape: base64url, the time it was issued and a version, joined by dashes,
// and more base64url in parts joined by dots.
func cloudflareCookie(r *rand.Rand) string {
	const b64url = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	random := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = b64url[r.IntN(len(b64url))]
		}
		return string(b)
	}
	parts := func(n int) string {
		var p []string
		for range n {
			p = append(p, random(10+r.IntN(60)))
		}
		return strings.Join(p, ".")
	}
	issued := strconv.FormatInt(1.7e9+r.Int64N(1e8), 10)
	switch r.IntN(3) {
	case 0:
		return "cf_clearance=" + random(43) + "-" + issued + "-1.2.1.1-" + parts(2+r.IntN(6))
	case 1:
		return "__cf_bm=" + random(43) + "-" + issued + "-1.0.1.1-" + parts(1+r.IntN(4))
	default:
		return "_cfuvid=" + random(43) + "-" + issued + strconv.Itoa(r.IntN(1000)) + "-0.0.1.1-604800000"
	}
}

// A browser sends Cloudflare's cookies to every name under a zone once one
// name in it has been through Cloudflare, and their random values trip the
// SQL rules now and then, a comment in one of eleven. No rule reads them at
// paranoia 4; any other cookie is read as before, one with no name too,
// which Coraza 3.8.0 dropped and Node's cookie package does not.
func TestCloudflaresCookiesAreNotRead(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "shop", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{{ID: "block", Mode: "block", Paranoia: 4}},
		Sites: []model.ProxySite{{ID: "shop", Enabled: true, Hosts: []string{"shop.example.com"}, Pool: "shop",
			PlainHTTP: true, WAF: "block"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, plain)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	send := func(path, cookie string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://shop.example.com"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "text/html")
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:151.0) Gecko/20100101 Firefox/151.0")
		req.Header.Set("Cookie", cookie)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	r := rand.New(rand.NewPCG(3, 4))
	for i := range 1000 {
		if status := send("/cf/"+strconv.Itoa(i), cloudflareCookie(r)+"; theme=dark"); status != http.StatusOK {
			t.Errorf("request %d: %d", i, status)
		}
	}
	for _, name := range []string{"session", "cf_clearance_copy", ""} {
		if status := send("/other/"+name, name+"=1' or '1'='1"); status != http.StatusForbidden {
			t.Errorf("an injection in %q: %d, want 403", name, status)
		}
	}
	waitFor(t, "the injections' events", func() bool {
		_, ok := findEvent(out, func(ev wafevent.Event) bool { return ev.URI == "/other/cf_clearance_copy" })
		return ok
	})
	if ev, ok := findEvent(out, func(ev wafevent.Event) bool { return strings.HasPrefix(ev.URI, "/cf/") }); ok {
		t.Errorf("a Cloudflare cookie matched: %s %+v", ev.URI, ev.Rules)
	}
}

// Coraza appends a request's removed targets to an exception list it
// shares between requests, where the list has room: 942421's has twelve
// entries and room for four. Two exclusions taking different cookies off
// it, hit at once, overwrote each other, and a request lost its own.
// third_party/coraza copies the list first, until upstream's #1723 is fixed.
func TestConcurrentExclusionsKeepTheirOwnTargets(t *testing.T) {
	t.Parallel()
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "shop", Upstreams: []model.ProxyUpstream{{Address: answer(t, "{}")}}}},
		Profiles: []model.WAFProfile{{ID: "detect", Mode: "detect", Paranoia: 4, Exclusions: []model.WAFExclusion{
			{Rule: "942421", Path: "/alpha", Target: "REQUEST_COOKIES:alpha"},
			{Rule: "942421", Path: "/beta", Target: "REQUEST_COOKIES:beta"},
		}}},
		Sites: []model.ProxySite{{ID: "shop", Enabled: true, Hosts: []string{"shop.example.com"}, Pool: "shop",
			PlainHTTP: true, WAF: "detect"}},
	}
	plain, _, out := runSidecar(t, sidecar(t), cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	tr := &http.Transport{MaxIdleConnsPerHost: 16, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, plain)
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	const n = 8000
	next := make(chan int)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for i := range next {
				name := []string{"alpha", "beta"}[i%2]
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://shop.example.com/"+name+"/"+strconv.Itoa(i), nil)
				if err != nil {
					t.Error(err)
					return
				}
				req.Header.Set("Accept", "text/html")
				req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:151.0) Gecko/20100101 Firefox/151.0")
				req.Header.Set("Cookie", name+"=a-b-c-d")
				resp, err := client.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				_ = resp.Body.Close()
			}
		})
	}
	for i := range n {
		next <- i
	}
	close(next)
	wg.Wait()
	// A cookie no exclusion takes off marks the end of the log.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://shop.example.com/end", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", "gamma=a-b-c-d")
	if resp, err := client.Do(req); err != nil {
		t.Fatal(err)
	} else {
		_ = resp.Body.Close()
	}
	waitFor(t, "the last event", func() bool {
		_, ok := findEvent(out, func(ev wafevent.Event) bool { return ev.URI == "/end" })
		return ok
	})
	lost := 0
	for line := range strings.SplitSeq(out.String(), "\n") {
		if ev, ok := wafevent.Parse(line); ok && (strings.HasPrefix(ev.URI, "/alpha/") || strings.HasPrefix(ev.URI, "/beta/")) {
			lost++
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d requests matched a cookie their exclusion takes off", lost, n)
	}
}

// A player whose buffer is full stops reading, and holds its download open
// for as long as it stays. A reload leaves the download running, past the
// grace period too; a stop waits for one only the grace period.
func TestAStalledDownloadHoldsAStopOnlyTheGracePeriod(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	grace, err := time.ParseDuration(proxyGracePeriod)
	if err != nil {
		t.Fatal(err)
	}
	var sent atomic.Int64
	film := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 32<<10)
		for r.Context().Err() == nil {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(film.Close)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "films", Upstreams: []model.ProxyUpstream{{Address: film.Listener.Addr().String()}}}},
		Sites: []model.ProxySite{{ID: "watch", Enabled: true, Hosts: []string{"watch.example.com"}, Pool: "films",
			PlainHTTP: true}},
	}
	s := runSidecarWith(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}}, nil)

	// download starts one and waits until every buffer between the film
	// and the client is full.
	download := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", s.plain)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if _, err := io.WriteString(conn, "GET /film HTTP/1.1\r\nHost: watch.example.com\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		stalled(t, &sent)
		return conn
	}
	before := download()

	reloadSidecar(t, bin, s.path)
	time.Sleep(grace + time.Second)
	// More than the kernel holds for the client, so some of it left the
	// film after the reload.
	_ = before.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.CopyN(io.Discard, before, 64<<20); err != nil {
		t.Fatalf("the reload cut the download: %v\n%s", err, s.out)
	}
	stalled(t, &sent)
	// A stop waits only for the servers of the configuration it stops.
	download()

	start := time.Now()
	if err := s.proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.exited:
	case <-time.After(grace + 10*time.Second):
		t.Fatalf("the proxy still ran %s after SIGTERM", time.Since(start).Round(time.Second))
	}
	if took := time.Since(start); took < grace/2 {
		t.Errorf("the proxy stopped in %s: the download did not hold it", took.Round(100*time.Millisecond))
	}
	if s.err != nil {
		t.Errorf("the proxy exited %v, want 0", s.err)
	}
}

// stalled waits until sent, what a server has written, stops growing.
func stalled(t *testing.T, sent *atomic.Int64) {
	t.Helper()
	last := int64(-1)
	waitFor(t, "the download to stall", func() bool {
		time.Sleep(300 * time.Millisecond)
		n := sent.Load()
		defer func() { last = n }()
		return n > 0 && n == last
	})
}

// wafEvent waits for the sidecar to write the WAF's entry for site, which
// comes once the response is out.
func wafEvent(t *testing.T, out *lockedBuffer, site string) wafevent.Event {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ev, ok := findEvent(out, func(ev wafevent.Event) bool { return ev.Site == site }); ok {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("no WAF event for %s:\n%s", site, out)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// findEvent is the first WAF entry the sidecar wrote that match takes.
func findEvent(out *lockedBuffer, match func(wafevent.Event) bool) (wafevent.Event, bool) {
	for line := range strings.SplitSeq(out.String(), "\n") {
		if ev, ok := wafevent.Parse(line); ok && match(ev) {
			return ev, true
		}
	}
	return wafevent.Event{}, false
}

// waitFor waits up to ten seconds for ok.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
	}
}

// sidecar is the built proxy, which the tests that run it need.
func sidecar(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs("../../bin/ostiole-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		testenv.Unavailable(t, "no bin/ostiole-proxy; run `task build:proxy`")
	}
	return bin
}

// runSidecar renders cfg into a directory of its own, with a self-signed
// pair for each certificate a site names, covering the hosts pairs lists
// for it, and runs the sidecar there until the test ends. solver, when
// set, stands in for the http-01 solver. It returns the addresses of the
// plain and secure servers, and what the sidecar writes.
func runSidecar(t *testing.T, bin string, cfg *model.Config, solver string, pairs map[string][]string) (plain, secure string, out *lockedBuffer) {
	t.Helper()
	s := runSidecarWith(t, bin, cfg, solver, pairs, nil)
	return s.plain, s.secure, s.out
}

// runningSidecar is a sidecar runSidecarWith started.
type runningSidecar struct {
	plain, secure string
	out           *lockedBuffer
	path          string // the configuration
	proc          *os.Process
	exited        <-chan struct{}
	err           error // how it exited, once exited is closed
}

// runSidecarWith is runSidecar with the configuration edited before the
// sidecar reads it, when edit is set.
func runSidecarWith(t *testing.T, bin string, cfg *model.Config, solver string, pairs map[string][]string,
	edit func(conf string) string,
) *runningSidecar {
	t.Helper()
	dir := t.TempDir()
	files, err := (&Proxy{Dir: dir}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range certIDs(files[proxyCertList]) {
		writeTestPair(t, filepath.Join(dir, proxyCertsDir, id), pairs[id]...)
	}
	// The exclusion sets, which an apply writes beside the configuration.
	for name, content := range crsFiles() {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The admin socket and the solver are at fixed places on a router.
	conf := strings.Replace(files[proxyConfName], "unix/"+ProxyAdminSocket, "unix/"+filepath.Join(dir, "admin.sock"), 1)
	if solver != "" {
		conf = strings.Replace(conf, `"`+challengeUpstream+`"`, `"`+solver+`"`, 1)
	}
	if edit != nil {
		conf = edit(conf)
	}
	path := filepath.Join(dir, proxyConfName)
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), bin, "run", "--config", path)
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	run := &runningSidecar{out: out, path: path, proc: cmd.Process, exited: exited}
	go func() {
		run.err = cmd.Wait()
		close(exited)
	}()
	// The test's context is cancelled first, which kills the sidecar; the
	// directory goes only once it has exited.
	t.Cleanup(func() { <-exited })

	pr := cfg.Services.Proxy
	plain := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(pr.HTTPPortOr())))
	secure := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(pr.HTTPSPortOr())))
	deadline := time.Now().Add(30 * time.Second)
	for _, addr := range []string{plain, secure} {
		for {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err == nil {
				_ = conn.Close()
				break
			}
			select {
			case <-exited:
				t.Fatalf("the sidecar exited:\n%s", out)
			default:
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				<-exited
				t.Fatalf("nothing listens on %s: %v\n%s", addr, err, out)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	run.plain, run.secure = plain, secure
	return run
}

// reloadSidecar loads the configuration at path into the sidecar
// running it, as the unit's reload does.
func reloadSidecar(t *testing.T, bin, path string) {
	t.Helper()
	admin := "unix/" + filepath.Join(filepath.Dir(path), "admin.sock")
	if b, err := exec.CommandContext(t.Context(), bin, "reload", "--config", path, "--address", admin, "--force").CombinedOutput(); err != nil {
		t.Fatalf("reload: %v\n%s", err, b)
	}
}

// lockedBuffer is the sidecar's output, which a test reads while the
// sidecar writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ports are the ones freePort gave out, which it gives no other test.
var (
	portsMu sync.Mutex
	ports   = map[uint16]bool{}
)

// freePort is a TCP port for a sidecar to listen on. A port is free when it
// is chosen, not when the sidecar binds it, and in between another test's
// server listening on port 0 could be given it: so it comes from below the
// kernel's ephemeral range, which starts at 32768, where no such server
// lands.
func freePort(t *testing.T) uint16 {
	t.Helper()
	portsMu.Lock()
	defer portsMu.Unlock()
	for range 1000 {
		port := uint16(20000 + rand.IntN(12000))
		if ports[port] {
			continue
		}
		l, err := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(int(port))))
		if err != nil {
			continue
		}
		_ = l.Close()
		ports[port] = true
		return port
	}
	t.Fatal("no free port")
	return 0
}

// answer is an HTTP server that says body to every request. It returns
// the server's address.
func answer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// fetch requests url from the proxy at addr, naming sni in the handshake
// when url is https, and returns the status and the body. Redirects are
// returned, not followed.
func fetch(t *testing.T, addr, sni, url string) (int, string) {
	t.Helper()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{ServerName: sni, InsecureSkipVerify: true},
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// post sends body to url over plain HTTP at addr, as contentType when set
// and accepting JSON, as a browser's script does, and returns the status.
func post(t *testing.T, addr, url, contentType, body string) int {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// handshake opens TLS to addr naming sni, or nothing when sni is empty, as
// a browser does for an address. It returns the names on the certificate
// it was given.
func handshake(addr, sni string) ([]string, error) {
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config:    &tls.Config{ServerName: sni, InsecureSkipVerify: true},
	}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	leaf := conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
	names := slices.Clone(leaf.DNSNames)
	for _, ip := range leaf.IPAddresses {
		names = append(names, ip.String())
	}
	return names, nil
}

// refused: the proxy answered the handshake with an alert, rather than a
// certificate or something that is not TLS.
func refused(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "remote error"
}
