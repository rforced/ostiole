package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/acme"
	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/cron"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/logging"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/server"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/smart"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/sysstat"
	"github.com/rforced/ostiole/internal/sysupdate"
	"github.com/rforced/ostiole/internal/tailscale"
	"github.com/rforced/ostiole/internal/timezone"
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
			g.tlsCert, g.tlsKey = cfg.TLSCert, cfg.TLSKey
			eng, err := g.engineWith(true)
			if err != nil {
				return err
			}
			// The daemon logs at the configured level from its first line,
			// not from the first apply. A --log-level on the command line
			// still wins.
			if c := eng.Effective(); c != nil && !g.logLevelSet {
				LogLevel.Set(logging.Slog(c.System.Logging.EffectiveLevel()))
			}
			if os.Geteuid() == 0 {
				// A router must forward from the moment the daemon is up.
				var kernel sysctl.Settings
				if cfg := eng.Effective(); cfg != nil {
					kernel.ConntrackMax = cfg.System.ConntrackMax
				}
				if err := (sysctl.Proc{}).Apply(kernel); err != nil {
					slog.Warn("could not apply router sysctls", "err", err)
				}
				// And read its clock in the configured zone from the same
				// moment, so the first log line is already comparable.
				zone := timezone.Default
				if cfg := eng.Effective(); cfg != nil {
					zone = cfg.System.Zone()
				}
				if err := (timezone.System{}).Apply(cmd.Context(), zone); err != nil {
					slog.Warn("could not set the router's timezone", "zone", zone, "err", err)
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
			// The DNS blocklists refresh on their own schedule and are
			// installed straight into dnsmasq's include file, so a list that
			// moved does not wait for the next apply.
			blocklists := &dnsblock.Refresher{
				Cache:   g.blocklists(),
				Fetcher: dnsblock.NewFetcher(version.Version),
				Source:  eng.Effective,
				Log:     slog.Default(),
			}
			if os.Geteuid() == 0 {
				// Writing dnsmasq's directory and restarting its unit needs
				// root; without it the next apply carries the list.
				blocklists.Loader = services.NewDNSBlock(g.blocklists())
			}
			// The certificates this router holds. The store exists with or
			// without --tls: another host can fetch a certificate from a
			// router whose own UI is plain HTTP.
			store := g.certStore()
			// The solver binds loopback when the proxy owns port 80: the
			// challenge route is in its configuration whatever it serves,
			// so a proxy switched on between two orders is seen by the next.
			issuer := acme.NewClient(version.Version, eng.Effective, store.AccountsDir())
			issuer.Addr = func() string {
				if cfg := eng.Effective(); cfg != nil && cfg.ProxyEnabled() {
					return "127.0.0.1:" + strconv.Itoa(acme.ChallengePort)
				}
				return ""
			}
			renewer := &acme.Renewer{
				Store:     store,
				Issuer:    issuer,
				Config:    eng.Effective,
				Addresses: publicAddresses,
				Log:       slog.Default(),
			}
			if certManager != nil {
				certManager.Store = store
				certManager.Selected = func() string {
					if cfg := eng.Effective(); cfg != nil {
						return cfg.System.Management.Certificate
					}
					return ""
				}
				store.Subscribe(func(string) {
					if err := certManager.Reload(); err != nil {
						slog.Warn("could not serve the selected certificate", "err", err)
					}
				})
			}
			// The distro package manager, driven from here so a router that
			// nobody logs into still gets its security fixes. It exists
			// even where it cannot be used, so the page can explain why.
			packages := sysupdate.New(sysupdate.Options{
				PackageManager: g.packageManager,
				StateDir:       g.updatesDir(),
				Root:           os.Geteuid() == 0,
				Log:            slog.Default(),
			})
			// An update that upgraded Ostiole itself restarted this
			// daemon; pick the transaction back up if it is still going.
			packages.Reattach(ctx)
			updater := newUpdater(cfg, g.updatesDir())
			actions := &cron.Actions{
				Config:            eng.Effective,
				Users:             as.Users,
				Version:           version.Version,
				Refresh:           func(ctx context.Context) error { refresher.Tick(ctx, true); return nil },
				RefreshBlocklists: func(ctx context.Context) error { blocklists.Tick(ctx, true); return nil },
				Restart:           restartService,
				SystemUpdate: func(ctx context.Context, mode string, exclude []string) (string, error) {
					return packages.RunScheduled(ctx, sysupdate.Mode(mode), exclude)
				},
				SelfUpdate: func(ctx context.Context, mode, channel string) (string, error) {
					if updater == nil {
						return "", errors.New("this binary cannot update itself")
					}
					return updater.RunScheduled(ctx, update.Mode(mode), update.Channel(channel))
				},
				SystemCheck: packages.CheckScheduled,
				SelfCheck: func(ctx context.Context, channel string) (string, error) {
					if updater == nil {
						return "", errors.New("this binary cannot check for its own releases")
					}
					return updater.CheckScheduled(ctx, update.Channel(channel))
				},
				Remote: func(ctx context.Context, r model.RemoteBackup, hostname string, archive *backup.Archive) (string, error) {
					return backup.Upload(ctx, r, hostname, "ostiole/"+version.Version, archive)
				},
				Certificates: renewer.Pass,
			}
			// The crons the operator asked for, plus the work Ostiole does
			// on its own account, reported together.
			crons := cron.NewRunner(eng.Effective, actions, slog.Default())
			refresher.OnTick = func() { crons.Note("system:aliases") }
			blocklists.OnTick = func() { crons.Note("system:blocklists") }
			deps := server.Deps{
				Engine:     eng,
				Auth:       as,
				Updater:    updater,
				Packages:   packages,
				Tables:     &nft.Exec{Bin: g.nftBin},
				Units:      install.ExecSystemctl{},
				Tokens:     tokens,
				Certs:      certManager,
				CertStore:  store,
				Renewer:    renewer,
				Feeds:      refresher,
				Blocklists: blocklists,
				Crons:      crons,
				// /proc and statfs need no privileges, so the dashboard
				// gets its usage card on a dev run as well as a real router.
				SysStat: sysstat.New(g.configDir),
				// The drives page is wired up everywhere so that a dev run
				// gets the page and its explanation of why it is empty.
				Drives: smart.New(g.smartctlBin),
				// The names are looked up fresh, so a certificate made
				// after the router moved covers where it moved to.
				CertHosts: certHosts,
				// The live queue figures need no privileges to read, so a
				// dev run gets the page too; without tc it says so.
				Shaping: g.shaper(),
				// The operating system underneath: which packages it still needs,
				// what competes with Ostiole on it, and what an older firewall
				// left in the kernel. Reported everywhere, acted on only
				// as root, because none of these steps are possible
				// otherwise.
				Host: host.Deps{
					Root:    os.Geteuid() == 0,
					Backend: g.netBackend,
					NFT:     g.nftBin,
					Dir:     g.configDir,
					Log:     slog.Default(),
				},
			}
			go refresher.Run(ctx)
			go blocklists.Run(ctx)
			go crons.Run(ctx)
			// The store follows the configuration the router is running
			// rather than the apply, so a revert or an expired window puts
			// the right certificate back.
			go (&certs.Watcher{Store: store, Manager: certManager, Source: eng.Effective, Log: slog.Default()}).Run(ctx)
			if os.Geteuid() == 0 {
				deps.Services = services.New()
				deps.Resolver = services.NewUnbound()
				deps.PPPoE = services.NewPPPoE()
				deps.UPnP = services.NewUPnP()
				deps.Tailscale = services.NewTailscale(g.configDir)
				deps.TSClient = tailscale.New()
				proxy := services.NewProxy(g.configDir, store)
				proxy.Log = slog.Default()
				if cfg.TLSCert != "" && cfg.TLSKey != "" {
					proxy.SelfCert, proxy.SelfKey = cfg.TLSCert, cfg.TLSKey
				}
				// A renewal reloads the proxy outside an apply, the way the
				// blocklist refresher installs outside one.
				proxy.Watch(ctx)
				deps.Proxy = proxy
				deps.Wireless = services.NewWireless(g.configDir)
				// Gateway probes need a raw socket and route changes need
				// netlink, so multi-WAN failover is a root-only feature.
				mon := gateway.New(gateway.NewICMPProber(), gateway.NewNetlinkRouter(), slog.Default())
				// The monitor follows the engine rather than the store, so a
				// gateway change is probed and routed during its confirmation
				// window and undone when the window expires.
				mon.Source = eng.Effective
				mon.Policy = policy.NewInstaller(slog.Default())
				// The same tick puts back a queue whose link has only just
				// come up, which is the boot and redial case.
				mon.Shaping = g.shaper()
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
				// The resolver's own answers, copied out of the kernel by
				// the same mechanism, and kept only while the
				// configuration says so.
				qlog := dnslog.New()
				qlog.Slog = slog.Default()
				deps.QueryLog = qlog
				blocklists.Installed = func(o dnsblock.Options) { qlog.Reindex(o, g.blocklists()) }
				go (&dnslog.Watcher{Log: qlog, Source: eng.Effective, Cache: g.blocklists()}).Run(ctx)
				go func() {
					if err := (&dnslog.Listener{Log: qlog, Slog: slog.Default()}).Run(ctx); err != nil {
						slog.Warn("query log listener stopped", "err", err)
					}
				}()
				// Reading a drive needs the device, so the hourly verdict
				// is root-only; the page itself is not.
				if deps.Drives.Bin != "" {
					drives := &smart.Monitor{Client: deps.Drives, Log: slog.Default()}
					drives.OnTick = func() { crons.Note("system:drives") }
					deps.DriveHealth = drives
					go drives.Run(ctx)
				}
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
// hostname, localhost, and every address currently on the router.
func certHosts() []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	if links, err := network.Discover(); err == nil {
		for _, l := range links {
			for _, a := range l.Addresses {
				if ip, ok := splitCIDR(a); ok {
					hosts = append(hosts, ip)
				}
			}
		}
	}
	return hosts
}

// publicAddresses lists the addresses an interface has that a CA would
// issue a certificate for.
func publicAddresses(iface string) []string {
	links, err := network.Discover()
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range links {
		if l.Name != iface {
			continue
		}
		for _, a := range l.Addresses {
			if ip, ok := splitCIDR(a); ok && acme.Public(ip) {
				out = append(out, ip)
			}
		}
	}
	return out
}

func splitCIDR(s string) (ip string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[:i], true
		}
	}
	return s, false
}

// newUpdater builds the update manager for the daemon. Updates go
// through the installed service binary and restart the unit. What the
// last check found is kept beside the package manager's own state, so a
// page can ask this router rather than GitHub.
func newUpdater(cfg server.Config, stateDir string) *update.Manager {
	bin, err := install.ServiceBinary(install.DefaultLayout())
	if err != nil {
		return nil
	}
	return &update.Manager{
		Client: update.NewClient(),
		Installer: &update.Installer{
			Binary: bin, Unit: install.DaemonUnit, HealthURL: healthURL(cfg), Run: install.ExecRunner{},
			Proxy:     filepath.Join(filepath.Dir(bin), services.ProxyBinaryName),
			ProxyUnit: services.ProxyUnit,
		},
		Current: version.Version,
		Cache:   update.NewCache(stateDir),
		Log:     slog.Default(),
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
		"dnsmasq":       services.Unit,
		"unbound":       services.UnboundUnit,
		"miniupnpd":     services.UPnPUnit,
		"tailscaled":    services.TailscaleUnit,
		"ostiole-proxy": services.ProxyUnit,
		"ostiole":       install.DaemonUnit,
	}
	unit, ok := units[name]
	if !ok {
		return fmt.Errorf("%q is not a service this router runs", name)
	}
	out, err := install.ExecRunner{}.Run(ctx, "systemctl", "restart", unit)
	if err != nil {
		return fmt.Errorf("restart %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}
