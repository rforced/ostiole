// Package host is about the operating system underneath Ostiole rather
// than about Ostiole itself: the packages a router needs installed, the firewall and network
// managers it has to take over from, and whatever an older firewall left
// in the kernel.
//
// All of it was already possible from a console — `ostiole services
// setup`, `ostiole takeover`, a package manager — and none of it was
// possible from the web UI, because the daemon runs behind
// ProtectSystem=strict and cannot write a unit file or a package
// database. So this package reports what is outstanding, and performs
// each step the one way that works from either front door: the steps that
// need to write outside the sandbox run the installed ostiole binary in a
// transient systemd unit, which is the same command an operator would
// have typed.
//
// The report is also the gate. A router with something outstanding sends
// the browser to the host page until it is done or the operator says they
// are leaving it alone, because a first apply that needs dnsmasq and
// finds no dnsmasq fails on the whole configuration and takes its
// rollback with it.
package host

import (
	"context"
	"log/slog"
	"strings"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// Runner runs a command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Kernel is the nftables side of the leftover sweep, named here as well
// so a caller wiring up these dependencies needs one import and not two.
type Kernel = iptables.Kernel

// Deps are the ways this package reaches the router.
type Deps struct {
	// Root says whether the daemon can act on any of this. Everything is
	// reported either way; a router that is not root gets the page and an
	// explanation instead of buttons.
	Root bool
	// Units answers systemd questions and performs the takeovers.
	Units install.Systemctl
	// Packages drives the distro package manager.
	Packages *sysupdate.Manager
	// Kernel reads and deletes nftables tables, for the leftovers an
	// older firewall wrote through the nf_tables front end.
	Kernel iptables.Kernel
	// Config is the configuration in force, which is what decides which
	// components this router needs: one that serves no DHCP does not
	// need dnsmasq, and should not be nagged about it.
	Config func() *model.Config
	// Backend is the network backend the daemon runs with (auto, networkd
	// or none).
	Backend string
	// NFT is the nft binary the daemon runs with, passed on to the
	// command line so both front doors talk to the same kernel.
	NFT string
	// TableLoaded reports whether Ostiole's ruleset is in the kernel,
	// which is what makes masking the old firewall safe rather than
	// reckless. A nil function refuses the firewall takeover.
	TableLoaded func(ctx context.Context) bool
	// Dir is the configuration directory, where the record of what has
	// been done is kept.
	Dir string
	// Binary is the ostiole to run for the steps that have to happen
	// outside the sandbox; empty finds the installed one.
	Binary string
	// Run runs commands; nil means the real ones.
	Run Runner
	// Locate finds a command on the router, which is how a component
	// known by a binary is told present. nil looks on PATH and in the
	// sbin directories; a test says what this router has without depending
	// on what the machine running it has.
	Locate func(name string) string
	// Proc is where the legacy iptables modules report their tables;
	// empty means /proc/net, and a test points it at a directory of its
	// own for the same reason it sets Locate.
	Proc string
	Log  *slog.Logger
}

// legacy is the iptables side of these dependencies.
func (d Deps) legacy() iptables.Deps {
	return iptables.Deps{Run: d.run(), Kernel: d.Kernel, Locate: d.Locate, Proc: d.Proc}
}

func (d Deps) log() *slog.Logger {
	if d.Log == nil {
		return slog.Default()
	}
	return d.Log
}

func (d Deps) run() Runner {
	if d.Run == nil {
		return install.ExecRunner{}
	}
	return d.Run
}

func (d Deps) locate(name string) string {
	if d.Locate != nil {
		return d.Locate(name)
	}
	return sysupdate.Locate(name)
}

// ComponentState is one piece of the operating system, and what this
// router has to say about it.
type ComponentState struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Needs string `json:"needs"`
	// Required is whether the configuration in force asks for it.
	Required bool `json:"required"`
	// Why names what asks for it, so a row that insists on being
	// installed can say who wants it.
	Why string `json:"why,omitempty"`
	// Present is whether the command or unit it brings is on the router.
	Present bool `json:"present"`
	// Ready is whether Ostiole's own unit for it has been written. A
	// package on its own is not a service: something has to point it at a
	// generated configuration.
	Ready bool `json:"ready"`
	// Unit is that unit's name, for the components that have one.
	Unit string `json:"unit,omitempty"`
	// Availability says whether installing it is even the right idea
	// here.
	Availability sysupdate.Availability `json:"availability"`
	// Packages is what would be installed.
	Packages []string `json:"packages,omitempty"`
	// Note is the distro caveat worth reading before pressing anything.
	Note string `json:"note,omitempty"`
}

// Outstanding reports whether this component is something the router
// still needs doing.
func (c ComponentState) Outstanding() bool {
	return c.Required && (!c.Present || (c.Unit != "" && !c.Ready))
}

// CompetitorState is a service that does Ostiole's job and has to stop.
type CompetitorState struct {
	install.Service
	// Conflicts is whether it is running or would start at boot.
	Conflicts bool `json:"conflicts"`
	// Packages names what a removal would take off the router, empty when
	// removal is not offered.
	Packages []string `json:"packages,omitempty"`
	// Installed is whether those packages are actually there, so the
	// button is not offered for a unit whose package has already gone.
	Installed bool `json:"installed"`
	// Note explains a competitor that is disabled but never removed.
	Note string `json:"note,omitempty"`
}

// NetworkState is who owns the addresses on this router.
type NetworkState struct {
	// Backend is what the daemon runs with.
	Backend string `json:"backend"`
	// Networkd is active, inactive or missing.
	Networkd string `json:"networkd"`
	// Owned is whether Ostiole has taken networking over.
	Owned bool `json:"owned"`
	// Managers are the network managers still running, which is what the
	// takeover would retire.
	Managers []string `json:"managers,omitempty"`
	// Pending is whether a takeover is waiting to be confirmed, in which
	// case the only thing to do is confirm it or let it revert.
	Pending bool `json:"pending"`
}

// Report is the whole answer to "is this router ready to be a firewall".
type Report struct {
	Root        bool              `json:"root"`
	Manager     string            `json:"manager,omitempty"`
	Distro      string            `json:"distro,omitempty"`
	Unavailable string            `json:"unavailable,omitempty"`
	Components  []ComponentState  `json:"components"`
	Competitors []CompetitorState `json:"competitors"`
	Legacy      iptables.Report   `json:"legacy"`
	Network     NetworkState      `json:"network"`
	Steps       []StepState       `json:"steps"`
	// Prepared is whether nothing is outstanding that has not been
	// deliberately left alone. It is what the router guard reads.
	Prepared bool `json:"prepared"`
}

// Status gathers everything, and never fails: a page that cannot say what
// is wrong with a router is worse than a page with gaps in it, and every
// probe here has an answer for "could not tell".
func Status(ctx context.Context, d Deps) Report {
	rep := Report{Root: d.Root, Components: []ComponentState{}, Competitors: []CompetitorState{}}
	manager := ""
	if d.Packages != nil {
		st := d.Packages.Status(false)
		rep.Manager, rep.Distro, rep.Unavailable = st.Manager, st.Distro, st.Unavailable
		manager = st.Manager
	}
	cfg := config(d)
	rep.Components = components(ctx, d, cfg, manager)
	rep.Competitors = competitors(ctx, d, manager)
	rep.Legacy = iptables.Detect(ctx, d.legacy())
	rep.Network = networkState(ctx, d, rep.Competitors)
	rep.Steps = steps(rep, Load(d.Dir))
	// A daemon that is not root can do none of this, so it is never sent
	// here: a dev run and the end-to-end server report what they found
	// and let the browser past. The steps still say what is outstanding,
	// because being unable to fix something is no reason to hide it.
	rep.Prepared = !d.Root || prepared(rep.Steps)
	return rep
}

// config is the configuration in force, or an empty one. A router with no
// configuration yet needs the firewall and nothing else, which is exactly
// what an empty configuration asks for.
func config(d Deps) *model.Config {
	if d.Config == nil {
		return &model.Config{}
	}
	if cfg := d.Config(); cfg != nil {
		return cfg
	}
	return &model.Config{}
}

// componentUnits are the units Ostiole writes for a component, which is
// the difference between a package being installed and a service being
// set up.
func componentUnits() map[string]string {
	return map[string]string{
		"dnsmasq":   services.Unit,
		"unbound":   services.UnboundUnit,
		"miniupnpd": services.UPnPUnit,
		"pppd":      services.PPPoEUnit,
	}
}

// components describes every piece Ostiole can install, whether this
// router wants it, and whether it is there.
func components(ctx context.Context, d Deps, cfg *model.Config, manager string) []ComponentState {
	units := componentUnits()
	out := make([]ComponentState, 0, len(sysupdate.Components()))
	for _, c := range sysupdate.Components() {
		pkgs, avail := c.Packages(manager)
		st := ComponentState{
			Key: c.Key, Label: c.Label, Needs: c.Needs,
			Availability: avail, Packages: pkgs, Note: c.Note,
			Unit:    units[c.Key],
			Present: c.Present(d.locate, func(unit string) bool { return unitKnown(ctx, d, unit) }),
		}
		st.Required, st.Why = requires(cfg, c.Key, d.Backend)
		if st.Unit == "" {
			// Nothing of ours points at it, so being installed is all
			// there is to being ready.
			st.Ready = st.Present
		} else {
			st.Ready = st.Present && unitKnown(ctx, d, st.Unit)
		}
		out = append(out, st)
	}
	return out
}

// requires reads the configuration for what asks for a component, and
// says so in the words the page shows: an operator who is told to install
// something is owed the reason.
func requires(cfg *model.Config, key, backend string) (bool, string) {
	switch key {
	case "nft":
		return true, "every ruleset this router loads"
	case "dnsmasq":
		switch {
		case cfg.Services.DHCP.Enabled && cfg.Services.DNS.Enabled:
			return true, "the DHCP and DNS services are turned on"
		case cfg.Services.DHCP.Enabled:
			return true, "the DHCP service is turned on"
		case cfg.Services.DNS.Enabled:
			return true, "the DNS service is turned on"
		}
	case "unbound":
		if cfg.Services.DNS.Enabled && cfg.Services.DNS.Resolver != "" &&
			cfg.Services.DNS.Resolver != model.ResolverForward {
			return true, "the DNS service resolves names itself (" + string(cfg.Services.DNS.Resolver) + ")"
		}
	case "miniupnpd":
		if cfg.Services.UPnP.Enabled {
			return true, "port mapping is turned on"
		}
	case "pppd":
		for _, i := range cfg.Interfaces {
			if i.PPPoE != nil {
				return true, i.Name + " dials a PPPoE session"
			}
		}
	case "tc":
		for _, i := range cfg.Interfaces {
			if i.Shaping != nil {
				return true, i.Name + " has a shaped queue"
			}
		}
	case "networkd":
		if backend != "none" {
			return true, "Ostiole configures this router's addresses and routes"
		}
	}
	return false, ""
}

// unitKnown reports whether systemd has heard of a unit. `systemctl cat`
// is the question that works for a unit that exists but has never run.
func unitKnown(ctx context.Context, d Deps, unit string) bool {
	if d.Units == nil || unit == "" {
		return false
	}
	_, err := d.Units.Run(ctx, "cat", unit)
	return err == nil
}

// competitors lists the services that do Ostiole's job, with what a
// removal would take with them.
func competitors(ctx context.Context, d Deps, manager string) []CompetitorState {
	if d.Units == nil {
		return []CompetitorState{}
	}
	found, err := install.Competitors(ctx, d.Units)
	if err != nil {
		return []CompetitorState{}
	}
	out := make([]CompetitorState, 0, len(found))
	for _, svc := range found {
		st := CompetitorState{Service: svc, Conflicts: svc.Conflicts()}
		pkgs, note := CompetitorPackages(svc.Name, manager)
		st.Packages, st.Note = pkgs, note
		if len(pkgs) > 0 && d.Packages != nil && d.Root {
			// One installed package is enough to have something to
			// remove; asking about the rest only slows the page down.
			for _, p := range pkgs {
				if ok, err := d.Packages.Installed(ctx, p); err == nil && ok {
					st.Installed = true
					break
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// networkState reports who owns the addressing. The competitors are the
// ones Status has already asked systemd about.
func networkState(ctx context.Context, d Deps, competitors []CompetitorState) NetworkState {
	st := NetworkState{Backend: d.Backend, Networkd: "unknown"}
	if st.Backend == "" {
		st.Backend = "auto"
	}
	if d.Units != nil {
		switch {
		case !install.HasNetworkd(ctx, d.Units):
			st.Networkd = "missing"
		default:
			out, err := d.Units.Run(ctx, "is-active", install.NetworkdUnit)
			st.Networkd = "inactive"
			if err == nil && strings.TrimSpace(out) == "active" {
				st.Networkd = "active"
			}
		}
	}
	if rec, err := install.LoadTakeoverRecord(d.Dir); err == nil && rec != nil {
		st.Owned, st.Managers = true, rec.Managers
	}
	if !st.Owned {
		// The network managers still running or enabled.
		for _, c := range competitors {
			if c.Kind == "network" && c.Conflicts {
				st.Managers = append(st.Managers, c.Name)
			}
		}
	}
	st.Pending = revertArmed(ctx, d)
	return st
}

// revertArmed reports whether a network takeover is waiting to be
// confirmed. The timer is the only record of that, and it is the thing
// that will put the old manager back if nobody says otherwise.
func revertArmed(ctx context.Context, d Deps) bool {
	if !d.Root || d.Units == nil {
		return false
	}
	out, err := d.Units.Run(ctx, "is-active", install.RevertTimerUnit+".timer")
	return err == nil && strings.TrimSpace(out) == "active"
}
