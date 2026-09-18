package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/install"
)

func newRepairCmd(_ *globals) *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Reinstall packages and units the way install does",
		Long: `Runs the install script again with the binary already in place: the
packages a router needs go back on, the units are rewritten, the firewalls
and updaters it replaces come off again. An existing ruleset, configuration
and network handover are left alone.

Run it after installing a package by hand, or if the session dropped
during the first install.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireRoot(); err != nil {
				return err
			}
			path, err := writeScript()
			if err != nil {
				return err
			}
			defer func() { _ = os.Remove(path) }()
			args := []string{path}
			if yes {
				args = append(args, "--yes")
			}
			if dryRun {
				args = append(args, "--dry-run")
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			sh := exec.CommandContext(cmd.Context(), "sh", args...)
			sh.Env = append(os.Environ(), "OSTIOLE_NO_DOWNLOAD=1", "OSTIOLE_BIN_DIR="+filepath.Dir(self))
			sh.Stdout, sh.Stderr, sh.Stdin = cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin
			if err := sh.Run(); err != nil {
				return fmt.Errorf("the install script failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
	return cmd
}

// writeScript puts the embedded installer somewhere sh can read it.
func writeScript() (string, error) {
	f, err := os.CreateTemp("", "ostiole-repair-*.sh")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.WriteString(install.Script); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
