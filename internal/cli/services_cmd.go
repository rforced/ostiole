package cli

import (
	"fmt"
	"log/slog"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/services"
)

func newServicesCmd(_ *globals) *cobra.Command {
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
			if maps, err := p.ReadMappings(); err == nil {
				fmt.Fprintf(w, "upnp mappings\t%d\n", len(maps))
			}
			return w.Flush()
		},
	}
	var withResolver, withPPPoE, withUPnP bool
	setup := &cobra.Command{
		Use:   "setup",
		Short: "Install dnsmasq, write its unit, and retire competing resolvers",
		Long: `Installs dnsmasq with the package manager if needed, writes
ostiole-dnsmasq.service, masks the distro dnsmasq unit and systemd-resolved
(both would take port 53), and makes /etc/resolv.conf a regular file that
Ostiole manages. Then enable DHCP and DNS under Services in the web UI.

With --with-resolver it also installs unbound, bootstraps the DNSSEC root
trust anchor, and writes ostiole-unbound.service, which the DNS service
can then use to validate DNSSEC or to speak DNS over TLS.

With --with-pppoe it installs pppd and writes ostiole-pppoe@.service, so
an interface can dial a session over Ethernet the way DSL is delivered.

With --with-upnp it installs miniupnpd and writes ostiole-miniupnpd.service,
so clients on the LAN can open their own port mappings. The build has to be
the nftables one, which is checked before anything is written.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			opts := services.SetupOptions{Resolver: withResolver, PPPoE: withPPPoE, UPnP: withUPnP}
			if err := services.Setup(cmd.Context(), services.New(), opts, slog.Default()); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "services ready: enable DHCP and DNS in the web UI (Services) or in the configuration, then apply")
			return nil
		},
	}
	setup.Flags().BoolVar(&withResolver, "with-resolver", false, "also install unbound for DNSSEC validation and DNS over TLS")
	setup.Flags().BoolVar(&withPPPoE, "with-pppoe", false, "also install pppd so an interface can dial a PPPoE session")
	setup.Flags().BoolVar(&withUPnP, "with-upnp", false, "also install miniupnpd so clients can ask for their own port mappings")
	mappings := &cobra.Command{
		Use:   "mappings",
		Short: "Show the port mappings clients have asked for",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			maps, err := services.NewUPnP().ReadMappings()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PROTO\tEXTERNAL\tCLIENT\tINTERNAL\tEXPIRES\tDESCRIPTION")
			for _, m := range maps {
				exp := "never"
				if m.Expires != nil {
					exp = m.Expires.Local().Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%s\n",
					m.Protocol, m.ExternalPort, m.Internal, m.InternalPort, exp, m.Description)
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
	cmd.AddCommand(setup, leases, mappings)
	return cmd
}
