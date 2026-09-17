package nft

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Mapping is one hole a client opened for itself through UPnP IGD, PCP, or
// NAT-PMP, read back out of the chain the daemon fills in.
//
// The rules are the source rather than a lease file because `lease_file` is
// a compile-time option and the builds we meet do not all have it: Fedora's
// does not, and miniupnpd rejects its whole configuration over one option it
// was not built with (ADR-0007). What the rules cannot say is the
// description the client sent and the lifetime it asked for, which the
// daemon keeps to itself.
type Mapping struct {
	Protocol     string `json:"protocol"`
	Internal     string `json:"internal"`
	ExternalPort int    `json:"externalPort"`
	InternalPort int    `json:"internalPort"`
}

// ParseMappings extracts the port mappings from `nft -j list table` output.
func ParseMappings(raw []byte) ([]Mapping, error) {
	var doc struct {
		Nftables []json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse nft json: %w", err)
	}
	out := []Mapping{}
	for _, item := range doc.Nftables {
		var wrapper struct {
			Rule *struct {
				Chain string            `json:"chain"`
				Expr  []json.RawMessage `json:"expr"`
			} `json:"rule"`
		}
		if err := json.Unmarshal(item, &wrapper); err != nil || wrapper.Rule == nil {
			continue
		}
		if wrapper.Rule.Chain != UPnPPreroutingChain {
			continue
		}
		if m, ok := mappingOf(wrapper.Rule.Expr); ok {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExternalPort != out[j].ExternalPort {
			return out[i].ExternalPort < out[j].ExternalPort
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out, nil
}

// mappingOf reads one rule: a destination port to match on and a dnat to
// send it to. Anything else in the chain, including whatever a future
// version of the daemon adds, is passed over rather than half-read.
func mappingOf(exprs []json.RawMessage) (Mapping, bool) {
	var m Mapping
	for _, e := range exprs {
		if addr, port, ok := dnatOf(e); ok {
			m.Internal, m.InternalPort = addr, port
			continue
		}
		var match struct {
			Match *struct {
				Left struct {
					Payload *struct {
						Protocol string `json:"protocol"`
						Field    string `json:"field"`
					} `json:"payload"`
					Meta *struct {
						Key string `json:"key"`
					} `json:"meta"`
				} `json:"left"`
				Right json.RawMessage `json:"right"`
			} `json:"match"`
		}
		if err := json.Unmarshal(e, &match); err != nil || match.Match == nil {
			continue
		}
		left := match.Match.Left
		switch {
		case left.Payload != nil && left.Payload.Field == "dport":
			m.ExternalPort = jsonPort(match.Match.Right)
			if p := left.Payload.Protocol; p == "tcp" || p == "udp" {
				m.Protocol = strings.ToUpper(p)
			}
		case left.Meta != nil && left.Meta.Key == "l4proto":
			if p := jsonText(match.Match.Right); p == "tcp" || p == "udp" {
				m.Protocol = strings.ToUpper(p)
			}
		}
	}
	if m.Internal == "" || m.ExternalPort == 0 {
		return Mapping{}, false
	}
	if m.InternalPort == 0 {
		// A dnat that names only an address keeps the port it arrived on.
		m.InternalPort = m.ExternalPort
	}
	return m, true
}

func dnatOf(e json.RawMessage) (addr string, port int, ok bool) {
	var wrapper struct {
		DNAT *struct {
			Addr json.RawMessage `json:"addr"`
			Port json.RawMessage `json:"port"`
		} `json:"dnat"`
	}
	if err := json.Unmarshal(e, &wrapper); err != nil || wrapper.DNAT == nil {
		return "", 0, false
	}
	addr = jsonText(wrapper.DNAT.Addr)
	if addr == "" {
		return "", 0, false
	}
	return addr, jsonPort(wrapper.DNAT.Port), true
}

// jsonText is a value nft wrote as a string, or nothing when it wrote
// something more elaborate: a set, a range, or a nested expression.
func jsonText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// jsonPort is a port nft wrote as a number, or as a service name in a
// string. Zero means it was neither, which drops the rule.
func jsonPort(raw json.RawMessage) int {
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		if n < 1 || n > 65535 {
			return 0
		}
		return n
	}
	n, err := strconv.Atoi(jsonText(raw))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}
