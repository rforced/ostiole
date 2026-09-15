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
	Time     time.Time `json:"time"`
	Prefix   string    `json:"prefix"`
	RuleID   string    `json:"ruleId,omitempty"`
	Zone     string    `json:"zone,omitempty"`
	Kind     string    `json:"kind"` // rule, zone-drop, default-drop, other
	InIface  string    `json:"in,omitempty"`
	OutIface string    `json:"out,omitempty"`
	Family   string    `json:"family"` // ipv4, ipv6, other
	Proto    string    `json:"proto"`  // tcp, udp, icmp, icmpv6, or a number
	Src      string    `json:"src,omitempty"`
	Dst      string    `json:"dst,omitempty"`
	SrcPort  uint16    `json:"srcPort,omitempty"`
	DstPort  uint16    `json:"dstPort,omitempty"`
	ICMPType uint8     `json:"icmpType,omitempty"`
	TCPFlags string    `json:"tcpFlags,omitempty"`
	Length   int       `json:"length"`
}

// ParsePrefix splits an nft log prefix the renderer produced:
// "ostiole:<rule-id>: ", "ostiole:<zone>:drop: ", "ostiole:<chain>:drop: ".
func ParsePrefix(prefix string) (ruleID, zone, kind string) {
	p := strings.TrimSpace(prefix)
	p = strings.TrimSuffix(p, ":")
	if !strings.HasPrefix(p, "ostiole:") {
		return "", "", "other"
	}
	rest := strings.TrimPrefix(p, "ostiole:")
	parts := strings.Split(rest, ":")
	switch {
	case len(parts) == 2 && parts[1] == "drop":
		if parts[0] == "input" || parts[0] == "forward" {
			return "", "", "default-drop"
		}
		return "", parts[0], "zone-drop"
	case len(parts) == 1 && parts[0] != "":
		return parts[0], "", "rule"
	}
	return "", "", "other"
}

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
	for i := 0; i < 8; i++ {
		switch next {
		case 0, 43, 60: // hop-by-hop, routing, destination options
			if len(rest) < 8 {
				return
			}
			l := int(rest[1]+1) * 8
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
			e.ICMPType = b[0]
		}
	case 58:
		e.Proto = "icmpv6"
		if !fragment && len(b) >= 1 {
			e.ICMPType = b[0]
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
