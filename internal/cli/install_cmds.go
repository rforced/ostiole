package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/kernel"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/timezone"
)

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

func newInstallCmd(g *globals) *cobra.Command {
	opts := install.Options{}
	var ignoreKernel, yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write Ostiole's units, load a ruleset, and take the network over",
		Long: `Writes ostiole-firewall.service and ostiole.service, writes the units
that point dnsmasq, unbound, pppd and miniupnpd at generated
configuration, persists the router sysctls and a ceiling on the journal,
sets the clock to UTC, masks the firewalls it replaces, clears what they
left in the kernel, and hands addressing to systemd-networkd with the
addresses this router has now.

Packages are the install script's job (ostiole repair re-runs it). Until
a configuration is applied and confirmed the router runs a bootstrap
ruleset: established connections, ICMP, its own DHCP replies and the
management ports get in, nothing is forwarded.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			// Refusing here is the last comfortable moment: once the units
			// are enabled, an unsupported kernel becomes a running firewall's
			// problem rather than an installer's.
			if err := kernel.Check(); err != nil {
				if !ignoreKernel {
					return fmt.Errorf("%w (pass --ignore-kernel-version to install anyway)", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v; continuing because --ignore-kernel-version was given\n", err)
			}
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			lay := install.DefaultLayout()
			lay.ConfigDir = g.configDir
			d := g.hostDeps()
			sc := install.ExecSystemctl{}

			bootstrap := !g.store().Exists() && !rulesetExists(g.configDir)
			firewalls := conflictingFirewalls(ctx, sc)
			owned := networkOwned(g.configDir)
			// What this router has already decided. `ostiole repair` runs
			// this command again, so anything installed from a default here
			// would quietly undo a setting the configuration holds.
			cfg, _ := g.store().Load()
			maxUse, retention := journalLimits(cfg)
			opts.Timezone = installZone(opts.Timezone, cfg)
			// The same goes for the UI's address, which only the unit holds.
			if opts.Listen == "" {
				opts.Listen = install.Listen(lay)
			}
			if opts.Listen == "" {
				opts.Listen = install.DefaultListen
			}

			fmt.Fprintln(out, "Ostiole will:")
			fmt.Fprintf(out, "  write and enable:     %s, %s\n", install.FirewallUnit, install.DaemonUnit)
			fmt.Fprintf(out, "  serve the web UI on:  %s\n", opts.Listen)
			fmt.Fprintf(out, "  write service units:  %s\n", strings.Join(serviceUnits(), ", "))
			fmt.Fprintf(out, "  persist:              router sysctls (%s), journal ceiling (%s)\n", sysctl.ConfFile, journald.ConfFile)
			fmt.Fprintf(out, "  block:                Bluetooth (%s)\n", install.BluetoothConfFile)
			if opts.Timezone != "-" {
				fmt.Fprintf(out, "  set the clock to:     %s\n", opts.Timezone)
			}
			fmt.Fprintf(out, "  bound the journal to: %dG, %d days\n", maxUse, retention)
			if bootstrap {
				fmt.Fprintln(out, "  write:                a bootstrap ruleset that forwards nothing")
			}
			if len(firewalls) > 0 {
				fmt.Fprintf(out, "  stop and mask:        %s\n", strings.Join(firewalls, ", "))
			}
			fmt.Fprintln(out, "  clear:                whatever an older firewall left in the kernel")
			if owned {
				fmt.Fprintln(out, "  leave alone:          addressing, which systemd-networkd already has")
			} else {
				fmt.Fprintln(out, "  hand to networkd:     this router's addresses, keeping the ones it has now")
			}
			if dryRun {
				return nil
			}
			if !yes {
				if err := confirmPrompt(cmd, "\nproceed?"); err != nil {
					return err
				}
			}

			if err := (journald.System{Run: install.ExecRunner{}}).Apply(ctx, maxUse, retention); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not bound the system journal: %v\n", err)
			}
			// Before Install: starting the daemon starts the firewall unit,
			// which loads the fallback and fails when there is no ruleset.
			if bootstrap {
				ports := []uint16{model.DefaultWebPort}
				if port := listenPortOf(opts.Listen); port != 0 {
					ports = []uint16{port}
				}
				ports = append(ports, 22)
				if err := os.MkdirAll(g.configDir, 0o700); err != nil {
					return fmt.Errorf("create %s: %w", g.configDir, err)
				}
				if err := os.WriteFile(filepath.Join(g.configDir, store.RulesetFile), []byte(nft.Bootstrap(ports)), 0o600); err != nil {
					return fmt.Errorf("write the bootstrap ruleset: %w", err)
				}
				fmt.Fprintln(out, "wrote the bootstrap ruleset")
			}
			rep, err := install.Install(ctx, sc, lay, opts, slog.Default())
			if err != nil {
				return err
			}
			// The firewall unit was enabled; make sure what it loads is in
			// the kernel before the old firewall is retired.
			if o, err := sc.Run(ctx, "restart", install.FirewallUnit); err != nil {
				return fmt.Errorf("load the ruleset: %w: %s", err, o)
			}
			fmt.Fprintf(out, "installed %s and %s\n", rep.Binary, strings.Join(rep.Units, ", "))
			if rep.Timezone != "" {
				fmt.Fprintf(out, "clock set to %s\n", rep.Timezone)
			}
			if err := writeServiceUnits(ctx, lay.ConfigDir); err != nil {
				return err
			}
			if len(firewalls) > 0 {
				if err := install.Takeover(ctx, sc, firewalls, slog.Default()); err != nil {
					return err
				}
				fmt.Fprintf(out, "masked and stopped: %s\n", strings.Join(firewalls, ", "))
			}
			switch said, err := host.FlushLegacy(ctx, d, nil); {
			case err == nil:
				fmt.Fprintln(out, said)
			case !errors.Is(err, iptables.ErrNothingToFlush):
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not clear what the old firewall left: %v\n", err)
			}
			if g.netBackend != "none" {
				if err := handOverNetwork(cmd, g, owned); err != nil {
					return err
				}
			}
			// The daemon's mount namespace is built when it starts, and a
			// ReadWritePaths entry written with a leading dash is skipped
			// while its path is missing. The service directories were made a
			// moment ago, so the daemon running now cannot write them.
			if state, err := sc.Run(ctx, "is-active", install.DaemonUnit); err == nil && state == "active" {
				if o, err := sc.Run(ctx, "restart", install.DaemonUnit); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not restart %s: %v: %s\n", install.DaemonUnit, err, o)
				} else {
					fmt.Fprintf(out, "%s restarted\n", install.DaemonUnit)
				}
			}
			fmt.Fprintf(out, "\nweb UI (self-signed certificate):\n")
			for _, u := range uiURLs(opts.Listen) {
				fmt.Fprintf(out, "  %s\n", u)
			}
			fmt.Fprintf(out, "\nnext:\n  1. create the admin account in the web UI, or: ostiole reset-password\n  2. run the setup wizard in the web UI, or: ostiole init --lan ... && ostiole apply\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Listen, "listen", "", "address the web UI listens on (default: what the installed unit has, else "+install.DefaultListen+")")
	cmd.Flags().StringVar(&opts.Timezone, "timezone", "", `timezone to set, UTC by default; "-" leaves the clock alone`)
	cmd.Flags().BoolVar(&ignoreKernel, "ignore-kernel-version", false,
		fmt.Sprintf("install even if the kernel is older than %s", kernel.Minimum))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
	return cmd
}

// journalLimits are the bounds the install writes: the ones the
// configuration holds once this router has a configuration, and the
// defaults before it does.
func journalLimits(cfg *model.Config) (maxUseGB, retentionDays int) {
	if cfg == nil {
		return journald.DefaultMaxUseGB, journald.DefaultRetentionDays
	}
	return cfg.System.Logging.MaxUse(), cfg.System.Logging.Retention()
}

// installZone is the clock the install sets: what was asked for on the
// command line, else what the configuration says, else UTC. "-" leaves
// the clock alone and is passed through.
func installZone(flag string, cfg *model.Config) string {
	if flag != "" {
		return flag
	}
	if cfg != nil && cfg.System.Zone() != "" {
		return cfg.System.Zone()
	}
	return timezone.Default
}

// serviceUnits are the units written for the daemons a router drives.
func serviceUnits() []string {
	return []string{services.Unit, services.UnboundUnit, services.PPPoEUnit, services.UPnPUnit,
		services.TailscaleUnit, services.WirelessUnit, services.ProxyUnit, services.NTPUnit}
}

// writeServiceUnits points dnsmasq, unbound, pppd, miniupnpd, tailscaled,
// hostapd, the reverse proxy and chronyd at Ostiole's generated
// configuration. One whose binary is not on the router is skipped; the
// install script is what puts them there.
func writeServiceUnits(ctx context.Context, configDir string) error {
	opts := services.SetupOptions{
		Dnsmasq: true, Resolver: true, PPPoE: true, UPnP: true, Tailscale: true, Wireless: true,
		Proxy: true, NTP: true, ConfigDir: configDir, NoRestart: true,
	}
	return services.Setup(ctx, services.New(), opts, slog.Default())
}

// conflictingFirewalls names the firewall services that are running or
// would start at boot.
func conflictingFirewalls(ctx context.Context, sc install.Systemctl) []string {
	comp, err := install.Competitors(ctx, sc)
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range comp {
		if c.Kind == "firewall" && c.Conflicts() {
			out = append(out, c.Name)
		}
	}
	return out
}

// networkOwned reports whether a handover has already happened.
func networkOwned(dir string) bool {
	rec, err := install.LoadTakeoverRecord(dir)
	return err == nil && rec != nil
}

// handOverNetwork gives addressing to systemd-networkd and then checks
// that the router kept what it had. A router that has already been handed
// over is left alone.
func handOverNetwork(cmd *cobra.Command, g *globals, owned bool) error {
	out := cmd.OutOrStdout()
	if owned {
		fmt.Fprintln(out, "systemd-networkd already has this router's addresses")
		return nil
	}
	before, err := network.Discover()
	if err != nil {
		return err
	}
	if err := networkTakeover(cmd, g, networkTakeoverOptions{yes: true, window: host.NetworkTakeoverWindow}); err != nil {
		// The router has its units, its ruleset and its firewall; whoever
		// owns the addresses goes on owning them. `ostiole takeover
		// --network` is the retry.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: addressing was left with its current manager: %v\n", err)
		return nil
	}
	if kept, missing := addressesKept(before); kept {
		install.CancelNetworkRevert(cmd.Context(), install.ExecRunner{})
		fmt.Fprintln(out, "addresses kept, systemd-networkd in charge")
		return nil
	} else if len(missing) > 0 {
		fmt.Fprintf(out, "still missing: %s\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(out, "the previous network manager returns in %s unless `ostiole takeover --network --confirm` runs\n",
		host.NetworkTakeoverWindow)
	return nil
}

// addressesKept polls until every global address that was there before is
// back and a default route exists. Twenty seconds is a DHCP round trip
// with room to spare; longer than that and the revert timer is the right
// answer rather than more waiting.
func addressesKept(before []network.Link) (bool, []string) {
	want := map[string]bool{}
	for _, l := range before {
		if l.Kind == "loopback" {
			continue
		}
		for _, a := range l.Addresses {
			if p, err := netip.ParsePrefix(a); err == nil && p.Addr().IsGlobalUnicast() {
				want[a] = true
			}
		}
	}
	var missing []string
	for range 20 {
		time.Sleep(time.Second)
		missing = missing[:0]
		have := map[string]bool{}
		links, err := network.Discover()
		if err != nil {
			continue
		}
		for _, l := range links {
			for _, a := range l.Addresses {
				have[a] = true
			}
		}
		for a := range want {
			if !have[a] {
				missing = append(missing, a)
			}
		}
		v4, v6, err := network.DefaultRoutes()
		if err != nil {
			continue
		}
		if len(missing) == 0 && (len(v4) > 0 || len(v6) > 0) {
			return true, nil
		}
	}
	return false, missing
}

// rulesetExists reports whether a ruleset is already on disk, in which
// case the bootstrap would only get in its way.
func rulesetExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, store.RulesetFile))
	return err == nil
}

// installedListen is where the installed daemon serves the UI.
func installedListen() string {
	if l := install.Listen(install.DefaultLayout()); l != "" {
		return l
	}
	return install.DefaultListen
}

// listenPortOf reads the port of a listen address, 0 when it has none.
func listenPortOf(listen string) uint16 {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}

// uiURLs lists https URLs for every global address on the router.
func uiURLs(listen string) []string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = strconv.Itoa(model.DefaultWebPort)
	}
	suffix := ""
	if port != "443" {
		suffix = ":" + port
	}
	var urls []string
	links, err := network.Discover()
	if err != nil {
		return []string{"https://<this-host>" + suffix + "/"}
	}
	for _, l := range links {
		if l.Kind == "loopback" {
			continue
		}
		for _, a := range l.Addresses {
			p, err := netip.ParsePrefix(a)
			if err != nil || !p.Addr().IsGlobalUnicast() {
				continue
			}
			host := p.Addr().String()
			if p.Addr().Is6() {
				host = "[" + host + "]"
			}
			urls = append(urls, "https://"+host+suffix+"/")
		}
	}
	if len(urls) == 0 {
		urls = []string{"https://<this-host>" + suffix + "/"}
	}
	return urls
}

func newTakeoverCmd(g *globals) *cobra.Command {
	var yes, dryRun, netFlag, confirm, revert, inUnit bool
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "takeover",
		Short: "Hand this router's addressing to systemd-networkd",
		Long: `Writes the networkd units rendered from the confirmed configuration —
or, on a router with none yet, from the addresses it has right now — stops
and masks NetworkManager and friends, and starts networkd. Existing
addresses persist across the switch; DHCP leases are re-acquired.

The switch runs in a transient unit and arms a timer that puts the
previous manager back unless --confirm runs in time, because losing the
session driving it is the expected case. --revert undoes it now.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			if !netFlag {
				return errors.New("--network is the only mode; firewalls are retired by `ostiole install`")
			}
			o := networkTakeoverOptions{yes: yes, dryRun: dryRun, window: window, confirm: confirm, revert: revert, inUnit: inUnit}
			if !confirm && !revert {
				// A handover leaves the router filtering with whatever is in
				// the kernel, so there has to be something in it.
				eng, err := g.engine()
				if err != nil {
					return err
				}
				st, err := eng.Status(cmd.Context())
				if err != nil {
					return err
				}
				if !st.TableLoaded {
					return errors.New("refusing: load an Ostiole ruleset first (ostiole install, or ostiole load)")
				}
			}
			return networkTakeover(cmd, g, o)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would change")
	cmd.Flags().BoolVar(&netFlag, "network", false, "hand addressing to systemd-networkd")
	cmd.Flags().DurationVar(&window, "confirm-window", 3*time.Minute, "restore the previous network manager unless --confirm runs within this time (0 disables)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "keep the handover and disarm the revert timer")
	cmd.Flags().BoolVar(&revert, "revert", false, "undo the handover and restore the previous network manager")
	cmd.Flags().BoolVar(&inUnit, "in-unit", false, "internal: already running inside the detached systemd unit")
	_ = cmd.Flags().MarkHidden("in-unit")
	return cmd
}

type networkTakeoverOptions struct {
	yes, dryRun     bool
	window          time.Duration
	confirm, revert bool
	inUnit          bool
}

// Transient unit names for the detached switch.
const (
	takeoverUnit = "ostiole-network-takeover"
	revertUnit   = "ostiole-network-revert-now"
)

// detach re-runs this command inside a transient systemd unit so that
// losing the SSH session mid-switch cannot kill it.
func detach(ctx context.Context, g *globals, unit string, args ...string) error {
	exe, err := install.ServiceBinary(install.DefaultLayout())
	if err != nil {
		return err
	}
	argv := append([]string{exe, "--config-dir", g.configDir, "--nft", g.nftBin, "--network-backend", g.netBackend, "takeover", "--network", "--in-unit"}, args...)
	return install.Detached(ctx, install.ExecRunner{}, unit, argv...)
}

func confirmPrompt(cmd *cobra.Command, question string) error {
	in, err := promptReader()
	if err != nil {
		return err
	}
	defer in.Close()
	fmt.Fprint(cmd.OutOrStdout(), question+" [y/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return errors.New("aborted")
	}
	return nil
}

// promptReader is what the question is put to. Under the documented
// install — `curl … | sudo sh` — stdin is the pipe the script itself
// came down and is at its end, so asking there is asking nobody: the
// terminal is still attached, it is just not stdin, and /dev/tty is how
// to reach it. A router with no terminal at all, a cloud-init script or
// a container build, has to say --yes.
func promptReader() (io.ReadCloser, error) {
	if stdinIsTerminal() {
		return io.NopCloser(os.Stdin), nil
	}
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return nil, errors.New("not interactive; pass --yes to proceed (curl … | sudo sh -s -- --yes)")
	}
	return tty, nil
}

// seedOrStored is the configuration the handover renders from: the
// confirmed one when there is one, and otherwise the router's own live
// addressing, so a fresh install keeps every address it came up with.
func seedOrStored(g *globals) (*model.Config, error) {
	if g.store().Exists() {
		return g.store().Load()
	}
	return install.SeedNetwork()
}

func networkTakeover(cmd *cobra.Command, g *globals, o networkTakeoverOptions) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	sc := install.ExecSystemctl{}
	run := install.ExecRunner{}

	if o.confirm {
		if install.CancelNetworkRevert(ctx, run) {
			fmt.Fprintln(out, "confirmed: systemd-networkd stays in charge; revert timer disarmed")
		} else {
			fmt.Fprintln(out, "no revert timer was armed; nothing to confirm")
		}
		return nil
	}
	if o.revert {
		rec, err := install.LoadTakeoverRecord(g.configDir)
		if err != nil {
			return err
		}
		if rec == nil {
			return errors.New("no network takeover record found; nothing to revert")
		}
		if !o.inUnit {
			restorable := install.Restorable(ctx, sc, rec.Managers)
			if len(restorable) == 0 {
				fmt.Fprintln(out, "nothing to revert to: the previous network manager is gone from this router; systemd-networkd stays in charge")
				return nil
			}
			fmt.Fprintf(out, "reverting in unit %s; if this session drops, it still completes (journalctl -u %s)\n", revertUnit, revertUnit)
			if err := detach(ctx, g, revertUnit, "--revert"); err != nil {
				return err
			}
			fmt.Fprintf(out, "reverted: %s restored, systemd-networkd stopped\n", strings.Join(restorable, ", "))
			return nil
		}
		install.CancelNetworkRevert(ctx, run)
		if err := install.NetworkRevert(ctx, sc, network.KernelRoutes{}, rec.Managers, slog.Default()); err != nil {
			return err
		}
		slog.Info("network takeover reverted", "restored", rec.Managers)
		return nil
	}

	cfg, err := seedOrStored(g)
	if err != nil {
		return err
	}
	nd := network.NewNetworkd()
	files, err := nd.Render(cfg)
	if err != nil {
		return fmt.Errorf("render network units: %w", err)
	}

	// Preflight: compare the configuration with what is live.
	live, err := network.Discover()
	if err != nil {
		return err
	}
	liveByName := map[string]network.Link{}
	for _, l := range live {
		liveByName[l.Name] = l
	}
	managed := map[string]bool{}
	fmt.Fprintln(out, "\nINTERFACE   CONFIGURED                      LIVE")
	for _, in := range cfg.Interfaces {
		managed[in.Name] = true
		l, ok := liveByName[in.Name]
		liveAddrs := "absent"
		if ok {
			liveAddrs = strings.Join(l.Addresses, " ")
		}
		conf := fmt.Sprintf("%s v4=%s", or(in.Zone, "-"), in.IPv4.Mode)
		if in.IPv4.Address != "" {
			conf += " " + in.IPv4.Address
		}
		conf += " v6=" + string(in.IPv6.Mode)
		if in.IPv6.Address != "" {
			conf += " " + in.IPv6.Address
		}
		if !in.Enabled {
			conf += " (disabled)"
		}
		fmt.Fprintf(out, "%-11s %-31s %s\n", in.Name, conf, liveAddrs)
		if in.Enabled && in.IPv4.Mode == model.AddrStatic && ok && !hasAddress(l, in.IPv4.Address) {
			fmt.Fprintf(out, "  warning: %s does not currently carry %s; the address will change on takeover\n", in.Name, in.IPv4.Address)
		}
		if in.Enabled && ok && in.MTU == 0 && l.MTU != 0 && l.MTU != 1500 {
			fmt.Fprintf(out, "  warning: %s currently has MTU %d but the configuration sets none; set mtu=%d on it to keep that after a reboot\n", in.Name, l.MTU, l.MTU)
		}
	}
	for _, l := range live {
		if l.Kind == "loopback" || managed[l.Name] || len(l.Addresses) == 0 {
			continue
		}
		fmt.Fprintf(out, "  warning: %s (%s) is not in the configuration; networkd will leave it alone and its DHCP lease, if any, will not be renewed\n", l.Name, strings.Join(l.Addresses, " "))
	}

	managers := install.NetworkTakeoverTargets(ctx, sc)
	fmt.Fprintf(out, "\nplan:\n")
	fmt.Fprintf(out, "  - write %d unit(s) to %s: %s\n", len(files), install.NetworkdUnitDir, strings.Join(files.Names(), ", "))
	if _, err := os.Stat("/etc/cloud"); err == nil {
		fmt.Fprintf(out, "  - disable cloud-init network rendering (%s)\n", install.CloudInitDropIn)
	}
	if len(managers) > 0 {
		fmt.Fprintf(out, "  - stop, disable, and mask: %s\n", strings.Join(managers, ", "))
	}
	fmt.Fprintf(out, "  - enable and start systemd-networkd\n")
	if o.window > 0 {
		fmt.Fprintf(out, "  - arm a timer that restores the old manager after %s unless `ostiole takeover --network --confirm` runs\n", o.window)
	}
	if o.dryRun {
		return nil
	}
	if !o.yes {
		if err := confirmPrompt(cmd, "proceed? Established connections survive; new DHCP leases are re-acquired."); err != nil {
			return err
		}
	}

	if !install.HasNetworkd(ctx, sc) {
		return errors.New("systemd-networkd is not installed; run `ostiole repair` to put it back")
	}
	if !o.inUnit {
		// The switch itself runs detached: stopping networkd or a manager can
		// drop this session's address for a moment, and a hang-up must not
		// leave the router half-switched.
		if bin, err := install.ServiceBinary(install.DefaultLayout()); err == nil {
			if self, err := os.Executable(); err == nil && self != bin {
				fmt.Fprintf(out, "note: the unit runs the installed binary %s (re-run `install` after upgrading this copy)\n", bin)
			}
		}
		fmt.Fprintf(out, "switching in unit %s; if this session drops, it still completes (journalctl -u %s)\n", takeoverUnit, takeoverUnit)
		if err := detach(ctx, g, takeoverUnit, "--yes", "--confirm-window", o.window.String()); err != nil {
			return err
		}
	} else {
		if _, err := nd.Write(files); err != nil {
			return fmt.Errorf("write network units: %w", err)
		}
		if _, err := install.DisableCloudInitNetwork(slog.Default()); err != nil {
			return err
		}
		if err := install.SaveTakeoverRecord(g.configDir, install.TakeoverRecord{Managers: managers, At: time.Now()}); err != nil {
			return err
		}
		if o.window > 0 {
			exe, err := install.ServiceBinary(install.DefaultLayout())
			if err != nil {
				return err
			}
			if err := install.ScheduleNetworkRevert(ctx, run, exe, g.configDir, o.window); err != nil {
				return err
			}
		}
		if err := install.NetworkTakeover(ctx, sc, managers, slog.Default()); err != nil {
			return err
		}
		// Apply the units now even if networkd was already running (re-runs).
		return nd.Reload(ctx, nd.LinkNames(files))
	}
	time.Sleep(3 * time.Second)
	after, err := network.Discover()
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "\nafter:")
	for _, l := range after {
		if managed[l.Name] {
			fmt.Fprintf(out, "  %-11s %s\n", l.Name, strings.Join(l.Addresses, " "))
		}
	}
	if o.window > 0 {
		fmt.Fprintf(out, "done, pending: confirm within %s with `ostiole takeover --network --confirm`, or the previous manager is restored automatically\n", o.window)
		return nil
	}
	fmt.Fprintln(out, "done: systemd-networkd manages addressing; interface changes in Ostiole now apply live")
	return nil
}

func hasAddress(l network.Link, cidr string) bool {
	want, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	for _, a := range l.Addresses {
		if p, err := netip.ParsePrefix(a); err == nil && p.Addr() == want.Addr() {
			return true
		}
	}
	return false
}

func newUninstallCmd(g *globals) *cobra.Command {
	var purge, yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the service, remove the units, and delete the Ostiole nftables table",
		Long: `Reverses install. With --purge the configuration directory and the binary
are removed too. Competing services that takeover masked are not restored;
run for example "systemctl unmask firewalld && systemctl enable --now firewalld".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			if !yes {
				if err := confirmPrompt(cmd, "this removes the Ostiole firewall from the kernel; proceed?"); err != nil {
					return err
				}
			}
			lay := install.DefaultLayout()
			lay.ConfigDir = g.configDir
			// Undo a network takeover first, detached, so the router keeps its
			// addressing even if this session drops during the switch.
			if rec, err := install.LoadTakeoverRecord(g.configDir); err == nil && rec != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "restoring %s in unit %s\n", strings.Join(rec.Managers, ", "), revertUnit)
				if err := detach(cmd.Context(), g, revertUnit, "--revert"); err != nil {
					return err
				}
				_ = os.Remove(filepath.Join(g.configDir, install.TakeoverRecordFile))
			}
			if err := install.Uninstall(cmd.Context(), install.ExecSystemctl{}, lay, purge, slog.Default()); err != nil {
				return err
			}
			// The queues come off before the table does: they hang off the
			// kernel rather than off anything the uninstall removes, so
			// nobody else would ever take them away.
			if err := g.shaper().Clear(cmd.Context()); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove traffic shaping: %v\n", err)
			}
			if err := (&nft.Exec{Bin: g.nftBin}).Apply(cmd.Context(), nft.EmptyRuleset()); err != nil {
				return fmt.Errorf("remove nftables table: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the configuration directory and the binary")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
