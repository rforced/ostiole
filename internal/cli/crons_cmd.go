package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/cron"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/sysupdate"
	"github.com/rforced/ostiole/internal/version"
)

func newCronsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crons",
		Short: "List the scheduled crons and when they next run",
		Long: `Shows the crons configured on this box and when each one next runs. The
daemon is what actually runs them; this reads the same configuration, so
it works whether or not the daemon is up.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			// The update crons are not written out anywhere; they come
			// from the update settings, and this is where somebody looks
			// to find out when the box next patches itself.
			crons := append(append([]model.Cron{}, cfg.Crons...), cfg.DerivedCrons()...)
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tKIND\tSCHEDULE\tNEXT\tDESCRIPTION")
			now := time.Now()
			for _, c := range crons {
				next := "disabled"
				if c.Enabled {
					next = "never"
					if s, err := cron.Parse(c.Schedule); err != nil {
						next = "bad schedule: " + err.Error()
					} else if when, ok := s.Next(now); ok {
						next = when.Local().Format(time.RFC3339)
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Kind, c.Schedule, next, c.Description)
			}
			return w.Flush()
		},
	}

	run := &cobra.Command{
		Use:   "run <id>",
		Short: "Run one cron now and print what it did",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			c, ok := cfg.Cron(args[0])
			if !ok {
				return fmt.Errorf("no cron called %q", args[0])
			}
			as, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			source := func() *model.Config { return cfg }
			actions := &cron.Actions{
				Config:  source,
				Users:   as.Users,
				Version: version.Version,
				Refresh: func(ctx context.Context) error {
					r := &feeds.Refresher{
						Cache:   g.feeds(),
						Fetcher: feeds.NewFetcher(version.Version),
						Source:  source,
						Sets:    &nft.Exec{Bin: g.nftBin},
					}
					r.Tick(ctx, true)
					return nil
				},
				// This command is root-only, so the refreshed lists can go
				// straight to the resolver rather than waiting for an apply.
				RefreshBlocklists: func(ctx context.Context) error {
					r := &dnsblock.Refresher{
						Cache:   g.blocklists(),
						Fetcher: dnsblock.NewFetcher(version.Version),
						Source:  source,
						Loader:  services.NewDNSBlock(g.blocklists()),
					}
					r.Tick(ctx, true)
					return nil
				},
				Restart: restartService,
				SystemUpdate: func(ctx context.Context, mode string, exclude []string) (string, error) {
					packages := sysupdate.New(sysupdate.Options{
						PackageManager: g.packageManager,
						StateDir:       g.updatesDir(),
						Root:           true,
					})
					return packages.RunScheduled(ctx, sysupdate.Mode(mode), exclude)
				},
			}
			out, err := actions.Run(cmd.Context(), *c)
			if out != "" {
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			return err
		},
	}
	cmd.AddCommand(run)
	return cmd
}
