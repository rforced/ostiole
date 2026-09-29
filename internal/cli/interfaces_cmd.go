package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/network"
)

func newInterfacesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "interfaces",
		Short: "List network interfaces and their live state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			links, err := network.Discover()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKIND\tSTATE\tMAC\tMTU\tADDRESSES")
			for _, l := range links {
				state := "down"
				switch {
				case l.Carrier:
					state = "up"
				case l.Up:
					state = "no-carrier"
				}
				kind := l.Kind
				if l.VLANID != 0 {
					kind = fmt.Sprintf("vlan %d on %s", l.VLANID, l.Parent)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", l.Name, kind, state, l.MAC, l.MTU, strings.Join(l.Addresses, " "))
			}
			return w.Flush()
		},
	}
	cmd.AddCommand(newInterfacesRenewCmd(g))
	return cmd
}

func newInterfacesRenewCmd(g *globals) *cobra.Command {
	var release bool
	cmd := &cobra.Command{
		Use:   "renew <name>",
		Short: "Ask for a fresh lease on a dynamic interface",
		Long: `Asks the DHCP server to extend the lease the interface holds. With --release the
lease is dropped first and the interface starts over from discovery, which is
what a WAN behind a modem that has stopped answering needs. The address is gone
until the server answers.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := g.engine()
			if err != nil {
				return err
			}
			if err := eng.RenewLease(cmd.Context(), args[0], release); err != nil {
				return err
			}
			if release {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: lease released, asking for a new one\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: renewal requested\n", args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&release, "release", false, "drop the lease and start over instead of extending it")
	return cmd
}
