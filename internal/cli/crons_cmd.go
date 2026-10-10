package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/cron"
	"ostiole/internal/dnsblock"
	"ostiole/internal/feeds"
	"ostiole/internal/model"
	"ostiole/internal/nft"
	"ostiole/internal/services"
	"ostiole/internal/sysupdate"
	"ostiole/internal/update"
	"ostiole/internal/version"
	"ostiole/internal/wol"
)

func newCronsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crons",
		Short: "List the scheduled crons and when they next run",
		Long: `Shows the crons configured on this router and when each one next runs. The
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
			// to find out when the router next patches itself.
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
				Config:    source,
				Users:     as.Users,
				Version:   version.Version,
				BackupDir: g.backupDir,
				Refresh: func(ctx context.Context) error {
					r := &feeds.Refresher{
						Cache:   g.feeds(),
						Fetcher: feeds.NewFetcher(g.listGetter()),
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
						Fetcher: dnsblock.NewFetcher(g.listGetter()),
						Source:  source,
						Loader:  services.NewDNSBlock(g.blocklists()),
					}
					r.Tick(ctx, true)
					return nil
				},
				Restart: restartService,
				SystemUpdate: func(ctx context.Context, mode string, exclude []string) (string, error) {
					return g.packages().RunScheduled(ctx, sysupdate.Mode(mode), exclude)
				},
				SystemCheck: func(ctx context.Context) (string, error) {
					return g.packages().CheckScheduled(ctx)
				},
				// Checking asks GitHub and writes down the answer; it needs
				// none of the machinery installing a release does, so it
				// works from here as well as from the daemon.
				SelfCheck: func(ctx context.Context, channel string) (string, error) {
					m := &update.Manager{
						Client:  g.updateClient(),
						Current: version.Version,
						Cache:   update.NewCache(g.updatesDir()),
					}
					return m.CheckScheduled(ctx, update.Channel(channel))
				},
				Wake: wol.Send,
			}
			out, err := actions.Run(cmd.Context(), *c)
			if out != "" {
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			// A job that ran and failed was run all the same.
			g.audit().Add(audit.Event{Action: audit.CronRun, By: audit.ShellActor(), Target: c.ID})
			return err
		},
	}
	cmd.AddCommand(run)
	return cmd
}

// packages drives the distro package manager as root, which is what a
// cron run from the command line is.
func (g *globals) packages() *sysupdate.Manager {
	return sysupdate.New(sysupdate.Options{
		PackageManager: g.packageManager,
		StateDir:       g.updatesDir(),
		Root:           true,
	})
}
