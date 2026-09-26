package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// older writes cfg as the oldest version Migrate reads would have: the
// reverse of every step, which a new step adds to.
func older(t *testing.T, cfg *Config) []byte {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"version":`+strconv.Itoa(SchemaVersion)),
		[]byte(`"version":`+strconv.Itoa(oldestMigrated)), 1)
	// 7 renamed the DNS resolver "validate" to "recursive".
	return bytes.Replace(raw, []byte(`"resolver":"recursive"`), []byte(`"resolver":"validate"`), 1)
}

// A file an older release wrote reads as the same configuration saved
// today, and validates.
func TestParseConfigBringsAnOlderFileUpToDate(t *testing.T) {
	t.Parallel()
	want := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	want.Services.DNS.Resolver = ResolverRecursive
	got, err := ParseConfig(older(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("migrated = %+v\nwant %+v", got.Services.DNS, want.Services.DNS)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("validate: %v", err)
	}
}

// Nothing it cannot bring up is touched, so the decoder and Validate still
// say what is wrong with it.
func TestMigrateLeavesWhatItCannotBringUp(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"version":` + strconv.Itoa(SchemaVersion) + `,"services":{"dns":{"resolver":"validate"}}}`,
		`{"version":5,"services":{"dns":{"resolver":"validate"}}}`,
		`{"version":` + strconv.Itoa(SchemaVersion+1) + `}`,
		`{"version":"6"}`,
		`{"version":6,`,
		`null`,
		``,
	} {
		if got := Migrate([]byte(raw)); string(got) != raw {
			t.Errorf("Migrate(%s) = %s, want it unchanged", raw, got)
		}
	}
}

// The document goes through a map on the way, which must not round a large
// number or drop a field this version does not know: the API refuses
// those, and it can only refuse what it sees.
func TestMigrateKeepsNumbersAndUnknownFields(t *testing.T) {
	t.Parallel()
	raw := `{"version":6,"services":{"dns":{"cacheSize":9007199254740993,"resolver":"validate"}},"surplus":true}`
	got := string(Migrate([]byte(raw)))
	for _, want := range []string{`9007199254740993`, `"surplus":true`, `"resolver":"recursive"`, `"version":` + strconv.Itoa(SchemaVersion)} {
		if !strings.Contains(got, want) {
			t.Errorf("Migrate = %s, want %s in it", got, want)
		}
	}
}

// Every bump needs its step, or a router that updates past it keeps a file
// the new binary refuses.
func TestEveryVersionHasAStep(t *testing.T) {
	t.Parallel()
	for v := oldestMigrated; v < SchemaVersion; v++ {
		if migrations[v] == nil {
			t.Errorf("no step from version %d to %d: add one to migrations", v, v+1)
		}
	}
}
