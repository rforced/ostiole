package model

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestValidateProxyRedactPaths(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"/s/", "/feed/", "/a-b/c.d/e_f/~g/", "/" + strings.Repeat("a", ProxyRedactPathMaxLen-2) + "/"} {
		cfg := proxyStarter()
		cfg.Services.Proxy = workingProxy()
		cfg.Services.Proxy.Sites[0].RedactPaths = []string{ok}
		if err := cfg.Validate(); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, tc := range []struct {
		name string
		list []string
		want string
	}{
		{"empty", []string{""}, "services.proxy.sites[0].redactPaths[0]"},
		{"too short", []string{"//"}, "services.proxy.sites[0].redactPaths[0]"},
		{"no leading slash", []string{"s/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"no trailing slash", []string{"/s"}, "services.proxy.sites[0].redactPaths[0]"},
		{"an empty segment", []string{"/s//"}, "services.proxy.sites[0].redactPaths[0]"},
		{"too long", []string{"/" + strings.Repeat("a", ProxyRedactPathMaxLen-1) + "/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a query", []string{"/s/?x=1/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a percent escape", []string{"/s%2Fx/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a pattern character", []string{"/s+/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a space", []string{"/a b/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a wildcard", []string{"/s/*/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a newline", []string{"/s\n/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"a double slash", []string{"/a//b/"}, "services.proxy.sites[0].redactPaths[0]"},
		{"the second is bad", []string{"/s/", "/s"}, "services.proxy.sites[0].redactPaths[1]"},
		{"duplicate", []string{"/s/", "/feed/", "/s/"}, "services.proxy.sites[0].redactPaths[2]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := proxyStarter()
			cfg.Services.Proxy = workingProxy()
			cfg.Services.Proxy.Sites[0].RedactPaths = tc.list
			if !hasIssue(t, cfg, tc.want) {
				t.Errorf("no issue at %s", tc.want)
			}
		})
	}
}

// Another site naming a prefix is not a duplicate, and the cap counts each
// prefix once, in sites that are switched off as well.
func TestValidateProxyRedactPathsAreCappedAcrossSites(t *testing.T) {
	t.Parallel()
	prefix := func(i int) string { return fmt.Sprintf("/p%d/", i) }
	build := func(n int) *Config {
		cfg := proxyStarter()
		cfg.Services.Proxy = workingProxy()
		var first, second []string
		for i := range n {
			first = append(first, prefix(i))
			if i%2 == 0 {
				second = append(second, prefix(i))
			}
		}
		cfg.Services.Proxy.Sites[0].RedactPaths = first
		cfg.Services.Proxy.Sites = append(cfg.Services.Proxy.Sites, ProxySite{
			ID: "other", Hosts: []string{"other.example.com"}, Pool: "web", RedactPaths: second})
		return cfg
	}
	if err := build(ProxyRedactPathsMax).Validate(); err != nil {
		t.Errorf("%d prefixes shared by two sites were refused: %v", ProxyRedactPathsMax, err)
	}
	over := build(ProxyRedactPathsMax + 1)
	if !hasIssue(t, over, fmt.Sprintf("services.proxy.sites[0].redactPaths[%d]", ProxyRedactPathsMax)) {
		t.Error("an eleventh different prefix passed")
	}
	own := proxyStarter()
	own.Services.Proxy = workingProxy()
	own.Services.Proxy.Sites = append(own.Services.Proxy.Sites, ProxySite{
		ID: "off", Hosts: []string{"off.example.com"}, Pool: "web", RedactPaths: []string{"/other/"}})
	own.Services.Proxy.Sites[0].RedactPaths = func() []string {
		var l []string
		for i := range ProxyRedactPathsMax {
			l = append(l, prefix(i))
		}
		return l
	}()
	if !hasIssue(t, own, "services.proxy.sites[1].redactPaths[0]") {
		t.Error("a prefix on a disabled site was left out of the count")
	}
}

func TestRedactedPrefixesAreTheEnabledSitesSortedOnce(t *testing.T) {
	t.Parallel()
	p := Proxy{Sites: []ProxySite{
		{ID: "a", Enabled: true, RedactPaths: []string{"/s/", "/feed/"}},
		{ID: "b", Enabled: false, RedactPaths: []string{"/off/"}},
		{ID: "c", Enabled: true, RedactPaths: []string{"/s/a/", "/s/"}},
	}}
	if got, want := p.RedactedPrefixes(), []string{"/feed/", "/s/", "/s/a/"}; !slices.Equal(got, want) {
		t.Errorf("prefixes = %v, want %v", got, want)
	}
	if got := (Proxy{Sites: []ProxySite{{Enabled: true}}}).RedactedPrefixes(); len(got) != 0 {
		t.Errorf("no prefixes gave %v", got)
	}
}

// The field is optional and written only when set, so a configuration
// without it reads and writes as it did before, at the same version.
func TestProxySiteRedactPathsAreOptionalInJSON(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(ProxySite{ID: "a", Hosts: []string{"a.example.com"}, Pool: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "redactPaths") {
		t.Errorf("an empty list is written: %s", raw)
	}
	var site ProxySite
	if err := json.Unmarshal([]byte(`{"id":"a","redactPaths":["/s/"]}`), &site); err != nil || !slices.Equal(site.RedactPaths, []string{"/s/"}) {
		t.Errorf("redactPaths read as %v, %v", site.RedactPaths, err)
	}
}
