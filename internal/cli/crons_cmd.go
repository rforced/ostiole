package cli

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/cron"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/version"
)

func newCronsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "crons",
		Short: "List the scheduled jobs and when they next run",
		Long: `Shows the jobs configured on this box and when each one next runs. The
daemon is what actually runs them; this reads the same configuration, so
it works whether or not the daemon is up.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			if len(cfg.Crons) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no scheduled jobs")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tJOB\tSCHEDULE\tNEXT\tDESCRIPTION")
			now := time.Now()
			for _, job := range cfg.Crons {
				next := "disabled"
				if job.Enabled {
					next = "never"
					if s, err := cron.Parse(job.Schedule); err != nil {
						next = "bad schedule: " + err.Error()
					} else if when, ok := s.Next(now); ok {
						next = when.Local().Format(time.RFC3339)
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", job.ID, job.Job, job.Schedule, next, job.Description)
			}
			return w.Flush()
		},
	}

	run := &cobra.Command{
		Use:   "run <id>",
		Short: "Run one job now and print what it did",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			job, ok := cfg.Cron(args[0])
			if !ok {
				return fmt.Errorf("no job called %q", args[0])
			}
			as, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			jobs := &cron.Jobs{
				Config:  func() *model.Config { return cfg },
				Users:   as.Users,
				Version: version.Version,
				Refresh: func(ctx context.Context) error {
					r := &feeds.Refresher{
						Cache:   g.feeds(),
						Fetcher: feeds.NewFetcher(version.Version),
						Source:  func() *model.Config { return cfg },
						Sets:    &nft.Exec{Bin: g.nftBin},
					}
					r.Tick(ctx, true)
					return nil
				},
				Restart: restartService,
			}
			out, err := jobs.Run(cmd.Context(), *job)
			if out != "" {
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			return err
		},
	}
	cmd.AddCommand(run)
	return cmd
}
