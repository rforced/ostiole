package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/update"
	"github.com/rforced/ostiole/internal/version"
)

func newUpdateCmd(_ *globals) *cobra.Command {
	var check, yes bool
	var channel, probe string
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for and install a newer release",
		Long: `Looks up the newest release on GitHub, verifies its signed checksums with
the key built into this binary, swaps the installed binary, and restarts
the service with a health check that rolls back on failure.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if probe != "" {
				return probeHealth(cmd.Context(), probe)
			}
			ch, err := channelFlag(channel)
			if err != nil {
				return err
			}
			bin, err := install.ServiceBinary(install.DefaultLayout())
			if err != nil {
				return err
			}
			client := update.NewClient()
			chk, err := client.Check(cmd.Context(), version.Version, ch)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "current  %s\n", chk.Current)
			if chk.Release == nil {
				fmt.Fprintf(out, "latest   none on the %s channel\n", ch)
				return nil
			}
			fmt.Fprintf(out, "latest   %s (%s, %s)\n", chk.Latest, ch, chk.Release.PublishedAt.Format("2006-01-02"))
			if !chk.Available {
				fmt.Fprintln(out, "up to date")
				return nil
			}
			if check {
				fmt.Fprintf(out, "update available: %s\n", chk.Release.URL)
				return nil
			}
			if install.PackageManaged(bin) {
				return update.ErrPackageManaged
			}
			if !yes {
				if err := confirmPrompt(cmd, fmt.Sprintf("install %s over %s and restart the service?", chk.Latest, bin)); err != nil {
					return err
				}
			}
			path, err := client.Download(cmd.Context(), chk.Release, filepath.Dir(bin), func(stage string, done, total int64) {
				if stage == "downloading" && total > 0 {
					fmt.Fprintf(out, "\r%s %d%%", stage, done*100/total)
				} else {
					fmt.Fprintf(out, "\r%s        ", stage)
				}
			})
			fmt.Fprintln(out)
			if err != nil {
				return err
			}
			inst := &update.Installer{Binary: bin, Unit: install.DaemonUnit, HealthURL: "https://127.0.0.1:443/api/v1/health", Run: install.ExecRunner{}}
			if err := inst.Install(cmd.Context(), path); err != nil {
				return err
			}
			fmt.Fprintf(out, "installed %s; the service restarts in a moment and rolls back if it fails its health check\n", chk.Latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "only report whether an update is available")
	cmd.Flags().StringVar(&channel, "channel", "stable", "release channel: stable or beta")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().StringVar(&probe, "probe", "", "internal: probe a health URL and exit 0 if it answers")
	_ = cmd.Flags().MarkHidden("probe")
	return cmd
}

func channelFlag(s string) (update.Channel, error) {
	switch update.Channel(s) {
	case update.Stable, update.Beta:
		return update.Channel(s), nil
	}
	return "", fmt.Errorf("unknown channel %q (stable or beta)", s)
}

// probeHealth is used by the post-update restart script. The daemon's
// certificate is self-signed, so verification is off; the probe only asks
// whether the new binary serves at all.
func probeHealth(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // local self-signed probe
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = errors.New(resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	slog.Error("health probe failed", "url", url, "err", lastErr)
	os.Exit(1)
	return nil
}
