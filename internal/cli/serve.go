package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/cron"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/server"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/update"
	"github.com/rforced/ostiole/internal/version"
)

func newServeCmd(g *globals) *cobra.Command {
	cfg := server.Config{}
	var useTLS bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web UI and API server",
		Long: `Serves the UI and API. With --tls a self-signed certificate is generated
under <config-dir>/tls on first use unless --tls-cert and --tls-key point
at your own.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var certManager *certs.Manager
			if useTLS {
				if cfg.TLSCert == "" {
					cfg.TLSCert = filepath.Join(g.configDir, "tls", "cert.pem")
				}
				if cfg.TLSKey == "" {
					cfg.TLSKey = filepath.Join(g.configDir, "tls", "key.pem")
				}
				certManager = certs.New(cfg.TLSCert, cfg.TLSKey)
				created, err := certManager.EnsureSelfSigned(certHosts())
				if err != nil {
					return err
				}
				if created {
					slog.Info("generated self-signed certificate", "cert", cfg.TLSCert)
				}
			} else {
				cfg.TLSCert, cfg.TLSKey = "", ""
			}
			eng, err := g.engine()
			if err != nil {
				return err
			}
			if os.Geteuid() == 0 {
				// A router must forward from the moment the daemon is up.
				if err := (sysctl.Proc{}).Apply(); err != nil {
					slog.Warn("could not apply router sysctls", "err", err)
				}
			}
			as, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			tokens, err := auth.NewTokens(g.configDir)
			if err != nil {
				return err
			}
			if as.NeedsSetup() {
				slog.Warn("no admin account yet; open the web UI to create one or run `ostiole reset-password`")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			// Blocklists and country ranges refresh in the background and
			// push straight into the loaded sets, so an update never
			// disturbs the rules that use them.
			refresher := &feeds.Refresher{
				Cache:   g.feeds(),
				Fetcher: feeds.NewFetcher(version.Version),
				Source:  eng.Effective,
				Sets:    &nft.Exec{Bin: g.nftBin},
				Log:     slog.Default(),
			}
			// The scheduled jobs the operator asked for, plus the work
			// Ostiole does on its own account, reported together.
			cronJobs := &cron.Jobs{
				Config:  eng.Effective,
				Users:   as.Users,
				Version: version.Version,
				Refresh: func(ctx context.Context) error { refresher.Tick(ctx, true); return nil },
				Restart: restartService,
			}
			crons := cron.NewRunner(eng.Effective, cronJobs, slog.Default())
			refresher.OnTick = func() { crons.Note("system:aliases") }
			deps := server.Deps{
				Engine:  eng,
				Auth:    as,
				Updater: newUpdater(cfg),
				Tables:  &nft.Exec{Bin: g.nftBin},
				Units:   install.ExecSystemctl{},
				Tokens:  tokens,
				Certs:   certManager,
				Feeds:   refresher,
				Crons:   crons,
				// The names are looked up fresh, so a certificate made
				// after the box moved covers where it moved to.
				CertHosts: certHosts,
			}
			go refresher.Run(ctx)
			go crons.Run(ctx)
			if os.Geteuid() == 0 {
				deps.Services = services.New()
				deps.Resolver = services.NewUnbound()
				deps.PPPoE = services.NewPPPoE()
				// Gateway probes need a raw socket and route changes need
				// netlink, so multi-WAN failover is a root-only feature.
				mon := gateway.New(gateway.NewICMPProber(), gateway.NewNetlinkRouter(), slog.Default())
				// The monitor follows the engine rather than the store, so a
				// gateway change is probed and routed during its confirmation
				// window and undone when the window expires.
				mon.Source = eng.Effective
				mon.Policy = policy.NewInstaller(slog.Default())
				mon.OnTick = func() { crons.Note("system:gateways") }
				deps.Gateways = mon
				go mon.Run(ctx)
				ring := fwlog.NewRing(2000)
				deps.Log = ring
				go func() {
					if err := (&fwlog.Listener{Ring: ring, Log: slog.Default()}).Run(ctx); err != nil {
						slog.Warn("firewall log listener stopped", "err", err)
					}
				}()
			}
			return server.Run(ctx, cfg, deps, slog.Default())
		},
	}
	cmd.Flags().StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "address to listen on")
	cmd.Flags().BoolVar(&useTLS, "tls", false, "serve HTTPS (self-signed certificate is generated if needed)")
	cmd.Flags().StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate (PEM); default <config-dir>/tls/cert.pem")
	cmd.Flags().StringVar(&cfg.TLSKey, "tls-key", "", "TLS private key (PEM); default <config-dir>/tls/key.pem")
	return cmd
}

// certHosts lists names the self-signed certificate should cover: the
// hostname, localhost, and every address currently on the box.
func certHosts() []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	if links, err := network.Discover(); err == nil {
		for _, l := range links {
			for _, a := range l.Addresses {
				if ip, _, ok := splitCIDR(a); ok {
					hosts = append(hosts, ip)
				}
			}
		}
	}
	return hosts
}

func splitCIDR(s string) (ip, prefix string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// newUpdater builds the update manager for the daemon. Updates go
// through the installed service binary and restart the unit.
func newUpdater(cfg server.Config) *update.Manager {
	bin, err := install.ServiceBinary(install.DefaultLayout())
	if err != nil {
		return nil
	}
	return &update.Manager{
		Client:         update.NewClient(),
		Installer:      &update.Installer{Binary: bin, Unit: install.DaemonUnit, HealthURL: healthURL(cfg), Run: install.ExecRunner{}},
		Current:        version.Version,
		PackageManaged: install.PackageManaged(bin),
		Log:            slog.Default(),
	}
}

// healthURL is where the post-update probe reaches this daemon.
func healthURL(cfg server.Config) string {
	scheme := "http"
	if cfg.TLS() {
		scheme = "https"
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || port == "" {
		port = "443"
	}
	return scheme + "://127.0.0.1:" + port + "/api/v1/health"
}

// restartService restarts one of the units Ostiole owns. The names the
// UI offers are mapped here, so a configuration can never name a unit
// that was not meant to be restartable.
func restartService(ctx context.Context, name string) error {
	units := map[string]string{
		"dnsmasq": services.Unit,
		"unbound": services.UnboundUnit,
		"ostiole": install.DaemonUnit,
	}
	unit, ok := units[name]
	if !ok {
		return fmt.Errorf("%q is not a service this box runs", name)
	}
	out, err := install.ExecRunner{}.Run(ctx, "systemctl", "restart", unit)
	if err != nil {
		return fmt.Errorf("restart %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}
