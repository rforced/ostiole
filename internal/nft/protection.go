package nft

import (
	"fmt"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// The edge defence, and the per-rule limits that use the same machinery.
//
// All of it rests on one nftables feature: a set with `flags dynamic` can
// be added to from the packet path, and an element can carry a limiter of
// its own. `add @set { ip saddr limit rate over 30/second }` therefore
// means "count this source, and tell me whether it has gone too fast" in
// one expression, with the kernel keeping a counter per source and ageing
// it out when the source goes quiet. Nothing has to be reset, nothing is
// counted in userspace, and a flood from one address cannot spend the
// allowance of every other address.
//
// The three defences sit at the head of a zone's chain, ahead of the
// operator's rules, because a flood should not be matched against forty
// rules before it is dropped. The port scan detector is the exception: it
// sits at the tail, where everything nothing allowed arrives, which is
// exactly what a scan looks like.

// protectionSetSize bounds each dynamic set. The elements are source
// addresses, and they expire, so this is a ceiling on how many sources
// can be misbehaving at once rather than a limit on the network.
const protectionSetSize = 65535

// floodTimeout is how long a per-source counter is kept after the source
// goes quiet. Long enough to catch a flood that pauses, short enough that
// a set does not fill with addresses that sent one packet.
const floodTimeout = "1m"

// Set names, per zone and family. The zone is in the name because the
// limits are per zone: a flood on the WAN and a flood on a guest network
// are different questions with different answers.
func synFloodSet(zone string, family int) string {
	return fmt.Sprintf("synflood_%s_v%d", zone, family)
}

func icmpFloodSet(zone string, family int) string {
	return fmt.Sprintf("icmpflood_%s_v%d", zone, family)
}

// scanCountSet holds the tally of refused packets per source; scanHoldSet
// holds the sources that went over it. Two sets, because they answer two
// questions: "how fast is this source being refused" and "is this source
// in the doghouse", and the second has to outlive the first.
func scanCountSet(zone string, family int) string {
	return fmt.Sprintf("scan_%s_v%d", zone, family)
}

func scanHoldSet(zone string, family int) string {
	return fmt.Sprintf("scanners_%s_v%d", zone, family)
}

// rateLimitSet holds the per-source tally of one rule.
func rateLimitSet(id string, family int) string {
	return fmt.Sprintf("rlimit_%s_v%d", id, family)
}

// families is the two address families with the expression prefix each
// one is matched by. In an inet table the prefix is what decides which
// packets a rule sees, so one rule per family is how anything per-source
// is written.
var families = []struct {
	n      int
	prefix string
}{{4, "ip"}, {6, "ip6"}}

// protectionSets defines the dynamic sets the defence needs. A set is
// declared per protected zone and family whether or not today's traffic
// fills it: the rules refer to it by name, and nftables will not load a
// rule that names a set it does not have.
func (r *renderer) protectionSets() {
	p := r.cfg.Protection
	for _, zone := range r.cfg.ProtectedZones() {
		for _, fam := range families {
			if p.SynFlood != nil {
				r.dynamicSet(synFloodSet(zone, fam.n), fam.n, floodTimeout,
					"new connections per source on "+zone)
			}
			if p.ICMPFlood != nil {
				r.dynamicSet(icmpFloodSet(zone, fam.n), fam.n, floodTimeout,
					"echo requests per source on "+zone)
			}
			if p.PortScan != nil {
				r.dynamicSet(scanCountSet(zone, fam.n), fam.n, floodTimeout,
					"refused packets per source on "+zone)
				r.dynamicSet(scanHoldSet(zone, fam.n), fam.n, p.PortScan.HoldOr(),
					"sources held for scanning "+zone)
			}
		}
	}
	// A rule's own limit needs a set of its own, for the same reason.
	for _, rule := range r.cfg.Rules {
		if rule.Limit == nil || !rule.Limit.PerSource || !rule.Enabled {
			continue
		}
		for _, fam := range families {
			r.dynamicSet(rateLimitSet(rule.ID, fam.n), fam.n, floodTimeout,
				"rate per source for rule "+rule.ID)
		}
	}
}

// dynamicSet declares a set the packet path adds to. The timeout is what
// keeps it bounded: an element that nothing has touched for that long is
// collected by the kernel.
func (r *renderer) dynamicSet(name string, family int, timeout, comment string) {
	typ := "ipv4_addr"
	if family == 6 {
		typ = "ipv6_addr"
	}
	r.block("set "+name, func() {
		r.line("type " + typ)
		r.line(fmt.Sprintf("size %d", protectionSetSize))
		r.line("flags dynamic,timeout")
		r.line("timeout " + timeout)
		r.line(fmt.Sprintf("comment %q", sanitizeComment(comment)))
	})
}

// limitOver is the expression that counts a source and matches when it
// has gone too fast. `over` is the only direction nftables offers here,
// which is why every use of it drops rather than accepts.
func limitOver(l model.RateLimit) string {
	out := fmt.Sprintf("limit rate over %d/%s", l.Rate, l.Unit.Or())
	if l.Burst > 0 {
		out += fmt.Sprintf(" burst %d packets", l.Burst)
	}
	return out
}

// protectZone writes the defence at the head of a zone's chain: the
// sources already held, then the two floods. Order is the point. A held
// source is dropped before anything else is evaluated, and a flood is
// dropped before the operator's rules are walked.
func (r *renderer) protectZone(zone string) {
	p := r.cfg.Protection
	if !p.On() || !r.protects(zone) {
		return
	}
	if p.PortScan != nil {
		for _, fam := range families {
			r.line(fmt.Sprintf(`%s saddr @%s counter drop comment "protect:scanner"`,
				fam.prefix, scanHoldSet(zone, fam.n)))
		}
		r.sys(SystemRule{
			Chain: "zone_" + zone, Zones: []string{zone}, Action: "drop",
			Protocol:    string(model.ProtocolAny),
			Source:      fmt.Sprintf("a source refused more than %s", p.PortScan.Limit()),
			Destination: "any",
			Description: "Drop a source that was scanning, for " + p.PortScan.HoldOr(),
			Keys:        []string{"zone_" + zone + "/protect:scanner"}, Setting: "protection",
		})
	}
	if p.SynFlood != nil {
		for _, fam := range families {
			// ct state new rather than the SYN flag: a flood of bare ACKs is
			// already dropped as invalid by the state rule in the base
			// chain, and this way the limit counts connections, which is
			// what the setting says it counts.
			r.line(fmt.Sprintf(`ct state new add @%s { %s saddr %s } counter drop comment "protect:synflood"`,
				synFloodSet(zone, fam.n), fam.prefix, limitOver(*p.SynFlood)))
		}
		r.sys(SystemRule{
			Chain: "zone_" + zone, Zones: []string{zone}, Action: "drop",
			Protocol:    string(model.ProtocolAny),
			Source:      "a source opening connections faster than " + p.SynFlood.String(),
			Destination: "any",
			Description: "Drop the new connections over the limit",
			Keys:        []string{"zone_" + zone + "/protect:synflood"}, Setting: "protection",
		})
	}
	if p.ICMPFlood != nil {
		for _, fam := range []struct {
			n      int
			prefix string
			proto  string
		}{{4, "ip", "icmp"}, {6, "ip6", "icmpv6"}} {
			r.line(fmt.Sprintf(`%s type echo-request add @%s { %s saddr %s } counter drop comment "protect:icmpflood"`,
				fam.proto, icmpFloodSet(zone, fam.n), fam.prefix, limitOver(*p.ICMPFlood)))
		}
		r.sys(SystemRule{
			Chain: "zone_" + zone, Zones: []string{zone}, Action: "drop",
			Protocol:    string(model.ProtocolICMP),
			Source:      "a source pinging faster than " + p.ICMPFlood.String(),
			Destination: "any",
			Description: "Drop the echo requests over the limit",
			Keys:        []string{"zone_" + zone + "/protect:icmpflood"}, Setting: "protection",
		})
	}
}

// scanTally sits at the tail of a zone's chain, where every packet no
// rule allowed arrives. It counts those per source and, for a source that
// keeps arriving there, writes the address into the set the head of the
// chain drops on. The verdict is left to the tail that follows: this
// rule's job is to notice, not to decide.
func (r *renderer) scanTally(zone string) {
	p := r.cfg.Protection.PortScan
	if p == nil || !r.protects(zone) {
		return
	}
	for _, fam := range families {
		r.line(fmt.Sprintf(`add @%s { %s saddr %s } add @%s { %s saddr } counter comment "protect:scan"`,
			scanCountSet(zone, fam.n), fam.prefix, limitOver(p.Limit()),
			scanHoldSet(zone, fam.n), fam.prefix))
	}
	r.sys(SystemRule{
		Chain: "zone_" + zone, After: true, Zones: []string{zone}, Action: "continue",
		Protocol:    string(model.ProtocolAny),
		Source:      "a source refused more than " + p.Limit().String(),
		Destination: "any",
		Description: "Hold a source that keeps knocking on closed ports, for " + p.HoldOr(),
		Keys:        []string{"zone_" + zone + "/protect:scan"}, Setting: "protection",
	})
}

// protects reports whether a zone is defended.
func (r *renderer) protects(zone string) bool {
	for _, z := range r.cfg.ProtectedZones() {
		if z == zone {
			return true
		}
	}
	return false
}

// ruleLimit is how a rule's own limit is written.
//
// Without PerSource it is one expression in the rule itself: `limit rate`
// matches while the traffic is under the rate, so the rule accepts up to
// it and everything past it falls through to whatever comes next — the
// drop at the end of the zone, usually.
//
// With PerSource there is no "under" to match on, so it takes two rules:
// this one drops what one source has sent too much of, and the rule
// itself accepts the rest.
func (r *renderer) ruleLimit(rule *model.Rule, m match) {
	l := rule.Limit
	if l == nil || !l.PerSource {
		return
	}
	for _, fam := range families {
		src, sok := m.src.expr(fam.n)
		dst, dok := m.dst.expr(fam.n)
		if !sok || !dok {
			// This rule cannot match this family at all, so neither can its
			// limit.
			continue
		}
		parts := append([]string{}, m.prefix...)
		for _, a := range []string{src, dst} {
			if a != "" {
				parts = append(parts, a)
			}
		}
		if m.l4 != "" {
			parts = append(parts, m.l4)
		}
		parts = append(parts,
			fmt.Sprintf("add @%s { %s saddr %s }", rateLimitSet(rule.ID, fam.n), fam.prefix, limitOver(*l)),
			"counter", "drop", fmt.Sprintf("comment %q", "limit:"+rule.ID))
		r.line(strings.Join(parts, " "))
	}
}

// limitExpr is the in-rule form, for a limit that is not per source.
func limitExpr(l *model.RateLimit) []string {
	if l == nil || l.PerSource {
		return nil
	}
	out := fmt.Sprintf("limit rate %d/%s", l.Rate, l.Unit.Or())
	if l.Burst > 0 {
		out += fmt.Sprintf(" burst %d packets", l.Burst)
	}
	return []string{out}
}
