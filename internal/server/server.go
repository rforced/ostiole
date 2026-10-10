// Package server hosts the HTTP API and the embedded web UI.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"ostiole/internal/acme"
	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/backup"
	"ostiole/internal/certs"
	"ostiole/internal/chrony"
	"ostiole/internal/ddns"
	"ostiole/internal/dhcplog"
	"ostiole/internal/diag"
	"ostiole/internal/discoverylog"
	"ostiole/internal/dnsblock"
	"ostiole/internal/dnslog"
	"ostiole/internal/engine"
	"ostiole/internal/feeds"
	"ostiole/internal/fwlog"
	"ostiole/internal/gateway"
	"ostiole/internal/host"
	"ostiole/internal/install"
	"ostiole/internal/logfile"
	"ostiole/internal/model"
	"ostiole/internal/modem"
	"ostiole/internal/notify"
	"ostiole/internal/panics"
	"ostiole/internal/peerlog"
	"ostiole/internal/requestlog"
	"ostiole/internal/services"
	"ostiole/internal/smart"
	"ostiole/internal/sysstat"
	"ostiole/internal/sysupdate"
	"ostiole/internal/tailscale"
	"ostiole/internal/traffic"
	"ostiole/internal/update"
	"ostiole/internal/version"
	"ostiole/internal/waflog"
	"ostiole/internal/web"
	"ostiole/internal/wg"
	"ostiole/internal/wirelesslog"
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
	// UPnP reads miniupnpd's state and the mappings clients hold; nil
	// reports "not set up".
	UPnP *services.UPnP
	// Tailscale reads tailscaled's state; nil reports "not set up".
	Tailscale *services.Tailscale
	// TSClient drives the tailscale command line; nil leaves the status
	// empty and refuses a login.
	TSClient *tailscale.Client
	// Wireless reads the radios and their clients; nil reports "not set
	// up".
	Wireless *services.Wireless
	// WireGuard reads a tunnel's device; nil asks the kernel.
	WireGuard wg.Device
	// Proxy reads the reverse proxy's state and its upstreams; nil
	// reports "not set up".
	Proxy *services.Proxy
	// NTP reads the time service's unit; nil reports "not set up".
	NTP *services.NTP
	// Chrony reads what the time service is doing; nil reads nothing.
	Chrony *chrony.Client
	// Journal reads the system journal; nil runs journalctl, which is
	// what a router does and a test does not.
	Journal func(context.Context, diag.JournalOptions) ([]diag.JournalEntry, error)
	// Wake sends a Wake on LAN packet; nil sends one for real.
	Wake func(iface string, mac net.HardwareAddr) error
	// Certs manages the certificate the UI serves; nil hides the
	// certificate endpoints and serves whatever the files hold.
	Certs *certs.Manager
	// CertStore holds the certificates this router issued or was given;
	// nil answers 503 on the file routes.
	CertStore *certs.Store
	// Renewer orders certificates; nil reports none and refuses to issue.
	Renewer *acme.Renewer
	// DDNS keeps the dynamic DNS records; nil leaves them unkept.
	DDNS *ddns.Updater
	// Tokens authenticates API tokens; nil leaves the API to sessions.
	Tokens *auth.Tokens
	// Feeds refreshes aliases fetched from a URL or a country list; nil
	// leaves them to whatever is cached.
	Feeds *feeds.Refresher
	// Blocklists refreshes the DNS blocklists; nil leaves them to
	// whatever is cached.
	Blocklists *dnsblock.Refresher
	// Crons reports and runs the scheduled work; nil reports none.
	Crons CronRunner
	// Bucket reaches the bucket the remote backups go in; nil builds the
	// client the configuration describes, which is what a router does.
	Bucket func(r model.RemoteBackup, hostname string) (*backup.Remote, error)
	// CertHosts lists the names a regenerated self-signed certificate
	// should cover.
	CertHosts func() []string
	// Log is the firewall log ring; nil disables the log endpoints.
	Log *fwlog.Ring
	// Keepalive is how often an open log stream says it is still there
	// and checks its caller may still read it; zero is 15 seconds.
	Keepalive time.Duration
	// QueryLog keeps what the DNS server answered; nil hides the query
	// log, which is what a daemon that is not root can offer.
	QueryLog *dnslog.Log
	// WAFLog keeps what the proxy's WAF matched; nil hides the events.
	WAFLog *waflog.Log
	// Requests keeps what the proxy answered; nil hides the requests.
	Requests *requestlog.Log
	// DHCPLog keeps what the DHCP server says of its clients; nil hides
	// the log.
	DHCPLog *dhcplog.Log
	// WirelessLog keeps the wireless clients' coming and going; nil hides
	// the log.
	WirelessLog *wirelesslog.Log
	// Discovery is the relay that carries mDNS and SSDP between networks;
	// nil hides its status and announcements.
	Discovery DiscoveryRelay
	// DiscoveryLog keeps the packets the relay saw; nil hides the log.
	DiscoveryLog *discoverylog.Log
	// WireGuardLog and TailscaleLog keep the VPN peers' coming and going;
	// nil hides them.
	WireGuardLog, TailscaleLog *peerlog.Log
	// LogFiles keeps the logs in files while the configuration says so;
	// nil reports the files off.
	LogFiles *logfile.Writer
	// ReadBackSkipped says this start left the logs in their files because
	// the last one did not finish reading them back.
	ReadBackSkipped bool
	// Traffic counts what crosses the router; nil answers 503.
	Traffic *traffic.Counter
	// Modems reads a cable modem's status pages; nil reads them for real.
	Modems *modem.Cache
	// Dial is how the DNS over TLS upstreams are probed; nil dials for real.
	Dial services.Dialer
	// Gateways reports multi-WAN health; nil means nothing is watching.
	Gateways GatewayStatuser
	// GatewayHistory keeps what the gateways' probes found and what
	// changed; nil answers 503.
	GatewayHistory *gateway.History
	// Tables lists the nftables tables on the router for the dashboard's
	// foreign-ruleset warning; nil skips that check.
	Tables TableLister
	// Units answers systemd state queries (service health, competitor
	// detection) for the dashboard; nil leaves those states unknown.
	Units install.Systemctl
	// SysStat samples CPU, memory and disk for the dashboard; nil hides
	// that endpoint.
	SysStat *sysstat.Sampler
	// Drives reads what each drive says about itself; nil leaves the
	// drives page saying the tool is missing.
	Drives *smart.Client
	// DriveHealth is the hourly verdict poll; nil adds no warning.
	DriveHealth *smart.Monitor
	// DriveHistory keeps what each hourly check read; nil hides it.
	DriveHistory *smart.History
	// Shaping reports the live traffic queues; nil leaves the page with
	// what is configured and no figures.
	Shaping Shaper
	// Host reports the operating system underneath Ostiole and clears what
	// an older firewall left in the kernel. Only the fields that are not
	// already somewhere in Deps need setting; Handler fills in the rest.
	Host host.Deps
	// Notify sends notices off the router; nil sends none, and Run then
	// watches for nothing to send.
	Notify *notify.Notifier
	// Audit keeps who did what; nil keeps nothing and answers 503 for the
	// list. The engine records into the same log.
	Audit *audit.Log
}

// Handler builds the full HTTP handler: API routes plus the SPA.
func Handler(d Deps) http.Handler {
	h, _ := build(d)
	return h
}

// build makes the handler and the API behind it, whose watchers Run starts.
func build(d Deps) (http.Handler, *api) {
	mux := &router{mux: http.NewServeMux()}
	api := &api{
		engine:          d.Engine,
		auth:            d.Auth,
		updater:         d.Updater,
		packages:        d.Packages,
		services:        d.Services,
		resolver:        d.Resolver,
		pppoe:           d.PPPoE,
		upnp:            d.UPnP,
		tailscale:       d.Tailscale,
		tsClient:        d.TSClient,
		wireless:        d.Wireless,
		wgDevice:        d.WireGuard,
		proxy:           d.Proxy,
		ntp:             d.NTP,
		chrony:          d.Chrony,
		journal:         d.Journal,
		wake:            d.Wake,
		certs:           d.Certs,
		certStore:       d.CertStore,
		renewer:         d.Renewer,
		ddns:            d.DDNS,
		tokens:          d.Tokens,
		feedCache:       feedCacheOf(d.Feeds),
		feeds:           feedRefresherOf(d.Feeds),
		blockCache:      blockCacheOf(d.Blocklists),
		blocklists:      blockRefresherOf(d.Blocklists),
		crons:           d.Crons,
		bucket:          d.Bucket,
		certHosts:       d.CertHosts,
		modems:          modemsOf(d.Modems),
		dial:            d.Dial,
		fwlog:           d.Log,
		keepalive:       d.Keepalive,
		querylog:        d.QueryLog,
		waflog:          d.WAFLog,
		requests:        d.Requests,
		dhcplog:         d.DHCPLog,
		wirelesslog:     d.WirelessLog,
		discovery:       d.Discovery,
		discoverylog:    d.DiscoveryLog,
		wireguardLog:    d.WireGuardLog,
		tailscaleLog:    d.TailscaleLog,
		logFiles:        d.LogFiles,
		readBackSkipped: d.ReadBackSkipped,
		traffic:         d.Traffic,
		gateways:        d.Gateways,
		shaping:         d.Shaping,
		tables:          d.Tables,
		units:           d.Units,
		sysstat:         d.SysStat,
		drives:          d.Drives,
		driveHealth:     d.DriveHealth,
		driveHistory:    d.DriveHistory,
		host:            hostDeps(d),
		notifier:        d.Notify,
		audit:           d.Audit,
	}
	api.gatewayHistory = d.GatewayHistory
	if d.Engine != nil {
		api.tracker = &notify.Tracker{State: d.Engine.Store(), Log: slog.Default()}
	}
	if api.keepalive <= 0 {
		api.keepalive = 15 * time.Second
	}
	api.stopping = make(chan struct{})
	api.routes = mux
	mux.HandleFunc("GET /api/v1/health", api.health)
	api.register(mux)
	mux.HandleFunc("/api/", mux.notFound)
	mux.Handle("/", web.Handler())
	return securityHeaders(requestLog(csrfGuard(mux.mux))), api
}

// hostDeps completes the host dependencies from the ones the API already
// has. The caller says which router this is — root, the configuration
// directory, the network backend — and everything else is the same engine
// and systemd the rest of the API talks to, so there is nothing to be
// gained by making the caller repeat it.
func hostDeps(d Deps) host.Deps {
	h := d.Host
	if h.Units == nil {
		h.Units = d.Units
	}
	if h.Kernel == nil {
		// The same nft runner the dashboard lists tables with, when it is
		// one that can also delete them.
		if k, ok := d.Tables.(host.Kernel); ok {
			h.Kernel = k
		}
	}
	if d.Engine != nil {
		if h.TableLoaded == nil {
			h.TableLoaded = func(ctx context.Context) bool {
				st, err := d.Engine.Status(ctx)
				return err == nil && st.TableLoaded
			}
		}
	}
	return h
}

// Run serves until ctx is cancelled, then shuts down gracefully. With a
// notifier it also watches for what to notify.
func Run(ctx context.Context, cfg Config, d Deps, logger *slog.Logger) error {
	handler, api := build(d)
	if api.notifier != nil && api.engine != nil {
		go panics.Loop(ctx, logger, "notification watcher", api.watchConditions)
		go panics.Loop(ctx, logger, "waf count", api.watchWAF)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          log.New(serverLog{logger}, "", 0),
	}
	srv.RegisterOnShutdown(api.endStreams)

	// A certificate manager serves the certificate through a callback, so
	// replacing it takes effect on the next connection rather than the
	// next restart.
	if cfg.TLS() && d.Certs != nil {
		srv.TLSConfig = &tls.Config{GetCertificate: d.Certs.GetCertificate, MinVersion: tls.VersionTLS12}
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "tls", cfg.TLS(), "version", version.Version)
		if !cfg.TLS() {
			errCh <- srv.ListenAndServe()
			return
		}
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", cfg.Listen)
		if err != nil {
			errCh <- err
			return
		}
		defer ln.Close()
		ln = redirectListener{ln}
		if srv.TLSConfig != nil {
			errCh <- srv.ServeTLS(ln, "", "")
			return
		}
		errCh <- srv.ServeTLS(ln, cfg.TLSCert, cfg.TLSKey)
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
	Version string `json:"version,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// health answers "is this daemon up" to anybody and which build it is
// only to somebody entitled to know: the updater's probe on the loopback,
// a signed-in operator, or a token. A version number is what tells a
// scanner which bugs to try.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	res := healthResponse{Status: "ok"}
	if a.knowsThisRouter(r) {
		res.Version, res.Commit = version.Version, version.Commit
	}
	writeJSON(w, http.StatusOK, res)
}

// knowsThisRouter reports whether the caller is already inside: on the
// loopback, holding a session, or holding a token. A token limited to
// fetching a certificate is not inside; it learns nothing else.
func (a *api) knowsThisRouter(r *http.Request) bool {
	if loopbackPeer(r.RemoteAddr) {
		return true
	}
	p, ok := a.authenticate(r)
	return ok && len(p.Certificates) == 0
}

// loopbackPeer reports a request that came from this router itself.
func loopbackPeer(remote string) bool {
	h, _, err := net.SplitHostPort(remote)
	if err != nil {
		h = remote
	}
	addr, err := netip.ParseAddr(h)
	return err == nil && addr.IsLoopback()
}

// notFound answers every unknown API path in JSON, and a known path asked
// with the wrong method as 405 with the methods that would have worked:
// the catch-all shadows the mux's own 405, which was plain text anyway.
func (r *router) notFound(w http.ResponseWriter, req *http.Request) {
	if allowed := r.allowed(req.URL.Path); len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
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

func modemsOf(c *modem.Cache) *modem.Cache {
	if c != nil {
		return c
	}
	return modem.NewCache(time.Minute)
}
