// Package policy turns per-rule gateway selection into kernel routing.
// The firewall marks a packet with the mark of the gateway or gateway
// group its rule chose; this package gives each of those marks a routing
// table holding the matching default route, and the ip rules that send
// marked packets there.
package policy

import (
	"sort"

	"github.com/rforced/ostiole/internal/model"
)

// RulePriorityBase is where Ostiole's ip rules start. Each target uses two
// consecutive priorities, well below the kernel's main lookup at 32766 so
// policy routing gets its say first.
const RulePriorityBase = 22000

// Hop is one gateway as the monitor currently sees it.
type Hop struct {
	Gateway string
	// Address is the resolved next hop; a gateway without one cannot carry
	// traffic yet.
	Address   string
	Interface string
	// Device is a tunnel gateway: traffic goes into its interface with no
	// next hop, in the families V4 and V6 say it carries.
	Device bool
	V4     bool
	V6     bool
	// Online is the monitor's verdict. A gateway it has not probed yet
	// counts as online: a router that just booted should still route.
	Online bool
}

// NewHop describes a gateway as a hop: a next hop at address, or for a
// tunnel gateway the tunnel itself.
func NewHop(cfg *model.Config, g model.Gateway, address string, online bool) Hop {
	h := Hop{Gateway: g.Name, Address: address, Interface: g.Interface, Online: online}
	if cfg.TunnelGateway(g) {
		h.Address, h.Device = "", true
		h.V4, h.V6 = cfg.TunnelFamilies(g)
	}
	return h
}

// usable reports whether the hop has somewhere to send traffic: a next
// hop, or a tunnel.
func (h Hop) usable() bool { return h.Address != "" || h.Device }

// carries reports whether a tunnel hop takes traffic of a family.
func (h Hop) carries(v4 bool) bool {
	if v4 {
		return h.V4
	}
	return h.V6
}

// Target is one destination rules can route through: the mark the firewall
// sets, the routing table that answers it, and the gateways behind it.
type Target struct {
	Name  string `json:"name"`
	Group bool   `json:"group,omitempty"`
	Mark  uint32 `json:"mark"`
	Table int    `json:"table"`
	// Index is the target's number, the one in its mark and its table, and
	// it fixes the ip rule priorities.
	Index int `json:"index"`
	// Tiers holds the usable hops, best tier first. Hops inside one tier
	// are used together and the kernel spreads connections over them.
	Tiers [][]Hop `json:"tiers,omitempty"`
	// Block drops traffic when no hop is online, instead of letting it out
	// the ordinary default route. It is what a tunnel that must not leak
	// needs.
	Block bool `json:"block,omitempty"`
}

// Priorities returns the ip rule priorities this target owns: the first
// consults the main table without its default route, the second is the
// policy table itself.
func (t Target) Priorities() (suppress, lookup int) {
	return RulePriorityBase + 2*t.Index, RulePriorityBase + 2*t.Index + 1
}

// Online reports whether any hop can carry traffic right now.
func (t Target) Online() bool {
	for _, tier := range t.Tiers {
		for _, h := range tier {
			if h.Online && h.usable() {
				return true
			}
		}
	}
	return false
}

// Plan pairs the marks the firewall hands out with the gateways behind
// them. hops says what the monitor knows about each gateway by name;
// gateways missing from it are unusable until one is resolved.
func Plan(cfg *model.Config, hops map[string]Hop) []Target {
	targets := cfg.PolicyTargets()
	out := make([]Target, 0, len(targets))
	for _, pt := range targets {
		t := Target{Name: pt.Name, Group: pt.Group, Mark: pt.Mark, Table: pt.Table,
			Index: int(pt.Mark >> model.PolicyMarkShift)}
		if !pt.Group {
			if h, ok := hops[pt.Name]; ok && h.usable() {
				t.Tiers = [][]Hop{{h}}
			}
			// A tunnel gateway on its own blocks when it is down: a way out
			// that falls back to the WAN leaks what it was meant to hide.
			// A fallback is a group with a WAN in a later tier.
			if g, ok := cfg.Gateway(pt.Name); ok && cfg.TunnelGateway(*g) {
				t.Block = true
			}
			out = append(out, t)
			continue
		}
		g, ok := cfg.GatewayGroup(pt.Name)
		if !ok {
			continue
		}
		t.Block = g.OnDown == model.OnDownBlock
		t.Tiers = tiers(g, hops)
		out = append(out, t)
	}
	return out
}

// tiers groups a gateway group's members by tier, best first, keeping only
// members the monitor has resolved an address for.
func tiers(g *model.GatewayGroup, hops map[string]Hop) [][]Hop {
	byTier := map[int][]Hop{}
	for _, m := range g.Members {
		h, ok := hops[m.Gateway]
		if !ok || !h.usable() {
			continue
		}
		byTier[m.Tier] = append(byTier[m.Tier], h)
	}
	levels := make([]int, 0, len(byTier))
	for tier := range byTier {
		levels = append(levels, tier)
	}
	sort.Ints(levels)
	out := make([][]Hop, 0, len(levels))
	for _, tier := range levels {
		out = append(out, byTier[tier])
	}
	return out
}
