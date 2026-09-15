// Package cli wires the ostiole command-line interface.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysctl"
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
	netBackend string
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

func (g *globals) engine() (*engine.Engine, error) {
	net, err := g.network()
	if err != nil {
		return nil, err
	}
	eng := engine.New(g.store(), &nft.Exec{Bin: g.nftBin}, net, slog.Default())
	if os.Geteuid() == 0 {
		eng.WithSysctl(sysctl.Proc{})
		if net != nil {
			// Services need root and a managed box; dev runs stay firewall-only.
			eng.WithServices(services.New())
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
	pf.StringVar(&g.netBackend, "network-backend", "auto", "network backend: auto (networkd when it is running), networkd, or none")
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
		newResetPasswordCmd(g),
		newUsersCmd(g),
		newInstallCmd(g),
		newTakeoverCmd(g),
		newUninstallCmd(g),
		newUpdateCmd(g),
		newServicesCmd(g),
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
