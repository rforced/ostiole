package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
)

// Overview is everything the dashboard shows, gathered in one request:
// engine status, interfaces with live kernel state, the busiest rules,
// service health, and warnings about anything fighting with Ostiole.
type Overview struct {
	Status     StatusSummary    `json:"status"`
	Interfaces []LinkSummary    `json:"interfaces"`
	TopRules   []RuleCounter    `json:"topRules"`
	Blocked    nft.Counter      `json:"blocked"`
	DHCP       DHCPSummary      `json:"dhcp"`
	DNS        DNSSummary       `json:"dns"`
	Gateways   []gateway.Status `json:"gateways"`
	Services   []ServiceState   `json:"services"`
	Warnings   []Warning        `json:"warnings"`
}

// StatusSummary repeats GET /status, plus a count of what is configured,
// so the dashboard needs one round trip.
type StatusSummary struct {
	Configured  bool   `json:"configured"`
	TableLoaded bool   `json:"tableLoaded"`
	Network     string `json:"network"`
	Hostname    string `json:"hostname,omitempty"`
	Zones       int    `json:"zones"`
	Rules       int    `json:"rules"`
	Revisions   int    `json:"revisions"`
}

// LinkSummary is one interface as configured and as the kernel has it.
type LinkSummary struct {
	Name        string `json:"name"`
	Zone        string `json:"zone,omitempty"`
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Configured  bool   `json:"configured"`
	Enabled     bool   `json:"enabled"`
	Present     bool   `json:"present"` // the kernel knows this link
	Up          bool   `json:"up"`
	Carrier     bool   `json:"carrier"`
	External    bool   `json:"external,omitempty"` // its zone is WAN-like
	MTU         int    `json:"mtu,omitempty"`
	MAC         string `json:"mac,omitempty"`
	VLANID      int    `json:"vlanId,omitempty"`
	IPv4Mode    string `json:"ipv4Mode,omitempty"`
	IPv6Mode    string `json:"ipv6Mode,omitempty"`
	// Addresses are live, from the kernel; StaticAddresses are the ones the
	// model asks for, so a configuration that has not reached the
	// interface yet is visible.
	Addresses       []string `json:"addresses"`
	StaticAddresses []string `json:"configuredAddresses,omitempty"`
	RXBytes         uint64   `json:"rxBytes"`
	TXBytes         uint64   `json:"txBytes"`
}

// RuleCounter is one configured rule with its kernel counter.
type RuleCounter struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Zone        string `json:"zone"`
	Action      string `json:"action"`
	Enabled     bool   `json:"enabled"`
	Packets     uint64 `json:"packets"`
	Bytes       uint64 `json:"bytes"`
}

// DHCPSummary counts what the DHCP server hands out.
type DHCPSummary struct {
	Enabled bool `json:"enabled"`
	Scopes  int  `json:"scopes"`
	Static  int  `json:"static"`
	Leases  int  `json:"leases"`
	// Capacity is how many addresses the enabled pools hold, 0 when unknown.
	Capacity int `json:"capacity"`
}

// DNSSummary describes the resolver.
type DNSSummary struct {
	Enabled   bool     `json:"enabled"`
	Domain    string   `json:"domain,omitempty"`
	Upstreams []string `json:"upstreams,omitempty"`
	Overrides int      `json:"overrides"`
}

// ServiceState is the unit state of something Ostiole drives.
type ServiceState struct {
	Name   string `json:"name"`
	Unit   string `json:"unit,omitempty"`
	State  string `json:"state"` // active, inactive, missing, unknown
	Want   bool   `json:"want"`  // the configuration expects it to run
	Detail string `json:"detail,omitempty"`
}

// Warning is something on the box that needs the admin's attention.
type Warning struct {
	Kind   string `json:"kind"`
	Level  string `json:"level"` // warn or info
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// TableLister reports every nftables table on the box so the dashboard can
// warn about rulesets Ostiole does not own.
type TableLister interface {
	ListTables(ctx context.Context) ([]string, error)
}

const (
	stateActive   = "active"
	stateInactive = "inactive"
	stateMissing  = "missing"
	stateUnknown  = "unknown"
)

func (a *api) overview(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	st, err := a.engine.Status(ctx)
	if err != nil {
		return err
	}
	ov := Overview{
		Status:     StatusSummary{Configured: st.Configured, TableLoaded: st.TableLoaded, Network: st.Network},
		Interfaces: []LinkSummary{},
		TopRules:   []RuleCounter{},
		Services:   []ServiceState{},
		Warnings:   []Warning{},
		Gateways:   []gateway.Status{},
	}
	if a.gateways != nil {
		ov.Gateways = a.gateways.Statuses()
	}

	cfg, err := a.engine.Store().Load()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if cfg != nil {
		ov.Status.Hostname = cfg.System.Hostname
		ov.Status.Zones = len(cfg.Zones)
		ov.Status.Rules = len(cfg.Rules)
		ov.DHCP, ov.DNS = summarizeServices(cfg)
	}
	if revs, err := a.engine.Store().Revisions(); err == nil {
		ov.Status.Revisions = len(revs)
	}

	links, _ := network.Discover()
	ov.Interfaces = summarizeLinks(cfg, links)

	if counters, err := a.engine.Counters(ctx); err == nil {
		ov.TopRules, ov.Blocked = topRules(cfg, counters, 5)
	}

	if a.services != nil {
		if leases, err := a.services.ReadLeases(); err == nil {
			ov.DHCP.Leases = len(leases)
		}
	}
	ov.Services = a.serviceStates(ctx, cfg)
	ov.Warnings = a.warnings(ctx, cfg, st, ov.Services, ov.Interfaces)
	writeJSON(w, http.StatusOK, ov)
	return nil
}

// summarizeLinks merges configured interfaces with what the kernel has.
// Configured interfaces come first, then anything else that is up; the
// loopback is left out.
func summarizeLinks(cfg *model.Config, links []network.Link) []LinkSummary {
	live := make(map[string]network.Link, len(links))
	for _, l := range links {
		live[l.Name] = l
	}
	out := []LinkSummary{}
	seen := map[string]bool{}
	if cfg != nil {
		for _, in := range cfg.Interfaces {
			s := LinkSummary{
				Name:        in.Name,
				Zone:        in.Zone,
				Description: in.Description,
				Configured:  true,
				Enabled:     in.Enabled,
				MTU:         in.MTU,
				IPv4Mode:    string(in.IPv4.Mode),
				IPv6Mode:    string(in.IPv6.Mode),
				Addresses:   []string{},
			}
			if in.IPv4.Mode == model.AddrStatic && in.IPv4.Address != "" {
				s.StaticAddresses = append(s.StaticAddresses, in.IPv4.Address)
			}
			if in.IPv6.Mode == model.AddrStatic && in.IPv6.Address != "" {
				s.StaticAddresses = append(s.StaticAddresses, in.IPv6.Address)
			}
			if in.VLAN != nil {
				s.VLANID = int(in.VLAN.ID)
			}
			if z, ok := cfg.Zone(in.Zone); ok {
				s.External = z.External
			}
			l, present := live[in.Name]
			out = append(out, withLive(s, l, present))
			seen[in.Name] = true
		}
	}
	for _, l := range links {
		if seen[l.Name] || l.Kind == "loopback" {
			continue
		}
		out = append(out, withLive(LinkSummary{Name: l.Name, Addresses: []string{}}, l, true))
	}
	return out
}

// withLive folds kernel state into a summary. present is false when the
// interface is configured but the kernel has no such link.
func withLive(s LinkSummary, l network.Link, present bool) LinkSummary {
	if !present {
		return s
	}
	s.Present = true
	s.Kind = l.Kind
	s.Up = l.Up
	s.Carrier = l.Carrier
	s.MAC = l.MAC
	s.RXBytes, s.TXBytes = l.RXBytes, l.TXBytes
	if l.VLANID != 0 {
		s.VLANID = l.VLANID
	}
	if s.MTU == 0 {
		s.MTU = l.MTU
	}
	if len(l.Addresses) > 0 {
		s.Addresses = l.Addresses
	}
	return s
}

// topRules pairs configured rules with their counters, busiest first, and
// sums the packets that fell through to a default drop.
func topRules(cfg *model.Config, counters nft.Counters, limit int) ([]RuleCounter, nft.Counter) {
	var blocked nft.Counter
	for key, c := range counters {
		if strings.HasSuffix(key, "/default-drop") || strings.HasSuffix(key, "/zone-default") {
			blocked.Packets += c.Packets
			blocked.Bytes += c.Bytes
		}
	}
	out := []RuleCounter{}
	if cfg == nil {
		return out, blocked
	}
	for _, rule := range cfg.Rules {
		c := counters[rule.ID]
		out = append(out, RuleCounter{
			ID:          rule.ID,
			Description: rule.Description,
			Zone:        rule.Zone,
			Action:      string(rule.Action),
			Enabled:     rule.Enabled,
			Packets:     c.Packets,
			Bytes:       c.Bytes,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Packets > out[j].Packets })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, blocked
}

func summarizeServices(cfg *model.Config) (DHCPSummary, DNSSummary) {
	d := DHCPSummary{
		Enabled: cfg.Services.DHCP.Enabled,
		Static:  len(cfg.Services.DHCP.StaticLeases),
	}
	for _, sc := range cfg.Services.DHCP.Scopes {
		if !sc.Enabled {
			continue
		}
		d.Scopes++
		d.Capacity += poolSize(sc.RangeStart, sc.RangeEnd)
	}
	n := DNSSummary{
		Enabled:   cfg.Services.DNS.Enabled,
		Domain:    cfg.Services.DNS.Domain,
		Upstreams: cfg.Services.DNS.Upstreams,
		Overrides: len(cfg.Services.DNS.HostOverrides),
	}
	return d, n
}

// poolSize counts the addresses in an IPv4 range, 0 if it cannot tell.
func poolSize(start, end string) int {
	from, err1 := netip.ParseAddr(start)
	to, err2 := netip.ParseAddr(end)
	if err1 != nil || err2 != nil || !from.Is4() || !to.Is4() || to.Less(from) {
		return 0
	}
	f, t := from.As4(), to.As4()
	n := int64(uint32(t[0])<<24|uint32(t[1])<<16|uint32(t[2])<<8|uint32(t[3])) -
		int64(uint32(f[0])<<24|uint32(f[1])<<16|uint32(f[2])<<8|uint32(f[3])) + 1
	if n < 0 || n > 1<<20 {
		return 0
	}
	return int(n)
}

// serviceStates reports the units Ostiole drives. Without a systemctl the
// states stay unknown, which is what a non-root dev run sees.
func (a *api) serviceStates(ctx context.Context, cfg *model.Config) []ServiceState {
	wantServices := cfg != nil && services.Enabled(cfg)
	dnsmasq := ServiceState{Name: "DHCP and DNS", Unit: services.Unit, State: stateUnknown, Want: wantServices}
	if a.services != nil {
		switch {
		case !a.services.Installed(ctx):
			dnsmasq.State = stateMissing
			dnsmasq.Detail = "Run `ostiole services setup` to install and enable it."
		case a.services.Active(ctx):
			dnsmasq.State = stateActive
		default:
			dnsmasq.State = stateInactive
		}
	}

	resolver := ServiceState{
		Name:  "Validating resolver",
		Unit:  services.UnboundUnit,
		State: stateUnknown,
		Want:  cfg != nil && services.ResolverEnabled(cfg),
	}
	if a.resolver != nil {
		switch {
		case !a.resolver.Installed(ctx):
			resolver.State = stateMissing
			resolver.Detail = "Run `ostiole services setup --with-resolver` to install unbound."
		case a.resolver.Active(ctx):
			resolver.State = stateActive
		default:
			resolver.State = stateInactive
		}
	}

	netd := ServiceState{
		Name:  "Network",
		Unit:  install.NetworkdUnit,
		State: stateUnknown,
		Want:  cfg != nil && len(cfg.Interfaces) > 0,
	}
	if a.units != nil {
		netd.State = unitState(ctx, a.units, install.NetworkdUnit)
	}
	if netd.State != stateActive {
		netd.Detail = "Run `ostiole takeover --network` to hand networking to Ostiole."
	}

	// The log listener needs root; on a dev run its absence is expected,
	// so it is reported but never warned about.
	logs := ServiceState{Name: "Firewall log", State: stateInactive}
	if a.fwlog != nil {
		logs.State = stateActive
	} else {
		logs.Detail = "The daemon must run as root to read nflog."
	}
	states := []ServiceState{dnsmasq}
	if resolver.Want || resolver.State == stateActive {
		states = append(states, resolver)
	}
	return append(states, netd, logs)
}

// unitState maps systemctl's answer onto our vocabulary.
func unitState(ctx context.Context, sc install.Systemctl, unit string) string {
	out, _ := sc.Run(ctx, "is-active", unit)
	switch s := strings.TrimSpace(out); s {
	case stateActive, "activating":
		return stateActive
	case "":
		return stateUnknown
	case "inactive", "failed", "deactivating":
		return stateInactive
	default:
		return s
	}
}

// warnings collects everything the admin should look at: rulesets and
// services that compete with Ostiole, interfaces that went missing, and
// services the configuration asks for but the box is not running.
func (a *api) warnings(ctx context.Context, cfg *model.Config, st engine.Status, svcs []ServiceState, links []LinkSummary) []Warning {
	out := []Warning{}
	if st.Configured && !st.TableLoaded {
		out = append(out, Warning{
			Kind: "table-missing", Level: "warn",
			Title:  "The firewall ruleset is not loaded",
			Detail: "A configuration exists but table inet ostiole is not in the kernel. Apply the configuration or run `ostiole load`.",
		})
	}
	if a.tables != nil {
		if tables, err := a.tables.ListTables(ctx); err == nil {
			var foreign []string
			for _, t := range tables {
				if t != nft.Table {
					foreign = append(foreign, t)
				}
			}
			if len(foreign) > 0 {
				out = append(out, Warning{
					Kind: "foreign-tables", Level: "warn",
					Title:  "Other nftables rulesets are loaded",
					Detail: "Ostiole only manages " + nft.Table + ". These tables filter traffic too: " + strings.Join(foreign, ", ") + ".",
				})
			}
		}
	}
	if a.units != nil {
		if comp, err := install.Competitors(ctx, a.units); err == nil {
			var names []string
			for _, c := range comp {
				if c.Conflicts() {
					names = append(names, c.Name+" ("+c.Active+", "+c.Enabled+")")
				}
			}
			if len(names) > 0 {
				out = append(out, Warning{
					Kind: "conflicting-services", Level: "warn",
					Title:  "Conflicting services are running",
					Detail: "Run `ostiole takeover` to disable them: " + strings.Join(names, ", ") + ".",
				})
			}
		}
	}
	for _, s := range svcs {
		if s.Want && s.State != stateActive && s.State != stateUnknown {
			detail := s.Detail
			if s.Unit != "" {
				detail = strings.TrimSpace(s.Unit + " is " + s.State + ". " + detail)
			}
			out = append(out, Warning{
				Kind: "service-down", Level: "warn",
				Title:  s.Name + " is not running",
				Detail: detail,
			})
		}
	}
	var missing []string
	for _, l := range links {
		if l.Configured && l.Enabled && !l.Present {
			missing = append(missing, l.Name)
		}
	}
	if len(missing) > 0 {
		out = append(out, Warning{
			Kind: "interface-missing", Level: "warn",
			Title:  "Configured interfaces are missing",
			Detail: "The kernel has no " + strings.Join(missing, ", ") + ". Check the cabling or the interface names.",
		})
	}
	for _, g := range a.gatewayStatuses() {
		if g.Unknown || g.Online {
			continue
		}
		detail := fmt.Sprintf("%s on %s stopped answering", g.Monitor, g.Interface)
		if g.LastError != "" {
			detail += " (" + g.LastError + ")"
		}
		out = append(out, Warning{
			Kind: "gateway-down", Level: "warn",
			Title:  "Gateway " + g.Name + " is down",
			Detail: detail + ". Traffic uses the next gateway that answers.",
		})
	}
	if routes(cfg) && !forwardingOn() {
		out = append(out, Warning{
			Kind: "forwarding-off", Level: "warn",
			Title:  "IP forwarding is off",
			Detail: "This box has interfaces in more than one zone but the kernel will not route between them.",
		})
	}
	return out
}

func (a *api) gatewayStatuses() []gateway.Status {
	if a.gateways == nil {
		return nil
	}
	return a.gateways.Statuses()
}

// routes reports whether the configuration expects traffic to be routed,
// i.e. enabled interfaces live in at least two zones.
func routes(cfg *model.Config) bool {
	if cfg == nil {
		return false
	}
	zones := map[string]bool{}
	for _, in := range cfg.Interfaces {
		if in.Enabled && in.Zone != "" {
			zones[in.Zone] = true
		}
	}
	return len(zones) > 1
}

func forwardingOn() bool {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return true // cannot tell; do not cry wolf
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return err != nil || n != 0
}
