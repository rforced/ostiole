package dnsblock

import (
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"example.com", "example.com", true},
		{"  Example.COM.  ", "example.com", true},
		{"ads.tracker.example.co.uk", "ads.tracker.example.co.uk", true},
		{"_dmarc.example.com", "_dmarc.example.com", true},
		{"xn--bcher-kva.example", "xn--bcher-kva.example", true},
		{"zip", "zip", true},
		// The wildcard form OISD and HaGeZi publish.
		{"*.ads.example.com", "ads.example.com", true},
		{"*.example.com", "example.com", true},
		{"*", "", false},
		{"ads.*.example.com", "", false},
		{"", "", false},
		{".", "", false},
		{"0.0.0.0", "", false},
		{"127.0.0.1", "", false},
		{"::1", "", false},
		{"localhost", "", false},
		{"ip6-allnodes", "", false},
		{"bücher.example", "", false}, // has to arrive as punycode
		{"example .com", "", false},
		{"-bad.example.com", "", false},
		{"bad-.example.com", "", false},
		{"double..dot.com", "", false},
		{"http://example.com", "", false},
		{"example.com/path", "", false},
		{strings.Repeat("a", 64) + ".com", "", false},
		{strings.Repeat("a.", 200) + "com", "", false},
	}
	for _, c := range cases {
		got, ok := Normalize(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Normalize(%q) = %q,%v; want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		format model.ListFormat
		in     string
		want   string
	}{
		{model.FormatHosts, "0.0.0.0 ads.example.com", "ads.example.com"},
		{model.FormatHosts, "127.0.0.1\tads.example.com # comment", "ads.example.com"},
		{model.FormatHosts, "0.0.0.0 localhost", ""},
		{model.FormatHosts, "# just a comment", ""},
		{model.FormatHosts, "ads.example.com", "ads.example.com"},
		{model.FormatDomains, "ads.example.com", "ads.example.com"},
		{model.FormatDomains, "ads.example.com extra", ""},
		{model.FormatDomains, "  ", ""},
		// A single label from a published list is junk, not a top-level
		// domain somebody meant to block.
		{model.FormatDomains, "zip", ""},
		{model.FormatHosts, "<html><body><h1>404 Not Found</h1></body></html>", ""},
		{model.FormatAdblock, "||ads.example.com^", "ads.example.com"},
		{model.FormatAdblock, "||ads.example.com", "ads.example.com"},
		{model.FormatAdblock, "@@||good.example.com^", ""},
		{model.FormatAdblock, "||example.com^$third-party", ""},
		{model.FormatAdblock, "||example.com/path^", ""},
		{model.FormatAdblock, "/banner.gif", ""},
		{model.FormatAdblock, "! a comment", ""},
		{model.FormatDnsmasq, "address=/ads.example.com/0.0.0.0", "ads.example.com"},
		{model.FormatDnsmasq, "local=/ads.example.com/", "ads.example.com"},
		{model.FormatDnsmasq, "server=/ads.example.com/1.1.1.1", "ads.example.com"},
		{model.FormatDnsmasq, "log-queries", ""},
		{model.FormatUnbound, `local-zone: "ads.example.com." always_nxdomain`, "ads.example.com"},
		{model.FormatUnbound, `local-zone: "ads.example.com." redirect`, "ads.example.com"},
		{model.FormatUnbound, "server:", ""},
	}
	for _, c := range cases {
		got, ok := ParseLine(c.in, c.format)
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("ParseLine(%q, %s) = %q; want %q", c.in, c.format, got, c.want)
		}
	}
}

func TestDetectFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want model.ListFormat
	}{
		{"hosts", "# Title\n0.0.0.0 a.example.com\n0.0.0.0 b.example.com\n", model.FormatHosts},
		{"domains", "# Title\na.example.com\nb.example.com\n", model.FormatDomains},
		{"adblock", "! Title\n||a.example.com^\n||b.example.com^\n", model.FormatAdblock},
		{"dnsmasq", "address=/a.example.com/0.0.0.0\naddress=/b.example.com/0.0.0.0\n", model.FormatDnsmasq},
		{"unbound", `local-zone: "a.example.com." always_nxdomain` + "\n", model.FormatUnbound},
		{"empty falls back to domains", "# nothing here\n", model.FormatDomains},
	}
	for _, c := range cases {
		if got := DetectFormat(c.in); got != c.want {
			t.Errorf("%s: DetectFormat = %s; want %s", c.name, got, c.want)
		}
	}
}

func TestReduceDropsWhatAParentCovers(t *testing.T) {
	got := Reduce([]string{
		"ads.example.com",
		"example.com",
		"deep.ads.example.com",
		"other.net",
		"example.com",
		"notexample.com",
	})
	want := []string{"example.com", "notexample.com", "other.net"}
	if len(got) != len(want) {
		t.Fatalf("Reduce = %v; want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Reduce = %v; want %v", got, want)
		}
	}
}

func TestReverseLabelsRoundTrip(t *testing.T) {
	for _, s := range []string{"example.com", "a.b.c.d.example.com", "zip"} {
		if got := reverseLabels(reverseLabels(s)); got != s {
			t.Errorf("reverseLabels twice on %q gave %q", s, got)
		}
	}
}

func TestCovers(t *testing.T) {
	cases := []struct {
		parent, child string
		want          bool
	}{
		{"com.example", "com.example", true},
		{"com.example", "com.example.ads", true},
		{"com.example", "com.examplezzz", false},
		{"com.example", "com.notexample", false},
		{"com.example.ads", "com.example", false},
	}
	for _, c := range cases {
		if got := covers(c.parent, c.child); got != c.want {
			t.Errorf("covers(%q,%q) = %v; want %v", c.parent, c.child, got, c.want)
		}
	}
}
