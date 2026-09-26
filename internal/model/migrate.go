package model

import (
	"bytes"
	"encoding/json"
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
