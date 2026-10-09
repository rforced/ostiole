package model

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// A configuration is read by every Ostiole that comes after the one that
// wrote it: the router's own file after an update, a revision in History,
// a backup, a draft from a page left open across an update. A change to
// its shape bumps SchemaVersion and adds a step to migrations that rewrites
// the version before, so the rest of the code only ever sees the current
// shape. Nothing is written back: the next save stores the new version,
// and until then a binary put back after a failed update still finds a
// file it can read.

// oldestMigrated is the version every release up to 1.4.0 wrote. Anything
// older is left for Validate to refuse.
const oldestMigrated = 6

// migrations[v] rewrites version v as version v+1.
var migrations = map[int]func(doc map[string]any){
	// 7 renamed the DNS resolver "validate" to "recursive".
	6: func(doc map[string]any) {
		if dns := object(doc, "services", "dns"); dns != nil && dns["resolver"] == "validate" {
			dns["resolver"] = string(ResolverRecursive)
		}
	},
	// 8 moved the DNS providers out of "acme", since dynamic DNS writes
	// through them as well.
	7: func(doc map[string]any) {
		acme := object(doc, "acme")
		if acme == nil {
			return
		}
		if providers, ok := acme["providers"]; ok {
			doc["dnsProviders"] = providers
			delete(acme, "providers")
		}
	},
	// 9 turned the proxy's zones into its access list: each zone one line
	// accepting HTTP, HTTPS and every route from anywhere, which opens what
	// the zone did.
	8: func(doc map[string]any) {
		proxy := object(doc, "services", "proxy")
		if proxy == nil {
			return
		}
		zones, _ := proxy["zones"].([]any)
		delete(proxy, "zones")
		var routes []any
		list, _ := proxy["routes"].([]any)
		for _, r := range list {
			if route, ok := r.(map[string]any); ok && route["id"] != nil {
				routes = append(routes, route["id"])
			}
		}
		var access []any
		seen := map[string]bool{}
		for _, z := range zones {
			name, ok := z.(string)
			if !ok || seen[name] {
				continue
			}
			seen[name] = true
			line := map[string]any{
				"id": name, "enabled": true, "zone": name, "action": string(ActionAccept),
				"ports": []any{ProxyPortHTTP, ProxyPortHTTPS}, "source": map[string]any{},
			}
			if len(routes) > 0 {
				line["routes"] = routes
			}
			access = append(access, line)
		}
		if len(access) > 0 {
			proxy["access"] = access
		}
	},
	// 10 gave the query log days, as every log in memory had, for hours:
	// rounded up, so nothing kept before goes early.
	9: func(doc map[string]any) {
		q := object(doc, "services", "dns", "queryLog")
		if q == nil {
			return
		}
		hours, _ := q["hours"].(json.Number)
		delete(q, "hours")
		if n, err := hours.Int64(); err == nil && n > 0 {
			q["days"] = json.Number(strconv.FormatInt((n+23)/24, 10))
		}
	},
	// 11 dropped a backup cron's directory: every one writes to the one
	// the unit opens. Backups already written elsewhere stay there.
	10: func(doc map[string]any) {
		crons, _ := doc["crons"].([]any)
		for _, c := range crons {
			if cron, ok := c.(map[string]any); ok {
				delete(cron, "directory")
			}
		}
	},
	// 12 dropped the Drupal, phpMyAdmin, cPanel and DokuWiki exclusion
	// sets. A profile keeps the other sets it loaded.
	11: func(doc map[string]any) {
		profiles, _ := object(doc, "services", "proxy")["wafProfiles"].([]any)
		for _, p := range profiles {
			profile, ok := p.(map[string]any)
			if !ok {
				continue
			}
			apps, _ := profile["applications"].([]any)
			var kept []any
			for _, app := range apps {
				switch app {
				case "drupal", "phpmyadmin", "cpanel", "dokuwiki":
				default:
					kept = append(kept, app)
				}
			}
			if len(kept) == 0 {
				delete(profile, "applications")
			} else {
				profile["applications"] = kept
			}
		}
	},
	// 13 replaced each log's days in memory with one setting under logging
	// for them all: the largest any log had.
	12: func(doc map[string]any) {
		var most int64
		for _, keys := range [][]string{
			{"system", "management", "firewallLog"},
			{"services", "dns", "queryLog"},
			{"services", "proxy", "events"},
			{"services", "proxy", "requests"},
			{"traffic", "destinations"},
			{"services", "dhcp", "log"},
			{"wireless", "log"},
			{"vpn", "wireguardLog"},
			{"vpn", "tailscaleLog"},
		} {
			l := object(doc, keys...)
			if l == nil {
				continue
			}
			days, _ := l["days"].(json.Number)
			delete(l, "days")
			if n, err := days.Int64(); err == nil && n > most {
				most = n
			}
		}
		if most > 0 && most != DefaultLogDays {
			if logging := makeObject(doc, "system", "logging"); logging != nil {
				logging["days"] = most
			}
		}
	},
}

// Migrate brings a configuration written at an older version up to
// SchemaVersion. What it cannot bring up comes back as it was, for the
// decoder or Validate to report: JSON it cannot read, the current version
// or a newer one, and anything older than the first step.
func Migrate(raw []byte) []byte {
	var head struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Version < oldestMigrated || head.Version >= SchemaVersion {
		return raw
	}
	// Numbers stay as written: through a float64 the large ones would round.
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&doc) != nil {
		return raw
	}
	for v := head.Version; v < SchemaVersion; v++ {
		step, ok := migrations[v]
		if !ok {
			return raw
		}
		step(doc)
	}
	doc["version"] = SchemaVersion
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

// ParseConfig reads a configuration written by this version or by one
// Migrate can bring up to it.
func ParseConfig(raw []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(Migrate(raw), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// object follows keys down doc, nil when one of them is not an object.
func object(doc map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := doc[k].(map[string]any)
		if !ok {
			return nil
		}
		doc = next
	}
	return doc
}

// makeObject follows keys down doc as object does, making the objects that
// are missing; nil when one of them is something else.
func makeObject(doc map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		switch next := doc[k].(type) {
		case map[string]any:
			doc = next
		case nil:
			m := map[string]any{}
			doc[k] = m
			doc = m
		default:
			return nil
		}
	}
	return doc
}
