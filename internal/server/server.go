// Package server hosts the HTTP API and the embedded web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/version"
	"github.com/rforced/ostiole/internal/web"
)

// Config controls the HTTP server.
type Config struct {
	// Listen is the host:port to bind.
	Listen string
	// TLSCert and TLSKey enable HTTPS when both are set.
	TLSCert, TLSKey string
}

// TLS reports whether the server will speak HTTPS.
func (c Config) TLS() bool { return c.TLSCert != "" && c.TLSKey != "" }

// Deps are the services the API exposes. Both are required for the
// engine routes; without Auth every protected route answers 503.
type Deps struct {
	Engine *engine.Engine
	Auth   *auth.Service
}

// Handler builds the full HTTP handler: API routes plus the SPA.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", handleHealth)
	api := &api{engine: d.Engine, auth: d.Auth}
	api.register(mux)
	mux.HandleFunc("/api/", handleAPINotFound)
	mux.Handle("/", web.Handler())
	return securityHeaders(requestLog(csrfGuard(mux)))
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func Run(ctx context.Context, cfg Config, d Deps, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           Handler(d),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "tls", cfg.TLS(), "version", version.Version)
		if cfg.TLS() {
			errCh <- srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Version: version.Version,
		Commit:  version.Commit,
	})
}

func handleAPINotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write json response", "err", err)
	}
}
