package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/kernel"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
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
	var keep []string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Ostiole and make this router its firewall",
		Long: `Installs the binary and the units, and prepares the router in the same
pass: installs nftables, systemd-networkd, dnsmasq, unbound and tc; stops,
masks and removes the firewalls it replaces (firewalld, ufw, iptables
services); removes the other updaters (unattended-upgrades, dnf-automatic)
and what a router has no use for (snapd, ModemManager, udisks2, upower,
fwupd, multipathd); persists the router sysctls and a ceiling on the
system journal; and sets the clock to UTC.

Everything it would install, mask and remove is listed first, with the
package manager's own account of what else comes away, and nothing
happens until you say yes. --keep leaves a named extra alone; --dry-run
prints the plan and stops.

Until a configuration is applied and confirmed the router runs a
bootstrap ruleset: established connections, ICMP, its own DHCP replies
and the management ports get in, nothing is forwarded. The setup wizard
in the web UI replaces it.`,
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
			opts.PackageManager = g.packageManager
			d := g.hostDeps()

			plan, err := host.PlanInstall(ctx, d, keep)
			if err != nil {
				return err
			}
			bootstrap := !g.store().Exists() && !rulesetExists(g.configDir)
			fmt.Fprintln(out, "Ostiole will:")
			plan.Print(out)
			fmt.Fprintf(out, "  write and enable:     %s, %s\n", install.FirewallUnit, install.DaemonUnit)
			fmt.Fprintf(out, "  persist:              router sysctls (%s), journal ceiling (%s)\n", sysctl.ConfFile, journald.ConfFile)
			if opts.Timezone != "-" {
				fmt.Fprintf(out, "  set the clock to:     %s\n", or(opts.Timezone, "UTC"))
			}
			if bootstrap {
				fmt.Fprintln(out, "\nUntil a configuration is applied and confirmed, this router accepts established")
				fmt.Fprintln(out, "connections, ICMP, its own DHCP replies and the management ports, and forwards nothing.")
			}
			if plan.Refused != nil {
				return errors.New("refusing to continue; use --keep or remove by hand and re-run")
			}
			if dryRun {
				return nil
			}
			if !yes {
				if err := confirmPrompt(cmd, "\nproceed?"); err != nil {
					return err
				}
			}

			// What a router needs goes on first, while it still resolves
			// names the way its image set it up to and before anything that
			// might be a dependency comes off.
			if said, err := host.InstallComponents(ctx, d, plan); said != "" || err != nil {
				if said != "" {
					fmt.Fprintln(out, said)
				}
				if err != nil {
					return err
				}
			}
			if bootstrap {
				ports := []uint16{443}
				if port := listenPortOf(opts.Listen); port != 0 {
					ports = []uint16{port}
				}
				ports = append(ports, 22)
				if err := os.WriteFile(filepath.Join(g.configDir, store.RulesetFile), []byte(nft.Bootstrap(ports)), 0o600); err != nil {
					return fmt.Errorf("write the bootstrap ruleset: %w", err)
				}
				fmt.Fprintln(out, "wrote the bootstrap ruleset")
			}
			if err := (journald.System{Run: install.ExecRunner{}}).Apply(ctx, journald.DefaultMaxUseGB); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not bound the system journal: %v\n", err)
			}
			sc := install.ExecSystemctl{}
			wasActive := false
			if state, err := sc.Run(ctx, "is-active", install.DaemonUnit); err == nil && state == "active" {
				wasActive = true
			}
			// The old firewall is retired a moment later, so there is no
			// point opening the UI port in it.
			opts.KeepOldFirewall = len(plan.Retire) > 0
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
			if said, err := host.RetireAndRemove(ctx, d, plan); said != "" || err != nil {
				if said != "" {
					fmt.Fprintln(out, said)
				}
				if err != nil {
					return err
				}
			}
			if wasActive {
				// A daemon that was already running keeps the mount namespace
				// it started with, and the unit it was started from.
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
			fmt.Fprintf(out, "\nnext:\n  1. create the admin account in the web UI, or: ostiole reset-password\n  2. run the setup wizard in the web UI, or: ostiole init --lan ... && ostiole apply\n  3. ostiole takeover --network    (hands addressing to systemd-networkd; needed for interface edits)\n")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Listen, "listen", ":443", "address the web UI listens on")
	cmd.Flags().StringVar(&opts.Timezone, "timezone", "", `timezone to set, UTC by default; "-" leaves the clock alone`)
	cmd.Flags().BoolVar(&ignoreKernel, "ignore-kernel-version", false,
		fmt.Sprintf("install even if the kernel is older than %s", kernel.Minimum))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
	cmd.Flags().StringSliceVar(&keep, "keep", nil, "leave an extra alone, by key (see `ostiole host`); repeatable")
	return cmd
}

// rulesetExists reports whether a ruleset is already on disk, in which
// case the bootstrap would only get in its way.
func rulesetExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, store.RulesetFile))
	return err == nil
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
		port = "443"
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

func printCompetitors(out interface{ Write([]byte) (int, error) }, comp []install.Service) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nSERVICE\tKIND\tACTIVE\tENABLED\tCONFLICT")
	for _, c := range comp {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", c.Name, c.Kind, c.Active, c.Enabled, c.Conflicts())
	}
	_ = w.Flush()
}

func newTakeoverCmd(g *globals) *cobra.Command {
	var yes, dryRun, net, confirm, revert, inUnit bool
	var window time.Duration
	cmd := &cobra.Command{
		Use:   "takeover",
		Short: "Disable competing firewall services so Ostiole is the only firewall",
		Long: `Stops, disables, and masks firewalld, ufw, nftables.service, iptables, and
similar. Refuses to run unless a confirmed Ostiole ruleset is loaded in the
kernel, so the router is never left without a firewall.

With --network it instead hands addressing to systemd-networkd: installs
networkd if missing (EPEL on RHEL-family), writes the units rendered from
the confirmed configuration, stops and masks NetworkManager and friends,
and starts networkd. Existing addresses persist across the switch; DHCP
leases are re-acquired. To undo: systemctl disable --now systemd-networkd;
systemctl unmask NetworkManager; systemctl enable --now NetworkManager.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			eng, err := g.engine()
			if err != nil {
				return err
			}
			st, err := eng.Status(cmd.Context())
			if err != nil {
				return err
			}
			if !st.Configured || !st.TableLoaded {
				return errors.New("refusing: apply and confirm an Ostiole configuration first (ostiole status)")
			}
			if net {
				return networkTakeover(cmd, g, networkTakeoverOptions{yes: yes, dryRun: dryRun, window: window, confirm: confirm, revert: revert, inUnit: inUnit})
			}
			if confirm || revert {
				return errors.New("--confirm and --revert only apply with --network")
			}
			comp, err := install.Competitors(cmd.Context(), install.ExecSystemctl{})
			if err != nil {
				return err
			}
			var targets []string
			for _, c := range comp {
				if c.Kind == "firewall" && c.Conflicts() {
					targets = append(targets, c.Name)
				}
			}
			out := cmd.OutOrStdout()
			printCompetitors(out, comp)
			if len(targets) == 0 {
				fmt.Fprintln(out, "\nnothing to do: no competing firewall service is active or enabled")
				return nil
			}
			fmt.Fprintf(out, "\nwill stop, disable, and mask: %s\n", strings.Join(targets, ", "))
			if dryRun {
				return nil
			}
			if !yes {
				if err := confirmPrompt(cmd, "proceed?"); err != nil {
					return err
				}
			}
			if err := install.Takeover(cmd.Context(), install.ExecSystemctl{}, targets, slog.Default()); err != nil {
				return err
			}
			if tables, err := (&nft.Exec{Bin: g.nftBin}).ListTables(cmd.Context()); err == nil {
				var foreign []string
				for _, t := range tables {
					if t != nft.Table {
						foreign = append(foreign, t)
					}
				}
				if len(foreign) > 0 {
					fmt.Fprintf(out, "\nnote: other nftables tables remain and still filter traffic: %s\n", strings.Join(foreign, ", "))
				}
			}
			fmt.Fprintln(out, "done: Ostiole is the only firewall service")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would change")
	cmd.Flags().BoolVar(&net, "network", false, "hand addressing to systemd-networkd instead of touching firewalls")
	cmd.Flags().DurationVar(&window, "confirm-window", 3*time.Minute, "with --network: restore the previous network manager unless --confirm runs within this time (0 disables)")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "with --network: keep the handover and disarm the revert timer")
	cmd.Flags().BoolVar(&revert, "revert", false, "with --network: undo the handover and restore the previous network manager")
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
	if !stdinIsTerminal() {
		return errors.New("not interactive; pass --yes to proceed")
	}
	fmt.Fprint(cmd.OutOrStdout(), question+" [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return errors.New("aborted")
	}
	return nil
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
			fmt.Fprintf(out, "reverting in unit %s; if this session drops, it still completes (journalctl -u %s)\n", revertUnit, revertUnit)
			if err := detach(ctx, g, revertUnit, "--revert"); err != nil {
				return err
			}
			fmt.Fprintf(out, "reverted: %s restored, systemd-networkd stopped\n", strings.Join(rec.Managers, ", "))
			return nil
		}
		install.CancelNetworkRevert(ctx, run)
		if err := install.NetworkRevert(ctx, sc, rec.Managers, slog.Default()); err != nil {
			return err
		}
		slog.Info("network takeover reverted", "restored", rec.Managers)
		return nil
	}

	cfg, err := g.store().Load()
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
		conf := fmt.Sprintf("%s v4=%s", in.Zone, in.IPv4.Mode)
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

	comp, err := install.Competitors(ctx, sc)
	if err != nil {
		return err
	}
	var managers []string
	for _, c := range comp {
		if c.Kind == "network" && c.Conflicts() {
			managers = append(managers, c.Name)
		}
	}
	for _, extra := range []string{"NetworkManager-wait-online"} {
		for _, m := range managers {
			if m == "NetworkManager" {
				managers = append(managers, extra)
				break
			}
		}
	}
	pm := install.PackageManager()
	fmt.Fprintf(out, "\nplan:\n")
	if !install.HasNetworkd(ctx, sc) {
		fmt.Fprintf(out, "  - install systemd-networkd with %s\n", pm)
	}
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

	if err := install.EnsureNetworkd(ctx, sc, run, pm, slog.Default()); err != nil {
		return err
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
		if err := nd.Reload(ctx, nd.LinkNames(files)); err != nil {
			return err
		}
		return nil
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
