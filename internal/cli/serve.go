package cli

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/server"
)

func newServeCmd(g *globals) *cobra.Command {
	cfg := server.Config{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web UI and API server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			eng, err := g.engine()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return server.Run(ctx, cfg, server.Deps{Engine: eng}, slog.Default())
		},
	}
	cmd.Flags().StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "address to listen on")
	return cmd
}
