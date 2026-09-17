package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/diff"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/version"
)

func newBackupCmd(g *globals) *cobra.Command {
	var out, note string
	var withUsers bool
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Write the configuration to a portable JSON file",
		Long: `Writes the saved configuration, with a little metadata, as JSON. The file
holds every secret the configuration does, WireGuard private keys among
them, so keep it somewhere you would keep a password. With --with-users it
also carries the administrator accounts and their password hashes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := g.store().Load()
			if err != nil {
				return err
			}
			opts := backup.Options{Ostiole: version.Version, Note: note}
			if withUsers {
				as, err := auth.NewService(g.configDir)
				if err != nil {
					return err
				}
				opts.Users = as.Users()
			}
			archive, err := backup.Create(cfg, opts)
			if err != nil {
				return err
			}
			raw, err := archive.Marshal()
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err := cmd.OutOrStdout().Write(raw)
				return err
			}
			// 0600: the file carries tunnel keys and possibly password hashes.
			if err := os.WriteFile(out, raw, 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%d bytes)\n", out, len(raw))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "", "file to write, or - for standard output")
	cmd.Flags().StringVar(&note, "note", "", "a line about why this backup was taken")
	cmd.Flags().BoolVar(&withUsers, "with-users", false, "include administrator accounts and password hashes")
	return cmd
}

func newRestoreCmd(g *globals) *cobra.Command {
	var apply, withUsers, yes bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "restore <file>",
		Short: "Read a backup and show, or apply, what it would change",
		Long: `Reads a backup file and reports how it differs from the running
configuration. Nothing changes until you pass --apply, which loads it
through the same confirmation window as a normal apply. With --with-users
the administrator accounts in the backup replace the ones on this router,
which signs everyone out.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			archive, err := backup.Parse(raw)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			s := archive.Summary()
			fmt.Fprintf(out, "backup of %s taken %s by ostiole %s\n",
				orDash(s.Hostname), s.CreatedAt.Format(time.RFC3339), orDash(s.Ostiole))
			if s.Note != "" {
				fmt.Fprintf(out, "note: %s\n", s.Note)
			}
			fmt.Fprintf(out, "%d zones, %d interfaces, %d rules, %d aliases, %d gateways, %d accounts\n",
				s.Zones, s.Interfaces, s.Rules, s.Aliases, s.Gateways, s.Users)

			if current, err := g.store().Load(); err == nil {
				changes, derr := diff.Compare(current, archive.Config)
				if derr != nil {
					return derr
				}
				printChanges(cmd, changes)
			} else {
				fmt.Fprintln(out, "\nnothing is configured yet, so this would be the first configuration")
			}

			if !apply {
				fmt.Fprintln(out, "\nnothing changed; pass --apply to load this backup")
				return nil
			}
			if withUsers {
				as, err := auth.NewService(g.configDir)
				if err != nil {
					return err
				}
				if err := as.Restore(archive.Users); err != nil {
					return fmt.Errorf("restore accounts: %w", err)
				}
				fmt.Fprintf(out, "restored %d account(s); everyone must sign in again\n", len(archive.Users))
			}
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
			res, err := eng.Apply(cmd.Context(), archive.Config, engine.ApplyOptions{ConfirmTimeout: timeout})
			if err != nil {
				return err
			}
			if !res.Pending {
				fmt.Fprintln(out, "restored and committed")
				return nil
			}
			fmt.Fprintf(out, "restored; press Enter within %s to confirm, or it reverts\n", timeout.Truncate(time.Second))
			return waitForConfirmation(cmd.Context(), eng, res.Deadline, os.Stdin, out)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "load the backup instead of only reporting on it")
	cmd.Flags().BoolVar(&withUsers, "with-users", false, "also replace the administrator accounts")
	cmd.Flags().DurationVar(&timeout, "confirm-timeout", 60*time.Second, "how long to wait for confirmation before reverting")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "commit immediately without a confirmation window")
	return cmd
}

func newDiffCmd(g *globals) *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare two configurations",
		Long: `Compares configurations and prints what differs. Each side is a revision
id from 'ostiole revisions', the word 'current' for the saved
configuration, or a path to a JSON file or backup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			left, err := loadSide(g, from)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			right, err := loadSide(g, to)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}
			changes, err := diff.Compare(left, right)
			if err != nil {
				return err
			}
			printChanges(cmd, changes)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "revision id, 'current', or a file (default: the newest revision)")
	cmd.Flags().StringVar(&to, "to", "current", "revision id, 'current', or a file")
	return cmd
}

// loadSide resolves one side of a comparison: a revision, the saved
// configuration, a backup file, or a plain configuration file.
func loadSide(g *globals, ref string) (*model.Config, error) {
	st := g.store()
	switch ref {
	case "current":
		return st.Load()
	case "":
		revs, err := st.Revisions()
		if err != nil {
			return nil, err
		}
		if len(revs) == 0 {
			return nil, errors.New("there are no revisions to compare against")
		}
		return st.LoadRevision(revs[0].ID)
	}
	if raw, err := os.ReadFile(ref); err == nil {
		if archive, perr := backup.Parse(raw); perr == nil {
			return archive.Config, nil
		}
		return readConfigFile(ref)
	}
	return st.LoadRevision(ref)
}

func printChanges(cmd *cobra.Command, changes []diff.Change) {
	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintln(out, "\nno differences")
		return
	}
	fmt.Fprintf(out, "\n%d difference(s):\n", len(changes))
	for _, c := range changes {
		fmt.Fprintln(out, "  "+c.String())
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
