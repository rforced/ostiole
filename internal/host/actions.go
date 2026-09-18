package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// ErrNotRoot means the daemon cannot change the router it is running on.
var ErrNotRoot = errors.New("preparing this router needs root; this daemon is not running as root")

// SetUp installs the components named by key and writes the units that
// point them at Ostiole's generated configuration. It is the real work,
// and it runs outside the daemon's sandbox: from the command line
// directly, and from the API through the command line (see Drive).
//
// Nothing here decides whether a service runs. That is the Enabled switch
// in the configuration, applied like everything else; this only makes it
// possible.
//
// With restart, a running ostiole.service is restarted at the end for the
// components that get a unit of Ostiole's (see RestartsDaemon). The API
// passes false and restarts the daemon itself once it has answered,
// because it is the daemon.
func SetUp(ctx context.Context, d Deps, keys []string, restart bool) (string, error) {
	if len(keys) == 0 {
		return "", errors.New("name at least one component to set up")
	}
	manager := ""
	if d.Packages != nil && d.Packages.Driver != nil {
		manager = d.Packages.Driver.Name()
	}
	var said []string
	var pkgs []string
	opts := services.SetupOptions{PackageManager: manager, NoRestart: !restart}
	wantsServices := RestartsDaemon(keys)
	for _, key := range keys {
		c, ok := sysupdate.ComponentByKey(key)
		if !ok {
			return "", fmt.Errorf("%q is not a component Ostiole installs", key)
		}
		names, avail := c.Packages(manager)
		switch avail {
		case sysupdate.Unpackaged:
			return strings.Join(said, "\n"), unpackaged(c, manager)
		case sysupdate.Bundled:
			said = append(said, c.Label+" comes with systemd on this distribution; nothing to install")
		}
		switch key {
		case "dnsmasq":
			opts.Dnsmasq = true
		case "unbound":
			opts.Resolver = true
		case "miniupnpd":
			opts.UPnP = true
		case "pppd":
			opts.PPPoE = true
		case "tc":
			// tc has a helper of its own because a router that has
			// iproute2 can still be missing the command, which is a
			// Red Hat family quirk worth keeping in one place.
			if err := install.EnsureTC(ctx, d.run(), manager, d.log()); err != nil {
				return strings.Join(said, "\n"), err
			}
			said = append(said, "traffic shaping: tc is installed")
		case "networkd":
			if d.Units == nil {
				return strings.Join(said, "\n"), errors.New("no systemd to ask about systemd-networkd")
			}
			if err := install.EnsureNetworkd(ctx, d.Units, d.run(), manager, d.log()); err != nil {
				return strings.Join(said, "\n"), err
			}
			said = append(said, "networking: systemd-networkd is installed")
		default:
			pkgs = append(pkgs, names...)
		}
	}
	if len(pkgs) > 0 {
		out, err := installPackages(ctx, d, pkgs)
		if out != "" {
			said = append(said, out)
		}
		if err != nil {
			return strings.Join(said, "\n"), err
		}
	}
	if wantsServices {
		// services.Setup installs what it needs itself, masks the
		// distribution's own units for the same job, and writes ours.
		if err := services.Setup(ctx, services.New(), opts, d.log()); err != nil {
			return strings.Join(said, "\n"), err
		}
		said = append(said, "services: units written and competing resolvers masked")
	}
	return strings.Join(said, "\n"), nil
}

// serviceComponents are the components services.Setup writes a unit
// for. Each also gets a directory the daemon has to be able to write —
// /etc/dnsmasq.d, /etc/unbound, /etc/miniupnpd, /etc/ppp — and the
// daemon's mount namespace is built once, at start.
var serviceComponents = map[string]bool{"dnsmasq": true, "unbound": true, "miniupnpd": true, "pppd": true}

// RestartsDaemon reports whether setting up these components ends with a
// restart of ostiole.service, so the front door that cannot restart in
// the middle of a request knows to do it afterwards.
func RestartsDaemon(keys []string) bool {
	for _, key := range keys {
		if serviceComponents[key] {
			return true
		}
	}
	return false
}

// RestartDaemonSoon restarts ostiole.service from a transient unit a
// moment from now, so a daemon that has just answered a request can be
// restarted without cutting that answer off. A daemon that is not running
// is left alone: it builds the namespace it needs when it next starts.
func RestartDaemonSoon(ctx context.Context, d Deps) error {
	if d.Units == nil {
		return errors.New("no systemd to restart the daemon with")
	}
	if out, err := d.Units.Run(ctx, "is-active", install.DaemonUnit); err != nil || strings.TrimSpace(out) != "active" {
		return nil
	}
	out, err := d.run().Run(ctx, "systemd-run", "--on-active=2", "--collect", "--quiet",
		"--", "systemctl", "restart", install.DaemonUnit)
	if err != nil {
		return fmt.Errorf("schedule the restart of %s: %w: %s", install.DaemonUnit, err, tail(out))
	}
	return nil
}

// unpackaged explains a component nobody ships here, using the
// component's own note when it has one — the miniupnpd case, where the
// answer is to unpack a Fedora build rather than to go without.
func unpackaged(c sysupdate.Component, manager string) error {
	where := "this distribution"
	if manager != "" {
		where = "a " + manager + " router"
	}
	msg := c.Label + " is not packaged for " + where
	if c.Note != "" {
		msg += ".\n\n" + c.Note
	}
	return errors.New(msg)
}

// installPackages installs with the router's own package manager. It runs
// the command directly rather than through the update manager, because
// everything in SetUp is already outside the sandbox and a transaction
// inside a transaction only makes the journal harder to read.
func installPackages(ctx context.Context, d Deps, pkgs []string) (string, error) {
	if d.Packages == nil || d.Packages.Driver == nil {
		return "", sysupdate.ErrNoManager
	}
	argv := d.Packages.Driver.InstallArgv(pkgs)
	if len(argv) == 0 {
		return "", sysupdate.ErrNothingToDo
	}
	ctx, cancel := context.WithTimeout(ctx, sysupdate.InstallTimeout)
	defer cancel()
	out, err := d.run().Run(ctx, argv[0], argv[1:]...)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, tail(out))
	}
	return "installed " + strings.Join(pkgs, ", "), nil
}

// TakeoverFirewall stops, disables and masks every competing firewall
// service. It refuses on a router with no ruleset loaded: masking the old
// firewall before ours is in the kernel leaves the router unfiltered, and
// this is the one moment nobody should be improvising.
func TakeoverFirewall(ctx context.Context, d Deps) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	if d.TableLoaded == nil || !d.TableLoaded(ctx) {
		return "", errors.New("refusing: apply and confirm an Ostiole configuration first, so this router has a firewall before the old one is masked")
	}
	rep := Status(ctx, d)
	var targets []string
	for _, c := range rep.Competitors {
		if c.Kind == "firewall" && c.Conflicts {
			targets = append(targets, c.Name)
		}
	}
	if len(targets) == 0 {
		return "no competing firewall service is active or enabled", nil
	}
	if err := install.Takeover(ctx, d.Units, targets, d.log()); err != nil {
		return "", err
	}
	return "disabled and masked: " + strings.Join(targets, ", "), nil
}

// RemovePackages takes a competitor's packages off the router. With
// preview it changes nothing and returns what the package manager says it
// would do, which is the only honest way to ask somebody to agree to a
// removal.
//
// A unit that is still running or still enabled is refused. Removing the
// package under a running service is how a router ends up with no addresses
// and no way to get any, and masking it first costs one click.
//
// The plan is read before anything is removed, preview or not, and a plan
// that names a package Ostiole itself needs is refused: firewalld requires
// nftables, and a package manager that tidies dependencies would take the
// nft command with the old firewall. The plan is returned with the
// refusal, so the console can show it.
func RemovePackages(ctx context.Context, d Deps, units []string, preview bool) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	if d.Packages == nil {
		return "", sysupdate.ErrNoManager
	}
	if len(units) == 0 {
		return "", errors.New("name at least one service whose packages should be removed")
	}
	rep := Status(ctx, d)
	var pkgs []string
	for _, name := range units {
		st, ok := find(rep.Competitors, name)
		switch {
		case !ok:
			return "", fmt.Errorf("%s is not a competing service on this router", name)
		case st.Conflicts:
			return "", fmt.Errorf("%s is still %s and %s; disable and mask it before removing its packages",
				name, st.Active, st.Enabled)
		case len(st.Packages) == 0:
			detail := st.Note
			if detail == "" {
				detail = "Ostiole does not know what it is packaged as here."
			}
			return "", fmt.Errorf("%s cannot be removed by Ostiole. %s", name, detail)
		}
		pkgs = append(pkgs, st.Packages...)
	}
	plan, err := d.Packages.Remove(ctx, pkgs, true)
	if err != nil {
		return plan, err
	}
	if err := refuseRemoval(plan, units, protectedPackages(rep)); err != nil {
		return plan, err
	}
	if preview {
		return plan, nil
	}
	return d.Packages.Remove(ctx, pkgs, false)
}

// protectedPackages are the packages a removal must never take: the ones
// behind every component this router has, and nftables whether or not
// nft is there right now, because without it nothing can load a ruleset.
func protectedPackages(rep Report) []string {
	var out []string
	for _, c := range rep.Components {
		if c.Present || c.Key == "nft" {
			out = append(out, c.Packages...)
		}
	}
	return out
}

// refuseRemoval reads a package manager's plan for the packages it would
// take, and refuses when one of them is protected.
func refuseRemoval(plan string, units, protected []string) error {
	taken := namedIn(decisive(plan), protected)
	if len(taken) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to remove %s: the package manager would also remove %s, which Ostiole needs",
		join(units), join(taken))
}

// decisive narrows a plan to the lines that say what the manager is
// going to do, for the managers that also print what they are not going
// to do. apt's simulation lists the packages that a removal would leave
// unused under "automatically installed and are no longer required" —
// advice about a later `apt autoremove`, not part of this transaction.
// Reading it as the transaction refuses a perfectly good install on any
// router where nftables arrived as a dependency of the firewall being
// replaced, which is every stock Ubuntu with ufw on it. The "Remv"
// lines are what apt will actually do.
func decisive(plan string) string {
	var remv []string
	for _, line := range strings.Split(plan, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Remv ") {
			remv = append(remv, line)
		}
	}
	if len(remv) == 0 {
		return plan
	}
	return strings.Join(remv, "\n")
}

// namedIn reports which of the names a plan mentions as a package. Every
// manager prints one package per token — "nftables" on its own line or in
// a list, or "nftables-1:1.1.5-6.el10" in dnf's transaction — so a token
// that is the name, or the name followed by a dash and a version, counts.
// A different package that merely contains the name (python3-nftables)
// does not.
func namedIn(plan string, names []string) []string {
	var out []string
	for _, name := range names {
		if name == "" {
			continue
		}
		found := false
		for _, tok := range strings.Fields(plan) {
			tok = strings.TrimRight(tok, ",;:)")
			tok = strings.TrimLeft(tok, "(")
			if tok == name || versioned(tok, name) {
				found = true
				break
			}
		}
		if found && !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// versioned reports whether tok is name followed by a version, the way
// rpm writes a package: nftables-1:1.1.5-6.el10_2.x86_64.
func versioned(tok, name string) bool {
	rest, ok := strings.CutPrefix(tok, name+"-")
	return ok && rest != "" && rest[0] >= '0' && rest[0] <= '9'
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// find looks a competitor up by unit name.
func find(all []CompetitorState, name string) (CompetitorState, bool) {
	for _, c := range all {
		if c.Name == name {
			return c, true
		}
	}
	return CompetitorState{}, false
}

// FlushLegacy clears what an older firewall left in the kernel. With no
// ids it sweeps everything with no recognisable owner; with ids it clears
// exactly those tables, which is the only way to take one that Ostiole
// would otherwise leave alone.
func FlushLegacy(ctx context.Context, d Deps, ids []string) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	deps := d.legacy()
	rep := iptables.Detect(ctx, deps)
	targets := rep.Sweepable()
	if len(ids) > 0 {
		targets = nil
		for _, id := range ids {
			t, ok := rep.Find(id)
			if !ok {
				return "", fmt.Errorf("%s is not a leftover ruleset on this router", id)
			}
			// Named explicitly, so an owner is a warning and not a veto:
			// the operator can see whose it is on the page.
			t.Owner = ""
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return "", iptables.ErrNothingToFlush
	}
	return iptables.Flush(ctx, deps, targets)
}

// NetworkTakeoverWindow is how long the handover waits to be confirmed
// before the revert timer puts the previous manager back.
const NetworkTakeoverWindow = 3 * time.Minute

// TakeNetwork hands the router's addressing to systemd-networkd. It runs
// the command line, which detaches the switch into a transient unit of
// its own and arms a revert timer: losing the session mid-switch is the
// expected case, not the surprise, and the revert is what makes that
// survivable.
func TakeNetwork(ctx context.Context, d Deps, window time.Duration) (string, error) {
	if window <= 0 {
		window = NetworkTakeoverWindow
	}
	return Drive(ctx, d, "takeover", "--network", "--yes", "--confirm-window", window.String())
}

// ConfirmNetwork keeps the handover and disarms the revert timer.
func ConfirmNetwork(ctx context.Context, d Deps) (string, error) {
	return Drive(ctx, d, "takeover", "--network", "--confirm")
}

// RevertNetwork puts the previous network manager back now, rather than
// waiting for the timer.
func RevertNetwork(ctx context.Context, d Deps) (string, error) {
	return Drive(ctx, d, "takeover", "--network", "--revert", "--yes")
}

// Drive runs the installed ostiole on the host and returns what it
// printed.
//
// This is the hinge between the two front doors. The daemon runs behind
// ProtectSystem=strict, so it cannot write a unit file, a package
// database or /etc/ppp; a transient unit has no sandbox. Rather than
// teach the daemon to do these things another way, the API runs the same
// command an operator would type, with the same globals the daemon is
// running with, and shows them its output.
func Drive(ctx context.Context, d Deps, args ...string) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	bin := d.Binary
	if bin == "" {
		found, err := install.ServiceBinary(install.DefaultLayout())
		if err != nil {
			return "", fmt.Errorf("find the installed ostiole: %w", err)
		}
		bin = found
	}
	argv := []string{"--config-dir", d.Dir}
	if d.NFT != "" {
		argv = append(argv, "--nft", d.NFT)
	}
	if d.Backend != "" {
		argv = append(argv, "--network-backend", d.Backend)
	}
	argv = append(argv, args...)
	out, err := sysupdate.NewHostRunner(nil).Run(ctx, bin, argv...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("ostiole %s: %w: %s", strings.Join(args, " "), err, tail(out))
	}
	return text, nil
}

// tail keeps the end of a failed command's output, which is where the
// reason is.
func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 800 {
		s = "…" + s[len(s)-800:]
	}
	return s
}
