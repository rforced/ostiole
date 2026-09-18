// Package cli wires the ostiole command-line interface.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/journald"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/shaping"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
	"github.com/rforced/ostiole/internal/timezone"
	"github.com/rforced/ostiole/internal/version"
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
	logLevel   string
	configDir  string
	nftBin     string
	tcBin      string
	netBackend string
	// packageManager names the distro package manager instead of looking
	// for one, which is how a dev run and the end-to-end tests point at
	// something that is not going to change the router they run on.
	packageManager string

	// blockCache is shared rather than made twice: the refresher and the
	// service backend have to agree on what was last fetched.
	blockCache *dnsblock.Cache
	// shapeBackend is shared for the same reason and one more: the engine
	// and the monitor's tick lock against each other through it.
	shapeBackend *shaping.Shaper
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

func (g *globals) engine() (*engine.Engine, error) {
	net, err := g.network()
	if err != nil {
		return nil, err
	}
	eng := engine.New(g.store(), &nft.Exec{Bin: g.nftBin}, net, slog.Default())
	eng.WithFeeds(g.feeds())
	if os.Geteuid() == 0 {
		eng.WithSysctl(sysctl.Proc{})
		eng.WithTimezone(timezone.System{})
		eng.WithJournal(journald.System{Run: install.ExecRunner{}})
		// Shaping needs root but not a managed network: a router whose
		// interfaces are set up by something else can still have its
		// queues held here.
		eng.WithShaping(g.shaper())
		if net != nil {
			// Services need root and a managed router; dev runs stay firewall-only.
			eng.WithServices(services.NewBundle(g.blocklists()))
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
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			return configureLogging(g.logLevel)
		},
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&g.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	pf.StringVar(&g.configDir, "config-dir", store.DefaultDir, "configuration directory")
	pf.StringVar(&g.nftBin, "nft", "nft", "path to the nft binary")
	pf.StringVar(&g.tcBin, "tc", "tc", "path to the tc binary, which traffic shaping drives")
	pf.StringVar(&g.netBackend, "network-backend", "auto", "network backend: auto (networkd when it is running), networkd, or none")
	pf.StringVar(&g.packageManager, "package-manager", "", "package manager to drive for system updates (dnf, apt-get, zypper, pacman, apk); empty detects one")
	cmd.AddCommand(
		newInterfacesCmd(),
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

func configureLogging(level string) error {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return fmt.Errorf("invalid --log-level %q: %w", level, err)
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	slog.SetDefault(slog.New(handler))
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
