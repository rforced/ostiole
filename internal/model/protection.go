package model

import "fmt"

// Rate limiting is the answer to a source that is not breaking a rule so
// much as repeating one. The rules are about who may reach what; a limit
// is about how often, and nftables can only answer that with state: a
// counter per source, kept in a dynamic set that ages its elements out on
// its own, so nothing has to be reset and nothing grows without bound.
//
// Three defences live here, and they are separate settings because they
// fail differently. A connection flood is stopped by holding new
// connections per source to a rate. A ping flood is the same idea for
// ICMP, which no connection tracking sees. A port scan is not a flood at
// all — it is a handful of packets to a hundred closed ports — so it is
// found at the other end of the zone's rules, where everything nothing
// allowed arrives, and answered by holding the source for a while.

// RateUnit is the period a rate is counted over.
type RateUnit string

// The periods a limit can be written in. Anything faster than a second or
// slower than an hour is not a rate anybody reasons about.
const (
	PerSecond RateUnit = "second"
	PerMinute RateUnit = "minute"
	PerHour   RateUnit = "hour"
)

// Or returns the unit or the default, which is per second.
func (u RateUnit) Or() RateUnit {
	if u == "" {
		return PerSecond
	}
	return u
}

// Valid reports whether the unit is one nftables can count in.
func (u RateUnit) Valid() bool {
	switch u.Or() {
	case PerSecond, PerMinute, PerHour:
		return true
	}
	return false
}

// RateLimit holds something to a rate. Burst is what a limit needs to be
// usable: a browser opening six connections for one page is not a flood,
// and a limit with no allowance for that would break the web while
// stopping nothing.
type RateLimit struct {
	// Rate is how many are allowed per Unit.
	Rate int `json:"rate"`
	// Unit is the period; empty means per second.
	Unit RateUnit `json:"unit,omitempty"`
	// Burst is how many may arrive at once before the rate is enforced.
	// Zero leaves it to nftables, which allows five.
	Burst int `json:"burst,omitempty"`
	// PerSource counts each source address on its own rather than
	// everything the rule matches together. It is the setting that makes
	// a limit a defence rather than a cap: without it, one host at the
	// limit holds back every other host as well.
	PerSource bool `json:"perSource,omitempty"`
}

// Bounds for a limit, in packets or connections per period. They are
// named apart from the line speeds in shaping.go, which are bits a
// second: the floor is one, because zero is "block it" and there are
// rules for that, and the ceiling is high enough for a gigabit of small
// packets and low enough that a typo is caught.
const (
	MinLimitRate = 1
	MaxLimitRate = 1_000_000
	// MaxLimitBurst is bounded for the same reason: a burst larger than
	// this is a limit that never takes effect.
	MaxLimitBurst = 1_000_000
)

// String renders the rate the way nftables and the page both read it.
func (l RateLimit) String() string {
	out := fmt.Sprintf("%d/%s", l.Rate, l.Unit.Or())
	if l.Burst > 0 {
		out += fmt.Sprintf(" burst %d packets", l.Burst)
	}
	return out
}

// Protection is the edge defence: what a zone does about traffic that is
// not so much against the rules as too much of itself. Nothing here is on
// by default — a limit that fires on a quiet network is a fault report
// from a user, not a protected router — and each part is set on its own.
type Protection struct {
	// Zones are the zones defended. Empty means every external zone,
	// which is where this belongs on an ordinary router: the inside of a
	// network is where the flood would be coming from, and holding the
	// office back at nine in the morning is not protection.
	Zones []string `json:"zones,omitempty"`
	// SynFlood holds new connections per source to a rate.
	SynFlood *RateLimit `json:"synFlood,omitempty"`
	// ICMPFlood does the same for echo requests, which connection
	// tracking never sees as new connections at all.
	ICMPFlood *RateLimit `json:"icmpFlood,omitempty"`
	// PortScan holds a source that keeps knocking on closed ports.
	PortScan *PortScan `json:"portScan,omitempty"`
}

// PortScan finds a source whose packets keep reaching the end of a zone's
// rules — which is where anything nothing allowed ends up — and holds it
// for a while. The rate is what tells a scan from a mistake: one refused
// packet is a client with a stale bookmark, forty in a minute is somebody
// reading the whole port range.
type PortScan struct {
	// Rate is how many refused packets from one source are tolerated.
	Rate int `json:"rate"`
	// Unit is the period; empty means per minute here, because a scan is
	// counted over longer than a flood.
	Unit RateUnit `json:"unit,omitempty"`
	// Hold is how long the source is dropped outright afterwards, as a
	// duration nftables understands: 10m, 1h, 30s.
	Hold string `json:"hold,omitempty"`
}

// Limit is the port scan threshold as a rate, so one renderer covers
// both. The default period is a minute rather than a second.
func (p PortScan) Limit() RateLimit {
	unit := p.Unit
	if unit == "" {
		unit = PerMinute
	}
	return RateLimit{Rate: p.Rate, Unit: unit}
}

// HoldOr returns how long a source is held, or the default.
func (p PortScan) HoldOr() string {
	if p.Hold == "" {
		return DefaultScanHold
	}
	return p.Hold
}

// What the UI offers when each defence is switched on. They are chosen to
// be invisible on a working network: a page load opens a handful of
// connections, a traceroute sends a handful of pings, and a client with a
// stale bookmark is refused once or twice.
var (
	// DefaultSynFlood allows a burst of sixty and then thirty new
	// connections a second from one source.
	DefaultSynFlood = RateLimit{Rate: 30, Unit: PerSecond, Burst: 60, PerSource: true}
	// DefaultICMPFlood is ten pings a second with a burst of twenty.
	DefaultICMPFlood = RateLimit{Rate: 10, Unit: PerSecond, Burst: 20, PerSource: true}
	// DefaultPortScan holds a source that is refused twenty times in a
	// minute.
	DefaultPortScan = PortScan{Rate: 20, Unit: PerMinute, Hold: DefaultScanHold}
)

// DefaultScanHold is how long a scanner is dropped for. Long enough to
// make a sweep of the port range pointless, short enough that a shared
// address behind carrier NAT is not blocked for the afternoon.
const DefaultScanHold = "10m"

// DefaultRuleLimit is what a rule's limit starts at when somebody turns
// it on: a rate that no ordinary client reaches, per source, so one busy
// host cannot spend the allowance of the rest.
var DefaultRuleLimit = RateLimit{Rate: 20, Unit: PerMinute, Burst: 10, PerSource: true}

// On reports whether anything is defended.
func (p Protection) On() bool {
	return p.SynFlood != nil || p.ICMPFlood != nil || p.PortScan != nil
}

// ProtectedZones lists the zones the edge defence applies to, in
// configuration order: the ones named, or every external zone that has an
// interface. A named zone with no interfaces renders nothing, the same
// way a rule on an empty zone does.
func (c *Config) ProtectedZones() []string {
	if !c.Protection.On() {
		return nil
	}
	named := map[string]bool{}
	for _, z := range c.Protection.Zones {
		named[z] = true
	}
	var out []string
	for _, z := range c.Zones {
		if len(c.ZoneInterfaces(z.Name)) == 0 {
			continue
		}
		if len(named) > 0 {
			if named[z.Name] {
				out = append(out, z.Name)
			}
			continue
		}
		if z.External {
			out = append(out, z.Name)
		}
	}
	return out
}
