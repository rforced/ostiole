package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
)

const (
	defaultBypass = 15 * time.Minute
	maxBypass     = 24 * time.Hour
)

func newSecondFactorCmd(g *globals) *cobra.Command {
	status := func(cmd *cobra.Command, _ []string) error {
		svc, err := auth.NewService(g.configDir)
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if b, ok := auth.ReadBypass(g.configDir); ok {
			fmt.Fprintf(out, "second factor bypassed until %s", b.Until.Local().Format(time.RFC3339))
			if b.By != "" {
				fmt.Fprintf(out, ", started by %s", b.By)
			}
			fmt.Fprintln(out)
		} else {
			fmt.Fprintln(out, "second factor not bypassed")
		}
		accounts := svc.Accounts()
		if len(accounts) > 0 {
			fmt.Fprintln(out)
		}
		for _, u := range accounts {
			totp, passkeys := svc.SecondFactorOf(u.Username)
			fmt.Fprintf(out, "%s  totp: %s  passkeys: %d\n", u.Username, yesNo(totp), len(passkeys))
		}
		return nil
	}

	cmd := &cobra.Command{
		Use:     "second-factor",
		Aliases: []string{"mfa"},
		Short:   "Bypass the second factor at sign-in, or take it off an account",
		Long: `Accounts can be set to ask for a code from an authenticator app, or a
passkey, after the password. These commands are the way back in when both
are lost. A bypass lets every sign-in skip the second factor for a while;
a reset takes an account's authenticator app and passkeys away. Sessions
that are already open and API tokens are not affected.

With no subcommand it shows the status.`,
		Args: cobra.NoArgs,
		RunE: status,
	}

	show := &cobra.Command{
		Use:   "status",
		Short: "Show whether the second factor is bypassed, and who has one",
		Args:  cobra.NoArgs,
		RunE:  status,
	}

	var length time.Duration
	var end bool
	bypass := &cobra.Command{
		Use:   "bypass",
		Short: "Let sign-ins skip the second factor for a while",
		Long: `Writes a bypass beside the accounts. The daemon reads it at every
sign-in, so it takes effect at once and needs no restart: until it ends,
the password alone is enough. It lasts 15 minutes unless --for says
otherwise, and 24 hours at most. --end stops a bypass early.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if end {
				_, running := auth.ReadBypass(g.configDir)
				if err := auth.ClearBypass(g.configDir); err != nil {
					return err
				}
				if !running {
					fmt.Fprintln(out, "no bypass was active")
					return nil
				}
				g.audit().Add(audit.Event{Action: audit.SecondFactorBypassEnd, By: audit.ShellActor()})
				fmt.Fprintln(out, "second factor bypass ended")
				return nil
			}
			if length <= 0 || length > maxBypass {
				return fmt.Errorf("--for must be longer than zero and at most %s", shortDuration(maxBypass))
			}
			who := audit.ShellActor()
			until := time.Now().Add(length)
			if err := auth.WriteBypass(g.configDir, auth.Bypass{Until: until, By: who.Name}); err != nil {
				return err
			}
			span := shortDuration(length)
			g.audit().Add(audit.Event{Action: audit.SecondFactorBypass, By: who, Detail: span})
			fmt.Fprintf(out, "second factor bypassed until %s (%s)\n", until.Format(time.RFC3339), span)
			return nil
		},
	}
	bypass.Flags().DurationVar(&length, "for", defaultBypass, "how long sign-ins skip the second factor, 24h at most")
	bypass.Flags().BoolVar(&end, "end", false, "end the bypass that is running")
	bypass.MarkFlagsMutuallyExclusive("for", "end")

	reset := &cobra.Command{
		Use:   "reset <account>",
		Short: "Remove the authenticator app and passkeys from an account",
		Long: `Removes the account's authenticator app and every passkey, so it signs in
with its password alone until it sets them up again. An account without a
second factor is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			name := args[0]
			if _, ok := svc.Account(name); !ok {
				return fmt.Errorf("%w %q", auth.ErrNoSuchUser, name)
			}
			if !svc.HasSecondFactor(name) {
				return auth.ErrNoSecondFactor
			}
			if err := svc.ClearSecondFactor(name); err != nil {
				return err
			}
			g.audit().Add(audit.Event{Action: audit.AccountSecondFactorClear, By: audit.ShellActor(), Target: name})
			fmt.Fprintf(cmd.OutOrStdout(), "second factor removed from %s\n", name)
			return nil
		},
	}

	cmd.AddCommand(show, bypass, reset)
	return cmd
}

// shortDuration writes 15m where Duration.String says 15m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
