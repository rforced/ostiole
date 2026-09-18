// Package host is about the operating system underneath Ostiole rather
// than about Ostiole itself: which of the daemons a router runs are
// present, who owns its addresses, and what an older firewall left in the
// kernel.
//
// It reports, and it does two things: clear those leftovers, and settle a
// network handover. Packages and units belong to the install script and
// to `ostiole repair`, which run outside the daemon's sandbox, where
// writing a unit file is possible at all.
package host

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/kernel"
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
	// Kernel reads and deletes nftables tables, for the leftovers an
	// older firewall wrote through the nf_tables front end.
	Kernel iptables.Kernel
	// Backend is the network backend the daemon runs with (auto, networkd
	// or none).
	Backend string
	// NFT is the nft binary the daemon runs with, passed on to the
	// command line so both front doors talk to the same kernel.
	NFT string
	// TableLoaded reports whether Ostiole's ruleset is in the kernel.
	TableLoaded func(ctx context.Context) bool
	// Dir is the configuration directory.
	Dir string
	// Binary is the ostiole to run for the steps that have to happen
	// outside the sandbox; empty finds the installed one.
	Binary string
	// Run runs commands; nil means the real ones.
	Run Runner
	// Locate finds a command on the router. nil looks on PATH and in the
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

// UnitState is one systemd unit as this router has it.
type UnitState struct {
	Name    string `json:"name"`
	Active  string `json:"active"`
	Enabled string `json:"enabled"`
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
	// handover would retire.
	Managers []string `json:"managers,omitempty"`
	// Pending is whether a handover is waiting to be confirmed, in which
	// case the only thing to do is confirm it or let it revert.
	Pending bool `json:"pending"`
}

// Report is what this router has to say about itself.
type Report struct {
	Root    bool   `json:"root"`
	Distro  string `json:"distro,omitempty"`
	Manager string `json:"manager,omitempty"`
	Kernel  string `json:"kernel,omitempty"`
	// Units are Ostiole's own and the one it hands the network to.
	Units []UnitState `json:"units"`
	// Present is the daemons a router runs, by command name.
	Present map[string]bool `json:"present"`
	Network NetworkState    `json:"network"`
	Legacy  iptables.Report `json:"legacy"`
	// Bluetooth is blocked, loaded, or absent.
	Bluetooth  string `json:"bluetooth,omitempty"`
	Firewalled bool   `json:"firewalled"`
}

// reportedUnits are the units the page shows a badge for.
var reportedUnits = []string{install.DaemonUnit, install.FirewallUnit, install.NetworkdUnit}

// reportedCommands are the daemons Ostiole drives, which the install
// script puts on the router.
var reportedCommands = []string{"nft", "dnsmasq", "unbound", "miniupnpd", "pppd", "tc", "tailscaled",
	"hostapd", "iw"}

// Commands names them in report order, for a console that lists them.
func Commands() []string { return append([]string(nil), reportedCommands...) }

// managers are the package managers a distribution is recognised by.
var managers = []string{"apt-get", "dnf", "pacman", "zypper"}

// Status gathers everything, and never fails: a page that cannot say what
// is wrong with a router is worse than a page with gaps in it, and every
// probe here has an answer for "could not tell".
func Status(ctx context.Context, d Deps) Report {
	rep := Report{
		Root:    d.Root,
		Distro:  distro(),
		Manager: packageManager(d),
		Kernel:  kernelRelease(),
		Units:   units(ctx, d),
		Present: present(d),
	}
	rep.Bluetooth = install.BluetoothState()
	rep.Legacy = iptables.Detect(ctx, d.legacy())
	rep.Network = networkState(ctx, d)
	rep.Firewalled = d.TableLoaded != nil && d.TableLoaded(ctx)
	return rep
}

// distro is what the image calls itself, or "".
func distro() string {
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// packageManager names the one this router has, or "".
func packageManager(d Deps) string {
	for _, m := range managers {
		if d.locate(m) != "" {
			return m
		}
	}
	return ""
}

// kernelRelease is what `uname -r` prints, read from /proc so it works
// without a shell.
func kernelRelease() string {
	raw, err := os.ReadFile(kernel.ReleaseFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func units(ctx context.Context, d Deps) []UnitState {
	out := make([]UnitState, 0, len(reportedUnits))
	for _, name := range reportedUnits {
		st := UnitState{Name: name, Active: "unknown", Enabled: "unknown"}
		if d.Units != nil {
			active, _ := d.Units.Run(ctx, "is-active", name)
			enabled, _ := d.Units.Run(ctx, "is-enabled", name)
			st.Active, st.Enabled = firstLine(active), firstLine(enabled)
		}
		out = append(out, st)
	}
	return out
}

func present(d Deps) map[string]bool {
	out := map[string]bool{}
	for _, name := range reportedCommands {
		out[name] = d.locate(name) != ""
	}
	return out
}

// networkState reports who owns the addressing.
func networkState(ctx context.Context, d Deps) NetworkState {
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
	if !st.Owned && d.Units != nil {
		st.Managers = install.NetworkTakeoverTargets(ctx, d.Units)
	}
	st.Pending = revertArmed(ctx, d)
	return st
}

// revertArmed reports whether a network handover is waiting to be
// confirmed. The timer is the only record of that, and it is the thing
// that will put the old manager back if nobody says otherwise.
func revertArmed(ctx context.Context, d Deps) bool {
	if !d.Root || d.Units == nil {
		return false
	}
	out, err := d.Units.Run(ctx, "is-active", install.RevertTimerUnit+".timer")
	return err == nil && strings.TrimSpace(out) == "active"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}
