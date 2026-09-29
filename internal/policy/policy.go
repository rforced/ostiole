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
	// Online is the monitor's verdict. A gateway it has not probed yet
	// counts as online: a router that just booted should still route.
	Online bool
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
			if h.Online && h.Address != "" {
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
			if h, ok := hops[pt.Name]; ok && h.Address != "" {
				t.Tiers = [][]Hop{{h}}
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
		if !ok || h.Address == "" {
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
