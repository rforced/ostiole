package nft

import (
	"fmt"

	"github.com/rforced/ostiole/internal/model"
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
// all. The drops do not depend on the lists or on the DNS server: a client
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

// exemptMatches are the "this client is allowed to resolve for itself"
// matches, one per address family the alias has.
func (r *renderer) exemptMatches(dir string) []string {
	name := r.cfg.Blocking.Enforce.ExemptAlias
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

// chainBlockDNS drops the encrypted DNS a client would use to go around
// this router. Plain DNS is not dropped here: it is redirected in
// nat_prerouting instead, so a client that insists on 8.8.8.8 still gets
// answers, just this router's answers.
func (r *renderer) chainBlockDNS() {
	if !r.enforcesDNS() {
		return
	}
	e := r.cfg.Blocking.Enforce
	// The rows are scoped to where forward jumps here from; the exempt
	// clients return first, so to the reader they are left out of the source.
	internal := r.internalInterfaces()
	r.block("chain "+BlockChain, func() {
		for _, m := range r.exemptMatches("saddr") {
			r.line(fmt.Sprintf(`%s counter return comment "block:exempt"`, m))
		}
		if e.BlockDoT {
			r.line(fmt.Sprintf(`meta l4proto { tcp, udp } th dport %d counter drop comment "block:dot"`, DoTPort))
			r.sysFor(internal, SystemRule{
				Chain: BlockChain, Action: "drop", Protocol: string(model.ProtocolTCPUDP),
				Source: r.exemptSource(), Destination: fmt.Sprintf("any : %d", DoTPort),
				Description: "DNS over TLS", Keys: []string{BlockChain + "/block:dot"}, Setting: "enforcement",
			})
		}
		if e.DoHAlias != "" {
			if a, ok := r.cfg.Alias(e.DoHAlias); ok {
				v4, v6 := splitFamilies(r.entriesOf(*a))
				emitted := false
				if len(v4) > 0 || a.Fetched() {
					r.line(fmt.Sprintf(`ip daddr @%s counter drop comment "block:doh"`, aliasSet(a.Name, 4)))
					emitted = true
				}
				if len(v6) > 0 || a.Fetched() {
					r.line(fmt.Sprintf(`ip6 daddr @%s counter drop comment "block:doh"`, aliasSet(a.Name, 6)))
					emitted = true
				}
				if emitted {
					r.sysFor(internal, SystemRule{
						Chain: BlockChain, Action: "drop", Protocol: string(model.ProtocolAny),
						Source: r.exemptSource(), Destination: "@" + a.Name,
						Description: "DNS over HTTPS servers", Keys: []string{BlockChain + "/block:doh"}, Setting: "enforcement",
					})
				}
			}
		}
	})
}

// blockDNSJump sends traffic leaving internal zones through the blocking
// chain. Traffic arriving from outside is not this feature's business.
func (r *renderer) blockDNSJump() {
	if !r.enforcesDNS() {
		return
	}
	ifs := r.internalInterfaces()
	if len(ifs) == 0 {
		return
	}
	r.line(fmt.Sprintf("iifname %s jump %s", ifnameSet(ifs), BlockChain))
}

// dnsRedirect pulls plain DNS from internal zones back to this router,
// whoever the client meant to ask. It is the last thing in nat_prerouting
// so that a port forward the operator wrote wins over it.
//
// Only traffic addressed elsewhere is redirected: a query already sent to
// this router needs no translation, and leaving it alone keeps the counter
// meaning what it says.
func (r *renderer) dnsRedirect() {
	if !r.redirectsDNS() {
		return
	}
	ifs := r.internalInterfaces()
	if len(ifs) == 0 {
		r.line("# DNS redirect skipped: no internal interfaces")
		return
	}
	set := ifnameSet(ifs)
	for _, m := range r.exemptMatches("saddr") {
		r.line(fmt.Sprintf(`iifname %s %s counter return comment "block:dns-exempt"`, set, m))
	}
	r.line(fmt.Sprintf(
		`iifname %s meta l4proto { tcp, udp } th dport 53 fib daddr type != local counter redirect to :53 comment "block:dns-redirect"`,
		set))
	r.sysFor(ifs, SystemRule{
		Chain: "nat_prerouting", Action: "redirect", Protocol: string(model.ProtocolTCPUDP),
		Source: r.exemptSource(), Destination: "not this firewall : 53",
		Description: "Plain DNS answered by this firewall instead",
		Keys:        []string{"nat_prerouting/block:dns-redirect"}, Setting: "enforcement",
	})
}

// BlockingUsesAlias reports whether DNS enforcement refers to an alias, so
// deleting it can say what would break.
func BlockingUsesAlias(cfg *model.Config, name string) bool {
	e := cfg.Blocking.Enforce
	return name != "" && (e.DoHAlias == name || e.ExemptAlias == name)
}
