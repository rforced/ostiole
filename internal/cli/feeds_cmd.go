package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/version"
)

func newAliasesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aliases",
		Short: "Show the aliases whose contents are fetched",
		Long: `Blocklists and country address ranges are fetched rather than typed, and
cached on this router so a reboot without a working line still blocks what
it blocked yesterday. The daemon refreshes them on a schedule; this shows
what it has and refreshes on demand.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			statuses := g.feeds().Statuses(cfg)
			if len(statuses) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no aliases are fetched from anywhere")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ALIAS\tENTRIES\tFETCHED\tSOURCES\tNOTE")
			for _, s := range statuses {
				note := ""
				switch {
				case s.LastError != "":
					note = s.LastError
				case s.Stale:
					note = "stale"
				}
				fetched := "never"
				if !s.FetchedAt.IsZero() {
					fetched = s.FetchedAt.Local().Format(time.RFC3339)
				}
				fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\n", s.Alias, s.Entries, fetched, len(s.Sources), note)
			}
			return w.Flush()
		},
	}

	refresh := &cobra.Command{
		Use:   "refresh [alias]",
		Short: "Fetch the lists now and push them into the running ruleset",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := &feeds.Refresher{
				Cache:   g.feeds(),
				Fetcher: feeds.NewFetcher(version.Version),
				Source: func() *model.Config {
					cfg, err := g.store().Load()
					if err != nil {
						return nil
					}
					return cfg
				},
				Sets: &nft.Exec{Bin: g.nftBin},
			}
			if len(args) == 1 {
				count, err := r.RefreshOne(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d entries\n", args[0], count)
				return nil
			}
			r.Tick(cmd.Context(), true)
			fmt.Fprintln(cmd.OutOrStdout(), "refreshed; run `ostiole aliases` to see the result")
			return nil
		},
	}
	cmd.AddCommand(refresh)
	return cmd
}
