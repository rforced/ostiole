package services

import (
	"encoding/json"
	"maps"
	"regexp"
	"strings"
	"testing"
	"time"

	"ostiole/internal/model"
	"ostiole/internal/requestlog"
	"ostiole/internal/wafevent"
)

// What Caddy v2.11.9's multi_regexp filter refuses (modules/logging/filters.go).
const (
	caddyMaxRegexpOperations = 50
	caddyMaxPatternLength    = 1000
)

func redactConfig(sites ...model.ProxySite) *model.Config {
	cfg := &model.Config{}
	cfg.Services.Proxy.Sites = sites
	return cfg
}

func uriFilterOf(t *testing.T, cfg *model.Config) (kind string, ops []struct{ Regexp, Value string }) {
	t.Helper()
	fields, _ := (&Proxy{}).logConfig(cfg).Encoder["fields"].(jsonMap)
	raw, err := json.Marshal(fields["request>uri"])
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Filter     string
		Regexp     string
		Value      string
		Operations []struct{ Regexp, Value string }
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Filter == "regexp" {
		return f.Filter, []struct{ Regexp, Value string }{{f.Regexp, f.Value}}
	}
	return f.Filter, f.Operations
}

// filtered is uri as the rendered filter writes it, read the way Caddy
// reads it: every operation in order, each replacing all its matches.
func filtered(t *testing.T, cfg *model.Config, uri string) string {
	t.Helper()
	_, ops := uriFilterOf(t, cfg)
	for _, op := range ops {
		uri = regexp.MustCompile(op.Regexp).ReplaceAllString(uri, op.Value)
	}
	return uri
}

// With no prefix the filter is what it was before the field existed, so a
// router that hides nothing renders the same file.
func TestProxyLogFilterWithNoPrefixesIsUnchanged(t *testing.T) {
	t.Parallel()
	for _, cfg := range []*model.Config{
		{},
		redactConfig(model.ProxySite{ID: "a", Enabled: true}),
		redactConfig(model.ProxySite{ID: "a", RedactPaths: []string{"/s/"}}),
	} {
		l := (&Proxy{}).logConfig(cfg)
		fields, _ := l.Encoder["fields"].(jsonMap)
		got, _ := json.Marshal(fields["request>uri"])
		if want := `{"filter":"regexp","regexp":"\\?.*$","value":""}`; string(got) != want {
			t.Errorf("filter = %s, want %s", got, want)
		}
	}
}

func TestProxyLogFilterHidesTheTailOfAPrefixedPath(t *testing.T) {
	t.Parallel()
	const m = wafevent.Redacted
	shared := redactConfig(
		model.ProxySite{ID: "a", Enabled: true, RedactPaths: []string{"/s/", "/feed/"}},
		model.ProxySite{ID: "b", Enabled: true, RedactPaths: []string{"/s/"}},
		model.ProxySite{ID: "c", Enabled: false, RedactPaths: []string{"/off/"}},
	)
	for _, tc := range []struct{ uri, want string }{
		{"/s/tok3n", "/s/" + m},
		{"/s/tok3n?x=1&access_token=2", "/s/" + m},
		{"/s/tok3n/deeper/still", "/s/" + m},
		{"/s/tok3n?next=/s/other", "/s/" + m},
		{"/s//tok3n", "/s/" + m},
		{"/s/a%2Fb", "/s/" + m},
		{"/feed/tok3n.ics", "/feed/" + m},
		{"/feed/tok3n.ics?x=1", "/feed/" + m},
		{"/s/", "/s/"},
		{"/s/?x=1", "/s/"},
		{"/s", "/s"},
		{"/", "/"},
		{"/static/app.js", "/static/app.js"},
		{"/static/app.js?v=3", "/static/app.js"},
		{"/x/s/tok3n", "/x/s/tok3n"},
		{"/S/tok3n", "/S/tok3n"},
		{"/off/tok3n", "/off/tok3n"},
		{"/s/tok3n?" + strings.Repeat("a", 5000), "/s/" + m},
	} {
		if got := filtered(t, shared, tc.uri); got != tc.want {
			t.Errorf("%q = %q, want %q", tc.uri, got, tc.want)
		}
	}
	kind, ops := uriFilterOf(t, shared)
	if kind != "multi_regexp" || len(ops) != 2 || !strings.Contains(ops[1].Regexp, "(/feed/|/s/)") {
		t.Errorf("filter %s %+v: the shared prefix is not written once, sorted", kind, ops)
	}

	// A dot in a prefix is a dot, and one prefix that starts another is
	// hidden from the shorter.
	dots := redactConfig(model.ProxySite{ID: "a", Enabled: true, RedactPaths: []string{"/a.b/", "/s/a/", "/s/"}})
	for uri, want := range map[string]string{
		"/a.b/tok3n":  "/a.b/" + m,
		"/axb/tok3n":  "/axb/tok3n",
		"/s/a/tok3n":  "/s/" + m,
		"/s/b/tok3n":  "/s/" + m,
		"/s/a?q=/s/b": "/s/" + m,
	} {
		if got := filtered(t, dots, uri); got != want {
			t.Errorf("%q = %q, want %q", uri, got, want)
		}
	}
}

// The largest list validation lets through still fits what Caddy accepts of
// a filter: the worst prefix is all dots, which a pattern writes twice.
func TestProxyLogFilterFitsCaddyForTheLargestValidList(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/proxy.json")
	prefixes := largestRedactList()
	cfg.Services.Proxy.Sites[0].RedactPaths = prefixes
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the largest list was refused: %v", err)
	}
	cfg.Services.Proxy.Sites[1].RedactPaths = []string{"/one/more/"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("one more prefix was accepted: the largest list is not the largest")
	}
	cfg.Services.Proxy.Sites[1].RedactPaths = nil

	kind, ops := uriFilterOf(t, cfg)
	if kind != "multi_regexp" || len(ops) > caddyMaxRegexpOperations {
		t.Fatalf("filter %s with %d operations", kind, len(ops))
	}
	for i, op := range ops {
		if n := len(op.Regexp); n == 0 || n > caddyMaxPatternLength {
			t.Errorf("operation %d is %d characters", i, n)
		}
		if _, err := regexp.Compile(op.Regexp); err != nil {
			t.Errorf("operation %d: %v", i, err)
		}
	}
	for _, p := range prefixes {
		if got, want := filtered(t, cfg, p+"tok3n?x=1"), p+wafevent.Redacted; got != want {
			t.Errorf("%q = %q, want %q", p, got, want)
		}
	}
}

// largestRedactList is the most characters a valid list can write into a
// pattern: ten prefixes of the longest length, as much of each a dot as
// distinct ones allow.
func largestRedactList() []string {
	n := model.ProxyRedactPathMaxLen - 2
	out := []string{"/" + strings.Repeat(".", n) + "/"}
	for i := 0; len(out) < model.ProxyRedactPathsMax; i++ {
		out = append(out, "/"+strings.Repeat(".", i)+"a"+strings.Repeat(".", n-1-i)+"/")
	}
	return out
}

// expectRequests waits up to ten seconds for the request lines in out to
// read as want, each "site path".
func expectRequests(t *testing.T, out *lockedBuffer, want map[string]bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		got := map[string]bool{}
		for l := range strings.SplitSeq(out.String(), "\n") {
			if r, ok := requestlog.Parse(l); ok {
				got[r.Site+" "+r.Path] = true
			}
		}
		if maps.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("request lines %v, want %v:\n%s", got, want, out)
		}
	}
}

// The sidecar loads the rendered filter with Caddy's own module, so this is
// what a line in the journal and in the Requests tab holds: the prefix and
// the marker on every site, the query gone, a near miss left alone, and
// no trace of the token anywhere in what the sidecar wrote.
func TestProxyRequestLinesHideTheTailOfAPrefixedPath(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Sites: []model.ProxySite{
			{ID: "share", Enabled: true, Hosts: []string{"share.example.com"}, Pool: "app", PlainHTTP: true, RedactPaths: []string{"/s/"}},
			{ID: "cal", Enabled: true, Hosts: []string{"cal.example.com"}, Pool: "app", PlainHTTP: true, RedactPaths: []string{"/feed/"}},
		},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	for _, u := range []string{
		"http://share.example.com/s/zq7tokenA?access_token=zq7tokenB",
		"http://share.example.com/s/",
		"http://share.example.com/static/app.js?v=3",
		"http://share.example.com/feed/zq7tokenC.ics",
		"http://cal.example.com/feed/zq7tokenD.ics?x=1",
		"http://cal.example.com/s/zq7tokenE",
	} {
		if status, _ := fetch(t, plain, "", u); status != 200 {
			t.Fatalf("%s: status %d", u, status)
		}
	}

	want := map[string]bool{
		"share /s/" + wafevent.Redacted: true, "share /s/": true, "share /static/app.js": true,
		"share /feed/" + wafevent.Redacted: true, "cal /feed/" + wafevent.Redacted: true, "cal /s/" + wafevent.Redacted: true,
	}
	expectRequests(t, out, want)
	if strings.Contains(out.String(), "zq7token") {
		t.Errorf("a token is in what the sidecar wrote:\n%s", out)
	}
}

// The longest list validation allows is one the sidecar starts with, and
// the prefix that is all dots, which a pattern escapes, hides its tail.
func TestProxyStartsWithTheLargestListOfHiddenPrefixes(t *testing.T) {
	t.Parallel()
	bin := sidecar(t)
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	prefixes := largestRedactList()
	cfg.Services.Proxy = model.Proxy{
		Enabled: true, HTTPPort: freePort(t), HTTPSPort: freePort(t),
		Pools: []model.ProxyPool{{ID: "app", Upstreams: []model.ProxyUpstream{{Address: answer(t, "ok")}}}},
		Sites: []model.ProxySite{{ID: "share", Enabled: true, Hosts: []string{"share.example.com"}, Pool: "app",
			PlainHTTP: true, RedactPaths: prefixes}},
	}
	plain, _, out := runSidecar(t, bin, cfg, "", map[string][]string{model.SelfCertificate: {"127.0.0.1"}})

	for _, p := range []string{prefixes[0], prefixes[len(prefixes)-1]} {
		if status, _ := fetch(t, plain, "", "http://share.example.com"+p+"zq7tokenF"); status != 200 {
			t.Fatalf("%s: status %d", p, status)
		}
	}
	want := map[string]bool{
		"share " + prefixes[0] + wafevent.Redacted:               true,
		"share " + prefixes[len(prefixes)-1] + wafevent.Redacted: true,
	}
	expectRequests(t, out, want)
}
