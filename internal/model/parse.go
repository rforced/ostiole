package model

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
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
