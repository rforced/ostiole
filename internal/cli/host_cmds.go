package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// hostDeps builds the host dependencies for a command. The same
// dependencies the daemon uses, so both front doors report the same
// router and act on it the same way.
func (g *globals) hostDeps() host.Deps {
	root := os.Geteuid() == 0
	packages := sysupdate.New(sysupdate.Options{
		PackageManager: g.packageManager,
		StateDir:       g.updatesDir(),
		Root:           root,
		Log:            slog.Default(),
	})
	d := host.Deps{
		Root:     root,
		Units:    install.ExecSystemctl{},
		Packages: packages,
		Kernel:   &nft.Exec{Bin: g.nftBin},
		Backend:  g.netBackend,
		NFT:      g.nftBin,
		Dir:      g.configDir,
		Log:      slog.Default(),
	}
	// The configuration decides which components this router needs, and a
	// store that cannot be read means an empty one: this command's job is
	// to make a router ready to hold a configuration, so not having one yet
	// is the normal case.
	d.Config = func() *model.Config {
		cfg, err := g.store().Load()
		if err != nil {
			return nil
		}
		return cfg
	}
	d.TableLoaded = func(ctx context.Context) bool {
		eng, err := g.engine()
		if err != nil {
			return false
		}
		st, err := eng.Status(ctx)
		return err == nil && st.TableLoaded
	}
	return d
}

func newHostCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Prepare this router: packages, competitors, leftover rulesets",
		Long: `Reports what this router still needs before it is a firewall, and does it.

The same steps are on the web UI under System, Host, which drives these
commands: nothing here is a second implementation of them. A router with
something outstanding sends the browser to that page until it is done or
the operator says they are leaving it alone.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep := host.Status(cmd.Context(), g.hostDeps())
			printHost(cmd, rep)
			return nil
		},
	}
	cmd.AddCommand(
		newHostSetupCmd(g),
		newHostRemoveCmd(g),
		newHostFlushCmd(g),
		newHostSkipCmd(g),
	)
	return cmd
}

// printHost writes the report the way a console reads it: what is
// outstanding first, then the detail behind it.
func printHost(cmd *cobra.Command, rep host.Report) {
	out := cmd.OutOrStdout()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "distribution\t%s\n", or(rep.Distro, "unknown"))
	fmt.Fprintf(w, "package manager\t%s\n", or(rep.Manager, "none found"))
	fmt.Fprintf(w, "root\t%v\n", rep.Root)
	if rep.Unavailable != "" {
		fmt.Fprintf(w, "note\t%s\n", rep.Unavailable)
	}
	_ = w.Flush()

	fmt.Fprintln(out, "\nSTEP        STATE        DETAIL")
	for _, s := range rep.Steps {
		fmt.Fprintf(out, "%-11s %-12s %s\n", s.Step, s.State, s.Detail)
	}

	fmt.Fprintln(out, "\nCOMPONENT   REQUIRED  STATE        PACKAGES")
	for _, c := range rep.Components {
		state := "missing"
		switch {
		case c.Ready:
			state = "ready"
		case c.Present:
			state = "installed"
		}
		if c.Availability != sysupdate.Installable && !c.Present {
			state = string(c.Availability)
		}
		fmt.Fprintf(out, "%-11s %-9v %-12s %s\n", c.Key, c.Required, state, strings.Join(c.Packages, " "))
	}

	if len(rep.Competitors) > 0 {
		fmt.Fprintln(out, "\nSERVICE                KIND      ACTIVE     ENABLED    PACKAGES")
		for _, c := range rep.Competitors {
			fmt.Fprintf(out, "%-22s %-9s %-10s %-10s %s\n",
				c.Name, c.Kind, c.Active, c.Enabled, strings.Join(c.Packages, " "))
		}
	}
	if len(rep.Legacy.Tables) > 0 {
		fmt.Fprintln(out, "\nLEFTOVER RULESET       RULES  CHAINS")
		for _, t := range rep.Legacy.Tables {
			owner := ""
			if t.Owner != "" {
				owner = "  (belongs to " + t.Owner + ", left alone)"
			}
			fmt.Fprintf(out, "%-22s %-6d %s%s\n", t.ID(), t.Rules, strings.Join(t.Chains, " "), owner)
		}
	}
	fmt.Fprintf(out, "\nnetworking: %s is %s, backend %s\n",
		install.NetworkdUnit, rep.Network.Networkd, rep.Network.Backend)
	if len(rep.Network.Managers) > 0 {
		verb := "still running"
		if rep.Network.Owned {
			verb = "retired by Ostiole"
		}
		fmt.Fprintf(out, "            %s: %s\n", verb, strings.Join(rep.Network.Managers, ", "))
	}
	if !rep.Prepared {
		fmt.Fprintln(out, "\nthis router is not ready: see the steps above")
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func newHostSetupCmd(g *globals) *cobra.Command {
	var all, noRestart bool
	cmd := &cobra.Command{
		Use:   "setup [component...]",
		Short: "Install components and write the units that point them at Ostiole",
		Long: `Installs the packages a component needs and writes Ostiole's unit for
it, so the configuration can turn the service on. Nothing here starts a
service: that is the Enabled switch, applied like everything else.

With no component, every one the configuration in force asks for and this
router does not have yet. With --all, every component Ostiole can install.

Components: ` + strings.Join(componentKeys(), ", "),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			d := g.hostDeps()
			keys := args
			if all {
				keys = componentKeys()
			}
			if len(keys) == 0 {
				rep := host.Status(cmd.Context(), d)
				for _, c := range rep.Components {
					if c.Outstanding() {
						keys = append(keys, c.Key)
					}
				}
				if len(keys) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "nothing to do: every component this configuration asks for is set up")
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "setting up: %s\n", strings.Join(keys, ", "))
			}
			said, err := host.SetUp(cmd.Context(), d, keys, !noRestart)
			if said != "" {
				fmt.Fprintln(cmd.OutOrStdout(), said)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "set up every component, not only the ones this configuration needs")
	cmd.Flags().BoolVar(&noRestart, "no-restart", false, "internal: the daemon drove this and restarts itself afterwards")
	_ = cmd.Flags().MarkHidden("no-restart")
	return cmd
}

func componentKeys() []string {
	var keys []string
	for _, c := range sysupdate.Components() {
		keys = append(keys, c.Key)
	}
	return keys
}

func newHostRemoveCmd(g *globals) *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "remove <service...>",
		Short: "Remove a retired competitor's packages",
		Long: `Takes the packages of a competing firewall or network manager off the
router. Disabling and masking a unit is enough to stop it and leaves a
way back, so this is never done for you and never done to a service that
is still running or still enabled: retire it first with ostiole takeover.

What else comes away with a package is the package manager's answer, not
Ostiole's, so it is asked first and printed before anything is removed.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			d := g.hostDeps()
			out := cmd.OutOrStdout()
			preview, err := host.RemovePackages(cmd.Context(), d, args, true)
			if preview != "" {
				fmt.Fprintln(out, strings.TrimSpace(preview))
			}
			if err != nil {
				return err
			}
			if dryRun {
				return nil
			}
			if !yes {
				if err := confirmPrompt(cmd, "\nremove these packages?"); err != nil {
					return err
				}
			}
			said, err := host.RemovePackages(cmd.Context(), d, args, false)
			if said != "" {
				fmt.Fprintln(out, said)
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only report what would be removed")
	return cmd
}

func newHostFlushCmd(g *globals) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "flush [table...]",
		Short: "Clear the rulesets an older firewall left in the kernel",
		Long: `An older firewall leaves rules behind, and they still filter traffic
next to Ostiole's. The legacy iptables tables live in the kernel module;
the nf_tables front end writes ordinary nftables tables called filter,
nat and the rest, which is why they turn up in the dashboard's warning
about rulesets Ostiole does not own.

With no arguments it clears every leftover with no recognisable owner. A
table that plainly belongs to something still running — Docker, libvirt,
fail2ban — is reported and left alone unless it is named, because taking
it would break whatever is using it.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			d := g.hostDeps()
			out := cmd.OutOrStdout()
			rep := host.Status(cmd.Context(), d).Legacy
			if len(rep.Tables) == 0 {
				fmt.Fprintln(out, "nothing to do: no leftover iptables rulesets were found")
				return nil
			}
			fmt.Fprintln(out, "LEFTOVER RULESET       RULES  CHAINS")
			for _, t := range rep.Tables {
				owner := ""
				if t.Owner != "" {
					owner = "  (belongs to " + t.Owner + ")"
				}
				fmt.Fprintf(out, "%-22s %-6d %s%s\n", t.ID(), t.Rules, strings.Join(t.Chains, " "), owner)
			}
			targets := args
			if len(targets) == 0 {
				for _, t := range rep.Sweepable() {
					targets = append(targets, t.ID())
				}
				if len(targets) == 0 {
					fmt.Fprintln(out, "\nnothing to clear: every leftover belongs to something still running")
					return nil
				}
			}
			fmt.Fprintf(out, "\nwill clear: %s\n", strings.Join(targets, ", "))
			if !yes {
				if err := confirmPrompt(cmd, "proceed?"); err != nil {
					return err
				}
			}
			said, err := host.FlushLegacy(cmd.Context(), d, args)
			if said != "" {
				fmt.Fprintln(out, said)
			}
			return err
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newHostSkipCmd(g *globals) *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "skip <step>",
		Short: "Leave a preparation step alone, so the web UI stops asking",
		Long: `Records that this router is deliberately staying as it is for one
step. The work still shows on the page, with who left it and when; what
stops is the browser being sent there.

Steps: ` + strings.Join(host.Steps(), ", "),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			by := "console"
			if u := os.Getenv("SUDO_USER"); u != "" {
				by = u
			}
			if err := host.SetSkip(g.configDir, args[0], by, !undo); err != nil {
				return err
			}
			word := "left alone"
			if undo {
				word = "back on the list"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", args[0], word)
			return nil
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "ask about this step again")
	return cmd
}
