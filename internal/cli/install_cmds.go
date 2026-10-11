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
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/atomicfile"
	"ostiole/internal/host"
	"ostiole/internal/install"
	"ostiole/internal/iptables"
	"ostiole/internal/journald"
	"ostiole/internal/kernel"
	"ostiole/internal/model"
	"ostiole/internal/network"
	"ostiole/internal/nft"
	"ostiole/internal/policy"
	"ostiole/internal/services"
	"ostiole/internal/store"
	"ostiole/internal/sysctl"
	"ostiole/internal/timezone"
)

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

func newInstallCmd(g *globals) *cobra.Command {
	opts := install.Options{}
	var ignoreKernel, yes, dryRun, unitsOnly, networkLater bool
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
management ports get in, nothing is forwarded.

With --units-only it writes the units and the directories they open and
reloads systemd, without asking: nothing is enabled or restarted. A
self-update runs it before restarting into the new binary. Releases up
to 1.7.2 run a bare install there instead, with nobody to ask, so on a
router that has its units that writes them too, and still fails for
want of --yes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			units := func() error {
				return writeUnitsOnly(cmd.Context(), cmd.OutOrStdout(), install.ExecSystemctl{}, unitsLayout(g.configDir), opts,
					func(ctx context.Context) error { return writeServiceUnits(ctx, g.configDir) })
			}
			if unitsOnly {
				return units()
			}
			if oldUpdateRestart(yes, dryRun, opts, interactive(), unitsLayout(g.configDir)) {
				if err := units(); err != nil {
					return err
				}
				return errors.New("not interactive, so only the units were written; pass --yes for the rest")
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
				fmt.Fprintln(out, "  hand to networkd:     this router's addresses, keeping the ones it has now, last")
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
				if err := atomicfile.Write(filepath.Join(g.configDir, store.RulesetFile), []byte(nft.Bootstrap(ports)), 0o600); err != nil {
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
			case !errors.Is(err, iptables.ErrNothingToFlush) && !errors.Is(err, iptables.ErrAllOwned):
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not clear what the old firewall left: %v\n", err)
			}
			// The install script hands over last, after its own cleanup, so
			// that a session the handover cuts off has nothing left to lose.
			if !networkLater {
				if err := installHandover(cmd, g, nil); err != nil {
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
	cmd.Flags().BoolVar(&unitsOnly, "units-only", false, "only write the units and reload systemd, as a self-update does")
	cmd.Flags().BoolVar(&networkLater, "network-later", false, "internal: leave the network handover to the install script's last step")
	_ = cmd.Flags().MarkHidden("network-later")
	return cmd
}

// writeUnitsOnly is `install --units-only`: the units and the directories
// they open, then a reload. A self-update runs it with nobody watching, so
// the rest of an install stays out: the prompt, the firewall's restart
// that loses UPnP mappings and counters, the sweep of other tables, a
// network handover nobody is there to confirm.
func writeUnitsOnly(ctx context.Context, out io.Writer, sc install.Systemctl, lay install.Layout, opts install.Options, serviceUnits func(context.Context) error) error {
	names, err := install.WriteUnits(ctx, sc, lay, opts)
	if err != nil {
		return err
	}
	if err := serviceUnits(ctx); err != nil {
		return fmt.Errorf("write the service units: %w", err)
	}
	fmt.Fprintf(out, "wrote %s and the service units\n", strings.Join(names, ", "))
	return nil
}

// oldUpdateRestart reports whether a bare install is a release up to 1.7.2
// restarting into an update: it runs `install >/dev/null 2>&1` in a
// transient unit, where the prompt has nobody to ask, and means the units
// by it. Anything else with nobody to ask still wants --yes.
func oldUpdateRestart(yes, dryRun bool, opts install.Options, interactive bool, lay install.Layout) bool {
	return !yes && !dryRun && opts.Listen == "" && opts.Timezone == "" && !interactive && install.Installed(lay)
}

// unitsLayout is the installed layout, with the binary where this one
// runs when that is a system bin directory, as Install would find it.
func unitsLayout(configDir string) install.Layout {
	lay := install.DefaultLayout()
	lay.ConfigDir = configDir
	if exe, err := os.Executable(); err == nil && install.InSystemBinDir(exe) {
		lay.BinDir = filepath.Dir(exe)
	}
	return lay
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
			addr := p.Addr().String()
			if p.Addr().Is6() {
				addr = "[" + addr + "]"
			}
			urls = append(urls, "https://"+addr+suffix+"/")
		}
	}
	if len(urls) == 0 {
		urls = []string{"https://<this-host>" + suffix + "/"}
	}
	return urls
}

func newTakeoverCmd(g *globals) *cobra.Command {
	var yes, dryRun, netFlag, confirm, revert, inUnit, finish, forInstall bool
	var window time.Duration
	var removeOnConfirm []string
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
			o := networkTakeoverOptions{yes: yes, dryRun: dryRun, window: window, confirm: confirm, revert: revert,
				inUnit: inUnit, finish: finish, removeOnConfirm: removeOnConfirm}
			if !confirm && !revert && !finish {
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
			if forInstall {
				return installHandover(cmd, g, removeOnConfirm)
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
	// The install script hands over last, naming the old managers'
	// packages, which go once the handover holds.
	cmd.Flags().BoolVar(&forInstall, "for-install", false, "internal: the install script's handover, its last step")
	_ = cmd.Flags().MarkHidden("for-install")
	cmd.Flags().StringSliceVar(&removeOnConfirm, "remove-on-confirm", nil, "internal: packages a confirmed handover removes")
	_ = cmd.Flags().MarkHidden("remove-on-confirm")
	cmd.Flags().BoolVar(&finish, "finish", false, "internal: remove what the install left for the handover")
	_ = cmd.Flags().MarkHidden("finish")
	return cmd
}

type networkTakeoverOptions struct {
	yes, dryRun     bool
	window          time.Duration
	confirm, revert bool
	inUnit, finish  bool
	// removeOnConfirm goes into the handover's record as it is made, so a
	// confirm from another session at any moment finds it there.
	removeOnConfirm []string
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

// interactive reports whether promptReader has somebody to ask.
func interactive() bool {
	in, err := promptReader()
	if err != nil {
		return false
	}
	_ = in.Close()
	return true
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
		if !install.CancelNetworkRevert(ctx, run) {
			fmt.Fprintln(out, "no revert timer was armed; nothing to confirm")
			return nil
		}
		fmt.Fprintln(out, "confirmed: systemd-networkd stays in charge; revert timer disarmed")
		return finishHandover(ctx, g, out)
	}
	if o.finish {
		return finishInstall(cmd, g)
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
		args := []string{"--yes", "--confirm-window", o.window.String()}
		if len(o.removeOnConfirm) > 0 {
			args = append(args, "--remove-on-confirm", strings.Join(o.removeOnConfirm, ","))
		}
		if err := detach(ctx, g, takeoverUnit, args...); err != nil {
			return err
		}
	} else {
		if _, err := nd.Write(files); err != nil {
			return fmt.Errorf("write network units: %w", err)
		}
		if _, err := install.DisableCloudInitNetwork(slog.Default()); err != nil {
			return err
		}
		rec := install.TakeoverRecord{Managers: managers, At: time.Now(), Remove: o.removeOnConfirm}
		// What an earlier install left waits for this handover instead.
		if prev, err := install.LoadTakeoverRecord(g.configDir); err == nil && prev != nil && len(rec.Remove) == 0 {
			rec.Remove = prev.Remove
		}
		if err := install.SaveTakeoverRecord(g.configDir, rec); err != nil {
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

// installHandover is the install's last step: the handover to networkd,
// and once it holds, the old managers' packages off the router. It comes
// last so a session it cuts off has nothing left to lose, and confirming
// it from the next one finishes the install.
func installHandover(cmd *cobra.Command, g *globals, remove []string) error {
	if g.netBackend == "none" {
		return nil
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	rec, err := install.LoadTakeoverRecord(g.configDir)
	if err != nil {
		return err
	}
	if rec != nil {
		// Handed over before. A revert has put the old manager back since,
		// and then it stays, with its packages, until a handover holds.
		if len(remove) > 0 {
			rec.Remove = remove
			if err := install.SaveTakeoverRecord(g.configDir, *rec); err != nil {
				return err
			}
		}
		if !install.HandoverHolds(ctx, install.ExecSystemctl{}, rec.Managers) {
			fmt.Fprintf(out, "%s has the network again, as a revert left it; `ostiole takeover --network` hands it over\n",
				strings.Join(rec.Managers, ", "))
			return nil
		}
		fmt.Fprintln(out, "systemd-networkd already has this router's addresses")
		return finishHandover(ctx, g, out)
	}
	before, err := network.Discover()
	if err != nil {
		return err
	}
	o := networkTakeoverOptions{yes: true, window: host.NetworkTakeoverWindow, removeOnConfirm: remove}
	if err := networkTakeover(cmd, g, o); err != nil {
		// The router has its units, its ruleset and its firewall; whoever
		// owns the addresses goes on owning them. `ostiole takeover
		// --network` is the retry.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: addressing was left with its current manager: %v\n", err)
		return nil
	}
	if kept, missing := addressesKept(before); kept {
		install.CancelNetworkRevert(ctx, install.ExecRunner{})
		fmt.Fprintln(out, "addresses kept, systemd-networkd in charge")
		return finishHandover(ctx, g, out)
	} else if len(missing) > 0 {
		fmt.Fprintf(out, "still missing: %s\n", strings.Join(missing, ", "))
	}
	fmt.Fprintf(out, "the previous network manager returns in %s unless `ostiole takeover --network --confirm` runs, which also finishes the install\n",
		host.NetworkTakeoverWindow)
	return nil
}

// finishHandover removes what the install left for the handover, once it
// holds, in a unit of its own: a package manager stopped halfway by a
// dropped session is worse than one never started.
func finishHandover(ctx context.Context, g *globals, out io.Writer) error {
	rec, err := install.LoadTakeoverRecord(g.configDir)
	if err != nil || rec == nil || len(rec.Remove) == 0 {
		return err
	}
	left := strings.Join(rec.Remove, " ")
	if !install.HandoverHolds(ctx, install.ExecSystemctl{}, rec.Managers) {
		fmt.Fprintf(out, "systemd-networkd does not have the network, so %s stays\n", left)
		return nil
	}
	fmt.Fprintf(out, "removing %s, which the install left until now, in unit %s; if this session drops, it still completes (journalctl -u %s)\n",
		left, install.FinishUnit, install.FinishUnit)
	if err := detach(ctx, g, install.FinishUnit, "--finish"); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s; the install is done\n", left)
	return nil
}

// finishInstall runs the install script's removal alone, for what the
// install left until the handover was confirmed, and clears the note.
func finishInstall(cmd *cobra.Command, g *globals) error {
	rec, err := install.LoadTakeoverRecord(g.configDir)
	if err != nil || rec == nil || len(rec.Remove) == 0 {
		return err
	}
	if err := runScript(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
		[]string{"OSTIOLE_REMOVE=" + strings.Join(rec.Remove, " ")}); err != nil {
		return err
	}
	rec.Remove = nil
	return install.SaveTakeoverRecord(g.configDir, *rec)
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
		Short: "Remove Ostiole's daemon and leave the router running as configured, or with --purge remove everything",
		Long: `Without --purge, removes Ostiole's management and leaves the router
running as it was configured. ostiole.service stops and its unit goes,
ostiole-firewall.service is rewritten to load the saved ruleset with nft,
config.json, users.json and tokens.json in the configuration directory
become .bak files, the sessions go, and so does the ostiole binary. The
ruleset stays in the kernel, and the services the daemon runs, their
files, the networkd units, the logs, the state and the backups all stay.
What only the daemon does stops, and the command lists it. Running the
install script again offers the kept configuration.

With --purge, reverts the network takeover and stops and removes
ostiole.service, ostiole-firewall.service and every service unit the
daemon writes, with their drop-ins and the configuration they read under
/etc, the sysctl, modprobe, journald and sysusers drop-ins and the
networkd units. It clears traffic shaping and policy routing, deletes the
Ostiole nftables table, unmasks the distribution's units Ostiole masked to
run its own (its resolver, unbound, miniupnpd, tailscaled, hostapd, time
services, bluetooth and update timers) and starts systemd-resolved and a
time service where the host has them. The configuration directory, the
log files, the services' state, the ostiole and ostiole-proxy binaries and
the ostiole-proxy account are removed too; the backups in
/var/backups/ostiole stay. Competitors the install removed or masked are
not restored; run for example
"systemctl unmask firewalld && systemctl enable --now firewalld".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			question := "this removes Ostiole's daemon and leaves the router running as configured; proceed?"
			if purge {
				question = "this removes the Ostiole firewall from the kernel; proceed?"
			}
			if !yes {
				if err := confirmPrompt(cmd, question); err != nil {
					return err
				}
			}
			// A dropped session must not leave the removal half done, and
			// stopping tailscaled, pppoe or hostapd in a purge can drop it.
			signal.Ignore(syscall.SIGHUP)
			lay := install.DefaultLayout()
			lay.ConfigDir = g.configDir
			opts := install.UninstallOptions{Purge: purge, Nft: g.nftBin}
			out := cmd.OutOrStdout()
			if !purge {
				listen := install.Listen(lay)
				if err := install.Uninstall(cmd.Context(), install.ExecSystemctl{}, install.ExecRunner{}, lay, opts, slog.Default()); err != nil {
					return err
				}
				fmt.Fprintln(out, "uninstalled; the router runs on as configured")
				for _, line := range install.StopsWithoutDaemon {
					fmt.Fprintln(out, line)
				}
				fmt.Fprintln(out, install.ResumeHint)
				if listen != "" && listen != install.DefaultListen {
					fmt.Fprintf(out, "the UI listened on %s; pass --listen %s to the install script to keep that\n", listen, listen)
				}
				return nil
			}
			// Undo a network takeover first, detached, so the router keeps its
			// addressing even if this session drops during the switch.
			if rec, err := install.LoadTakeoverRecord(g.configDir); err == nil && rec != nil {
				fmt.Fprintf(out, "restoring %s in unit %s\n", strings.Join(rec.Managers, ", "), revertUnit)
				if err := detach(cmd.Context(), g, revertUnit, "--revert"); err != nil {
					return err
				}
				_ = os.Remove(filepath.Join(g.configDir, install.TakeoverRecordFile))
			}
			if err := install.Uninstall(cmd.Context(), install.ExecSystemctl{}, install.ExecRunner{}, lay, opts, slog.Default()); err != nil {
				return err
			}
			// The queues and ip rules come off before the table does: they
			// hang off the kernel rather than off anything the uninstall
			// removes, so nobody else would ever take them away.
			if err := g.shaper().Clear(cmd.Context()); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove traffic shaping: %v\n", err)
			}
			if err := policy.NewInstaller(slog.Default()).Clear(); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not remove policy routing: %v\n", err)
			}
			if err := (&nft.Exec{Bin: g.nftBin}).Apply(cmd.Context(), nft.EmptyRuleset()); err != nil {
				return fmt.Errorf("remove nftables table: %w", err)
			}
			fmt.Fprintln(out, "uninstalled")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "remove everything instead: the network takeover, the services, the nftables table, the configuration directory, the log files, the services' state, the binaries and the proxy's account, keeping the backups")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
