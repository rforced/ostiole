// Package fwlog receives packets the firewall logs through nflog, decodes
// the headers that matter to an admin, and keeps a ring of recent entries
// that the API streams to the UI.
package fwlog

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"time"
)

// Entry is one logged packet.
type Entry struct {
	Time   time.Time `json:"time"`
	Prefix string    `json:"prefix"`
	RuleID string    `json:"ruleId,omitempty"`
	Zone   string    `json:"zone,omitempty"`
	// Kind is what matched: rule, zone-drop, default-drop, block-private,
	// block-bogons, one of SystemKinds, or other. Action is what happened to
	// the packet: accept, drop, reject, or empty when the prefix does not say.
	Kind     string `json:"kind"`
	Action   string `json:"action,omitempty"`
	InIface  string `json:"in,omitempty"`
	OutIface string `json:"out,omitempty"`
	Family   string `json:"family"` // ipv4, ipv6, other
	Proto    string `json:"proto"`  // tcp, udp, icmp, icmpv6, or a number
	Src      string `json:"src,omitempty"`
	Dst      string `json:"dst,omitempty"`
	SrcPort  uint16 `json:"srcPort,omitempty"`
	DstPort  uint16 `json:"dstPort,omitempty"`
	// ICMPType is nil when the packet did not carry one, so an echo reply
	// (type 0) is not mistaken for no type at all.
	ICMPType *uint8 `json:"icmpType,omitempty"`
	TCPFlags string `json:"tcpFlags,omitempty"`
	Length   int    `json:"length"`
}

// SystemKinds are the drops Ostiole makes on its own account, as the "s"
// prefix shape spells them. They carry no zone: three parts leave no room
// for one, and the interface the kernel reports names it well enough.
var SystemKinds = map[string]bool{
	"block-dot":         true,
	"block-doh":         true,
	"protect-scanner":   true,
	"protect-synflood":  true,
	"protect-icmpflood": true,
}

// ParsePrefix splits an nft log prefix the renderer produced. Each shape
// names what matched, so the verdict can be read straight back off the
// packet instead of being guessed from the configuration as it stands now:
//
//	ostiole:r:<rule-id>:<accept|drop|reject>:
//	ostiole:z:<zone>:drop:
//	ostiole:c:<chain>:<drop|block-private|block-bogons>:
//	ostiole:s:<system-kind>:<accept|drop|reject>:
func ParsePrefix(prefix string) (ruleID, zone, kind, action string) {
	p := strings.TrimSpace(prefix)
	p = strings.TrimSuffix(p, ":")
	rest, ok := strings.CutPrefix(p, "ostiole:")
	if !ok {
		return "", "", "other", ""
	}
	// No identifier the renderer puts in a prefix may contain a colon, so
	// the tagged shapes are always three parts.
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return legacyPrefix(parts)
	}
	switch parts[0] {
	case "r":
		if parts[1] != "" && isAction(parts[2]) {
			return parts[1], "", "rule", parts[2]
		}
	case "z":
		if parts[1] != "" && parts[2] == "drop" {
			return "", parts[1], "zone-drop", "drop"
		}
	case "c":
		switch parts[2] {
		case "drop":
			return "", "", "default-drop", "drop"
		case "block-private", "block-bogons":
			return "", "", parts[2], "drop"
		}
	case "s":
		if SystemKinds[parts[1]] && isAction(parts[2]) {
			return "", "", parts[1], parts[2]
		}
	}
	return "", "", "other", ""
}

// legacyPrefix reads the untagged shapes written before the verdict was
// recorded. A router that has not applied since the upgrade is running the
// stored ruleset, which still spells them this way; its rule entries have
// no action to report.
func legacyPrefix(parts []string) (ruleID, zone, kind, action string) {
	switch {
	case len(parts) == 2 && parts[1] == "drop":
		if parts[0] == "input" || parts[0] == "forward" {
			return "", "", "default-drop", "drop"
		}
		return "", parts[0], "zone-drop", "drop"
	case len(parts) == 2 && (parts[1] == "block-private" || parts[1] == "block-bogons"):
		return "", "", parts[1], "drop"
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", "rule", ""
	}
	return "", "", "other", ""
}

func isAction(s string) bool { return s == "accept" || s == "drop" || s == "reject" }

// Decode fills the packet fields of e from a raw IP packet.
func Decode(e *Entry, payload []byte) {
	e.Length = len(payload)
	if len(payload) < 1 {
		e.Family = "other"
		return
	}
	switch payload[0] >> 4 {
	case 4:
		decodeIPv4(e, payload)
	case 6:
		decodeIPv6(e, payload)
	default:
		e.Family = "other"
	}
}

func decodeIPv4(e *Entry, b []byte) {
	e.Family = "ipv4"
	if len(b) < 20 {
		return
	}
	ihl := int(b[0]&0x0f) * 4
	if ihl < 20 || len(b) < ihl {
		return
	}
	e.Src = netip.AddrFrom4([4]byte(b[12:16])).String()
	e.Dst = netip.AddrFrom4([4]byte(b[16:20])).String()
	fragOffset := binary.BigEndian.Uint16(b[6:8]) & 0x1fff
	decodeL4(e, b[9], b[ihl:], fragOffset != 0)
}

func decodeIPv6(e *Entry, b []byte) {
	e.Family = "ipv6"
	if len(b) < 40 {
		return
	}
	e.Src = netip.AddrFrom16([16]byte(b[8:24])).String()
	e.Dst = netip.AddrFrom16([16]byte(b[24:40])).String()
	next := b[6]
	rest := b[40:]
	// Skip the common extension headers to reach the transport header.
	for range 8 {
		switch next {
		case 0, 43, 60: // hop-by-hop, routing, destination options
			if len(rest) < 8 {
				return
			}
			l := (int(rest[1]) + 1) * 8
			if len(rest) < l {
				return
			}
			next, rest = rest[0], rest[l:]
			continue
		case 44: // fragment
			if len(rest) < 8 {
				return
			}
			next, rest = rest[0], rest[8:]
			decodeL4(e, next, rest, true)
			return
		}
		break
	}
	decodeL4(e, next, rest, false)
}

func decodeL4(e *Entry, proto byte, b []byte, fragment bool) {
	switch proto {
	case 6:
		e.Proto = "tcp"
		if fragment || len(b) < 14 {
			return
		}
		e.SrcPort = binary.BigEndian.Uint16(b[0:2])
		e.DstPort = binary.BigEndian.Uint16(b[2:4])
		e.TCPFlags = tcpFlags(b[13])
	case 17:
		e.Proto = "udp"
		if fragment || len(b) < 4 {
			return
		}
		e.SrcPort = binary.BigEndian.Uint16(b[0:2])
		e.DstPort = binary.BigEndian.Uint16(b[2:4])
	case 1:
		e.Proto = "icmp"
		if !fragment && len(b) >= 1 {
			e.ICMPType = new(b[0])
		}
	case 58:
		e.Proto = "icmpv6"
		if !fragment && len(b) >= 1 {
			e.ICMPType = new(b[0])
		}
	default:
		e.Proto = protoName(proto)
	}
}

func tcpFlags(f byte) string {
	names := []string{"FIN", "SYN", "RST", "PSH", "ACK", "URG"}
	var out []string
	for i, n := range names {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return strings.Join(out, ",")
}

func protoName(p byte) string {
	switch p {
	case 2:
		return "igmp"
	case 47:
		return "gre"
	case 50:
		return "esp"
	case 51:
		return "ah"
	case 132:
		return "sctp"
	}
	return "proto-" + itoa(int(p))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [4]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}
