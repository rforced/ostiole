package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"ostiole/internal/acme"
	"ostiole/internal/atomicfile"
	"ostiole/internal/auth"
	"ostiole/internal/backup"
	"ostiole/internal/certs"
	"ostiole/internal/chrony"
	"ostiole/internal/cron"
	"ostiole/internal/ddns"
	"ostiole/internal/dhcplog"
	"ostiole/internal/dnsblock"
	"ostiole/internal/dnslog"
	"ostiole/internal/dnsprovider"
	"ostiole/internal/engine"
	"ostiole/internal/feeds"
	"ostiole/internal/fwlog"
	"ostiole/internal/gateway"
	"ostiole/internal/host"
	"ostiole/internal/install"
	"ostiole/internal/journalfeed"
	"ostiole/internal/logfile"
	"ostiole/internal/logging"
	"ostiole/internal/logring"
	"ostiole/internal/memlimit"
	"ostiole/internal/model"
	"ostiole/internal/network"
	"ostiole/internal/nft"
	"ostiole/internal/notify"
	"ostiole/internal/panics"
	"ostiole/internal/peerlog"
	"ostiole/internal/policy"
	"ostiole/internal/requestlog"
	"ostiole/internal/server"
	"ostiole/internal/services"
	"ostiole/internal/smart"
	"ostiole/internal/store"
	"ostiole/internal/sysctl"
	"ostiole/internal/sysstat"
	"ostiole/internal/sysupdate"
	"ostiole/internal/tailscale"
	"ostiole/internal/timezone"
	"ostiole/internal/traffic"
	"ostiole/internal/update"
	"ostiole/internal/version"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
	"ostiole/internal/wirelesslog"
	"ostiole/internal/wol"
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
			release, err := lockDaemon(g.configDir)
			if err != nil {
				return err
			}
			defer release()
			// Nothing of this daemon writes yet and the lock keeps another
			// out, so a temporary file here is one an earlier run stopped
			// in the middle of saving.
			if removed := atomicfile.Sweep(g.configDir); len(removed) > 0 {
				slog.Info("removed files an earlier run left half written", "files", removed)
			}
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
			// An apply that a crash or a reboot cut short was never
			// confirmed. It goes back before anything reads the
			// configuration.
			if _, err := eng.Recover(cmd.Context()); err != nil {
				slog.Error("could not undo an apply that was never confirmed", "err", err)
			}
			// The daemon logs at the configured level from its first line,
			// not from the first apply. A --log-level on the command line
			// still wins.
			if c := eng.Effective(); c != nil && !g.logLevelSet {
				LogLevel.Set(logging.Slog(c.System.Logging.EffectiveLevel()))
			}
			if os.Geteuid() == 0 {
				ensureRuleset(cmd.Context(), eng)
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
				Fetcher: feeds.NewFetcher(g.listGetter()),
				Source:  eng.Effective,
				Sets:    &nft.Exec{Bin: g.nftBin},
				Log:     slog.Default(),
			}
			// The DNS blocklists refresh on their own schedule and are
			// installed straight into dnsmasq's include file, so a list that
			// moved does not wait for the next apply.
			blocklists := &dnsblock.Refresher{
				Cache:   g.blocklists(),
				Fetcher: dnsblock.NewFetcher(g.listGetter()),
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
			certStore := g.certStore()
			// The solver moves off port 80 when the proxy owns it. The proxy
			// forwards the challenge path to it over loopback, and the
			// firewall redirects port 80 from outside to it when the proxy is
			// not there to answer, so it listens on every address. Read per
			// order, so a proxy switched on between two orders is seen by the
			// next.
			issuer := acme.NewClient(eng.Effective, certStore.AccountsDir())
			issuer.Addr = func() string {
				if cfg := eng.Effective(); cfg != nil && cfg.ProxyEnabled() {
					return ":" + strconv.Itoa(model.ChallengePort)
				}
				return ""
			}
			// Dynamic DNS reads the interfaces itself and talks to a
			// provider only when an address changed, once a day, or when
			// asked.
			dyn := &ddns.Updater{
				Config:  eng.Effective,
				Options: dnsprovider.Options{UserAgent: version.Agent, CloudflareAPI: g.cloudflareAPI},
				State:   eng.Store(),
				Log:     slog.Default(),
			}
			renewer := &acme.Renewer{
				Store:     certStore,
				Issuer:    issuer,
				Config:    eng.Effective,
				Addresses: publicAddresses,
				Log:       slog.Default(),
			}
			if certManager != nil {
				certManager.Store = certStore
				certManager.Selected = func() string {
					if cfg := eng.Effective(); cfg != nil {
						return cfg.System.Management.Certificate
					}
					return ""
				}
				certStore.Subscribe(func(string) {
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
			updater := newUpdater(cfg, g.updatesDir(), g.updateClient())
			actions := &cron.Actions{
				Config:            eng.Effective,
				Users:             as.Users,
				Version:           version.Version,
				BackupDir:         g.backupDir,
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
				Remote:       backup.Upload,
				Certificates: renewer.Pass,
				Wake:         wol.Send,
			}
			// The crons the operator asked for, plus the work Ostiole does
			// on its own account, reported together.
			crons := cron.NewRunner(eng.Effective, actions, slog.Default(), g.configDir)
			refresher.OnTick = func() { crons.Note("system:aliases") }
			blocklists.OnTick = func() { crons.Note("system:blocklists") }
			dyn.OnPass = func() { crons.Note("system:ddns") }
			deps := server.Deps{
				Engine:     eng,
				Auth:       as,
				Updater:    updater,
				Packages:   packages,
				Tables:     &nft.Exec{Bin: g.nftBin},
				Units:      install.ExecSystemctl{},
				Tokens:     tokens,
				Certs:      certManager,
				CertStore:  certStore,
				Renewer:    renewer,
				DDNS:       dyn,
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
					Dir:     g.configDir,
					Log:     slog.Default(),
				},
			}
			// Every loop starts again after a panic rather than taking the
			// daemon with it: several parse what the internet sends.
			log := slog.Default()
			// Notices leave the router only once an admin turns them on;
			// until then the notifier keeps and sends nothing.
			notifier := &notify.Notifier{Config: eng.Effective, Log: log}
			eng.WithNotifier(notifier)
			deps.Notify = notifier
			// The logs in memory go to files as well while System, General
			// says so. The last write of the files, and of the crons'
			// state, happens as the daemon stops, which waits for them.
			files := &logfile.Writer{Dir: g.logDir, Source: eng.Effective, Log: log}
			deps.LogFiles = files
			var stopping sync.WaitGroup
			stopping.Go(func() { panics.Loop(ctx, log, "log files", files.Run) })
			var memTotal uint64
			if eng.MemTotal != nil {
				memTotal = eng.MemTotal()
			}
			budget := model.MemoryBudget{Total: int64(min(memTotal, math.MaxInt64))}
			limits := memlimit.New(eng.Effective, budget, log)
			limits.Apply()
			reads := &readBacks{}
			// What crosses the router: every link always, and every device
			// while the configuration says so.
			counter := &traffic.Counter{Source: eng.Effective, Log: log}
			deps.Traffic = counter
			reads.start(ctx, log, "traffic files", "traffic", func() {
				readTraffic(eng.Effective(), counter, files, log)
			}, counter.Run)
			go panics.Loop(ctx, log, "notifications", notifier.Run)
			go panics.Loop(ctx, log, "alias refresher", refresher.Run)
			go panics.Loop(ctx, log, "blocklist refresher", blocklists.Run)
			stopping.Go(func() { panics.Loop(ctx, log, "crons", crons.Run) })
			go panics.Loop(ctx, log, "dynamic DNS", dyn.Run)
			// The store follows the configuration the router is running
			// rather than the apply, so a revert or an expired window puts
			// the right certificate back.
			go panics.Loop(ctx, log, "certificate watcher",
				(&certs.Watcher{Store: certStore, Manager: certManager, Source: eng.Effective, Log: log}).Run)
			if os.Geteuid() == 0 {
				deps.Services = services.New()
				deps.Resolver = services.NewUnbound()
				deps.PPPoE = services.NewPPPoE()
				deps.UPnP = services.NewUPnP()
				deps.Tailscale = services.NewTailscale(g.configDir)
				deps.TSClient = tailscale.New()
				proxy := services.NewProxy(g.configDir, certStore)
				proxy.Log = slog.Default()
				if cfg.TLSCert != "" && cfg.TLSKey != "" {
					proxy.SelfCert, proxy.SelfKey = cfg.TLSCert, cfg.TLSKey
				}
				// A renewal reloads the proxy outside an apply, the way the
				// blocklist refresher installs outside one.
				proxy.Watch(ctx)
				// And its files follow the proxy an update puts in, without
				// waiting for an apply (ADR-0038).
				go panics.Loop(ctx, log, "proxy follower", func(ctx context.Context) {
					followRelease(ctx, eng, proxy, log)
				})
				deps.Proxy = proxy
				deps.Wireless = services.NewWireless(g.configDir)
				ntp := services.NewNTP()
				deps.NTP = ntp
				deps.Chrony = chrony.New()
				// The first start after `ostiole repair` wrote the unit hands
				// the clock over here rather than at the next apply, which
				// may be days away.
				go func() {
					defer panics.Recover(log, "time service")
					if err := ntp.Start(ctx, eng.Effective()); err != nil {
						log.Warn("could not start the time service; the next apply tries again", "err", err)
					}
				}()
				// Gateway probes need a raw socket and route changes need
				// netlink, so multi-WAN failover is a root-only feature.
				mon := gateway.New(gateway.NewICMPProber(), gateway.NewNetlinkRouter(), slog.Default())
				// The monitor follows the engine rather than the store, so a
				// gateway change is probed and routed during its confirmation
				// window and undone when the window expires.
				mon.Source = eng.Effective
				mon.Policy = policy.NewInstaller(slog.Default())
				mon.Removed = policy.WatchRemoved
				// The same tick puts back a queue whose link has only just
				// come up, which is the boot and redial case.
				mon.Shaping = g.shaper()
				watchGateways(ctx, mon, eng, &deps, files, crons, reads, log)
				// Daylight saving, or a zone set at start that differs from
				// the one the ruleset was loaded in, moves the offset a
				// schedule's hours were converted at, so its scheduled chains
				// go in again and the kernel takes the offset for its days.
				// When the whole table had to go in, what the old one held
				// outside the ruleset goes back.
				go panics.Loop(ctx, log, "schedule clock", func(ctx context.Context) {
					eng.FollowOffset(ctx, func(ctx context.Context) {
						refresher.Push(ctx)
						if err := deps.UPnP.Rebuild(ctx); err != nil {
							log.Warn("could not restart the port mapping service; mappings made before the reload do not work until it restarts", "err", err)
						}
					})
				})
				// The ring starts at the default and the watcher sizes it
				// from the configuration a moment later; it grows into its
				// ceiling as packets arrive, so a big one costs nothing up
				// front.
				ring := fwlog.NewRing(model.FirewallLog{}.Size())
				deps.Log = ring
				zones := &fwlog.Zones{}
				go panics.Loop(ctx, log, "firewall log watcher", (&fwlog.Watcher{Ring: ring, Source: eng.Effective, Zones: zones}).Run)
				reads.start(ctx, log, "firewall log files", "firewall log listener", func() {
					readFirewallLog(eng.Effective(), ring, files, log)
				}, func(ctx context.Context) {
					if err := (&fwlog.Listener{Ring: ring, Log: log, Zones: zones}).Run(ctx); err != nil {
						log.Warn("firewall log listener stopped", "err", err)
					}
				})
				// The resolver's own answers, copied out of the kernel by
				// the same mechanism, and kept only while the
				// configuration says so.
				qlog := dnslog.New()
				qlog.Slog = slog.Default()
				deps.QueryLog = qlog
				blocklists.Installed = func(o dnsblock.Options) { qlog.Reindex(o, g.blocklists()) }
				go panics.Loop(ctx, log, "query log watcher",
					(&dnslog.Watcher{Log: qlog, Source: eng.Effective, Cache: g.blocklists()}).Run)
				reads.start(ctx, log, "query log files", "query log listener", func() {
					readQueryLog(eng.Effective(), qlog, files, log)
				}, func(ctx context.Context) {
					if err := (&dnslog.Listener{Log: qlog, Names: counter.Names(), Slog: log}).Run(ctx); err != nil {
						log.Warn("query log listener stopped", "err", err)
					}
				})
				// Reading a drive needs the device, so the hourly verdict
				// is root-only; the page itself is not.
				if deps.Drives.Bin != "" {
					// Each verdict's readings are kept too, as many as
					// the configuration says.
					history := smart.NewHistory()
					drives := &smart.Monitor{Client: deps.Drives, History: history, Source: eng.Effective, Log: slog.Default()}
					drives.OnTick = func() { crons.Note("system:drives") }
					deps.DriveHealth, deps.DriveHistory = drives, history
					reads.start(ctx, log, "drive history files", "drive monitor", func() {
						readRing(eng.Effective(), history, smart.HistoryFiles(history), smart.HistorySettings, files, log)
					}, drives.Run)
				}
			} else if g.fakeProbes != "" {
				fake := &gateway.FakeProbes{Path: g.fakeProbes}
				mon := gateway.New(fake, fake, slog.Default())
				mon.ProbeEvery = time.Second
				watchGateways(ctx, mon, eng, &deps, files, crons, reads, log)
			}
			// What the WAF matched, fed from the proxy's journal: read back
			// from its files at start while they are on, then from the
			// journal after the newest event they held. Without root there
			// is no proxy to read, and the log stays empty.
			wafLog := waflog.New()
			deps.WAFLog = wafLog
			// The proxy's requests, fed from the same journal while the
			// level keeps them.
			requests := requestlog.New()
			deps.Requests = requests
			proxyFeed := &journalfeed.Feed{
				Journal: journalfeed.Journalctl{Unit: services.ProxyUnit}, Source: eng.Effective, Slog: log,
				Taps: []journalfeed.AnyTap{
					&journalfeed.Tap[wafevent.Event]{
						Log: wafLog, Parse: wafevent.Parse, Name: "the WAF events",
						Settings: func(c *model.Config) (int, time.Duration) {
							return c.Services.Proxy.Events.Size(), c.Services.Proxy.Events.Retention()
						},
						On: func(c *model.Config) bool { return c.ProxyEnabled() },
					},
					&journalfeed.Tap[requestlog.Request]{
						Log: requests, Parse: requestlog.Parse, Name: "the proxy's requests",
						Settings: requestlog.Settings, Kept: requestlog.Kept,
						On: func(c *model.Config) bool { return c.ProxyEnabled() },
					},
				},
			}
			if deps.Proxy != nil {
				proxyFeed.Installed = deps.Proxy.Installed
			}
			var proxyFiles sync.WaitGroup
			proxyFiles.Add(2)
			reads.start(ctx, log, "WAF event files", "proxy journal", func() {
				defer proxyFiles.Done()
				readWAFEvents(eng.Effective(), wafLog, files, log)
			}, func(ctx context.Context) {
				proxyFiles.Wait()
				proxyFeed.Run(ctx)
			})
			reads.start(ctx, log, "proxy request files", "proxy requests", func() {
				defer proxyFiles.Done()
				readRing(eng.Effective(), requests, requestlog.Files(requests), requestlog.Settings, files, log)
			}, func(ctx context.Context) { <-ctx.Done() })
			// What the DHCP server says of its clients, fed from its journal
			// while the level keeps it.
			dhcpLog := dhcplog.New()
			deps.DHCPLog = dhcpLog
			dhcpFeed := &journalfeed.Feed{
				Journal: journalfeed.Journalctl{Unit: services.Unit}, Source: eng.Effective, Slog: log,
				Taps: []journalfeed.AnyTap{&journalfeed.Tap[dhcplog.Event]{
					Log: dhcpLog, Parse: dhcplog.Parse, Name: "the DHCP server's messages",
					Settings: dhcplog.Settings, Kept: dhcplog.Kept,
					On: func(c *model.Config) bool { return c.Services.DHCP.Enabled },
				}},
			}
			if os.Geteuid() == 0 {
				dhcpFeed.Installed = func(context.Context) bool { return true }
			}
			reads.start(ctx, log, "DHCP log files", "DHCP log", func() {
				readRing(eng.Effective(), dhcpLog, dhcplog.Files(dhcpLog), dhcplog.Settings, files, log)
			}, dhcpFeed.Run)
			// The wireless clients' coming and going, fed from every radio's
			// access point while the level keeps it.
			wirelessLog := wirelesslog.New()
			deps.WirelessLog = wirelessLog
			wirelessFeed := &journalfeed.Feed{
				Journal: journalfeed.Journalctl{Unit: strings.Replace(services.WirelessUnit, "@.", "@*.", 1)},
				Source:  eng.Effective, Slog: log,
				Taps: []journalfeed.AnyTap{&journalfeed.Tap[wirelesslog.Event]{
					Log: wirelessLog, Parse: wirelesslog.Parse, Name: "the wireless clients",
					Settings: wirelesslog.Settings, Kept: wirelesslog.Kept,
					On: func(c *model.Config) bool { return c.WirelessEnabled() },
				}},
			}
			if os.Geteuid() == 0 {
				wirelessFeed.Installed = func(context.Context) bool { return true }
			}
			reads.start(ctx, log, "wireless log files", "wireless log", func() {
				readRing(eng.Effective(), wirelessLog, wirelesslog.Files(wirelessLog), wirelesslog.Settings, files, log)
			}, wirelessFeed.Run)
			// The VPN peers' coming and going, read every few seconds while
			// the level keeps it. Reading them needs root.
			for _, kind := range []peerlog.Kind{peerlog.WireGuard, peerlog.Tailscale} {
				peers := peerlog.New()
				poll := &peerlog.Poller{Log: peers, Kind: kind, Source: eng.Effective, Slog: log}
				if kind.Name == peerlog.WireGuard.Name {
					deps.WireGuardLog = peers
					poll.Read, poll.Changes = peerlog.ReadWireGuard(nil), peerlog.WireGuardChanges
				} else {
					deps.TailscaleLog = peers
					if deps.TSClient != nil {
						poll.Read = peerlog.ReadTailscale(deps.TSClient.Status)
					}
					poll.Changes = peerlog.TailscaleChanges
				}
				run := poll.Run
				if os.Geteuid() != 0 || poll.Read == nil {
					run = func(ctx context.Context) { <-ctx.Done() }
				}
				reads.start(ctx, log, kind.Name+" log files", kind.Name+" peers", func() {
					readRing(eng.Effective(), peers, kind.Files(peers), kind.Settings, files, log)
				}, run)
			}
			go func() {
				started := time.Now()
				reads.wait()
				limits.Settle()
				log.Info("every log is read back", "took", time.Since(started))
			}()
			go panics.Loop(ctx, log, "memory limit", limits.Run)
			err = server.Run(ctx, cfg, deps, slog.Default())
			stop()
			waitFor(&stopping, stopWait, log)
			return err
		},
	}
	cmd.Flags().StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "address to listen on")
	cmd.Flags().BoolVar(&useTLS, "tls", false, "serve HTTPS (self-signed certificate is generated if needed)")
	cmd.Flags().StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate (PEM); default <config-dir>/tls/cert.pem")
	cmd.Flags().StringVar(&cfg.TLSKey, "tls-key", "", "TLS private key (PEM); default <config-dir>/tls/key.pem")
	return cmd
}

// stopWait is how long a stopping daemon waits for its last writes. The
// unit gives it 45 seconds on Fedora and 90 elsewhere before it is killed.
const stopWait = 20 * time.Second

// waitFor waits for wg, or until limit is up.
func waitFor(wg *sync.WaitGroup, limit time.Duration, log *slog.Logger) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(limit):
		log.Warn("stopped without waiting any longer for the last writes", "after", limit)
		return false
	}
}

// readBacks counts the logs still reading their files back.
type readBacks struct{ wg sync.WaitGroup }

// start reads a log's files back, and only then starts what feeds it, so
// what comes back is in place before the first new entry. A read back that
// panics still lets it start.
func (r *readBacks) start(ctx context.Context, log *slog.Logger, files, loop string, read func(), run func(context.Context)) {
	r.wg.Add(1)
	go func() {
		func() {
			defer r.wg.Done()
			defer panics.Recover(log, files)
			read()
		}()
		panics.Loop(ctx, log, loop, run)
	}()
}

func (r *readBacks) wait() { r.wg.Wait() }

// readFirewallLog fills the firewall log from its files while the
// configuration writes them, as its own entries and days allow, and hands
// it to the writer.
func readFirewallLog(cfg *model.Config, ring *fwlog.Ring, files *logfile.Writer, log *slog.Logger) {
	var st logfile.ReadStats
	if cfg != nil && cfg.System.Logging.Files.Enabled {
		f := cfg.System.Management.FirewallLog
		ring.Configure(f.Size(), f.Retention())
		started := time.Now()
		upgradeFiles(files.Dir, fwlog.FileName, fwlog.FileVersion, log)
		entries, stats, err := logfile.Read(files.Dir, fwlog.FileName, fwlog.FileVersion, f.Size(),
			started.Add(-ring.Files().Kept(cfg)), fwlog.ParseLine)
		if err != nil {
			log.Warn("could not read all of the firewall log's files back", "err", err)
		}
		st = stats
		if err := ring.Restore(entries, logfile.NewestSeq(files.Dir, fwlog.FileName)); err != nil {
			log.Warn("could not put the firewall log's files back", "err", err)
		}
		log.Info("read the firewall log back from its files", "entries", len(entries), "took", time.Since(started))
	}
	files.Add(ring.Files(), st)
}

// upgradeFiles numbers a log's files written before its lines kept their
// numbers, before it is read back. Files it could not number are left
// out: the log reads only its own format.
func upgradeFiles(dir, name string, version int, log *slog.Logger) {
	n, err := logfile.Upgrade(dir, name, version-1, version, logfile.WithSeq)
	if err != nil {
		log.Warn("could not number all of a log's older files; those left are not read", "log", name, "err", err)
	}
	if n > 0 {
		log.Info("numbered a log's older files", "log", name, "files", n)
	}
}

// readQueryLog does the same for the query log, while it is on, streaming
// its answers into a ring sized from the files' indexes.
func readQueryLog(cfg *model.Config, qlog *dnslog.Log, files *logfile.Writer, log *slog.Logger) {
	var st logfile.ReadStats
	if cfg != nil && cfg.System.Logging.Files.Enabled && cfg.Services.DNS.QueryLog.Enabled {
		q := cfg.Services.DNS.QueryLog
		started := time.Now()
		upgradeFiles(files.Dir, dnslog.FileName, dnslog.FileVersion, log)
		since := started.Add(-qlog.Files().Kept(cfg))
		count, err := logfile.Count(files.Dir, dnslog.FileName, since)
		if err != nil {
			log.Warn("could not count all of the query log's answers in its files", "err", err)
		}
		n := 0
		rs, err := qlog.Restorer(q, count)
		if err != nil {
			log.Warn("could not put the query log's files back", "err", err)
		} else {
			stats, err := logfile.Stream(files.Dir, dnslog.FileName, dnslog.FileVersion, q.Size(), since,
				dnslog.ParseLine, rs.Push)
			if err != nil {
				log.Warn("could not read all of the query log's files back", "err", err)
			}
			st = stats
			if n, err = rs.Done(logfile.NewestSeq(files.Dir, dnslog.FileName)); err != nil {
				log.Warn("could not put the query log's files back", "err", err)
			}
		}
		log.Info("read the query log back from its files", "answers", n, "took", time.Since(started))
	}
	files.Add(qlog.Files(), st)
}

// readWAFEvents does the same for the WAF events. Files that hold none,
// as a Clear leaves them, start the log empty, as the files being off does.
func readWAFEvents(cfg *model.Config, events *waflog.Log, files *logfile.Writer, log *slog.Logger) {
	var st logfile.ReadStats
	if cfg != nil && cfg.System.Logging.Files.Enabled {
		e := cfg.Services.Proxy.Events
		events.Configure(e.Size(), e.Retention())
		started := time.Now()
		entries, stats, err := logfile.Read(files.Dir, waflog.FileName, waflog.FileVersion, e.Size(),
			started.Add(-events.Files().Kept(cfg)), waflog.ParseLine)
		if err != nil {
			log.Warn("could not read all of the WAF events' files back", "err", err)
		}
		st = stats
		if err := events.Restore(entries, logfile.NewestSeq(files.Dir, waflog.FileName)); err != nil {
			log.Warn("could not put the WAF events' files back", "err", err)
		}
		log.Info("read the WAF events back from their files", "events", len(entries), "took", time.Since(started))
	}
	files.Add(events.Files(), st)
}

// readRing does the same for one of the newer logs, while the
// configuration keeps it.
func readRing[T any, P logring.Entry[T]](cfg *model.Config, ring *logring.Ring[T, P], l logfile.Log,
	settings func(*model.Config) (int, time.Duration), files *logfile.Writer, log *slog.Logger,
) {
	var st logfile.ReadStats
	if cfg != nil && cfg.System.Logging.Files.Enabled && l.On(cfg) {
		size, keep := settings(cfg)
		ring.Configure(size, keep)
		started := time.Now()
		since := started.Add(-l.Kept(cfg))
		count, err := logfile.Count(files.Dir, l.Name, since)
		if err != nil {
			log.Warn("could not count all of a log's files", "log", l.Name, "err", err)
		}
		rs, err := ring.Restorer(count)
		if err != nil {
			log.Warn("could not put a log's files back", "log", l.Name, "err", err)
		} else {
			stats, err := logfile.Stream(files.Dir, l.Name, l.Version, size, since, logring.Parse[T, P], rs.Push)
			if err != nil {
				log.Warn("could not read all of a log's files back", "log", l.Name, "err", err)
			}
			st = stats
			n, err := rs.Done(logfile.NewestSeq(files.Dir, l.Name))
			if err != nil {
				log.Warn("could not put a log's files back", "log", l.Name, "err", err)
			}
			log.Info("read a log back from its files", "log", l.Name, "entries", n, "took", time.Since(started))
		}
	}
	files.Add(l, st)
}

// watchGateways runs the gateway monitor once its history is read back
// from the files.
func watchGateways(ctx context.Context, mon *gateway.Monitor, eng *engine.Engine, deps *server.Deps,
	files *logfile.Writer, crons *cron.Runner, reads *readBacks, log *slog.Logger,
) {
	// The monitor follows the engine rather than the store, so a gateway
	// change is probed and routed during its confirmation window and undone
	// when the window expires.
	mon.Source = eng.Effective
	mon.OnTick = func() { crons.Note("system:gateways") }
	mon.History = gateway.NewHistory()
	deps.Gateways, deps.GatewayHistory = mon, mon.History
	reads.start(ctx, log, "gateway files", "gateway monitor", func() {
		readGateways(eng.Effective(), mon.History, files, log)
	}, mon.Run)
}

// readGateways rebuilds the gateways' minutes and events from their files
// while the configuration writes them, and hands both logs to the writer.
func readGateways(cfg *model.Config, h *gateway.History, files *logfile.Writer, log *slog.Logger) {
	logs := h.FileLogs()
	var st logfile.ReadStats
	if cfg != nil && cfg.System.Logging.Files.Enabled {
		started := time.Now()
		stats, err := logfile.ReadEach(files.Dir, gateway.HistoryFileName, gateway.FileVersion,
			started.Add(-logs[0].Kept(cfg)), gateway.ParseMinute, func(m gateway.MinuteLine) { h.RestoreMinute(m, started) })
		if err != nil {
			log.Warn("could not read all of the gateways' history back", "err", err)
		}
		st = stats
		h.EndRestore(logfile.NewestSeq(files.Dir, gateway.HistoryFileName))
		log.Info("read the gateways' history back from its files", "took", time.Since(started))
	}
	files.Add(logs[0], st)
	readRing(cfg, h.Events, logs[1], gateway.EventSettings, files, log)
}

// readTraffic rebuilds what traffic counted from its files while the
// configuration writes them, before counting starts, and hands its logs to
// the writer: the minutes of the links, and of the devices while they are
// counted, as a day of minutes and a month of hours, and the destinations'
// hours as their own entries and days allow.
func readTraffic(cfg *model.Config, counter *traffic.Counter, files *logfile.Writer, log *slog.Logger) {
	stats := map[string]logfile.ReadStats{}
	logs := counter.FileLogs()
	kept := func(name string) time.Duration {
		for _, l := range logs {
			if l.Name == name {
				return l.Kept(cfg)
			}
		}
		return 0
	}
	if cfg != nil && cfg.System.Logging.Files.Enabled {
		started := time.Now()
		counter.BeginRestore(started)
		for _, name := range []string{traffic.LinksFile, traffic.DevicesFile} {
			if name == traffic.DevicesFile && !cfg.Traffic.Devices {
				continue
			}
			st, err := logfile.ReadEach(files.Dir, name, traffic.FileVersion, started.Add(-kept(name)), traffic.ParseMinute,
				func(m traffic.MinuteLine) { counter.RestoreMinute(name, m, started) })
			if err != nil {
				log.Warn("could not read all of traffic's files back", "log", name, "err", err)
			}
			stats[name] = st
		}
		if cfg.Traffic.DestinationsOn() {
			d := cfg.Traffic.Destinations
			st, err := logfile.Stream(files.Dir, traffic.DestinationsFile, traffic.FileVersion, d.Size(),
				started.Add(-kept(traffic.DestinationsFile)), traffic.ParseHour, counter.RestoreHour)
			if err != nil {
				log.Warn("could not read all of the destinations' files back", "err", err)
			}
			stats[traffic.DestinationsFile] = st
		}
		counter.EndRestore(started)
		log.Info("read traffic back from its files", "took", time.Since(started))
	}
	for _, l := range logs {
		files.Add(l, stats[l.Name])
	}
}

// lockDaemon keeps a second daemon off the same configuration: two would
// fight over the kernel's table, the store and every unit Ostiole drives,
// and each would hold its own idea of what is pending. The lock goes with
// the process, so a crash never leaves it behind.
func lockDaemon(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, ".serve.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(f.Name())
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("ostiole serve is already running on %s (pid %s)", dir, strings.TrimSpace(string(holder)))
		}
		return nil, fmt.Errorf("lock %s: %w", dir, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() { _ = f.Close() }, nil
}

// daemonArgs is the command line of the daemon serving dir, read through
// the pid lockDaemon wrote, or nil when none runs. proc is /proc outside
// tests.
func daemonArgs(dir, proc string) []string {
	raw, err := os.ReadFile(filepath.Join(dir, ".serve.lock"))
	if err != nil {
		return nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return nil
	}
	cmdline, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
	// The file outlives a daemon that crashed, and its pid can go to any
	// process afterwards.
	abs, err := filepath.Abs(dir)
	if err != nil || !strings.HasPrefix(filepath.Base(args[0]), "ostiole") || !slices.Contains(args, "serve") ||
		filepath.Clean(flagValue(args, "config-dir", store.DefaultDir)) != abs {
		return nil
	}
	return args
}

// flagValue reads a flag's value from a command line, or def when the
// flag is not on it.
func flagValue(args []string, name, def string) string {
	for i, a := range args {
		if a == "--"+name && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--"+name+"="); ok {
			return v
		}
	}
	return def
}

// ensureRuleset loads the firewall when the kernel has none, whatever
// happened to the unit that loads it at boot: a router must never run
// without one.
func ensureRuleset(ctx context.Context, eng *engine.Engine) {
	st, err := eng.Status(ctx)
	if err != nil || st.TableLoaded {
		return
	}
	res, err := eng.Load(ctx)
	switch {
	case err != nil:
		slog.Error("no firewall ruleset is loaded and none would load", "err", err)
	case res.Source == engine.LoadedSaved:
		slog.Warn("no firewall ruleset was loaded; loaded the saved one")
	default:
		slog.Error("no firewall ruleset was loaded and the saved one would not load",
			"loaded", res.Source, "reason", res.Reason)
	}
}

// followRelease brings the proxy's files in line with this release whenever
// the proxy running is this release's. An update restarts the proxy into
// the new binary seconds after this daemon starts, so it looks every ten
// seconds for the first two minutes, then once a minute.
func followRelease(ctx context.Context, eng *engine.Engine, proxy *services.Proxy, log *slog.Logger) {
	start := time.Now()
	for {
		if proxy.RunsRelease(ctx, version.Version) {
			switch changed, err := eng.Follow(ctx, proxy); {
			case err != nil:
				log.Error("the reverse proxy refused this release's configuration and serves the one before; an apply tries again",
					"err", err)
			case changed:
				log.Info("the reverse proxy's configuration follows this release", "release", version.Version)
			}
		}
		wait := time.Minute
		if time.Since(start) < 2*time.Minute {
			wait = 10 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
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
			if ip, ok := splitCIDR(a); ok && isPublic(ip) {
				out = append(out, ip)
			}
		}
	}
	return out
}

func isPublic(s string) bool {
	ip, err := netip.ParseAddr(s)
	return err == nil && model.PublicAddress(ip)
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
func newUpdater(cfg server.Config, stateDir string, client *update.Client) *update.Manager {
	bin, err := install.ServiceBinary(install.DefaultLayout())
	if err != nil {
		return nil
	}
	return &update.Manager{
		Client: client,
		Installer: &update.Installer{
			Binary: bin, Unit: install.DaemonUnit, HealthURL: healthURL(cfg), Run: install.ExecRunner{},
			Proxy:      filepath.Join(filepath.Dir(bin), services.ProxyBinaryName),
			ProxyUnit:  services.ProxyUnit,
			RolledBack: filepath.Join(stateDir, update.RolledBackFile),
		},
		Current: version.Version,
		Cache:   update.NewCache(stateDir),
		Log:     slog.Default(),
	}
}

// healthURL is where the post-update probe reaches this daemon.
func healthURL(cfg server.Config) string {
	return probeURL(cfg.Listen, cfg.TLS())
}

// probeURL is the health endpoint of a daemon listening on listen.
func probeURL(listen string, tls bool) string {
	scheme := "http"
	if tls {
		scheme = "https"
	}
	// Go listens on a service's name as well as its number.
	port := model.DefaultWebPort
	if _, name, err := net.SplitHostPort(listen); err == nil {
		if n, err := net.DefaultResolver.LookupPort(context.Background(), "tcp", name); err == nil && n > 0 {
			port = n
		}
	}
	return fmt.Sprintf("%s://127.0.0.1:%d/api/v1/health", scheme, port)
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
		"chronyd":       services.NTPUnit,
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
