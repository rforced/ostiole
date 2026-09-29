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
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	doc["version"] = oldestMigrated
	// 12 dropped four exclusion sets, which a profile could load.
	if proxy := object(doc, "services", "proxy"); proxy != nil {
		profiles, _ := proxy["wafProfiles"].([]any)
		for _, p := range profiles {
			profile := p.(map[string]any)
			apps, _ := profile["applications"].([]any)
			profile["applications"] = append(apps, "drupal")
		}
	}
	// 11 dropped a backup cron's directory, which every one had to name.
	if crons, ok := doc["crons"].([]any); ok {
		for _, c := range crons {
			if cron := c.(map[string]any); cron["kind"] == string(CronBackup) {
				cron["directory"] = "/var/backups/ostiole"
			}
		}
	}
	// 10 made the query log's hours its days.
	if q := object(doc, "services", "dns", "queryLog"); q != nil {
		if days, ok := q["days"].(json.Number); ok {
			n, _ := days.Int64()
			q["hours"] = n * 24
			delete(q, "days")
		}
	}
	// 9 turned the proxy's zones into its access list. Only the lines 8
	// could write go back: each opening HTTP, HTTPS and every route.
	if proxy := object(doc, "services", "proxy"); proxy != nil {
		if access, ok := proxy["access"].([]any); ok {
			zones := []any{}
			for _, a := range access {
				zones = append(zones, a.(map[string]any)["zone"])
			}
			proxy["zones"] = zones
			delete(proxy, "access")
		}
	}
	// 8 moved the DNS providers out of "acme".
	if providers, ok := doc["dnsProviders"]; ok {
		acme := object(doc, "acme")
		if acme == nil {
			acme = map[string]any{}
			doc["acme"] = acme
		}
		acme["providers"] = providers
		delete(doc, "dnsProviders")
	}
	// 7 renamed the DNS resolver "validate" to "recursive".
	if dns := object(doc, "services", "dns"); dns != nil && dns["resolver"] == string(ResolverRecursive) {
		dns["resolver"] = "validate"
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A file an older release wrote reads as the same configuration saved
// today, and validates.
func TestParseConfigBringsAnOlderFileUpToDate(t *testing.T) {
	t.Parallel()
	want := Starter(StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	want.Services.DNS.Resolver = ResolverRecursive
	want.DNSProviders = []DNSProvider{{ID: "cf", Kind: "cloudflare", Settings: map[string]string{"token": "t"}}}
	want.System.Management.WebPort = 8443
	want.Services.Proxy = workingProxy()
	want.Services.DNS.QueryLog = QueryLog{Enabled: true, Entries: 5000, Days: 7}
	want.Crons = []Cron{{ID: "nightly", Enabled: true, Schedule: "@daily", Kind: CronBackup, Keep: 3}}
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

// The DNS providers leave "acme" and the accounts stay in it.
func TestMigrateMovesTheDNSProviders(t *testing.T) {
	t.Parallel()
	raw := `{"version":7,"acme":{"accounts":[{"id":"le"}],"providers":[{"id":"cf","kind":"cloudflare"}]}}`
	var got map[string]any
	if err := json.Unmarshal(Migrate([]byte(raw)), &got); err != nil {
		t.Fatal(err)
	}
	acme, _ := got["acme"].(map[string]any)
	if _, left := acme["providers"]; left || acme["accounts"] == nil {
		t.Errorf("acme = %v, want the accounts and no providers", acme)
	}
	if providers, _ := got["dnsProviders"].([]any); len(providers) != 1 {
		t.Errorf("dnsProviders = %v, want the one provider", got["dnsProviders"])
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

// Each zone the proxy was open on becomes one line opening what it did:
// both listeners and every route, from anywhere. A zone written twice is
// one line.
func TestMigrateTurnsProxyZonesIntoAccess(t *testing.T) {
	t.Parallel()
	raw := `{"version":8,"services":{"proxy":{"enabled":true,"zones":["wan","lan","wan"],` +
		`"routes":[{"id":"imap"},{"id":"dns"}]}}}`
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Services.Proxy
	want := []ProxyAccess{
		{ID: "wan", Enabled: true, Zone: "wan", Action: ActionAccept,
			Ports: []string{ProxyPortHTTP, ProxyPortHTTPS}, Routes: []string{"imap", "dns"}},
		{ID: "lan", Enabled: true, Zone: "lan", Action: ActionAccept,
			Ports: []string{ProxyPortHTTP, ProxyPortHTTPS}, Routes: []string{"imap", "dns"}},
	}
	if !reflect.DeepEqual(p.Access, want) {
		t.Errorf("access = %+v\nwant %+v", p.Access, want)
	}
	// A proxy open nowhere stays open nowhere.
	cfg, err = ParseConfig([]byte(`{"version":8,"services":{"proxy":{"enabled":true}}}`))
	if err != nil || cfg.Services.Proxy.Access != nil {
		t.Errorf("access = %+v, %v; want none", cfg.Services.Proxy.Access, err)
	}
}

// Hours become days rounded up, so the log keeps at least what it did: a
// week of hours is a week, and a day and an hour is two days.
func TestMigrateTurnsQueryLogHoursIntoDays(t *testing.T) {
	t.Parallel()
	for hours, want := range map[string]int{`168`: 7, `25`: 2, `1`: 1, `0`: 0, `"x"`: 0} {
		raw := `{"version":9,"services":{"dns":{"queryLog":{"enabled":true,"hours":` + hours + `}}}}`
		cfg, err := ParseConfig([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", hours, err)
		}
		if got := cfg.Services.DNS.QueryLog; got.Days != want || !got.Enabled {
			t.Errorf("%s hours = %+v, want %d days", hours, got, want)
		}
	}
	if got := string(Migrate([]byte(`{"version":9,"services":{"dns":{"queryLog":{"hours":24}}}}`))); strings.Contains(got, "hours") {
		t.Errorf("hours left behind: %s", got)
	}
}

// A backup cron keeps everything but where it wrote, and a strict decode
// of the result finds nothing it does not know.
func TestMigrateDropsTheBackupDirectory(t *testing.T) {
	t.Parallel()
	raw := `{"version":10,"crons":[{"id":"nightly","enabled":true,"schedule":"@daily","kind":"backup",` +
		`"directory":"/srv/backups","keep":3,"withUsers":true}]}`
	dec := json.NewDecoder(bytes.NewReader(Migrate([]byte(raw))))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	want := []Cron{{ID: "nightly", Enabled: true, Schedule: "@daily", Kind: CronBackup, Keep: 3, WithUsers: true}}
	if !reflect.DeepEqual(cfg.Crons, want) {
		t.Errorf("crons = %+v, want %+v", cfg.Crons, want)
	}
}

// A profile loses the exclusion sets that were dropped and keeps the rest,
// in order; one left with none loads none.
func TestMigrateDropsTheRetiredExclusionSets(t *testing.T) {
	t.Parallel()
	raw := `{"version":11,"services":{"proxy":{"wafProfiles":[` +
		`{"id":"a","applications":["drupal","wordpress","phpmyadmin","nextcloud"]},` +
		`{"id":"b","applications":["cpanel","dokuwiki"]},{"id":"c"}]}}}`
	cfg, err := ParseConfig([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := []WAFProfile{{ID: "a", Applications: []string{"wordpress", "nextcloud"}}, {ID: "b"}, {ID: "c"}}
	if !reflect.DeepEqual(cfg.Services.Proxy.Profiles, want) {
		t.Errorf("profiles = %+v, want %+v", cfg.Services.Proxy.Profiles, want)
	}
	if got := string(Migrate([]byte(raw))); strings.Contains(got, `"applications":null`) || strings.Contains(got, `"applications":[]`) {
		t.Errorf("an emptied list was left behind: %s", got)
	}
}
