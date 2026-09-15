package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/server"
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
			if useTLS {
				if cfg.TLSCert == "" {
					cfg.TLSCert = filepath.Join(g.configDir, "tls", "cert.pem")
				}
				if cfg.TLSKey == "" {
					cfg.TLSKey = filepath.Join(g.configDir, "tls", "key.pem")
				}
				created, err := server.EnsureCert(cfg.TLSCert, cfg.TLSKey, certHosts())
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
			as, err := auth.NewService(g.configDir)
			if err != nil {
				return err
			}
			if as.NeedsSetup() {
				slog.Warn("no admin account yet; open the web UI to create one or run `ostiole reset-password`")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return server.Run(ctx, cfg, server.Deps{Engine: eng, Auth: as}, slog.Default())
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
