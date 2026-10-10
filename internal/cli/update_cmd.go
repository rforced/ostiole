package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/audit"
	"ostiole/internal/install"
	"ostiole/internal/services"
	"ostiole/internal/update"
	"ostiole/internal/version"
)

// updateDeps are what `ostiole update` reaches outside its own process:
// GitHub, the installed binary, systemd and the person at the console.
type updateDeps struct {
	client  func() *update.Client
	current string
	binary  func() (string, error)
	listen  func() string
	run     update.Runner
	confirm func(cmd *cobra.Command, question string) error
}

func newUpdateCmd(g *globals) *cobra.Command {
	return updateCmd(g, updateDeps{
		client:  g.updateClient,
		current: version.Version,
		binary:  func() (string, error) { return install.ServiceBinary(install.DefaultLayout()) },
		listen:  installedListen,
		run:     install.ExecRunner{},
		confirm: confirmPrompt,
	})
}

func updateCmd(g *globals, d updateDeps) *cobra.Command {
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
			bin, err := d.binary()
			if err != nil {
				return err
			}
			client := d.client()
			chk, err := client.Check(cmd.Context(), d.current, ch)
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
			if !yes {
				if err := d.confirm(cmd, fmt.Sprintf("install %s over %s and restart the service?", chk.Latest, bin)); err != nil {
					return err
				}
			}
			inst := &update.Installer{
				Binary: bin, Unit: install.DaemonUnit,
				HealthURL: probeURL(d.listen(), true), Run: d.run,
				Proxy:      filepath.Join(filepath.Dir(bin), services.ProxyBinaryName),
				ProxyUnit:  services.ProxyUnit,
				RolledBack: filepath.Join(g.updatesDir(), update.RolledBackFile),
			}
			got, err := client.Download(cmd.Context(), chk.Release, filepath.Dir(bin), inst.WantsProxy(), func(stage string, done, total int64) {
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
			if err := inst.Install(cmd.Context(), got); err != nil {
				return err
			}
			g.audit().Add(audit.Event{Action: audit.Update, By: audit.ShellActor(), Detail: chk.Latest})
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
// certificate is self-signed, so verification is off — but only for the
// loopback address the script probes. Any other host is verified the usual
// way, so the hidden flag cannot be aimed somewhere else and made to accept
// whatever answers. The probe only asks whether the new binary serves at all.
func probeHealth(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("probe %q: %w", rawURL, err)
	}
	tr := &http.Transport{}
	if loopbackHost(u.Hostname()) {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed daemon certificate, loopback only
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	var lastErr error
	for range 10 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
	slog.Error("health probe failed", "url", rawURL, "err", lastErr)
	os.Exit(1)
	return nil
}

// loopbackHost reports whether host names this machine. A literal address is
// checked as one; the only name accepted is localhost, because any other name
// would have to be resolved, and what a name resolves to is not what the
// certificate would have been checked against.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
