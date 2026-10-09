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
	// 14 split the exempt alias in two; the one there was matched clients.
	// An alias's one URL was a field of its own, beside a selection.
	if enforce := object(doc, "blocking", "enforce"); enforce != nil {
		if name, ok := enforce["exemptClients"]; ok {
			enforce["exemptAlias"] = name
			delete(enforce, "exemptClients")
		}
	}
	if aliases, ok := doc["aliases"].([]any); ok {
		for _, item := range aliases {
			alias := item.(map[string]any)
			entries, _ := alias["entries"].([]any)
			kept := []any{}
			for _, e := range entries {
				if s, _ := e.(string); IsURLEntry(s) {
					alias["url"] = s
					alias["select"] = []any{"region=us-east-1"}
				} else {
					kept = append(kept, e)
				}
			}
			alias["entries"] = kept
		}
	}
	// 13 put every log's days in memory under logging; each log had its own.
	if logging := object(doc, "system", "logging"); logging != nil {
		if days, ok := logging["days"]; ok {
			delete(logging, "days")
			for _, keys := range [][]string{
				{"system", "management", "firewallLog"}, {"services", "dns", "queryLog"},
				{"services", "proxy", "events"}, {"services", "proxy", "requests"},
				{"traffic", "destinations"}, {"services", "dhcp", "log"}, {"wireless", "log"},
				{"vpn", "wireguardLog"}, {"vpn", "tailscaleLog"},
			} {
				if l := object(doc, keys...); l != nil {
					l["days"] = days
				}
			}
		}
	}
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
	want.Services.DNS.QueryLog = QueryLog{Enabled: true, Entries: 5000}
	want.System.Logging.Days = 30
	want.Crons = []Cron{{ID: "nightly", Enabled: true, Schedule: "@daily", Kind: CronBackup, Keep: 3}}
	want.Aliases = append(want.Aliases, Alias{Name: "resolver_exempt", Type: AliasHosts, Entries: []string{"192.168.1.9"}},
		Alias{Name: "drop", Type: AliasHosts, Entries: []string{"198.51.100.7", "https://lists.example.net/drop.txt"}, RefreshHours: 12})
	want.Blocking.Enforce.ExemptClients = "resolver_exempt"
	got, err := ParseConfig(older(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("migrated = %+v %+v\nwant %+v %+v", got.Services.DNS, got.System.Logging, want.Services.DNS, want.System.Logging)
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
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Services.Proxy.Access != nil {
		t.Errorf("access = %+v, want none", cfg.Services.Proxy.Access)
	}
}

// Hours become days rounded up, so the log keeps at least what it did: a
// week of hours is a week, and a day and an hour is two days. Since 13
// those days are every log's in memory, under logging, and none or a week
// writes nothing there.
func TestMigrateTurnsQueryLogHoursIntoDays(t *testing.T) {
	t.Parallel()
	for hours, want := range map[string]int{`168`: 7, `25`: 2, `1`: 1, `0`: 7, `"x"`: 7} {
		raw := `{"version":9,"services":{"dns":{"queryLog":{"enabled":true,"hours":` + hours + `}}}}`
		cfg, err := ParseConfig([]byte(raw))
		if err != nil {
			t.Fatalf("%s: %v", hours, err)
		}
		if got := cfg.System.Logging.MemoryDays(); got != want || !cfg.Services.DNS.QueryLog.Enabled {
			t.Errorf("%s hours = %d days in memory, query log %+v; want %d days", hours, got, cfg.Services.DNS.QueryLog, want)
		}
	}
	got := string(Migrate([]byte(`{"version":9,"services":{"dns":{"queryLog":{"hours":24}}}}`)))
	if strings.Contains(got, "hours") || strings.Count(got, `"days"`) != 1 || !strings.Contains(got, `"logging":{"days":1}`) {
		t.Errorf("Migrate = %s, want one day under logging and nothing on the query log", got)
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

// Every log's days in memory become one under logging, the largest any log
// had; a log keeps nothing of its own. None, or none above a week, writes
// nothing: zero keeps the week.
func TestMigrateGathersTheLogDays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{"version":12,"system":{"logging":{"level":"info"},"management":{"firewallLog":{"entries":1000,"days":3}}},` +
			`"services":{"dns":{"queryLog":{"enabled":true,"days":30}},"proxy":{"requests":{"days":"x"}}},` +
			`"vpn":{"tailscaleLog":{"days":14}}}`, 30},
		{`{"version":12,"services":{"dns":{"queryLog":{"enabled":true}}},"wireless":{"log":{"entries":20}}}`, 0},
		{`{"version":12,"services":{"proxy":{"events":{"days":2}}},"traffic":{"destinations":{"days":7}}}`, 0},
	} {
		got := Migrate([]byte(tc.raw))
		dec := json.NewDecoder(bytes.NewReader(got))
		dec.DisallowUnknownFields()
		var cfg Config
		if err := dec.Decode(&cfg); err != nil {
			t.Errorf("%s: %v", got, err)
			continue
		}
		if cfg.System.Logging.Days != tc.want {
			t.Errorf("%s: days = %d, want %d", got, cfg.System.Logging.Days, tc.want)
		}
		if tc.want == 0 && strings.Contains(string(got), `"logging"`) {
			t.Errorf("%s: nothing to write, but logging was", got)
		}
	}
	cfg, err := ParseConfig([]byte(`{"version":12,"system":{"logging":{"level":"info"},` +
		`"management":{"firewallLog":{"entries":1000,"days":3}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.System.Logging != (Logging{Level: LogInfo, Days: 3}) ||
		cfg.System.Management.FirewallLog != (FirewallLog{Entries: 1000}) {
		t.Errorf("logging %+v, firewall log %+v; want the level kept beside 3 days and the entries kept",
			cfg.System.Logging, cfg.System.Management.FirewallLog)
	}
}

// The one exempt alias there was matched clients, so it becomes the clients
// one, and a strict decode of the result finds nothing it does not know.
func TestMigrateRenamesTheExemptAlias(t *testing.T) {
	t.Parallel()
	raw := `{"version":13,"blocking":{"enforce":{"blockDot":true,"exemptAlias":"resolver_exempt"}}}`
	dec := json.NewDecoder(bytes.NewReader(Migrate([]byte(raw))))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	want := DNSEnforce{BlockDoT: true, ExemptClients: "resolver_exempt"}
	if cfg.Blocking.Enforce != want {
		t.Errorf("enforce = %+v, want %+v", cfg.Blocking.Enforce, want)
	}
}

// An alias's URL becomes its last entry and a selection goes, so a strict
// decode of the result finds nothing it does not know.
func TestMigrateMovesAliasURLsIntoEntries(t *testing.T) {
	t.Parallel()
	raw := `{"version":13,"aliases":[
		{"name":"drop","type":"hosts","entries":["192.0.2.1"],"url":"https://lists.example.net/drop.txt","refreshHours":12},
		{"name":"cloud","type":"hosts","entries":null,"url":"https://lists.example.net/ranges.json","select":["region=us-east-1"]},
		{"name":"feed","type":"hosts","url":"https://lists.example.net/feed.txt"},
		{"name":"office","type":"hosts","entries":["198.51.100.0/24"],"url":""},
		{"name":"games","type":"ports","entries":["27015"],"url":"https://lists.example.net/ports.txt"}]}`
	dec := json.NewDecoder(bytes.NewReader(Migrate([]byte(raw))))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	want := []Alias{
		{Name: "drop", Type: AliasHosts, Entries: []string{"192.0.2.1", "https://lists.example.net/drop.txt"}, RefreshHours: 12},
		{Name: "cloud", Type: AliasHosts, Entries: []string{"https://lists.example.net/ranges.json"}},
		{Name: "feed", Type: AliasHosts, Entries: []string{"https://lists.example.net/feed.txt"}},
		{Name: "office", Type: AliasHosts, Entries: []string{"198.51.100.0/24"}},
		{Name: "games", Type: AliasPorts, Entries: []string{"27015", "https://lists.example.net/ports.txt"}},
	}
	if !reflect.DeepEqual(cfg.Aliases, want) {
		t.Errorf("aliases = %+v\nwant %+v", cfg.Aliases, want)
	}
}
