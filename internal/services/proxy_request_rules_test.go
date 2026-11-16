package services

import (
	"bufio"
	"context"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/wafevent"
)

// requestTrait is one shape of a request built to break a backend's own
// parser, as the php2shell and Broccoli2Shell write-ups send them, and the
// rule that refuses it.
type requestTrait struct {
	name string
	rule int
	req  func(t *testing.T, url string) *http.Request
}

func requestTraits(maxCookies, maxParts, maxDepth, maxLength int) []requestTrait {
	urlencoded := func(header ...string) func(t *testing.T, url string) *http.Request {
		return func(t *testing.T, url string) *http.Request {
			return newRequest(t, url, "application/x-www-form-urlencoded", "title=Holiday", header...)
		}
	}
	form := func(parts ...string) func(t *testing.T, url string) *http.Request {
		return func(t *testing.T, url string) *http.Request {
			return newRequest(t, url, "multipart/form-data; boundary=XyZ", mpBody("XyZ", parts...))
		}
	}
	query := func(name string) func(t *testing.T, url string) *http.Request {
		return func(t *testing.T, url string) *http.Request {
			return newRequest(t, url+"?"+strings.NewReplacer("[", "%5B", "]", "%5D").Replace(name)+"=1", "", "")
		}
	}
	nested := func(n int) string { return "a" + strings.Repeat("[b]", n) }
	long := func(n int) string { return strings.Repeat("n", n) }
	files := func(n int) []string { return slices.Repeat([]string{mpFile("f[]")}, n) }
	return []requestTrait{
		{"plain-form", 0, form(mpField("title", "Holiday"), mpFile("photo"))},
		{"progress", 19001, form(mpField("PHP_SESSION_UPLOAD_PROGRESS", "x"), mpFile("photo"))},
		{"cookies-at-cap", 0, urlencoded("Cookie", cookies(maxCookies, "c%d=1"))},
		{"cookies-past-cap", 19002, urlencoded("Cookie", cookies(maxCookies+1, "c%d=1"))},
		{"cookies-empty", 19002, urlencoded("Cookie", cookies(maxCookies+1, "a="))},
		{"parts-at-cap", 0, form(files(maxParts)...)},
		{"parts-past-cap", 19003, form(files(maxParts + 1)...)},
		{"depth-at-cap", 0, query(nested(maxDepth))},
		{"depth-past-cap-query", 19004, query(nested(maxDepth + 1))},
		{"depth-past-cap-part", 19004, form(mpField(nested(maxDepth+1), "1"))},
		{"depth-past-cap-cookie", 19004, urlencoded("Cookie", nested(maxDepth+1)+"=1")},
		{"length-at-cap", 0, query(long(maxLength))},
		{"length-past-cap-query", 19005, query(long(maxLength + 1))},
		{"length-past-cap-file", 19005, form(mpFile(long(maxLength + 1)))},
		{"length-past-cap-cookie", 19005, urlencoded("Cookie", long(maxLength+1)+"=1")},
		{"gzip", 19007, urlencoded("Content-Encoding", "gzip")},
		{"brotli-after-identity", 19007, urlencoded("Content-Encoding", "identity, br")},
		{"empty-encoding", 19007, urlencoded("Content-Encoding", "")},
		{"two-content-types", 19010, urlencoded("Content-Type", "application/x-www-form-urlencoded")},
	}
}

// Requests shaped like the php2shell and Broccoli2Shell write-ups are
// refused in block mode and logged in detect mode, each trait by its own
// rule, and the caps a profile sets take the place of the defaults. A
// profile can refuse multipart bodies but on its upload paths.
func TestProxyRefusesRequestsBuiltToBreakAParser(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{
			{ID: "block", Mode: "block"},
			{ID: "detect"},
			{ID: "tight", Mode: "block", MaxCookies: 5, MaxParts: 3, MaxNameDepth: 2, MaxNameLength: 16,
				RefuseMultipart: true, Exclusions: []model.WAFExclusion{{Rule: "19008", Path: "/upload"}}},
		},
		Sites: []model.ProxySite{
			{ID: "block", Enabled: true, Hosts: []string{"block.example.com"}, Pool: "app", PlainHTTP: true, WAF: "block"},
			{ID: "detect", Enabled: true, Hosts: []string{"detect.example.com"}, Pool: "app", PlainHTTP: true, WAF: "detect"},
			{ID: "tight", Enabled: true, Hosts: []string{"tight.example.com"}, Pool: "app", PlainHTTP: true, WAF: "tight"},
		},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	type sent struct {
		site, path string
		rule       int
	}
	var all []sent
	check := func(site, path string, req *http.Request, rule int, blocks bool) {
		t.Helper()
		want := http.StatusOK
		if rule != 0 && blocks {
			want = http.StatusForbidden
		}
		if got := send(t, plain, req); got != want {
			time.Sleep(300 * time.Millisecond)
			ev, _ := findEvent(out, func(ev wafevent.Event) bool { p, _, _ := strings.Cut(ev.URI, "?"); return ev.Site == site && p == path })
			t.Errorf("%s%s: status %d, want %d, refused by %+v", site, path, got, want, ev.Rules)
		}
		all = append(all, sent{site, path, rule})
	}
	for _, site := range []string{"block", "detect"} {
		for _, tr := range requestTraits(model.DefaultMaxCookies, model.DefaultMaxParts, model.DefaultMaxNameDepth, model.DefaultMaxNameLength) {
			path := "/t/" + tr.name
			check(site, path, tr.req(t, "http://"+site+".example.com"+path), tr.rule, site == "block")
		}
	}
	for _, tr := range requestTraits(5, 3, 2, 16) {
		path := "/upload/" + tr.name
		check("tight", path, tr.req(t, "http://tight.example.com"+path), tr.rule, true)
	}
	refused := newRequest(t, "http://tight.example.com/profile", "multipart/form-data; boundary=XyZ", mpBody("XyZ", mpField("title", "Holiday")))
	check("tight", "/profile", refused, 19008, true)

	// Every trait of the write-up's request at once.
	writeUp := mpBody("XyZ", append(slices.Repeat([]string{mpFile("a" + strings.Repeat("[x]", 40))}, 300),
		mpField("PHP_SESSION_UPLOAD_PROGRESS", "1"), mpField("1", "x"), mpField("1", "y"), mpField(strings.Repeat("A", 5000), "z"))...)
	req := newRequest(t, "http://block.example.com/write-up", "multipart/form-data; boundary=XyZ", writeUp, "Cookie", cookies(1100, "a="))
	if got := send(t, plain, req); got != http.StatusForbidden {
		t.Errorf("the write-up's request: status %d, want 403", got)
	}

	event := func(site, path string) (wafevent.Event, bool) {
		return findEvent(out, func(ev wafevent.Event) bool {
			p, _, _ := strings.Cut(ev.URI, "?")
			return ev.Site == site && p == path
		})
	}
	waitFor(t, "the events", func() bool {
		for _, s := range all {
			if _, ok := event(s.site, s.path); s.rule != 0 && !ok {
				return false
			}
		}
		return true
	})
	time.Sleep(200 * time.Millisecond) // the passing requests' lines, had there been any
	for _, s := range all {
		ev, _ := event(s.site, s.path)
		var ours []int
		for _, h := range ev.Rules {
			if h.ID > 19000 && h.ID < 19100 {
				ours = append(ours, h.ID)
			}
		}
		switch {
		case s.rule == 0 && len(ours) > 0:
			t.Errorf("%s%s matched %v", s.site, s.path, ours)
		case s.rule != 0 && !slices.Contains(ours, s.rule):
			t.Errorf("%s%s: matched %+v, want %d", s.site, s.path, ev.Rules, s.rule)
		}
	}
}

// A multipart/form-data Content-Type passes only in the shape PHP and Go
// read alike, which every common client sends.
func TestProxyTakesOnlyPlainMultipartContentTypes(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block"}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	goWriter := multipart.NewWriter(nil).FormDataContentType()
	_, goBoundary, _ := strings.Cut(goWriter, "boundary=")
	for i, c := range []struct {
		contentType, boundary string
		plain                 bool
	}{
		{"multipart/form-data; boundary=----WebKitFormBoundary7MA4YWxkTrZu0gW", "----WebKitFormBoundary7MA4YWxkTrZu0gW", true},
		{"multipart/form-data; boundary=----geckoformboundary9c3a5f0e2b7d4a1c8e6f0b3d5a7c9e1f", "----geckoformboundary9c3a5f0e2b7d4a1c8e6f0b3d5a7c9e1f", true},
		{"multipart/form-data; boundary=---------------------------974767299852498929531610575", "---------------------------974767299852498929531610575", true},
		{"multipart/form-data; boundary=------------------------d74496d66958873e", "------------------------d74496d66958873e", true},
		{goWriter, goBoundary, true},
		{"multipart/form-data; boundary=a3f8c2e1d4b5a6f7e8d9c0b1a2f3e4d5", "a3f8c2e1d4b5a6f7e8d9c0b1a2f3e4d5", true},
		{"multipart/form-data; boundary=----formdata-undici-087613547382", "----formdata-undici-087613547382", true},
		{"multipart/form-data; boundary=--------------------------515890814546601021194782", "--------------------------515890814546601021194782", true},
		{`multipart/form-data; boundary="3b1e5c2a-9f0d-4c8e-a1b2-6d7e8f9a0b1c"`, "3b1e5c2a-9f0d-4c8e-a1b2-6d7e8f9a0b1c", true},
		{"multipart/form-data; boundary=3b1e5c2a-9f0d-4c8e-a1b2-6d7e8f9a0b1c", "3b1e5c2a-9f0d-4c8e-a1b2-6d7e8f9a0b1c", true},
		{"multipart/form-data; boundary=dart-http-boundary-Qx+v_5.kR0bN-yzW3aT9sLm1hJ2pD4fG6cV8", "dart-http-boundary-Qx+v_5.kR0bN-yzW3aT9sLm1hJ2pD4fG6cV8", true},
		{"multipart/form-data; charset=utf-8; boundary=XyZ", "XyZ", true},
		{"multipart/form-data;boundary=XyZ; charset=UTF-8", "XyZ", true},
		{`multipart/form-data; boundary="gc0p4Jq0M2Yt08jU534c0p"`, "gc0p4Jq0M2Yt08jU534c0p", true},
		{"Multipart/Form-Data; boundary=XyZ", "XyZ", true},
		{"multipart/form-data, boundary=XyZ", "XyZ", false},
		{"multipart/form-data boundary=XyZ", "XyZ", false},
		{"multipart/form-data; xboundary=XyZ", "XyZ", false},
		{"multipart/form-data; boundary=XyZ; name=x", "XyZ", false},
		{"multipart/form-data; boundary=Xy,Z", "Xy", false},
		{`multipart/form-data; boundary="Xy\Z"`, "XyZ", false},
		{"multipart/form-data; boundary=XyZ ; charset=utf-8", "XyZ", false},
		{"multipart/form-data; BOUNDARY=XyZ", "XyZ", false},
		{"multipart/form-data; boundary=XyZ; boundary=AbC", "XyZ", false},
		{"multipart/form-data", "XyZ", false},
		{"multipart/form-data; boundary=XyZ;", "XyZ", false},
		{" multipart/form-data; boundary=XyZ", "XyZ", false},
		{"multipart/form-data; boundary=" + strings.Repeat("x", 71), strings.Repeat("x", 71), false},
	} {
		path := "/ct/" + strconv.Itoa(i)
		req := newRequest(t, "http://web.example.com"+path, c.contentType, mpBody(c.boundary, mpField("title", "Holiday")))
		want := http.StatusOK
		if !c.plain {
			want = http.StatusForbidden
		}
		if got := send(t, plain, req); got != want {
			time.Sleep(300 * time.Millisecond)
			ev, _ := findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
			t.Errorf("%q: status %d, want %d, refused by %+v", c.contentType, got, want, ev.Rules)
			continue
		}
		if c.plain {
			continue
		}
		var ev wafevent.Event
		waitFor(t, "the event for "+c.contentType, func() bool {
			var ok bool
			ev, ok = findEvent(out, func(ev wafevent.Event) bool { return ev.URI == path })
			return ok
		})
		if !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID == 19006 }) {
			t.Errorf("%q was refused by %+v", c.contentType, ev.Rules)
		}
	}
}

// A path exclusion lifts a request rule on its path alone, in either
// phase, and a global exclusion lifts one everywhere. A rule holds where
// the CRS rule beside it is lifted, as the wordpress and nextcloud sets do
// 920450 on some paths, or scores under a raised threshold: CRS refuses a
// request Content-Encoding, even identity, and a second Content-Type by
// score alone.
func TestAnExclusionLiftsARequestRule(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Exclusions: []model.WAFExclusion{
			{Rule: "19001-19010", Path: "/open"}, {Rule: "19002"}, {Rule: "920450"}, {Rule: "920620"},
		}}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	for _, tr := range requestTraits(model.DefaultMaxCookies, model.DefaultMaxParts, model.DefaultMaxNameDepth, model.DefaultMaxNameLength) {
		if tr.rule == 0 {
			continue
		}
		if got := send(t, plain, tr.req(t, "http://web.example.com/open/"+tr.name)); got != http.StatusOK {
			time.Sleep(300 * time.Millisecond)
			ev, _ := findEvent(out, func(ev wafevent.Event) bool { p, _, _ := strings.Cut(ev.URI, "?"); return p == "/open/"+tr.name })
			t.Errorf("%s on the lifted path: status %d, refused by %+v", tr.name, got, ev.Rules)
		}
		want := http.StatusForbidden
		if tr.rule == 19002 {
			want = http.StatusOK
		}
		if got := send(t, plain, tr.req(t, "http://web.example.com/shut/"+tr.name)); got != want {
			t.Errorf("%s elsewhere: status %d, want %d", tr.name, got, want)
		}
	}
	identity := newRequest(t, "http://web.example.com/shut/identity", "application/x-www-form-urlencoded", "title=Holiday", "Content-Encoding", "Identity")
	if got := send(t, plain, identity); got != http.StatusOK {
		t.Errorf("an identity Content-Encoding: status %d", got)
	}
}

// Coraza picks its multipart reader only for multipart/form-data, and CRS
// read any other multipart body as a form, where the framing in the names
// tripped the header injection rules and a payload was split at its & and
// = signs. Any multipart body is now read as parts: for a site that lets
// multipart/related through, an upload passes, a body that never closes is
// refused as a multipart/form-data one is, and an attack in a part is read.
func TestProxyReadsEveryMultipartBodyAsMultipart(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Profiles: []model.WAFProfile{{ID: "p1", Mode: "block", Exclusions: []model.WAFExclusion{
			{Rule: "920420"}, {Rule: "921421"},
		}}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	const related = `multipart/related; boundary=XyZ; type="application/json"`
	part := func(contentType, data string) string { return "Content-Type: " + contentType + "\r\n\r\n" + data }
	upload := mpBody("XyZ", part("application/json", `{"name":"report.pdf"}`), part("application/octet-stream", "%PDF-1.7 report"))
	attack := mpBody("XyZ", part("application/json", `{"name":"report.pdf"}`), part("text/plain", "1' or '1'='1' --"))
	for _, c := range []struct {
		path, body string
		status     int
		rule       int
	}{
		{"/upload", upload, http.StatusOK, 0},
		{"/unclosed", strings.TrimSuffix(upload, "--XyZ--\r\n"), http.StatusForbidden, 10008},
		{"/attack", attack, http.StatusForbidden, 942100},
	} {
		if got := send(t, plain, newRequest(t, "http://web.example.com"+c.path, related, c.body)); got != c.status {
			t.Errorf("%s: status %d, want %d", c.path, got, c.status)
		}
		if c.rule == 0 {
			continue
		}
		var ev wafevent.Event
		waitFor(t, "the event for "+c.path, func() bool {
			var ok bool
			ev, ok = findEvent(out, func(ev wafevent.Event) bool { return ev.URI == c.path })
			return ok
		})
		if !slices.ContainsFunc(ev.Rules, func(h wafevent.Hit) bool { return h.ID == c.rule }) {
			t.Errorf("%s was refused by %+v, want %d", c.path, ev.Rules, c.rule)
		}
	}
}

// Go's server refuses a transfer coding other than chunked, so a body only
// a backend would inflate never reaches one.
func TestProxyRefusesACodedTransfer(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	t.Cleanup(srv.Close)
	cfg := &model.Config{}
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools:    []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: srv.Listener.Addr().String()}}}},
		Profiles: []model.WAFProfile{{ID: "p1"}},
		Sites: []model.ProxySite{{ID: "web", Enabled: true, Hosts: []string{"web.example.com"}, Pool: "app",
			PlainHTTP: true, WAF: "p1"}},
	}
	plain, _, _ := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})
	conn, err := net.DialTimeout("tcp", plain, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("POST / HTTP/1.1\r\nHost: web.example.com\r\nTransfer-Encoding: gzip, chunked\r\n\r\n0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 400 || reached.Load() != 0 {
		t.Errorf("status %d, the upstream reached %d times", resp.StatusCode, reached.Load())
	}
}

// newRequest is a POST of body to url as contentType, when set, with each
// pair of header added: the same name twice sends it twice.
func newRequest(t *testing.T, url, contentType, body string, header ...string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header[header[i]] = append(req.Header[header[i]], header[i+1])
	}
	return req
}

// send sends req over plain HTTP to the proxy at addr and returns the
// status.
func send(t *testing.T, addr string, req *http.Request) int {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}}
	defer tr.CloseIdleConnections()
	resp, err := (&http.Client{Transport: tr, Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// cookies is a Cookie header of n cookies, each from format and its index.
func cookies(n int, format string) string {
	c := make([]string, n)
	for i := range c {
		c[i] = strings.Replace(format, "%d", strconv.Itoa(i), 1)
	}
	return strings.Join(c, "; ")
}

func mpBody(boundary string, parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString("--" + boundary + "\r\n" + p + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

func mpField(name, value string) string {
	return `Content-Disposition: form-data; name="` + name + "\"\r\n\r\n" + value
}

// mpFile is an empty file part, as the php2shell write-up sends hundreds of.
func mpFile(name string) string {
	return `Content-Disposition: form-data; name="` + name + `"; filename="a.txt"` + "\r\nContent-Type: text/plain\r\n\r\n"
}
