package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

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

func newUsersCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "Manage local accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "USERNAME\tROLE")
			for _, u := range svc.Accounts() {
				fmt.Fprintf(w, "%s\t%s\n", u.Username, u.Role)
			}
			return w.Flush()
		},
	}
	var role string
	var fromStdin bool
	create := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a local account",
		Long: `Creates an account with a password read from the terminal, or from stdin
with --password-stdin. A name that is already taken is an error; use
reset-password to change an existing account's password.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			password, err := readPassword(fromStdin, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if err := svc.CreateUser(args[0], password, auth.Role(role)); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s as %s\n", args[0], role)
			return nil
		},
	}
	create.Flags().StringVar(&role, "role", string(auth.RoleViewer), "admin, operator, or viewer")
	create.Flags().BoolVar(&fromStdin, "password-stdin", false, "read the password from stdin instead of prompting")

	rename := &cobra.Command{
		Use:   "rename <username> <new-username>",
		Short: "Rename an account, keeping its role, password, and sessions",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			if err := svc.Rename(args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s is now %s\n", args[0], args[1])
			return nil
		},
	}

	del := &cobra.Command{
		Use:   "delete <username>",
		Short: "Delete an account and end its sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			if err := svc.DeleteUser(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
			return nil
		},
	}
	setRole := &cobra.Command{
		Use:   "role <username> <admin|operator|viewer>",
		Short: "Change what an account may do",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			if err := svc.SetRole(args[0], auth.Role(args[1])); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s is now %s\n", args[0], args[1])
			return nil
		},
	}
	cmd.AddCommand(create, rename, del, setRole)
	return cmd
}
