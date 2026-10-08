// Package cli wires the ostiole command-line interface.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"ostiole/internal/backup"
	"ostiole/internal/certs"
	"ostiole/internal/dnsblock"
	"ostiole/internal/engine"
	"ostiole/internal/feeds"
	"ostiole/internal/fetch"
	"ostiole/internal/install"
	"ostiole/internal/journald"
	"ostiole/internal/logfile"
	"ostiole/internal/logging"
	"ostiole/internal/model"
	"ostiole/internal/network"
	"ostiole/internal/nft"
	"ostiole/internal/services"
	"ostiole/internal/shaping"
	"ostiole/internal/sshd"
	"ostiole/internal/store"
	"ostiole/internal/sysctl"
	"ostiole/internal/sysupdate"
	"ostiole/internal/timezone"
	"ostiole/internal/update"
	"ostiole/internal/version"
)

// Main runs the CLI with the given arguments and returns a process exit code.
func Main(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// globals are the persistent flags shared by every command.
type globals struct {
	logLevel string
	// logLevelSet records that --log-level was given, which makes it beat
	// the level in the configuration.
	logLevelSet bool
	configDir   string
	nftBin      string
	tcBin       string
	smartctlBin string
	netBackend  string
	// packageManager names the distro package manager instead of looking
	// for one, which is how a dev run and the end-to-end tests point at
	// something that is not going to change the router they run on.
	packageManager string
	// updateAPI is where the updater asks for releases; empty is GitHub.
	updateAPI string
	// listsOnLoopback has lists read from the loopback as if it were
	// inside the network; only the end-to-end tests set it.
	listsOnLoopback bool
	// cloudflareAPI is where dynamic DNS writes Cloudflare records;
	// empty is Cloudflare's API.
	cloudflareAPI string
	// fakeProbes has a daemon that is not root watch its gateways through
	// the file named, for the end-to-end tests.
	fakeProbes string
	// backupDir is where backup crons write, backup.Dir unless a test
	// says otherwise.
	backupDir string
	// logDir is where the logs are written while the configuration says
	// so, logfile.Dir unless a test says otherwise.
	logDir string

	// blockCache is shared rather than made twice: the refresher and the
	// service backend have to agree on what was last fetched.
	blockCache *dnsblock.Cache
	// shapeBackend is shared for the same reason and one more: the engine
	// and the monitor's tick lock against each other through it.
	shapeBackend *shaping.Shaper
	// certStore is shared because a subscriber is only told about writes
	// to the instance it subscribed to: the renewer and the proxy have to
	// be looking at the same one.
	certs *certs.Store
	// tlsCert and tlsKey are the pair the web UI serves, set by serve
	// before the engine is built, so a site on the built-in certificate
	// reads the same files.
	tlsCert, tlsKey string
}

func (g *globals) certStore() *certs.Store {
	if g.certs == nil {
		g.certs = certs.NewStore(filepath.Join(g.configDir, "certs"))
	}
	return g.certs
}

func (g *globals) store() *store.Store {
	return store.New(g.configDir)
}

func (g *globals) network() (network.Backend, error) {
	switch g.netBackend {
	case "auto":
		return network.NewAuto(), nil
	case "networkd":
		return network.NewNetworkd(), nil
	case "none":
		return nil, nil
	}
	return nil, fmt.Errorf("unknown --network-backend %q (auto, networkd, or none)", g.netBackend)
}

// feedsDir is where fetched blocklists and country ranges are cached,
// beside the configuration but never part of it.
func (g *globals) feedsDir() string { return filepath.Join(g.configDir, "feeds") }

func (g *globals) feeds() *feeds.Cache { return feeds.NewCache(g.feedsDir()) }

// listGetter reads the lists a configuration names: under the router's
// own rules, or with --lists-on-loopback, from the tests' list servers.
func (g *globals) listGetter() *fetch.Getter {
	if g.listsOnLoopback {
		return fetch.Inside()
	}
	return nil
}

// updateClient asks for releases where --update-api says.
func (g *globals) updateClient() *update.Client {
	c := update.NewClient()
	c.BaseURL = g.updateAPI
	return c
}

// updatesDir is where what the router last learned about its own updates is
// kept: pending packages, the last run, whether a reboot is waiting.
func (g *globals) updatesDir() string { return filepath.Join(g.configDir, "updates") }

// blocklistDir is where the names fetched for DNS blocking are cached,
// beside the feed cache and just as disposable.
func (g *globals) blocklistDir() string { return filepath.Join(g.configDir, "dnsblock") }

func (g *globals) blocklists() *dnsblock.Cache {
	if g.blockCache == nil {
		g.blockCache = dnsblock.NewCache(g.blocklistDir())
	}
	return g.blockCache
}

// loggingApplier caps what each daemon writes. Ostiole's own level
// follows only in the daemon (ownLevel), and only when nobody named one
// on the command line: a --log-level is a deliberate act and beats the
// configuration.
func (g *globals) loggingApplier(ownLevel bool) logging.System {
	sys := logging.System{Run: install.ExecRunner{}}
	if ownLevel && !g.logLevelSet {
		sys.Slog = LogLevel
	}
	return sys
}

// shaper is the traffic shaping backend. There is one per process: the
// engine applies through it and the gateway tick reconciles through it,
// and they hold the same lock so neither catches the other halfway.
func (g *globals) shaper() *shaping.Shaper {
	if g.shapeBackend == nil {
		pm := g.packageManager
		if pm == "" {
			pm = install.PackageManager()
		}
		g.shapeBackend = shaping.New(g.configDir, g.tcBin, pm, slog.Default())
	}
	return g.shapeBackend
}

func (g *globals) engine() (*engine.Engine, error) { return g.engineWith(false) }

// engineWith builds the engine. ownLevel says the process should follow
// the configured log level too, which only the daemon wants: a console
// command that printed less than --log-level asked for would be
// surprising.
func (g *globals) engineWith(ownLevel bool) (*engine.Engine, error) {
	net, err := g.network()
	if err != nil {
		return nil, err
	}
	eng := engine.New(g.store(), &nft.Exec{Bin: g.nftBin}, net, slog.Default())
	eng.WithFeeds(g.feeds())
	if port := listenPortOf(installedListen()); port != 0 {
		eng.WithDefaultPorts(port, 22)
	}
	if os.Geteuid() == 0 {
		eng.WithSysctl(sysctl.Proc{})
		eng.WithTimezone(timezone.System{})
		eng.WithJournal(journald.System{Run: install.ExecRunner{}})
		eng.WithLogging(g.loggingApplier(ownLevel))
		eng.WithSSH(sshd.System{})
		// Shaping needs root but not a managed network: a router whose
		// interfaces are set up by something else can still have its
		// queues held here.
		eng.WithShaping(g.shaper())
		if net != nil {
			// Services need root and a managed router; dev runs stay firewall-only.
			bundle := services.NewBundle(g.blocklists(), g.configDir, g.certStore())
			if g.tlsCert != "" && g.tlsKey != "" {
				bundle.SetSelfCertificate(g.tlsCert, g.tlsKey)
			}
			eng.WithServices(bundle)
		}
	}
	return eng, nil
}

func newRootCmd() *cobra.Command {
	g := &globals{}
	cmd := &cobra.Command{
		Use:           "ostiole",
		Short:         "Firewall and router appliance manager for Linux nftables",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(c *cobra.Command, _ []string) error {
			g.logLevelSet = c.Flags().Changed("log-level")
			// Arch has no security channel, so its default is one it can run.
			if d, err := sysupdate.Detect(g.packageManager); err == nil && !d.SecurityCapable() {
				model.DefaultSystemMode = model.UpdateManual
			}
			return configureLogging(g.logLevel)
		},
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&g.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	pf.StringVar(&g.configDir, "config-dir", store.DefaultDir, "configuration directory")
	pf.StringVar(&g.nftBin, "nft", "nft", "path to the nft binary")
	pf.StringVar(&g.tcBin, "tc", "tc", "path to the tc binary, which traffic shaping drives")
	pf.StringVar(&g.smartctlBin, "smartctl", "smartctl", "path to the smartctl binary, which the drives page and the health poll read through")
	pf.StringVar(&g.netBackend, "network-backend", "auto", "network backend: auto (networkd when it is running), networkd, or none")
	pf.StringVar(&g.packageManager, "package-manager", "", "package manager to drive for system updates (dnf, apt-get, pacman); empty detects one")
	// The end-to-end tests point it at a stand-in for GitHub. A release
	// from anywhere still has to verify against the embedded keys.
	pf.StringVar(&g.updateAPI, "update-api", "", "API the updater asks for releases; empty is GitHub's")
	_ = pf.MarkHidden("update-api")
	// And at a stand-in for Cloudflare, so a record is written somewhere
	// that is not a real zone.
	pf.StringVar(&g.cloudflareAPI, "cloudflare-api", "", "API dynamic DNS writes Cloudflare records through; empty is Cloudflare's")
	_ = pf.MarkHidden("cloudflare-api")
	// And at a directory it may write, since it is not root.
	pf.StringVar(&g.backupDir, "backup-dir", backup.Dir, "directory backup crons write to")
	_ = pf.MarkHidden("backup-dir")
	pf.StringVar(&g.logDir, "log-dir", logfile.Dir, "directory the log files go in")
	_ = pf.MarkHidden("log-dir")
	// And at the lists its specs serve on the loopback, which the router
	// otherwise never reads from.
	pf.BoolVar(&g.listsOnLoopback, "lists-on-loopback", false, "read lists served on the loopback as if from inside the network")
	_ = pf.MarkHidden("lists-on-loopback")
	pf.StringVar(&g.fakeProbes, "fake-probes", "", "watch the gateways through this file's next hops and answers when not root")
	_ = pf.MarkHidden("fake-probes")
	cmd.AddCommand(
		newInterfacesCmd(g),
		newServeCmd(g),
		newInitCmd(g),
		newCheckCmd(g),
		newRenderCmd(g),
		newApplyCmd(g),
		newLoadCmd(g),
		newStatusCmd(g),
		newRevisionsCmd(g),
		newCountersCmd(g),
		newGatewaysCmd(g),
		newPolicyCmd(g),
		newShapingCmd(g),
		newAliasesCmd(g),
		newDNSBlockCmd(g),
		newCronsCmd(g),
		newBackupCmd(g),
		newRestoreCmd(g),
		newDiffCmd(g),
		newResetPasswordCmd(g),
		newUsersCmd(g),
		newTokensCmd(g),
		newInstallCmd(g),
		newRepairCmd(g),
		newTakeoverCmd(g),
		newUninstallCmd(g),
		newUpdateCmd(g),
		newServicesCmd(g),
		newHostCmd(g),
		newOpenAPICmd(),
		newVersionCmd(),
	)
	return cmd
}

// LogLevel is the level Ostiole's own logger writes at. It is a variable
// the handler reads on every record, so an apply can move it without
// rebuilding the logger.
var LogLevel = new(slog.LevelVar)

func configureLogging(level string) error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return fmt.Errorf("invalid --log-level %q: %w", level, err)
	}
	LogLevel.Set(lvl)
	opts := &slog.HandlerOptions{Level: LogLevel}
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if journald.Stream(os.Stderr) {
		// Each line carries its priority, so the journal tells a warning
		// from the rest.
		handler = logging.Journal(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(logging.Handler(handler)))
	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version.String())
		},
	}
}
