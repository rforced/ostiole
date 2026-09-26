package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/model"
)

func cfg() *model.Config {
	return model.Starter(model.StarterOptions{
		Hostname: "gateway", LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0",
	})
}

func at(s string) func() time.Time {
	when, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return when }
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	a, err := Create(cfg(), Options{
		Ostiole: "v0.3.0",
		Note:    "before the rule change",
		Users:   []auth.User{{Username: "admin", Hash: "$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"}},
		Now:     at("2026-09-16T10:11:12Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if back.Config.System.Hostname != "gateway" {
		t.Errorf("hostname = %q", back.Config.System.Hostname)
	}
	if len(back.Users) != 1 || back.Users[0].Username != "admin" {
		t.Errorf("users = %+v", back.Users)
	}
	if back.Note != "before the rule change" || back.Ostiole != "v0.3.0" {
		t.Errorf("metadata lost: %+v", back)
	}
	if got := a.Filename(); got != "gateway-20260916-101112.json" {
		t.Errorf("Filename() = %q", got)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Error("file should end with a newline")
	}
}

func TestCreateTakesTheHostnameFromTheConfig(t *testing.T) {
	t.Parallel()
	a, err := Create(cfg(), Options{Now: at("2026-09-16T10:11:12Z")})
	if err != nil {
		t.Fatal(err)
	}
	if a.Hostname != "gateway" {
		t.Errorf("hostname = %q, want the one from the configuration", a.Hostname)
	}
	if len(a.Users) != 0 {
		t.Error("accounts must not travel unless asked for")
	}
}

// A router with no hostname still gets a sensible filename.
func TestFilenameFallsBack(t *testing.T) {
	t.Parallel()
	a := &Archive{CreatedAt: time.Date(2026, 9, 16, 1, 2, 3, 0, time.UTC)}
	if got := a.Filename(); got != "ostiole-20260916-010203.json" {
		t.Errorf("Filename() = %q", got)
	}
	a.Hostname = "my router/../etc"
	if got := a.Filename(); got != "my-router-.-etc-20260916-010203.json" {
		t.Errorf("Filename() = %q, want the name made safe", got)
	}
}

func TestParseRejectsRubbish(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		in   string
		want error
	}{
		"not json":      {"{", ErrNotABackup},
		"another file":  {`{"kind":"something-else","version":1}`, ErrNotABackup},
		"from a newer":  {`{"kind":"ostiole-backup","version":99}`, ErrUnknownVersion},
		"no config":     {`{"kind":"ostiole-backup","version":1}`, ErrNoConfig},
		"empty config":  {`{"kind":"ostiole-backup","version":1,"config":{}}`, nil},
		"plain config ": {`{"version":1,"zones":[]}`, ErrNotABackup},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.in))
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A backup carrying a configuration this build would refuse to apply is
// caught at restore, not later at apply.
func TestParseRejectsAnInvalidConfiguration(t *testing.T) {
	t.Parallel()
	broken := cfg()
	broken.Rules[0].Zone = "nowhere"
	a, _ := Create(broken, Options{})
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Errorf("err = %v, want the validation to be reported", err)
	}
}

// A backup an older release took restores like one taken today.
func TestParseBringsAnOlderConfigurationUpToDate(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.Services.DNS.Resolver = model.ResolverRecursive
	a, err := Create(c, Options{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"version":7`), []byte(`"version":6`), 1)
	raw = bytes.Replace(raw, []byte(`"resolver":"recursive"`), []byte(`"resolver":"validate"`), 1)
	got, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Config.Version != model.SchemaVersion || got.Config.Services.DNS.Resolver != model.ResolverRecursive {
		t.Errorf("config version %d, resolver %q; want %d, recursive",
			got.Config.Version, got.Config.Services.DNS.Resolver, model.SchemaVersion)
	}
}

func TestSummaryCounts(t *testing.T) {
	t.Parallel()
	c := cfg()
	c.Gateways = []model.Gateway{{Name: "wan1", Enabled: true, Interface: "eth0", Address: "10.0.0.1"}}
	c.GatewayGroups = []model.GatewayGroup{{Name: "g", Enabled: true, Members: []model.GatewayMember{{Gateway: "wan1"}}}}
	a, err := Create(c, Options{Users: []auth.User{{Username: "admin"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := a.Summary()
	if s.Zones != len(c.Zones) || s.Interfaces != len(c.Interfaces) || s.Rules != len(c.Rules) {
		t.Errorf("summary = %+v", s)
	}
	if s.Gateways != 2 {
		t.Errorf("gateways = %d, want gateways and groups counted together", s.Gateways)
	}
	if s.Users != 1 {
		t.Errorf("users = %d", s.Users)
	}
}
