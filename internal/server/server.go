// Package server hosts the HTTP API and the embedded web UI.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/sysstat"
	"github.com/rforced/ostiole/internal/sysupdate"
	"github.com/rforced/ostiole/internal/update"
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
	Engine  *engine.Engine
	Auth    *auth.Service
	Updater *update.Manager // optional; nil disables the update endpoints
	// Packages drives the distro package manager for system updates; nil
	// hides those endpoints.
	Packages *sysupdate.Manager
	// Services reads dnsmasq state and leases; nil reports "not set up".
	Services *services.Dnsmasq
	// Resolver reads unbound's state; nil reports "not set up".
	Resolver *services.Unbound
	// PPPoE reports whether a dialled session can be run here; nil reports
	// "not set up".
	PPPoE *services.PPPoE
	// Certs manages the certificate the UI serves; nil hides the
	// certificate endpoints and serves whatever the files hold.
	Certs *certs.Manager
	// Tokens authenticates API tokens; nil leaves the API to sessions.
	Tokens *auth.Tokens
	// Feeds refreshes aliases fetched from a URL or a country list; nil
	// leaves them to whatever is cached.
	Feeds *feeds.Refresher
	// Blocklists refreshes the DNS blocklists; nil leaves them to
	// whatever is cached.
	Blocklists *dnsblock.Refresher
	// Crons reports and runs the scheduled jobs; nil reports none.
	Crons CronRunner
	// CertHosts lists the names a regenerated self-signed certificate
	// should cover.
	CertHosts func() []string
	// Log is the firewall log ring; nil disables the log endpoints.
	Log *fwlog.Ring
	// Gateways reports multi-WAN health; nil means nothing is watching.
	Gateways GatewayStatuser
	// Tables lists the nftables tables on the box for the dashboard's
	// foreign-ruleset warning; nil skips that check.
	Tables TableLister
	// Units answers systemd state queries (service health, competitor
	// detection) for the dashboard; nil leaves those states unknown.
	Units install.Systemctl
	// SysStat samples CPU, memory and disk for the dashboard; nil hides
	// that endpoint.
	SysStat *sysstat.Sampler
}

// Handler builds the full HTTP handler: API routes plus the SPA.
func Handler(d Deps) http.Handler {
	mux := &router{mux: http.NewServeMux()}
	mux.HandleFunc("GET /api/v1/health", handleHealth)
	api := &api{
		engine:     d.Engine,
		auth:       d.Auth,
		updater:    d.Updater,
		packages:   d.Packages,
		services:   d.Services,
		resolver:   d.Resolver,
		pppoe:      d.PPPoE,
		certs:      d.Certs,
		tokens:     d.Tokens,
		feedCache:  feedCacheOf(d.Feeds),
		feeds:      feedRefresherOf(d.Feeds),
		blockCache: blockCacheOf(d.Blocklists),
		blocklists: blockRefresherOf(d.Blocklists),
		crons:      d.Crons,
		certHosts:  d.CertHosts,
		fwlog:      d.Log,
		gateways:   d.Gateways,
		tables:     d.Tables,
		units:      d.Units,
		sysstat:    d.SysStat,
	}
	api.routes = mux
	api.register(mux)
	mux.HandleFunc("/api/", handleAPINotFound)
	mux.Handle("/", web.Handler())
	return securityHeaders(requestLog(csrfGuard(mux.mux)))
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

	// A certificate manager serves the certificate through a callback, so
	// replacing it takes effect on the next connection rather than the
	// next restart.
	if cfg.TLS() && d.Certs != nil {
		srv.TLSConfig = &tls.Config{GetCertificate: d.Certs.GetCertificate, MinVersion: tls.VersionTLS12}
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "tls", cfg.TLS(), "version", version.Version)
		if cfg.TLS() {
			if srv.TLSConfig != nil {
				errCh <- srv.ListenAndServeTLS("", "")
				return
			}
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

// feedCacheOf and feedRefresherOf keep a nil *feeds.Refresher from
// becoming a non-nil interface, which would make the handlers think
// something is refreshing when nothing is.
func feedCacheOf(r *feeds.Refresher) *feeds.Cache {
	if r == nil {
		return nil
	}
	return r.Cache
}

func feedRefresherOf(r *feeds.Refresher) FeedRefresher {
	if r == nil {
		return nil
	}
	return r
}

func blockCacheOf(r *dnsblock.Refresher) *dnsblock.Cache {
	if r == nil {
		return nil
	}
	return r.Cache
}

func blockRefresherOf(r *dnsblock.Refresher) BlocklistRefresher {
	if r == nil {
		return nil
	}
	return r
}
