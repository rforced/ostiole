package model

import (
	"errors"
	"strings"
	"testing"
)

func TestParseASN(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]uint32{
		"AS15169": 15169, "as15169": 15169, "15169": 15169, "As64512": 64512,
		"AS4294967295": MaxASN,
	} {
		got, err := ParseASN(in)
		if err != nil || got != want {
			t.Errorf("ParseASN(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if got, err := ParseASN(" AS15169\n"); err != nil || got != 15169 {
		t.Errorf("surrounding space was not ignored: %d, %v", got, err)
	}
	for _, in := range []string{"", "AS", "0", "AS0", "-1", "AS4294967296", "google", "AS15169x", "AS 15169", "1.5"} {
		if _, err := ParseASN(in); err == nil {
			t.Errorf("ParseASN(%q) accepted junk", in)
		}
	}
	if got := FormatASN(15169); got != "AS15169" {
		t.Errorf("FormatASN = %q", got)
	}
}

// issues returns path: message for each problem, or nothing for a valid
// configuration.
func issues(t *testing.T, c *Config) []string {
	t.Helper()
	err := c.Validate()
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type %T, want *ValidationError", err)
	}
	out := make([]string, 0, len(ve.Issues))
	for _, i := range ve.Issues {
		out = append(out, i.String())
	}
	return out
}

// Everything an AS alias can get wrong, each at its own path, so the
// dialog can point at the line.
func TestASAliasValidation(t *testing.T) {
	c := starterForBlocking()
	c.Aliases = []Alias{{Name: "google", Type: AliasASN, Entries: []string{"AS15169", "as36040", "396982"}}}
	if got := issues(t, c); len(got) != 0 {
		t.Fatalf("a good AS alias was refused: %v", got)
	}
	// And it stands where a rule wants addresses.
	c.Rules = append(c.Rules, Rule{ID: "google", Enabled: true, Zone: "wan", Action: "drop", Protocol: "any",
		Source: Endpoint{Alias: "google"}})
	if got := issues(t, c); len(got) != 0 {
		t.Fatalf("a rule could not use it: %v", got)
	}
	c.Rules = c.Rules[:len(c.Rules)-1]

	for name, tc := range map[string]struct {
		alias Alias
		want  string
	}{
		"empty":     {Alias{Name: "x", Type: AliasASN}, "aliases[0].entries: list the AS numbers"},
		"junk":      {Alias{Name: "x", Type: AliasASN, Entries: []string{"AS15169", "google"}}, `aliases[0].entries[1]: invalid AS number "google"`},
		"duplicate": {Alias{Name: "x", Type: AliasASN, Entries: []string{"15169", "AS15169"}}, "aliases[0].entries[1]: AS15169 is listed twice"},
		"url":       {Alias{Name: "x", Type: AliasASN, Entries: []string{"AS15169"}, URL: "https://example.test/l"}, "aliases[0].url: an AS alias fetches from the ASN source"},
	} {
		t.Run(name, func(t *testing.T) {
			c := starterForBlocking()
			c.Aliases = []Alias{tc.alias}
			got := issues(t, c)
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.want) {
				t.Errorf("issues = %v, want one starting %q", got, tc.want)
			}
		})
	}
}

func TestASNSourceTemplates(t *testing.T) {
	t.Parallel()
	c := starterForBlocking()
	c.System.ASNURL = "https://mirror.test/prefixes/{asn}.txt"
	c.System.ASNNamesURL = "https://mirror.test/names?as={asns}"
	if got := issues(t, c); len(got) != 0 {
		t.Fatalf("good templates were refused: %v", got)
	}
	prefixes, names := c.System.ASNTemplates()
	if prefixes != c.System.ASNURL || names != c.System.ASNNamesURL {
		t.Errorf("ASNTemplates = %q, %q", prefixes, names)
	}
	if p, n := (System{}).ASNTemplates(); p != DefaultASNURL || n != DefaultASNNamesURL {
		t.Errorf("empty settings gave %q, %q", p, n)
	}
	// Setting only the prefix source leaves the names unfetched, which is
	// how a mirror without a names call is described.
	if _, n := (System{ASNURL: "https://mirror.test/{asn}"}).ASNTemplates(); n != "" {
		t.Errorf("names = %q, want none", n)
	}

	for name, s := range map[string]System{
		"no placeholder": {ASNURL: "https://mirror.test/prefixes.txt"},
		"wrong scheme":   {ASNURL: "ftp://mirror.test/{asn}"},
		"names no list":  {ASNNamesURL: "https://mirror.test/names"},
	} {
		t.Run(name, func(t *testing.T) {
			c := starterForBlocking()
			c.System = s
			c.System.Management = starterForBlocking().System.Management
			got := issues(t, c)
			if len(got) != 1 || !strings.HasPrefix(got[0], "system.asn") {
				t.Errorf("issues = %v, want one under system.asn*", got)
			}
		})
	}
}
