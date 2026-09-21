package dnslog

import (
	"net/netip"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Entry is one answer the resolver gave. It is deliberately small: the
// ring runs to ten million, where one more word costs eighty megabytes,
// and a device name would go stale, so names are put on at read time
// instead.
type Entry struct {
	Seq    uint64
	Time   time.Time
	Client netip.Addr
	Name   string
	Type   uint16
	Status Status
	// Reason is why a blocked name was blocked.
	Reason Reason
	// Lists is one bit per interned list name; see Log.ListNames.
	Lists uint64
	// Answer is the first address answered, for an answered query.
	Answer netip.Addr
}

// Status is what became of one question.
type Status uint8

// The statuses. StatusNone is not one: it is what an unset filter matches.
const (
	StatusNone Status = iota
	StatusOK
	StatusBlocked
	StatusNXDomain
	StatusNoData
	StatusServFail
	StatusRefused
	StatusOther
)

var statusNames = [...]string{"", "ok", "blocked", "nxdomain", "nodata", "servfail", "refused", "other"}

func (s Status) String() string {
	if int(s) >= len(statusNames) {
		return "other"
	}
	return statusNames[s]
}

// ParseStatus reads a status the API was asked to filter by.
func ParseStatus(s string) (Status, bool) {
	if s == "" {
		return StatusNone, true
	}
	for i, name := range statusNames {
		if i > 0 && name == s {
			return Status(i), true
		}
	}
	return StatusNone, false
}

// Reason is what decided a blocked name, matching dnsblock's reasons.
type Reason uint8

// The reasons a name is refused.
const (
	ReasonNone Reason = iota
	ReasonList
	ReasonDeny
	ReasonCanary
)

var reasonNames = [...]string{"", "list", "deny", "canary"}

func (r Reason) String() string {
	if int(r) >= len(reasonNames) {
		return ""
	}
	return reasonNames[r]
}

// extraTypeNames are the record types dnsmessage has no name for. HTTPS is
// the one that matters: a browser asks for it before it connects, so a
// blocked name that only shows A and AAAA would look half-blocked.
var extraTypeNames = map[uint16]string{
	43:  "DS",
	44:  "SSHFP",
	46:  "RRSIG",
	47:  "NSEC",
	48:  "DNSKEY",
	52:  "TLSA",
	64:  "SVCB",
	65:  "HTTPS",
	257: "CAA",
}

// TypeName names a record type: dnsmessage's own name without its "Type"
// prefix, one of the types it does not know, or the number.
func TypeName(t uint16) string {
	if name, ok := extraTypeNames[t]; ok {
		return name
	}
	name := dnsmessage.Type(t).String()
	if s, ok := strings.CutPrefix(name, "Type"); ok {
		return s
	}
	return strconv.Itoa(int(t))
}

// ParseType reads a record type the API was asked to filter by. Zero is
// every type.
func ParseType(s string) (uint16, bool) {
	if s == "" {
		return 0, true
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	for t, name := range extraTypeNames {
		if name == s {
			return t, true
		}
	}
	// dnsmessage keeps no reverse table, and there are fifteen of them.
	for t := uint16(1); t <= 255; t++ {
		if name, ok := strings.CutPrefix(dnsmessage.Type(t).String(), "Type"); ok && name == s {
			return t, true
		}
	}
	if n, err := strconv.ParseUint(s, 10, 16); err == nil && n > 0 {
		return uint16(n), true
	}
	return 0, false
}
