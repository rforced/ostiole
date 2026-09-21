package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/version"
)

func newRepairCmd(_ *globals) *cobra.Command {
	var yes, dryRun, tailscale, wireless, proxy bool
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Reinstall packages and units the way install does",
		Long: `Runs the install script again with the binary already in place: the
packages a router needs go back on, the units are rewritten, the firewalls
and updaters it replaces come off again. An existing ruleset, configuration
and network handover are left alone.

Run it on a router whose packages were changed by hand, or if the session
dropped during the first install. --tailscale adds tailscaled and its unit;
--wireless adds hostapd, iw and the firmware for the card in this router;
--proxy adds the reverse proxy sidecar.`,
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
			if tailscale {
				args = append(args, "--with-tailscale")
			}
			if wireless {
				args = append(args, "--with-wireless")
			}
			if proxy {
				args = append(args, "--with-proxy")
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			sh := exec.CommandContext(cmd.Context(), "sh", args...)
			// The binary is already here, so nothing of ostiole's is
			// fetched; the version is passed anyway, because the sidecar
			// has to match the release this binary came from.
			sh.Env = append(os.Environ(), "OSTIOLE_NO_DOWNLOAD=1",
				"OSTIOLE_BIN_DIR="+filepath.Dir(self), "OSTIOLE_VERSION="+version.Version)
			// No stdin: under the documented install the script comes down
			// a pipe that is already at its end, so nothing it runs can
			// read the terminal, and it asks its own question on /dev/tty.
			// Handing it a live terminal here instead is how a package
			// manager that asks something with its output on /dev/null
			// leaves the repair hanging on a question nobody can see.
			sh.Stdout, sh.Stderr, sh.Stdin = cmd.OutOrStdout(), cmd.ErrOrStderr(), nil
			if err := sh.Run(); err != nil {
				return fmt.Errorf("the install script failed: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan and change nothing")
	cmd.Flags().BoolVar(&tailscale, "tailscale", false, "install Tailscale as well, from its own repository where a distribution packages none")
	cmd.Flags().BoolVar(&wireless, "wireless", false, "install what a wifi card needs as well, whether or not this router has one")
	cmd.Flags().BoolVar(&proxy, "proxy", false, "install the reverse proxy as well")
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
