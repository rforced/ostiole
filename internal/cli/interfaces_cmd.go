package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/network"
)

func newInterfacesCmd() *cobra.Command {
	return &cobra.Command{
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
}
