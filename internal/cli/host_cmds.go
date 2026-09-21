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
	"github.com/rforced/ostiole/internal/nft"
)

// hostDeps builds the host dependencies for a command. The same
// dependencies the daemon uses, so both front doors report the same
// router and act on it the same way.
func (g *globals) hostDeps() host.Deps {
	d := host.Deps{
		Root:    os.Geteuid() == 0,
		Units:   install.ExecSystemctl{},
		Kernel:  &nft.Exec{Bin: g.nftBin},
		Backend: g.netBackend,
		NFT:     g.nftBin,
		Dir:     g.configDir,
		Log:     slog.Default(),
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
		Short: "What this router is, and what an older firewall left behind",
		Long: `Reports the distribution, the kernel, Ostiole's units, the daemons it
drives, and who owns the addresses. The same facts are on the web UI under
System, Host.

Packages and units are the install script's job: run "ostiole repair" to
put them back.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printHost(cmd, host.Status(cmd.Context(), g.hostDeps()))
			return nil
		},
	}
	cmd.AddCommand(newHostFlushCmd(g))
	return cmd
}

// printHost writes the report the way a console reads it.
func printHost(cmd *cobra.Command, rep host.Report) {
	out := cmd.OutOrStdout()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "distribution\t%s\n", or(rep.Distro, "unknown"))
	fmt.Fprintf(w, "package manager\t%s\n", or(rep.Manager, "none found"))
	fmt.Fprintf(w, "kernel\t%s\n", or(rep.Kernel, "unknown"))
	fmt.Fprintf(w, "root\t%v\n", rep.Root)
	fmt.Fprintf(w, "ruleset loaded\t%v\n", rep.Firewalled)
	fmt.Fprintf(w, "addresses\t%s\n", addressOwner(rep.Network))
	for _, u := range rep.Units {
		fmt.Fprintf(w, "%s\t%s, %s\n", u.Name, u.Active, u.Enabled)
	}
	var have, missing []string
	for _, name := range host.Commands() {
		if rep.Present[name] {
			have = append(have, name)
		} else {
			missing = append(missing, name)
		}
	}
	fmt.Fprintf(w, "present\t%s\n", or(strings.Join(have, " "), "none"))
	if len(missing) > 0 {
		fmt.Fprintf(w, "missing\t%s\n", strings.Join(missing, " "))
	}
	_ = w.Flush()

	fmt.Fprintf(out, "\nnetworking: %s is %s, backend %s\n",
		install.NetworkdUnit, rep.Network.Networkd, rep.Network.Backend)
	if len(rep.Network.Managers) > 0 {
		verb := "still running"
		if rep.Network.Owned {
			verb = "retired by Ostiole"
		}
		fmt.Fprintf(out, "            %s: %s\n", verb, strings.Join(rep.Network.Managers, ", "))
	}
	if rep.Network.Pending {
		fmt.Fprintln(out, "            a handover is waiting: ostiole takeover --network --confirm")
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
}

// addressOwner says who has the router's addresses, in the words the
// page uses.
func addressOwner(net host.NetworkState) string {
	if net.Owned {
		return "owned by Ostiole"
	}
	if len(net.Managers) == 0 {
		return "owned by something Ostiole does not drive"
	}
	return "owned by " + strings.Join(net.Managers, ", ")
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
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
