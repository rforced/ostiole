package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/shaping"
	"github.com/rforced/ostiole/internal/sshd"
	"github.com/rforced/ostiole/internal/store"
)

// configSource selects where a command reads its configuration from.
type configSource struct {
	file     string
	revision string
}

func (s *configSource) addFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&s.file, "file", "f", "", "read configuration from a JSON file instead of the store")
	cmd.Flags().StringVar(&s.revision, "revision", "", "read an archived revision from the store")
}

func (s *configSource) load(st *store.Store) (*model.Config, error) {
	switch {
	case s.file != "" && s.revision != "":
		return nil, errors.New("--file and --revision are mutually exclusive")
	case s.file != "":
		return readConfigFile(s.file)
	case s.revision != "":
		return st.LoadRevision(s.revision)
	}
	cfg, err := st.Load()
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("no configuration in %s (run `ostiole init` first)", st.Dir)
	}
	return cfg, err
}

func readConfigFile(path string) (*model.Config, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

func newInitCmd(g *globals) *cobra.Command {
	var opts model.StarterOptions
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter configuration (does not apply it)",
		Long: `Writes a starter configuration with a lan zone (anti-lockout, allow all)
and an external wan zone with automatic outbound NAT. The rendered ruleset is
saved as the boot ruleset; run "ostiole apply" to load it now.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := g.store()
			if st.Exists() && !force {
				return fmt.Errorf("%s already has a configuration (use --force to overwrite)", st.Dir)
			}
			// How sshd lets people in now is what a first configuration
			// keeps, unless the flag says otherwise.
			if !cmd.Flags().Changed("ssh-passwords") {
				opts.SSHPasswords, _ = sshd.System{}.State(cmd.Context())
			}
			cfg := model.Starter(opts)
			ruleset, err := nft.Render(cfg)
			if err != nil {
				return err
			}
			if _, err := st.Save(cfg, ruleset); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\nnext: ostiole check && ostiole apply\n", st.Dir)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.LAN, "lan", "", "LAN interface name (required)")
	f.StringVar(&opts.LANAddress, "lan-address", "", "LAN IPv4 address in CIDR form, e.g. 192.168.1.1/24 (required)")
	f.StringVar(&opts.WAN, "wan", "", "WAN interface name (DHCP)")
	f.StringVar(&opts.Hostname, "hostname", "", "hostname")
	f.BoolVar(&opts.ManagementFromWAN, "management-from-wan", false, "turn anti-lockout on for the wan zone, so the web UI and SSH answer on the public side")
	f.BoolVar(&opts.Services, "services", false, "enable DHCP and DNS on the LAN (pool derived from the LAN address)")
	f.BoolVar(&opts.SSHPasswords, "ssh-passwords", true, "allow password logins over SSH (default: as this router is set now)")
	f.StringSliceVar(&opts.DNSUpstreams, "dns-upstream", nil, "upstream resolvers for the DNS service (default 1.1.1.1, 9.9.9.9)")
	f.BoolVar(&force, "force", false, "overwrite an existing configuration")
	_ = cmd.MarkFlagRequired("lan")
	_ = cmd.MarkFlagRequired("lan-address")
	return cmd
}

func newCheckCmd(g *globals) *cobra.Command {
	src := &configSource{}
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate the configuration and dry-run the ruleset with nft",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := src.load(g.store())
			if err != nil {
				return err
			}
			eng, err := g.engine()
			if err != nil {
				return err
			}
			plan, err := eng.Check(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "ok: %d zones, %d interfaces, %d rules, %d bytes of nftables, %d network units\n",
				len(cfg.Zones), len(cfg.Interfaces), len(cfg.Rules), len(plan.Ruleset), len(plan.Network))
			return nil
		},
	}
	src.addFlags(cmd)
	return cmd
}

func newRenderCmd(g *globals) *cobra.Command {
	src := &configSource{}
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Print the nftables ruleset for the configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := src.load(g.store())
			if err != nil {
				return err
			}
			ruleset, err := nft.Render(cfg)
			if err != nil {
				return err
			}
			_, err = io.WriteString(cmd.OutOrStdout(), ruleset)
			return err
		},
	}
	src.addFlags(cmd)
	return cmd
}

func newApplyCmd(g *globals) *cobra.Command {
	src := &configSource{}
	var timeout time.Duration
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Load the configuration into the kernel with a confirmation window",
		Long: `Applies the ruleset, then waits for you to press Enter. If you do not confirm
within the timeout (for example because the change cut your session), the
previous ruleset is restored automatically. Use --yes to commit immediately.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := src.load(g.store())
			if err != nil {
				return err
			}
			if yes {
				timeout = 0
			}
			if timeout > 0 && !stdinIsTerminal() {
				return errors.New("confirmation needs an interactive terminal; use --yes to commit immediately")
			}
			eng, err := g.engine()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			res, err := eng.Apply(cmd.Context(), cfg, engine.ApplyOptions{ConfirmTimeout: timeout})
			if err != nil {
				return err
			}
			if !res.Pending {
				fmt.Fprintln(out, "applied and committed")
				return nil
			}
			fmt.Fprintf(out, "applied; press Enter within %s to confirm, or it reverts\n", timeout.Truncate(time.Second))
			return waitForConfirmation(cmd.Context(), eng, res.Deadline, os.Stdin, out)
		},
	}
	src.addFlags(cmd)
	cmd.Flags().DurationVar(&timeout, "confirm-timeout", 60*time.Second, "how long to wait for confirmation before reverting")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "commit immediately without a confirmation window")
	return cmd
}

// waitForConfirmation blocks until Enter is pressed (confirm), the
// deadline passes (the engine has already reverted), or ctx is cancelled
// (revert now).
func waitForConfirmation(ctx context.Context, eng *engine.Engine, deadline time.Time, in io.Reader, out io.Writer) error {
	enter := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(in).ReadString('\n')
		enter <- err
	}()
	select {
	case err := <-enter:
		if err != nil {
			// stdin closed without a newline: the session is gone, revert.
			if rerr := eng.Revert(context.Background()); rerr != nil && !errors.Is(rerr, engine.ErrNothingPending) {
				return rerr
			}
			return errors.New("input closed before confirmation; reverted")
		}
		if _, err := eng.Confirm(ctx); err != nil {
			return err
		}
		fmt.Fprintln(out, "confirmed and committed")
		return nil
	case <-time.After(time.Until(deadline) + 500*time.Millisecond):
		return errors.New("not confirmed in time; reverted to the previous ruleset")
	case <-ctx.Done():
		if err := eng.Revert(context.Background()); err != nil && !errors.Is(err, engine.ErrNothingPending) {
			return err
		}
		return errors.New("interrupted; reverted to the previous ruleset")
	}
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func newLoadCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "load",
		Short: "Load the last confirmed ruleset (used at boot)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := g.engine()
			if err != nil {
				return err
			}
			if err := eng.Load(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "loaded confirmed ruleset")
			return nil
		},
	}
}

func newStatusCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show configuration and kernel state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st := g.store()
			eng, err := g.engine()
			if err != nil {
				return err
			}
			status, err := eng.Status(cmd.Context())
			if err != nil {
				return err
			}
			revs, _ := st.Revisions()
			nftVersion := "unavailable"
			if v, err := (&nft.Exec{Bin: g.nftBin}).Version(cmd.Context()); err == nil {
				nftVersion = v
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "config dir\t%s\n", st.Dir)
			fmt.Fprintf(w, "configured\t%v\n", status.Configured)
			fmt.Fprintf(w, "table loaded\t%v\n", status.TableLoaded)
			fmt.Fprintf(w, "network backend\t%s\n", status.Network)
			fmt.Fprintf(w, "revisions\t%d\n", len(revs))
			fmt.Fprintf(w, "nft\t%s\n", nftVersion)
			if tables, err := (&nft.Exec{Bin: g.nftBin}).ListTables(cmd.Context()); err == nil {
				var foreign []string
				for _, t := range tables {
					if t != nft.Table {
						foreign = append(foreign, t)
					}
				}
				fmt.Fprintf(w, "other nft tables\t%s\n", strings.Join(foreign, ", "))
			}
			if comp, err := install.Competitors(cmd.Context(), install.ExecSystemctl{}); err == nil {
				var names []string
				for _, c := range comp {
					if c.Conflicts() {
						names = append(names, fmt.Sprintf("%s (%s, %s)", c.Name, c.Active, c.Enabled))
					}
				}
				fmt.Fprintf(w, "conflicting services\t%s\n", strings.Join(names, ", "))
			}
			return w.Flush()
		},
	}
}

func newRevisionsCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "revisions",
		Short: "List archived configuration revisions, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			revs, err := g.store().Revisions()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTIME\tSIZE")
			for _, r := range revs {
				fmt.Fprintf(w, "%s\t%s\t%d\n", r.ID, r.Time.Local().Format(time.RFC3339), r.Size)
			}
			return w.Flush()
		},
	}
}

func newCountersCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "counters",
		Short: "Show live packet and byte counters per rule",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := g.engine()
			if err != nil {
				return err
			}
			counters, err := eng.Counters(cmd.Context())
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(counters))
			for k := range counters {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RULE\tPACKETS\tBYTES")
			for _, k := range keys {
				c := counters[k]
				fmt.Fprintf(w, "%s\t%d\t%d\n", k, c.Packets, c.Bytes)
			}
			return w.Flush()
		},
	}
}

// printDetected lists the default routes the kernel already has. Most
// routers get one from DHCP before anyone configures anything, and it is
// the one carrying the traffic.
func printDetected(cmd *cobra.Command, cfg *model.Config) {
	found, err := gateway.Detect(cfg)
	if err != nil || len(found) == 0 {
		return
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "\ndefault routes this router already has:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GATEWAY\tINTERFACE\tFAMILY\tMETRIC\tFROM\tCONFIGURED AS")
	for _, d := range found {
		as := d.Configured
		if as == "" {
			as = "not configured (" + gateway.Suggest(cfg, d).Name + " would cover it)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n", d.Address, d.Interface, d.Family, d.Metric, d.Protocol, as)
	}
	_ = w.Flush()
}

func newPolicyCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "policy",
		Short: "Show where rules that pick a gateway send their traffic",
		Long: `Lists every gateway and gateway group that firewall rules can route
through, with the packet mark the firewall sets, the routing table that
answers it, and what the kernel currently has in that table.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			targets := policy.Plan(cfg, policyHops(cmd.Context(), cfg))
			if len(targets) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no gateways or gateway groups configured")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKIND\tMARK\tTABLE\tRULES\tNEXT HOP")
			for _, t := range targets {
				kind := "gateway"
				if t.Group {
					kind = "group"
				}
				rules := 0
				for _, r := range cfg.Rules {
					if r.Enabled && r.Gateway == t.Name {
						rules++
					}
				}
				fmt.Fprintf(w, "%s\t%s\t0x%x\t%d\t%d\t%s\n",
					t.Name, kind, t.Mark, t.Table, rules, policyNextHop(t))
			}
			return w.Flush()
		},
	}
}

// policyHops resolves each gateway's next hop without probing: the CLI
// reports what the kernel is doing, it does not decide anything. Gateways
// count as usable so the plan shows where traffic would go.
func policyHops(_ context.Context, cfg *model.Config) map[string]policy.Hop {
	router := gateway.ReadOnlyRouter{Router: gateway.NewNetlinkRouter()}
	hops := map[string]policy.Hop{}
	for _, gw := range cfg.Gateways {
		if !gw.Enabled {
			continue
		}
		h := policy.Hop{Gateway: gw.Name, Address: gw.Address, Interface: gw.Interface, Online: true}
		if addr, ok := router.Resolve(gateway.Status{Name: gw.Name, Interface: gw.Interface, Address: gw.Address}); ok {
			h.Address = addr
		}
		hops[gw.Name] = h
	}
	return hops
}

func policyNextHop(t policy.Target) string {
	for _, tier := range t.Tiers {
		var hops []string
		for _, h := range tier {
			if h.Address != "" {
				hops = append(hops, h.Address+" dev "+h.Interface)
			}
		}
		if len(hops) > 0 {
			return strings.Join(hops, ", ")
		}
	}
	if t.Block {
		return "blackhole (group is set to block)"
	}
	return "none (traffic follows the default route)"
}

func newShapingCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "shaping",
		Short: "Show the speeds interfaces are held to and what the kernel has",
		Long: `Lists every interface that has been given a line speed, each shaped
direction, and whether the queue for it is in the kernel. Nothing here
changes anything: it reports what the last apply put in place.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			rep, err := g.shaper().Status(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if rep.Reason != "" {
				fmt.Fprintln(out, rep.Reason)
			}
			if len(rep.Interfaces) == 0 {
				fmt.Fprintln(out, "no interface has a speed set")
				return nil
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "INTERFACE\tZONE\tLINK\tDIRECTION\tSPEED\tSTATE")
			for _, in := range rep.Interfaces {
				for _, d := range []struct {
					name  string
					state shaping.DirectionStatus
				}{{"download", in.Download}, {"upload", in.Upload}} {
					if d.state.Rate == 0 {
						continue
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						in.Name, orDash(in.Zone), in.Link, d.name,
						formatBits(d.state.Rate), shapeState(in, d.state))
				}
			}
			return w.Flush()
		},
	}
}

// shapeState answers the only question worth asking of a row: is the
// queue there, and if not, whose fault is that.
func shapeState(in shaping.InterfaceStatus, d shaping.DirectionStatus) string {
	switch {
	case d.Installed:
		return "in place"
	case !in.Present:
		return "waiting for the link"
	}
	return "not installed"
}

// formatBits writes a rate the way a line is sold.
func formatBits(bits int64) string {
	switch {
	case bits >= 1_000_000_000 && bits%1_000_000_000 == 0:
		return fmt.Sprintf("%d Gbit/s", bits/1_000_000_000)
	case bits >= 1_000_000 && bits%1_000_000 == 0:
		return fmt.Sprintf("%d Mbit/s", bits/1_000_000)
	case bits%1000 == 0:
		return fmt.Sprintf("%d kbit/s", bits/1000)
	}
	return fmt.Sprintf("%d bit/s", bits)
}

func newGatewaysCmd(g *globals) *cobra.Command {
	var count int
	cmd := &cobra.Command{
		Use:   "gateways",
		Short: "Probe the configured gateways and show the result",
		Long: `Probes each enabled gateway the same way the daemon's monitor does.
Needs root for the raw socket. The daemon keeps watching continuously and
moves the default route off a gateway that stops answering.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			defer printDetected(cmd, cfg)
			if len(cfg.Gateways) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no gateways configured")
				return nil
			}
			// Probing only: the CLI never moves routes out from under the
			// daemon.
			router := gateway.ReadOnlyRouter{Router: gateway.NewNetlinkRouter()}
			mon := gateway.New(gateway.NewICMPProber(), router, slog.Default())
			mon.Configure(cfg.Gateways)
			for i := 0; i < count; i++ {
				mon.Tick(cmd.Context())
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tINTERFACE\tGATEWAY\tMONITOR\tSTATE\tRTT\tLOSS")
			for _, s := range mon.Statuses() {
				state := "down"
				switch {
				case s.Unknown:
					state = "unknown"
				case s.Online:
					state = "up"
				}
				if s.Active {
					state += " (active)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%.1fms\t%.0f%%\n",
					s.Name, s.Interface, s.Address, s.Monitor, state, s.LatencyMS, s.LossPercent)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&count, "probes", gateway.RiseAfter,
		"how many probes to send before reporting; the daemon needs three losses to call a gateway down")
	return cmd
}
