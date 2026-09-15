package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/rforced/ostiole/internal/auth"
)

func newResetPasswordCmd(g *globals) *cobra.Command {
	var username string
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "reset-password",
		Short: "Create the admin account or set a new password",
		Long: `Creates the account if it does not exist, otherwise replaces its password
and ends its sessions. The password is read from the terminal, or from
stdin with --password-stdin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			password, err := readPassword(fromStdin, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := svc.SetPassword(username, password); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "password set for %s\n", username)
			return nil
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "admin", "account name")
	cmd.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password from stdin instead of prompting")
	return cmd
}

func readPassword(fromStdin bool, prompt io.Writer) (string, error) {
	if fromStdin {
		raw, err := io.ReadAll(bufio.NewReader(os.Stdin))
		if err != nil {
			return "", err
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no terminal to prompt on; use --password-stdin")
	}
	fmt.Fprint(prompt, "New password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	fmt.Fprint(prompt, "Repeat password: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(prompt)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
