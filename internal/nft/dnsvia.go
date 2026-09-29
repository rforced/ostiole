package nft

import (
	"fmt"
	"os/user"
	"slices"
	"strconv"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// ResolverAccounts are the users the resolvers run as and look names up
// as: dnsmasq's configuration names dnsmasq, and unbound's names unbound.
var ResolverAccounts = []string{"dnsmasq", "unbound"}

// ResolverUIDs reads the resolvers' accounts on this machine. They go in
// the ruleset as numbers: a name nft cannot find fails the whole load, and
// the ruleset loads at boot.
func ResolverUIDs() []uint32 {
	var out []uint32
	for _, name := range ResolverAccounts {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		if n, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
			out = append(out, uint32(n))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// dnsViaChain sends the resolvers' own lookups through services.dns.via,
// marked the way the policy chains mark a rule's traffic so the target's
// table routes them. Only what leaves this router: dnsmasq asking unbound
// is a lookup to a local address.
func (r *renderer) dnsViaChain() {
	via := r.cfg.Services.DNS.Via
	if via == "" {
		return
	}
	t, ok := r.cfg.PolicyTarget(via)
	if !ok {
		return
	}
	r.block("chain dns_via", func() {
		r.line("type route hook output priority mangle; policy accept;")
		if len(r.resolverUIDs) == 0 {
			r.line(fmt.Sprintf("# lookups through %s skipped: no resolver account on this router", via))
			return
		}
		key := "dns-via:" + via
		r.line(fmt.Sprintf(`meta skuid %s meta l4proto { tcp, udp } th dport { 53, 853 } fib daddr type != local `+
			`meta mark set meta mark & 0x%08x | 0x%x ct mark set ct mark & 0x%08x | 0x%x counter comment %q`,
			uidSet(r.resolverUIDs), ^uint32(model.PolicyMarkMask), t.Mark, ^uint32(model.PolicyMarkMask), t.Mark, key))
		r.sys(SystemRule{
			Chain: "dns_via", Action: "continue", Protocol: string(model.ProtocolTCPUDP),
			Source: "this router's lookups", Destination: "upstream, port 53 or 853",
			Description: "Send this router's lookups through " + via,
			Keys:        []string{"dns_via/" + key}, Setting: "dns",
		})
	})
}

// uidSet writes one uid, or a set of several.
func uidSet(uids []uint32) string {
	if len(uids) == 1 {
		return strconv.FormatUint(uint64(uids[0]), 10)
	}
	parts := make([]string, len(uids))
	for i, u := range uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// chainPostrouting holds what the router sends to the lines it has to
// leave by: its lookups to the target they go through when it blocks, as
// the kill switch in forward holds a rule's traffic, and its answers to a
// tunnel's shown networks to that tunnel. Until the daemon's first pass
// the marks route nowhere. It cannot sit in output: every chain of that
// hook sees the device routing chose before a route chain changed the
// mark, so a packet the mark had already sent into the tunnel would be
// dropped as leaving by the WAN.
func (r *renderer) chainPostrouting() {
	lookups, holds := r.lookupsBlocked(), false
	for _, tr := range r.translations() {
		holds = holds || tr.mark != 0
	}
	if lookups == nil && !holds {
		return
	}
	r.block("chain postrouting", func() {
		r.line("type filter hook postrouting priority filter; policy accept;")
		if lookups != nil {
			others := r.otherExternals(*lookups)
			key := "kill-switch:" + lookups.Name
			r.line(fmt.Sprintf(`ct mark & 0x%08x == 0x%x oifname %s counter drop comment %q`,
				model.PolicyMarkMask, lookups.Mark, ifnameSet(others), key))
			r.sys(SystemRule{
				Chain: "postrouting", Action: "drop", Protocol: string(model.ProtocolTCPUDP),
				Source: "this router's lookups", Destination: "out " + strings.Join(others, ", "),
				Description: "Keep this router's lookups off every other WAN",
				Keys:        []string{"postrouting/" + key}, Setting: "dns",
			})
		}
		r.translateHold("postrouting")
	})
}

// lookupsBlocked is the blocking target the resolvers' lookups go
// through, when there is one and another external line to hold them off.
func (r *renderer) lookupsBlocked() *model.BlockingTarget {
	via := r.cfg.Services.DNS.Via
	if via == "" || len(r.resolverUIDs) == 0 {
		return nil
	}
	for _, t := range r.cfg.BlockingTargets() {
		if t.Name == via && len(r.otherExternals(t)) > 0 {
			return &t
		}
	}
	return nil
}
