package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/diff"
	"ostiole/internal/engine"
	"ostiole/internal/model"
)

func newUnusedCmd(g *globals) *cobra.Command {
	var remove, yes bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "unused",
		Short: "Show the configuration nothing uses, and what is switched off",
		Long: `Lists what nothing in the configuration uses, such as an alias no rule
names or a zone no interface is in, and below it what is switched off.
Nothing changes until you pass --remove, which deletes the unused items
through the same confirmation window as a normal apply. A zone takes the
rules and NAT written against it. What is switched off is kept.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, base, err := (&configSource{}).load(g.store())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			unused, disabled := cfg.Unused()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			if len(unused) > 0 {
				fmt.Fprintln(w, "KIND\tNAME\tWHY\tTAKES")
				for _, u := range unused {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", u.Kind, u.Name, u.Why, strings.Join(u.Takes, ", "))
				}
			}
			if len(disabled) > 0 {
				if len(unused) > 0 {
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, "DISABLED")
				fmt.Fprintln(w, "KIND\tNAME")
				for _, d := range disabled {
					fmt.Fprintf(w, "%s\t%s\n", d.Kind, d.Name)
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if len(unused) == 0 {
				if len(disabled) > 0 {
					fmt.Fprintln(out)
				}
				if remove {
					fmt.Fprintln(out, "nothing to remove: everything is used")
				} else {
					fmt.Fprintln(out, "nothing to do: everything is used")
				}
				return nil
			}
			if !remove {
				fmt.Fprintln(out, "\nnothing changed; pass --remove to delete the unused items")
				return nil
			}

			keys := make([]model.UnusedKey, len(unused))
			for i, u := range unused {
				keys[i] = model.UnusedKey{Kind: u.Kind, ID: u.ID}
			}
			next := *cfg
			if err := next.RemoveUnused(keys); err != nil {
				return err
			}
			changes, err := diff.Compare(cfg, &next)
			if err != nil {
				return err
			}
			printChanges(cmd, changes)
			if yes {
				timeout = 0
			}
			if timeout > 0 && !stdinIsTerminal() {
				return errors.New("confirmation needs an interactive terminal; use --yes to commit immediately")
			}
			eng, err := g.engine()
			if err != nil {
				return err
			}
			res, err := eng.Apply(cmd.Context(), &next, engine.ApplyOptions{ConfirmTimeout: timeout, Base: base})
			if err != nil {
				return err
			}
			if !res.Pending {
				fmt.Fprintln(out, "removed and committed")
				return nil
			}
			fmt.Fprintf(out, "removed; press Enter within %s to confirm, or it reverts\n", timeout.Truncate(time.Second))
			return waitForConfirmation(cmd.Context(), eng, res.Deadline, os.Stdin, out)
		},
	}
	cmd.Flags().BoolVar(&remove, "remove", false, "delete the unused items instead of only listing them")
	cmd.Flags().DurationVar(&timeout, "confirm-timeout", 60*time.Second, "how long to wait for confirmation before reverting")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "commit immediately without a confirmation window")
	return cmd
}
