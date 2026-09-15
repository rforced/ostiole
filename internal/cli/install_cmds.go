package cli

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/nft"
)

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("this command must run as root")
	}
	return nil
}

func newInstallCmd(g *globals) *cobra.Command {
	opts := install.Options{}
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the binary and systemd units and start the service",
		Long: `Copies this executable to /usr/local/bin, writes ostiole-firewall.service
(loads the confirmed ruleset before networking) and ostiole.service (the
web UI on --listen), enables both, and reports competing services.

Nothing about the existing firewall changes yet. Create the admin account,
apply and confirm a configuration, then run "ostiole takeover" to disable
firewalld, ufw, and friends.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			lay := install.DefaultLayout()
			lay.ConfigDir = g.configDir
			rep, err := install.Install(cmd.Context(), install.ExecSystemctl{}, lay, opts, slog.Default())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "installed %s and %s\n", rep.Binary, strings.Join(rep.Units, ", "))
			printCompetitors(out, rep.Competitors)
			fmt.Fprintf(out, "\nnext:\n  1. ostiole reset-password        (or open https://<this-host>%s/ and create the account there)\n  2. run the setup wizard in the web UI, or: ostiole init --lan ... && ostiole apply\n  3. ostiole takeover              (disables competing firewalls once your ruleset is confirmed)\n", opts.Listen)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Listen, "listen", ":443", "address the web UI listens on")
	cmd.Flags().BoolVar(&opts.ManageNetwork, "manage-network", false, "let Ostiole manage addressing through systemd-networkd (requires networkd on this host)")
	return cmd
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
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "takeover",
		Short: "Disable competing firewall services so Ostiole is the only firewall",
		Long: `Stops, disables, and masks firewalld, ufw, nftables.service, iptables, and
similar. Refuses to run unless a confirmed Ostiole ruleset is loaded in the
kernel, so the box is never left without a firewall. Network managers are
reported but left alone.`,
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
				if !stdinIsTerminal() {
					return errors.New("not interactive; pass --yes to proceed")
				}
				fmt.Fprint(out, "proceed? [y/N] ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(line)) != "y" {
					return errors.New("aborted")
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
	return cmd
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
				if !stdinIsTerminal() {
					return errors.New("not interactive; pass --yes to proceed")
				}
				fmt.Fprint(cmd.OutOrStdout(), "this removes the Ostiole firewall from the kernel; proceed? [y/N] ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.ToLower(strings.TrimSpace(line)) != "y" {
					return errors.New("aborted")
				}
			}
			lay := install.DefaultLayout()
			lay.ConfigDir = g.configDir
			if err := install.Uninstall(cmd.Context(), install.ExecSystemctl{}, lay, purge, slog.Default()); err != nil {
				return err
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
