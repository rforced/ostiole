package nft

import (
	"fmt"

	"ostiole/internal/model"
)

// replyChains answer a connection out of the line it came in on,
// whichever line holds the default route, as pf's reply-to does
// (ADR-0037). A connection's line goes onto it as it starts; its answers
// carry the line again, forwarded ones before the routing decision and
// the router's own in a route chain, and the line's ip rule sends them to
// its table. Only answers: what else arrives on a connection routes as it
// would anyway. With fewer than two lines nothing is rendered.
func (r *renderer) replyChains() {
	lines := r.cfg.ReplyLines()
	if len(lines) == 0 {
		return
	}
	others := ^uint32(model.ReplyMarkMask)
	back := func() {
		for _, l := range lines {
			r.line(fmt.Sprintf(`ct direction reply ct mark & 0x%08x == 0x%08x meta mark set meta mark & 0x%08x | 0x%08x counter comment %q`,
				uint32(model.ReplyMarkMask), l.Mark, others, l.Mark, "reply-back:"+l.Interface))
		}
	}
	// After policy routing's chain, whose restore leaves the line off.
	r.block("chain reply_prerouting", func() {
		r.line("type filter hook prerouting priority mangle + 1; policy accept;")
		for _, l := range lines {
			r.line(fmt.Sprintf(`iifname %s ct state new ct mark set ct mark & 0x%08x | 0x%08x counter comment %q`,
				ifnameSet([]string{l.Interface}), others, l.Mark, "reply:"+l.Interface))
		}
		back()
	})
	r.block("chain reply_output", func() {
		r.line("type route hook output priority mangle; policy accept;")
		back()
	})
	for _, l := range lines {
		r.sysFor([]string{l.Interface}, SystemRule{
			Chain: "reply_prerouting", Action: "continue", Protocol: string(model.ProtocolAny),
			Source: "in on " + l.Interface, Destination: "any",
			Description: "Answer what comes in on " + l.Interface + " out of " + l.Interface,
			// The answers sent back, forwarded and the router's own.
			Keys:    []string{"reply_prerouting/reply-back:" + l.Interface, "reply_output/reply-back:" + l.Interface},
			Setting: "routing",
		})
	}
}
