package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
)

func newTokensCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tokens",
		Short: "List, create, and delete API tokens",
		Long: `API tokens authenticate scripts and monitoring without a browser
session. Send one as "Authorization: Bearer ost_…". A token carries a
role: admin does everything, operator changes and applies the
configuration, viewer only reads (which is all a metrics scraper needs).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tk, err := auth.NewTokens(g.configDir)
			if err != nil {
				return err
			}
			list := tk.List()
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no API tokens")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tROLE\tCREATED\tEXPIRES\tLAST USED")
			for _, t := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.Name, t.Role,
					t.CreatedAt.Format(time.DateOnly), stamp(t.ExpiresAt, "never"), stamp(t.LastUsedAt, "never"))
			}
			return w.Flush()
		},
	}

	var role string
	var days int
	create := &cobra.Command{
		Use:   "create <name>",
		Short: "Mint a token and print it once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := auth.NewTokens(g.configDir)
			if err != nil {
				return err
			}
			var ttl time.Duration
			if days > 0 {
				ttl = time.Duration(days) * 24 * time.Hour
			}
			tok, secret, err := tk.Create(args[0], auth.Role(role), ttl, "cli")
			if err != nil {
				return err
			}
			// To stdout on its own so it can be piped; everything else to
			// stderr, where it will not end up in a variable by accident.
			fmt.Fprintf(cmd.ErrOrStderr(), "created %s (%s, role %s)\nthis is the only time the token is shown:\n",
				tok.Name, tok.ID, tok.Role)
			fmt.Fprintln(cmd.OutOrStdout(), secret)
			return nil
		},
	}
	create.Flags().StringVar(&role, "role", string(auth.RoleViewer), "admin, operator, or viewer")
	create.Flags().IntVar(&days, "days", 0, "expire the token after this many days (0 never expires)")

	del := &cobra.Command{
		Use:   "delete <id-or-name>",
		Short: "Delete a token, which stops it working at once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tk, err := auth.NewTokens(g.configDir)
			if err != nil {
				return err
			}
			if err := tk.Delete(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(create, del)
	return cmd
}

func stamp(t *time.Time, empty string) string {
	if t == nil {
		return empty
	}
	return t.Format(time.DateOnly)
}
