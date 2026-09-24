package model

import (
	"strings"
	"testing"
)

func TestParseCondition(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Condition{
		"region=us-ashburn-1":    {Field: "region", Value: "us-ashburn-1"},
		" tags = OCI ":           {Field: "tags", Value: "OCI"},
		"hooks":                  {Value: "hooks"},
		"region=us-*":            {Field: "region", Value: "us-*"},
		"service=Google Cloud":   {Field: "service", Value: "Google Cloud"},
		"note=a=b":               {Field: "note", Value: "a=b"},
		"actions*":               {Value: "actions*"},
		"serviceArea=Exchange  ": {Field: "serviceArea", Value: "Exchange"},
	} {
		got, err := ParseCondition(in)
		if err != nil || got != want {
			t.Errorf("ParseCondition(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "  ", "=OCI", "tags=", " = ", "re*gion=x", "tags=OCI\n2", strings.Repeat("x", 129)} {
		if _, err := ParseCondition(in); err == nil {
			t.Errorf("ParseCondition(%q) accepted it", in)
		}
	}
	if got := (Condition{Field: "tags", Value: "OCI"}).String(); got != "tags=OCI" {
		t.Errorf("String = %q", got)
	}
	if got := (Condition{Value: "hooks"}).String(); got != "hooks" {
		t.Errorf("String = %q", got)
	}
}

// Each mistake at its own path, so the dialog can point at the line.
func TestAliasSelectValidation(t *testing.T) {
	t.Parallel()
	good := Alias{Name: "oracle", Type: AliasHosts, URL: "https://example.test/ranges.json",
		Select: []string{"region=us-ashburn-1", "region=us-phoenix-1", "tags=OCI"}}
	c := starterForBlocking()
	c.Aliases = []Alias{good}
	if got := issues(t, c); len(got) != 0 {
		t.Fatalf("a good selection was refused: %v", got)
	}

	for name, tc := range map[string]struct {
		alias Alias
		want  string
	}{
		"no url": {
			Alias{Name: "x", Type: AliasHosts, Entries: []string{"10.0.0.1"}, Select: []string{"tags=OCI"}},
			"aliases[0].select: only a hosts alias with a URL",
		},
		"ports": {
			Alias{Name: "x", Type: AliasPorts, URL: "https://example.test/ports", Select: []string{"tags=OCI"}},
			"aliases[0].select: only a hosts alias with a URL",
		},
		"country": {
			Alias{Name: "x", Type: AliasGeoIP, Entries: []string{"de"}, Select: []string{"tags=OCI"}},
			"aliases[0].select: only a hosts alias with a URL",
		},
		"half": {
			Alias{Name: "x", Type: AliasHosts, URL: "https://example.test/l.json", Select: []string{"tags=OCI", "region="}},
			`aliases[0].select[1]: condition "region=" needs a field and a value`,
		},
		"twice": {
			Alias{Name: "x", Type: AliasHosts, URL: "https://example.test/l.json", Select: []string{"tags=OCI", "Tags=oci"}},
			"aliases[0].select[1]: Tags=oci is listed twice",
		},
		"too many": {
			Alias{Name: "x", Type: AliasHosts, URL: "https://example.test/l.json", Select: manyConditions(MaxSelect + 1)},
			"aliases[0].select: at most 256 conditions",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := starterForBlocking()
			c.Aliases = []Alias{tc.alias}
			got := issues(t, c)
			if len(got) != 1 || !strings.HasPrefix(got[0], tc.want) {
				t.Errorf("issues = %v, want one starting %q", got, tc.want)
			}
		})
	}
}

func manyConditions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "region=r" + strings.Repeat("x", i%100) + string(rune('a'+i/100))
	}
	return out
}
