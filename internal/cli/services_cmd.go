package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
)

func newServicesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "services",
		Short: "LAN services (dnsmasq, optionally unbound and miniupnpd)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := services.New()
			u := services.NewUnbound()
			ctx := cmd.Context()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "set up\t%v\n", d.Installed(ctx))
			fmt.Fprintf(w, "running\t%v\n", d.Active(ctx))
			leases, err := d.ReadLeases()
			if err == nil {
				fmt.Fprintf(w, "leases\t%d\n", len(leases))
			}
			fmt.Fprintf(w, "resolver set up\t%v\n", u.Installed(ctx))
			fmt.Fprintf(w, "resolver running\t%v\n", u.Active(ctx))
			p := services.NewUPnP()
			fmt.Fprintf(w, "upnp set up\t%v\n", p.Installed(ctx))
			fmt.Fprintf(w, "upnp running\t%v\n", p.Active(ctx))
			return w.Flush()
		},
	}
	mappings := &cobra.Command{
		Use:   "mappings",
		Short: "Show the port mappings clients have opened for themselves",
		Long: `Reads the mappings out of the loaded ruleset, which is where they are:
the daemon that answers UPnP IGD, PCP and NAT-PMP writes into a chain of
Ostiole's. The description a client sent and the lifetime it asked for stay
with the daemon and are not shown.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := (&nft.Exec{Bin: g.nftBin}).ListTableJSON(cmd.Context())
			if errors.Is(err, nft.ErrNoTable) {
				return errors.New("no ruleset is loaded, so nothing is mapped")
			}
			if err != nil {
				return err
			}
			maps, err := nft.ParseMappings(raw)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PROTO\tEXTERNAL\tCLIENT\tINTERNAL")
			for _, m := range maps {
				fmt.Fprintf(w, "%s\t%d\t%s\t%d\n", m.Protocol, m.ExternalPort, m.Internal, m.InternalPort)
			}
			return w.Flush()
		},
	}
	leases := &cobra.Command{
		Use:   "leases",
		Short: "Show DHCP leases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			leases, err := services.New().ReadLeases()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "IP\tMAC\tHOSTNAME\tEXPIRES")
			for _, l := range leases {
				exp := "static"
				if !l.Static {
					exp = l.Expires.Local().Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.IP, l.MAC, l.Hostname, exp)
			}
			return w.Flush()
		},
	}
	cmd.AddCommand(leases, mappings)
	return cmd
}
