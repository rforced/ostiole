package nft

import (
	"fmt"
	"slices"

	"ostiole/internal/model"
)

// BlockChain holds the rules that keep clients on this router's resolver. It
// is a chain of its own so an exempt client can return from it and carry on
// through the rest of the forward chain: returning from a base chain would
// mean the drop policy, not "carry on".
const BlockChain = "block_dns"

// DoTPort is DNS over TLS, which is easy to stop because it has a port of
// its own. DNS over HTTPS has no port of its own — it is https — so only an
// address list can catch it, which is what DoHAlias is for.
const DoTPort = 853

// enforcesDNS reports whether the forward chain needs the blocking chain at
// all. The blocks do not depend on the lists or on the DNS server: a client
// that must not use encrypted DNS must not use it whoever answers plain DNS.
func (r *renderer) enforcesDNS() bool {
	e := r.cfg.Blocking.Enforce
	return e.BlockDoT || e.DoHAlias != ""
}

// redirectsDNS reports whether client DNS is being pulled back to this
// router. It needs the DNS server on; validation refuses the combination,
// and this is the last line if something got past it.
func (r *renderer) redirectsDNS() bool {
	return r.cfg.Services.DNS.Enabled && r.cfg.Blocking.Enforce.RedirectDNS
}

// aliasMatches match dir against the named alias, one per address family
// the alias has.
func (r *renderer) aliasMatches(name, dir string) []string {
	if name == "" {
		return nil
	}
	a, ok := r.cfg.Alias(name)
	if !ok {
		return nil
	}
	v4, v6 := splitFamilies(r.entriesOf(*a))
	var out []string
	if len(v4) > 0 || a.Fetched() {
		out = append(out, fmt.Sprintf("ip %s @%s", dir, aliasSet(a.Name, 4)))
	}
	if len(v6) > 0 || a.Fetched() {
		out = append(out, fmt.Sprintf("ip6 %s @%s", dir, aliasSet(a.Name, 6)))
	}
	return out
}

// chainBlockDNS refuses the encrypted DNS a client would use to go around
// this router, with rules of its own for each enforced zone. Plain DNS is
// not refused here: it is redirected in nat_prerouting instead, so a client
// that insists on 8.8.8.8 still gets answers, just this router's answers.
func (r *renderer) chainBlockDNS() {
	if !r.enforcesDNS() {
		return
	}
	e := r.cfg.Blocking.Enforce
	doh := r.aliasMatches(e.DoHAlias, "daddr")
	r.block("chain "+BlockChain, func() {
		for _, m := range r.aliasMatches(e.ExemptClients, "saddr") {
			r.line(fmt.Sprintf(`%s counter return comment "block:exempt"`, m))
		}
		for _, m := range r.aliasMatches(e.ExemptDestinations, "daddr") {
			r.line(fmt.Sprintf(`%s counter return comment "block:exempt-dst"`, m))
		}
		for _, zone := range r.cfg.EnforcedZones() {
			if ifs := r.cfg.ZoneInterfaces(zone); len(ifs) > 0 {
				r.blockDNSZone(zone, ifs, doh)
			}
		}
	})
}

// blockDNSZone writes one zone's rejects, scoped to its interfaces, each
// behind a sampled log when the zone logs its drops.
func (r *renderer) blockDNSZone(zone string, ifs, doh []string) {
	e := r.cfg.Blocking.Enforce
	set := ifnameSet(ifs)
	logs := r.zoneNamedLogsDrops(zone)
	if e.BlockDoT {
		if logs {
			r.line(enforceLogLine(fmt.Sprintf("meta l4proto { tcp, udp } th dport %d", DoTPort), set, "block-dot", zone))
		}
		r.line(fmt.Sprintf(`iifname %s tcp dport %d counter %s comment "block:dot:%s"`, set, DoTPort, rejectTCP, zone))
		r.line(fmt.Sprintf(`iifname %s udp dport %d counter %s comment "block:dot:%s"`, set, DoTPort, rejectOther, zone))
		r.sysFor(ifs, SystemRule{
			Chain: BlockChain, Action: "reject", Protocol: string(model.ProtocolTCPUDP),
			Source: r.exemptSource(), Destination: r.exemptDestination(fmt.Sprintf("any : %d", DoTPort)),
			Description: "DNS over TLS", Log: logs,
			Keys:    []string{BlockChain + "/block:dot:" + zone},
			LogKeys: logKeys(logs, BlockChain+"/log:block-dot:"+zone),
			Setting: "enforcement",
		})
	}
	if len(doh) == 0 {
		return
	}
	for _, match := range doh {
		if logs {
			r.line(enforceLogLine(match, set, "block-doh", zone))
		}
		r.line(fmt.Sprintf(`iifname %s %s meta l4proto tcp counter %s comment "block:doh:%s"`, set, match, rejectTCP, zone))
		r.line(fmt.Sprintf(`iifname %s %s counter %s comment "block:doh:%s"`, set, match, rejectOther, zone))
	}
	r.sysFor(ifs, SystemRule{
		Chain: BlockChain, Action: "reject", Protocol: string(model.ProtocolAny),
		Source: r.exemptSource(), Destination: r.exemptDestination("@" + e.DoHAlias),
		Description: "DNS over HTTPS servers", Log: logs,
		Keys:    []string{BlockChain + "/block:doh:" + zone},
		LogKeys: logKeys(logs, BlockChain+"/log:block-doh:"+zone),
		Setting: "enforcement",
	})
}

// enforceLogLine is the sampled log in front of a zone's rejects. The prefix
// names the kind alone, as the log reader parses it; the comment adds the
// zone, so each zone counts its own.
func enforceLogLine(match, set, kind, zone string) string {
	return sampledLog(match, set, "ostiole:s:"+kind+":reject: ", "log:"+kind+":"+zone)
}

// blockDNSJump sends traffic leaving the enforced zones through the
// blocking chain. Traffic arriving from outside is not this feature's
// business.
func (r *renderer) blockDNSJump() {
	if !r.enforcesDNS() {
		return
	}
	var ifs []string
	for _, zone := range r.cfg.EnforcedZones() {
		ifs = append(ifs, r.cfg.ZoneInterfaces(zone)...)
	}
	if len(ifs) == 0 {
		return
	}
	r.line(fmt.Sprintf("iifname %s jump %s", ifnameSet(ifs), BlockChain))
}

// dnsRedirect pulls plain DNS back to this router in each enforced zone, on
// the interfaces it listens on, whoever the client meant to ask. It is the
// last thing in nat_prerouting so that a port forward the operator wrote
// wins over it.
//
// Only traffic addressed elsewhere is redirected: a query already sent to
// this router needs no translation, and leaving it alone keeps the counter
// meaning what it says.
func (r *renderer) dnsRedirect() {
	if !r.redirectsDNS() {
		return
	}
	zones := r.cfg.EnforcedZones()
	var all []string
	for _, zone := range zones {
		all = append(all, r.redirectInterfaces(zone)...)
	}
	if len(all) == 0 {
		r.line("# DNS redirect skipped: DNS listens on no internal interface")
		return
	}
	set := ifnameSet(all)
	e := r.cfg.Blocking.Enforce
	for _, m := range r.aliasMatches(e.ExemptClients, "saddr") {
		r.line(fmt.Sprintf(`iifname %s %s counter return comment "block:dns-exempt"`, set, m))
	}
	for _, m := range r.aliasMatches(e.ExemptDestinations, "daddr") {
		r.line(fmt.Sprintf(`iifname %s %s counter return comment "block:dns-exempt-dst"`, set, m))
	}
	for _, zone := range zones {
		ifs := r.redirectInterfaces(zone)
		if len(ifs) == 0 {
			continue
		}
		r.line(fmt.Sprintf(
			`iifname %s meta l4proto { tcp, udp } th dport 53 fib daddr type != local counter redirect to :53 comment "block:dns-redirect:%s"`,
			ifnameSet(ifs), zone))
		r.sysFor(ifs, SystemRule{
			Chain: "nat_prerouting", Action: "redirect", Protocol: string(model.ProtocolTCPUDP),
			Source: r.exemptSource(), Destination: r.exemptDestination("not this router : 53"),
			Description: "Forward DNS queries to this router",
			Keys:        []string{"nat_prerouting/block:dns-redirect:" + zone}, Setting: "enforcement",
		})
	}
}

// redirectInterfaces are the zone's interfaces the DNS server listens on. A
// query redirected anywhere else reaches a port nothing answers on, and the
// client loses the DNS it had.
func (r *renderer) redirectInterfaces(zone string) []string {
	listens := DNSInterfaces(r.cfg)
	var out []string
	for _, name := range r.cfg.ZoneInterfaces(zone) {
		if slices.Contains(listens, name) {
			out = append(out, name)
		}
	}
	return out
}
