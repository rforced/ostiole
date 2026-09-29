package model

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	// nameRe covers zone and alias names. They become nftables identifiers.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	// idRe covers rule, NAT, and route IDs. They end up inside nftables
	// comments and log prefixes, so quotes and spaces are excluded.
	idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,39}$`)
	// ifaceRe follows IFNAMSIZ (15 chars) and the kernel's character rules.
	ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)
	// hostnameRe is a conservative RFC 1123 label sequence.
	hostnameRe = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)
	// labelRe is one RFC 1123 label, what a name is between the dots.
	labelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// PortRange is an inclusive port interval. A single port has Lo == Hi.
type PortRange struct {
	Lo, Hi uint16
}

// String renders the range in nftables syntax.
func (p PortRange) String() string {
	if p.Lo == p.Hi {
		return strconv.Itoa(int(p.Lo))
	}
	return fmt.Sprintf("%d-%d", p.Lo, p.Hi)
}

// ParsePortRange accepts "443" or "8000-8100".
func ParsePortRange(s string) (PortRange, error) {
	s = strings.TrimSpace(s)
	lo, hi, found := strings.Cut(s, "-")
	a, err := parsePort(lo)
	if err != nil {
		return PortRange{}, err
	}
	if !found {
		return PortRange{Lo: a, Hi: a}, nil
	}
	b, err := parsePort(hi)
	if err != nil {
		return PortRange{}, err
	}
	if b < a {
		return PortRange{}, fmt.Errorf("invalid port range %q: end before start", s)
	}
	return PortRange{Lo: a, Hi: b}, nil
}

func parsePort(s string) (uint16, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid port %q: must be 1-65535", s)
	}
	return uint16(n), nil
}

// MaxSelect is how many conditions an alias's select may hold, and
// MaxSelectFields how many different fields they may name, the bare key
// names counting as one. Picking a hundred regions from a list is
// reasonable; a thousand, or forty fields, is a mistake.
const (
	MaxSelect       = 256
	MaxSelectFields = 32
)

// Condition is one line of an alias's select: a field and the value it
// must hold, or, with no field, a key the address must be listed under.
type Condition struct {
	Field, Value string
}

// String is the condition as it is written in the configuration.
func (c Condition) String() string {
	if c.Field == "" {
		return c.Value
	}
	return c.Field + "=" + c.Value
}

// ParseCondition accepts "field=value" or a bare key name. Only the value
// or the key may hold a *; a field name is matched exactly.
func ParseCondition(s string) (Condition, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return Condition{}, errors.New("empty condition")
	case len(s) > 128:
		return Condition{}, errors.New("a condition may be at most 128 characters")
	case strings.ContainsFunc(s, unicode.IsControl):
		return Condition{}, fmt.Errorf("condition %q holds a control character", s)
	}
	field, value, found := strings.Cut(s, "=")
	if !found {
		return Condition{Value: s}, nil
	}
	field, value = strings.TrimSpace(field), strings.TrimSpace(value)
	if field == "" || value == "" {
		return Condition{}, fmt.Errorf("condition %q needs a field and a value, like region=us-ashburn-1", s)
	}
	if strings.Contains(field, "*") {
		return Condition{}, fmt.Errorf("condition %q: * matches in a value, not in a field name", s)
	}
	return Condition{Field: field, Value: value}, nil
}

// MaxASN is the largest four-byte AS number.
const MaxASN = 1<<32 - 1

// ParseASN accepts "AS15169", "as15169", or "15169". Private ranges are
// let through: they announce nothing publicly, so the fetch says so, and
// somebody pointing at their own mirror is entitled to use them.
func ParseASN(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	digits := s
	if len(s) >= 2 && strings.EqualFold(s[:2], "AS") {
		digits = s[2:]
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid AS number %q: must be AS1-AS%d", s, uint64(MaxASN))
	}
	return uint32(n), nil
}

// FormatASN is the canonical written form, "AS15169".
func FormatASN(n uint32) string {
	return "AS" + strconv.FormatUint(uint64(n), 10)
}

// ParseAddress accepts a single IP ("10.0.0.1", "2001:db8::1") or a CIDR
// prefix and returns a canonical prefix (single IPs get a full-length mask).
func ParseAddress(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid address %q: want an IP or CIDR", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// ParseIP accepts a bare IP address only.
func ParseIP(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q", s)
	}
	return a, nil
}

// NormalizeDomain lowercases a domain and drops the space around it and the
// trailing dot, so the same name written three ways compares equal.
func NormalizeDomain(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), "."))
}

// ParseDNSServer accepts a resolver in dnsmasq's syntax: a bare IP, or an
// IP and a port joined by a hash, like 10.0.0.1#5353. The hash rather than
// a colon is what lets an IPv6 address carry a port without brackets.
func ParseDNSServer(s string) (netip.AddrPort, error) {
	s = strings.TrimSpace(s)
	host, port, found := strings.Cut(s, "#")
	a, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("invalid DNS server %q: want an IP, optionally with #port", s)
	}
	p := uint16(53)
	if found {
		if p, err = parsePort(port); err != nil {
			return netip.AddrPort{}, fmt.Errorf("invalid DNS server %q: %w", s, err)
		}
	}
	return netip.AddrPortFrom(a, p), nil
}

// ParseClock reads "HH:MM" and returns minutes since midnight.
func ParseClock(s string) (int, error) {
	hh, mm, found := strings.Cut(strings.TrimSpace(s), ":")
	h, herr := strconv.Atoi(hh)
	m, merr := strconv.Atoi(mm)
	if !found || herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("invalid time %q: want HH:MM", s)
	}
	return h*60 + m, nil
}

// Clock renders minutes since midnight back as "HH:MM".
func Clock(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60%24, minutes%60)
}

// weekdays maps the names a schedule may use to the capitalised form
// nftables expects.
var weekdays = map[string]string{
	"monday": "Monday", "tuesday": "Tuesday", "wednesday": "Wednesday",
	"thursday": "Thursday", "friday": "Friday", "saturday": "Saturday", "sunday": "Sunday",
}

// Weekday canonicalises a day name for nftables, case insensitively.
func Weekday(s string) (string, bool) {
	day, ok := weekdays[strings.ToLower(strings.TrimSpace(s))]
	return day, ok
}
