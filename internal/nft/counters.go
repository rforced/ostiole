package nft

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Counter is the packet/byte tally of one rendered rule.
type Counter struct {
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

// Counters holds per-rule counters keyed by the "id:…" comment the renderer
// attaches, plus named baseline counters ("default-drop", "anti-lockout:lan",
// "auto-nat:wan", "port-forwards", "zone-default" are keyed by chain-qualified
// comment as "<chain>/<comment>").
type Counters map[string]Counter

// ParseCounters extracts rule counters from `nft -j list table` output.
func ParseCounters(raw []byte) (Counters, error) {
	var doc struct {
		Nftables []json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse nft json: %w", err)
	}
	out := Counters{}
	for _, item := range doc.Nftables {
		var wrapper struct {
			Rule *struct {
				Chain   string            `json:"chain"`
				Comment string            `json:"comment"`
				Expr    []json.RawMessage `json:"expr"`
			} `json:"rule"`
		}
		if err := json.Unmarshal(item, &wrapper); err != nil || wrapper.Rule == nil {
			continue
		}
		rule := wrapper.Rule
		if rule.Comment == "" {
			continue
		}
		for _, e := range rule.Expr {
			var c struct {
				Counter *Counter `json:"counter"`
			}
			if err := json.Unmarshal(e, &c); err != nil || c.Counter == nil {
				continue
			}
			key := rule.Comment
			if !strings.HasPrefix(key, "id:") {
				key = rule.Chain + "/" + key
			} else {
				key = strings.TrimPrefix(key, "id:")
			}
			// A rule that renders per family produces two nft rules with the
			// same id; sum them.
			cur := out[key]
			cur.Packets += c.Counter.Packets
			cur.Bytes += c.Counter.Bytes
			out[key] = cur
			break
		}
	}
	return out, nil
}
