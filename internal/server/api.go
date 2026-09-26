package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/acme"
	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/diag"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/dnslog"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/modem"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/notify"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/smart"
	"github.com/rforced/ostiole/internal/sshd"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysstat"
	"github.com/rforced/ostiole/internal/sysupdate"
	"github.com/rforced/ostiole/internal/tailscale"
	"github.com/rforced/ostiole/internal/timezone"
	"github.com/rforced/ostiole/internal/update"
	"github.com/rforced/ostiole/internal/wg"
)

const maxBodyBytes = 1 << 20

type api struct {
	engine    *engine.Engine
	auth      *auth.Service
	updater   *update.Manager
	packages  *sysupdate.Manager
	services  *services.Dnsmasq
	resolver  *services.Unbound
	pppoe     *services.PPPoE
	upnp      *services.UPnP
	tailscale *services.Tailscale
	tsClient  *tailscale.Client
	wireless  *services.Wireless
	proxy     *services.Proxy
	ntp       *services.NTP
	chrony    *chrony.Client
	// journal reads the system journal; nil uses diag.Journal, which is
	// what a router does and a test does not.
	journal func(context.Context, diag.JournalOptions) ([]diag.JournalEntry, error)
	// wake sends a magic packet; nil uses wol.Send.
	wake  func(iface string, mac net.HardwareAddr) error
	certs *certs.Manager
	// certStore holds the certificates this router issued or was given;
	// certRenewer orders them.
	certStore *certs.Store
	renewer   *acme.Renewer
	tokens    *auth.Tokens
	feeds     FeedRefresher
	feedCache *feeds.Cache
	// blocklists refreshes the DNS lists; blockCache reads what they last
	// gave us.
	blocklists BlocklistRefresher
	blockCache *dnsblock.Cache
	crons      CronRunner
	// bucket reaches the bucket the remote backups go in; nil builds the
	// client from the configuration, which is what a router does.
	bucket func(r model.RemoteBackup, hostname string) (*backup.Remote, error)
	// routes is the router the handlers were registered on, which the
	// OpenAPI description is generated from.
	routes *router
	// certHosts lists the names a regenerated certificate should cover.
	certHosts func() []string
	// modems keeps the last status read from a cable modem for a minute.
	modems *modem.Cache
	// dial probes the DNS over TLS upstreams; dot remembers the answer
	// for a minute so the status strip is not a port scan.
	dial services.Dialer
	dot  struct {
		mu  sync.Mutex
		key string
		at  time.Time
		bad []string
	}
	fwlog *fwlog.Ring
	// keepalive paces the log streams' keepalives and their checks.
	keepalive time.Duration
	// querylog keeps what the DNS server answered; nil answers 503.
	querylog *dnslog.Log
	// devices caches the lease-to-name mapping the query log puts on its
	// rows, for a few seconds at a time.
	devices namers
	tables  TableLister
	units   install.Systemctl
	// host is how the operating system underneath is reported on and prepared.
	host     host.Deps
	gateways GatewayStatuser
	// shaping reports the live traffic queues; nil hides the live figures
	// and leaves the page showing what is configured.
	shaping Shaper
	// sysstat samples CPU, memory and disk for the dashboard; nil hides
	// the endpoint.
	sysstat *sysstat.Sampler
	// drives reads what each drive says about itself; driveHealth is the
	// hourly verdict the dashboard warns from.
	drives      *smart.Client
	driveHealth *smart.Monitor
	// notifier sends notices, and tracker remembers which conditions it
	// sent them for; a nil notifier sends nothing.
	notifier *notify.Notifier
	tracker  *notify.Tracker
	// watchEvery replaces the minute between looks in tests.
	watchEvery time.Duration
}

// GatewayStatuser reports what the gateway monitor knows.
type GatewayStatuser interface {
	Statuses() []gateway.Status
}

func (a *api) register(mux *router) {
	a.registerAuth(mux)
	a.registerLog(mux)
	a.registerDiag(mux)
	a.registerDrives(mux)
	a.registerNTP(mux)
	a.registerWoL(mux)
	a.registerBackup(mux)
	a.registerCerts(mux)
	a.registerTokens(mux)
	a.registerUsers(mux)
	a.registerFeeds(mux)
	a.registerBlocking(mux)
	a.registerQueryLog(mux)
	a.registerCrons(mux)
	a.registerSysUpdate(mux)
	a.registerHost(mux)
	a.registerNotify(mux)
	a.registerMetrics(mux)
	a.registerOpenAPI(mux)
	mux.HandleFunc("GET /api/v1/status", a.read(a.status))
	mux.HandleFunc("GET /api/v1/system/stats", a.readNoEngine(a.systemStats))
	mux.HandleFunc("GET /api/v1/system/timezones", a.readNoEngine(a.timezones))
	mux.HandleFunc("GET /api/v1/overview", a.read(a.overview))
	mux.HandleFunc("GET /api/v1/config", a.read(a.getConfig))
	mux.HandleFunc("GET /api/v1/config/revisions", a.read(a.revisions))
	mux.HandleFunc("GET /api/v1/config/revisions/{id}", a.read(a.revision))
	mux.HandleFunc("GET /api/v1/ruleset", a.read(a.ruleset))
	mux.HandleFunc("GET /api/v1/counters", a.read(a.counters))
	mux.HandleFunc("POST /api/v1/rules/system", a.read(a.systemRules))
	mux.HandleFunc("POST /api/v1/nat/system", a.read(a.systemNAT))
	mux.HandleFunc("POST /api/v1/dns/system-hosts", a.readNoEngine(a.systemHosts))
	mux.HandleFunc("DELETE /api/v1/dns/cache", a.write(a.clearDNSCache))
	mux.HandleFunc("GET /api/v1/interfaces/live", a.readNoEngine(a.liveInterfaces))
	mux.HandleFunc("POST /api/v1/interfaces/{name}/renew", a.write(a.renewLease))
	mux.HandleFunc("POST /api/v1/config/starter", a.write(a.starter))
	mux.HandleFunc("POST /api/v1/check", a.write(a.check))
	mux.HandleFunc("POST /api/v1/apply", a.write(a.apply))
	mux.HandleFunc("POST /api/v1/apply/confirm", a.write(a.confirm))
	mux.HandleFunc("POST /api/v1/apply/revert", a.write(a.revert))
	mux.HandleFunc("GET /api/v1/services/status", a.readNoEngine(a.servicesStatus))
	mux.HandleFunc("GET /api/v1/dhcp/leases", a.readNoEngine(a.dhcpLeases))
	mux.HandleFunc("GET /api/v1/upnp/mappings", a.readNoEngine(a.upnpMappings))
	a.registerTailscale(mux)
	a.registerProxy(mux)
	a.registerWireless(mux)
	mux.HandleFunc("POST /api/v1/wireguard/keys", a.write(a.wireguardKeys))
	mux.HandleFunc("GET /api/v1/gateways", a.readNoEngine(a.gatewayStatus))
	mux.HandleFunc("GET /api/v1/gateways/detected", a.read(a.detectedGateways))
	mux.HandleFunc("GET /api/v1/policy", a.readNoEngine(a.policyStatus))
	mux.HandleFunc("GET /api/v1/shaping", a.read(a.shapingStatus))
	mux.HandleFunc("GET /api/v1/update/check", a.admin(a.updateCheck))
	mux.HandleFunc("GET /api/v1/update/status", a.readNoEngine(a.updateStatus))
	mux.HandleFunc("POST /api/v1/update/apply", a.admin(a.updateApply))
}

type servicesStatus struct {
	SetUp   bool `json:"setUp"`
	Running bool `json:"running"`
	Leases  int  `json:"leases"`
	// Resolver is unbound, which only exists once it has been set up.
	ResolverSetUp   bool `json:"resolverSetUp"`
	ResolverRunning bool `json:"resolverRunning"`
	// PPPoE reports whether pppd and its unit are in place, which a
	// dialled line needs before it can be applied.
	PPPoESetUp bool `json:"pppoeSetUp"`
	// UPnP is miniupnpd, which answers the mapping protocols.
	UPnPSetUp   bool `json:"upnpSetUp"`
	UPnPRunning bool `json:"upnpRunning"`
	Mappings    int  `json:"mappings"`
	// Tailscale is tailscaled, which joins the tailnet.
	TailscaleSetUp   bool `json:"tailscaleSetUp"`
	TailscaleRunning bool `json:"tailscaleRunning"`
	// Wireless is hostapd, which serves the networks a radio carries.
	WirelessSetUp bool `json:"wirelessSetUp"`
	// Proxy is the sidecar that publishes what is behind the router.
	ProxySetUp   bool `json:"proxySetUp"`
	ProxyRunning bool `json:"proxyRunning"`
	// NTP is chronyd, which keeps the clock and answers the LAN.
	NTPSetUp   bool `json:"ntpSetUp"`
	NTPRunning bool `json:"ntpRunning"`
	// ResolverUnreachable lists the DNS over TLS upstreams that do not
	// answer on their port, when that is the resolver in use. Behind a
	// network that blocks the port every name fails silently.
	ResolverUnreachable []string `json:"resolverUnreachable,omitempty"`
}

// unreachableTLS probes the configured DNS over TLS upstreams, at most
// once a minute for the same set.
func (a *api) unreachableTLS(ctx context.Context, ups []model.TLSUpstream) []string {
	key := ""
	for _, u := range ups {
		key += u.Address + ","
	}
	a.dot.mu.Lock()
	defer a.dot.mu.Unlock()
	if a.dot.key == key && time.Since(a.dot.at) < time.Minute {
		return a.dot.bad
	}
	a.dot.key, a.dot.at, a.dot.bad = key, time.Now(), services.UnreachableTLS(ctx, ups, a.dial)
	return a.dot.bad
}

func (a *api) servicesStatus(w http.ResponseWriter, r *http.Request) error {
	st := servicesStatus{}
	if a.services != nil {
		st.SetUp = a.services.Installed(r.Context())
		st.Running = a.services.Active(r.Context())
		if leases, err := a.services.ReadLeases(); err == nil {
			st.Leases = len(leases)
		}
	}
	if a.resolver != nil {
		st.ResolverSetUp = a.resolver.Installed(r.Context())
		st.ResolverRunning = a.resolver.Active(r.Context())
	}
	if a.pppoe != nil {
		st.PPPoESetUp = a.pppoe.Installed(r.Context())
	}
	if a.upnp != nil {
		st.UPnPSetUp = a.upnp.Installed(r.Context())
		st.UPnPRunning = a.upnp.Active(r.Context())
	}
	if a.tailscale != nil {
		st.TailscaleSetUp = a.tailscale.Installed(r.Context())
		st.TailscaleRunning = a.tailscale.Active(r.Context())
	}
	if a.wireless != nil {
		st.WirelessSetUp = a.wireless.Installed(r.Context())
	}
	if a.proxy != nil {
		st.ProxySetUp = a.proxy.Installed(r.Context())
		st.ProxyRunning = a.proxy.Active(r.Context())
	}
	if a.ntp != nil {
		st.NTPSetUp = a.ntp.Installed(r.Context())
		st.NTPRunning = a.ntp.Active(r.Context())
	}
	if a.engine != nil {
		if cfg := a.engine.Effective(); cfg != nil && cfg.Services.DNS.Enabled && cfg.Services.DNS.Resolver == model.ResolverTLS {
			st.ResolverUnreachable = a.unreachableTLS(r.Context(), cfg.Services.DNS.TLSUpstreams)
		}
	}
	// The mappings are in the ruleset, so a router with no table loaded
	// reports none rather than failing the whole strip.
	if maps, err := a.engine.Mappings(r.Context()); err == nil {
		st.Mappings = len(maps)
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// clearDNSCache empties the caches the router is running now: dnsmasq
// always, the validating resolver when it is on. Both clear on a HUP, so
// nothing restarts. It reads the applied configuration, not a draft: a
// resolver that has not been applied is not the one holding answers.
func (a *api) clearDNSCache(w http.ResponseWriter, r *http.Request) error {
	if a.services == nil {
		return &unavailable{errors.New("DNS is not set up on this router (daemon not running as root?)")}
	}
	cfg := a.engine.Effective()
	if cfg == nil || !cfg.Services.DNS.Enabled {
		return &badRequest{errors.New("the DNS server is off")}
	}
	ctx := r.Context()
	if !a.services.Installed(ctx) {
		return &unavailable{errors.New("dnsmasq is not set up on this router: run `ostiole repair` once as root")}
	}
	cleared := []string{}
	if a.services.Active(ctx) {
		if err := a.services.Reload(ctx); err != nil {
			return err
		}
		cleared = append(cleared, "dnsmasq")
	}
	if a.resolver != nil && services.ResolverEnabled(cfg) && a.resolver.Active(ctx) {
		if err := a.resolver.Reload(ctx); err != nil {
			return err
		}
		cleared = append(cleared, "unbound")
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
	return nil
}

// upnpMappings lists the holes clients have opened for themselves, read
// from the ruleset. A router with no table loaded has none rather than an
// error: the page has a line of its own for "not set up".
func (a *api) upnpMappings(w http.ResponseWriter, r *http.Request) error {
	maps, err := a.engine.Mappings(r.Context())
	if err != nil {
		if errors.Is(err, nft.ErrNoTable) {
			writeJSON(w, http.StatusOK, []nft.Mapping{})
			return nil
		}
		return err
	}
	writeJSON(w, http.StatusOK, maps)
	return nil
}

// gatewayStatus reports gateway health. Without a monitor (a dev run, or
// a daemon that is not root) the list is empty rather than an error.
func (a *api) gatewayStatus(w http.ResponseWriter, _ *http.Request) error {
	out := []gateway.Status{}
	if a.gateways != nil {
		out = a.gateways.Statuses()
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// detectedGateways reports the default routes the kernel already has.
// Most routers get one from DHCP before anyone configures anything, and it
// is the one carrying the traffic, so it belongs on the page whether or
// not Ostiole put it there.
func (a *api) detectedGateways(w http.ResponseWriter, _ *http.Request) error {
	cfg := a.engine.Effective()
	found, err := gateway.Detect(cfg)
	if err != nil {
		return err
	}
	type detected struct {
		gateway.Detected
		// Suggested is the gateway this route would become, ready for the
		// UI to put straight into the draft.
		Suggested *model.Gateway `json:"suggested,omitempty"`
	}
	out := make([]detected, 0, len(found))
	for _, d := range found {
		row := detected{Detected: d}
		if d.Configured == "" && cfg != nil {
			g := gateway.Suggest(cfg, d)
			row.Suggested = &g
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// policyTarget is one gateway or group as policy routing sees it: the
// mark the firewall sets, the table that answers it, and where that table
// currently points.
type policyTarget struct {
	policy.Target
	// Rules counts the enabled firewall rules routing through this target.
	Rules int `json:"rules"`
	// NextHops are the addresses traffic goes to right now; empty means
	// the target has nothing to offer and traffic falls back or is
	// blocked.
	NextHops []string `json:"nextHops"`
}

// policyStatus explains where marked traffic goes. It reads the
// configuration the kernel is running, which during a confirmation window
// is the unconfirmed one, rather than the draft in the browser.
func (a *api) policyStatus(w http.ResponseWriter, _ *http.Request) error {
	out := []policyTarget{}
	cfg := a.engine.Effective()
	if cfg == nil {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	hops := map[string]policy.Hop{}
	if a.gateways != nil {
		for _, s := range a.gateways.Statuses() {
			hops[s.Name] = policy.Hop{
				Gateway:   s.Name,
				Address:   s.Address,
				Interface: s.Interface,
				Online:    s.Online || s.Unknown,
			}
		}
	}
	for _, t := range policy.Plan(cfg, hops) {
		pt := policyTarget{Target: t, NextHops: []string{}}
		for _, r := range cfg.Rules {
			if r.Enabled && r.Gateway == t.Name {
				pt.Rules++
			}
		}
		for _, tier := range t.Tiers {
			for _, h := range tier {
				if h.Online && h.Address != "" {
					pt.NextHops = append(pt.NextHops, h.Address)
				}
			}
			if len(pt.NextHops) > 0 {
				break
			}
		}
		out = append(out, pt)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// wireguardKeys mints a key pair for a new tunnel, or a preshared key for
// a peer. Generating them here keeps the browser out of the crypto and
// gives every router the same well-seeded source.
func (a *api) wireguardKeys(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		// Kind is "pair" (default) or "psk".
		Kind string `json:"kind"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	switch req.Kind {
	case "", "pair":
		priv, err := wg.GenerateKey()
		if err != nil {
			return err
		}
		pub, err := wg.PublicKey(priv)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"privateKey": priv, "publicKey": pub})
	case "psk":
		psk, err := wg.GeneratePSK()
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"presharedKey": psk})
	default:
		return &badRequest{fmt.Errorf("unknown kind %q", req.Kind)}
	}
	return nil
}

func channelFrom(s string) (update.Channel, error) {
	switch update.Channel(s) {
	case "", update.Stable:
		return update.Stable, nil
	case update.Beta:
		return update.Beta, nil
	}
	return "", &badRequest{fmt.Errorf("unknown channel %q", s)}
}

func (a *api) updateCheck(w http.ResponseWriter, r *http.Request) error {
	if a.updater == nil {
		return &unavailable{errors.New("updates not available")}
	}
	ch, err := channelFrom(r.URL.Query().Get("channel"))
	if err != nil {
		return err
	}
	chk, err := a.updater.Check(r.Context(), ch)
	if err != nil {
		return &upstream{err}
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": chk, "status": a.updater.Status()})
	return nil
}

// updateStatus answers both halves of "what about Ostiole itself": what
// the last scheduled check found, and whatever install is running now.
// It asks GitHub nothing, which is what lets the dashboard show a waiting
// release on every page load without an admin session or a round trip.
func (a *api) updateStatus(w http.ResponseWriter, _ *http.Request) error {
	if a.updater == nil {
		return &unavailable{errors.New("updates not available")}
	}
	writeJSON(w, http.StatusOK, map[string]any{"check": a.updater.Cached(), "status": a.updater.Status()})
	return nil
}

func (a *api) updateApply(w http.ResponseWriter, r *http.Request) error {
	if a.updater == nil {
		return &unavailable{errors.New("updates not available")}
	}
	var req struct {
		Channel string `json:"channel"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	ch, err := channelFrom(req.Channel)
	if err != nil {
		return err
	}
	if err := a.updater.Start(ch); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, a.updater.Status())
	return nil
}

// upstream marks failures talking to something beyond this router: GitHub
// for a release, or the modem for its status.
type upstream struct{ err error }

func (u *upstream) Error() string { return u.err.Error() }
func (u *upstream) Unwrap() error { return u.err }

// protect requires a valid session cookie.
func (a *api) protect(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return a.public(func(w http.ResponseWriter, r *http.Request) error {
		if a.auth == nil {
			return &unavailable{errors.New("authentication not available")}
		}
		if _, ok := a.session(r); !ok {
			return errUnauthorized
		}
		return h(w, r)
	})
}

// public wraps a handler that needs no session.
func (a *api) public(h func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			writeError(w, statusFor(err), err)
		}
	}
}

var errUnauthorized = errors.New("authentication required")

type unavailable struct{ err error }

func (u *unavailable) Error() string { return u.err.Error() }
func (u *unavailable) Unwrap() error { return u.err }

type errorResponse struct {
	Error  string        `json:"error"`
	Issues []model.Issue `json:"issues,omitempty"`
}

func writeError(w http.ResponseWriter, status int, err error) {
	resp := errorResponse{Error: err.Error()}
	if ve, ok := errors.AsType[*model.ValidationError](err); ok {
		resp.Error = "invalid configuration"
		resp.Issues = ve.Issues
	}
	writeJSON(w, status, resp)
}

func statusFor(err error) int {
	var ve *model.ValidationError
	var ne *nft.Error
	var be *badRequest
	var ua *unavailable
	var up *upstream
	var nf *notFound
	var pe *services.PreflightError
	switch {
	case errors.As(err, &be):
		return http.StatusBadRequest
	case errors.As(err, &nf):
		return http.StatusNotFound
	case errors.As(err, &ua):
		return http.StatusServiceUnavailable
	case errors.As(err, &up):
		return http.StatusBadGateway
	case errors.Is(err, update.ErrBusy):
		return http.StatusConflict
	case errors.Is(err, errUnauthorized), errors.Is(err, auth.ErrInvalidCredentials),
		errors.Is(err, auth.ErrInvalidToken), errors.Is(err, auth.ErrTokenExpired):
		return http.StatusUnauthorized
	case errors.Is(err, errForbidden):
		return http.StatusForbidden
	case errors.Is(err, auth.ErrTokenNotFound), errors.Is(err, auth.ErrNoSuchUser):
		return http.StatusNotFound
	case errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, auth.ErrBusy):
		return http.StatusServiceUnavailable
	case errors.Is(err, auth.ErrSetupDone), errors.Is(err, auth.ErrUserExists),
		errors.Is(err, auth.ErrLastAdmin), errors.Is(err, auth.ErrLastAccount):
		return http.StatusConflict
	case errors.Is(err, auth.ErrUnknownRole):
		return http.StatusUnprocessableEntity
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidUsername):
		return http.StatusUnprocessableEntity
	case errors.As(err, &ve), errors.As(err, &ne):
		return http.StatusUnprocessableEntity
	case errors.Is(err, engine.ErrPending), errors.Is(err, engine.ErrNothingPending):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotFound), errors.Is(err, nft.ErrNoTable),
		errors.Is(err, engine.ErrUnknownInterface), errors.Is(err, smart.ErrNoDevice),
		errors.Is(err, certs.ErrNoCertificate), errors.Is(err, acme.ErrUnknownCertificate):
		return http.StatusNotFound
	case errors.Is(err, acme.ErrIssuing):
		return http.StatusConflict
	case errors.Is(err, engine.ErrNotDynamic), errors.As(err, &pe):
		return http.StatusUnprocessableEntity
	case errors.Is(err, engine.ErrNoNetwork), errors.Is(err, network.ErrNotRunning):
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

type badRequest struct{ err error }

func (b *badRequest) Error() string { return b.err.Error() }
func (b *badRequest) Unwrap() error { return b.err }

func decodeJSON(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &badRequest{fmt.Errorf("invalid JSON body: %w", err)}
	}
	if dec.More() {
		return &badRequest{errors.New("invalid JSON body: trailing data")}
	}
	return nil
}

func (a *api) status(w http.ResponseWriter, r *http.Request) error {
	st, err := a.engine.Status(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// systemStats reports CPU, memory and disk for the dashboard card. The CPU
// figure covers the time since the previous request, so the first answer
// after a restart has none.
func (a *api) systemStats(w http.ResponseWriter, _ *http.Request) error {
	if a.sysstat == nil {
		return &unavailable{errors.New("this router does not report system statistics")}
	}
	st, err := a.sysstat.Read()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

// timezones is the list the setting's picker offers, and what the clock
// reads now. Current is the router's own answer rather than the
// configuration's, so a clock that never took the setting is visible.
type timezones struct {
	Zones   []string `json:"zones"`
	Current string   `json:"current"`
}

func (a *api) timezones(w http.ResponseWriter, _ *http.Request) error {
	sys := timezone.System{}
	writeJSON(w, http.StatusOK, timezones{Zones: sys.Zones(), Current: sys.Current()})
	return nil
}

func (a *api) getConfig(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a.visible(r, cfg))
	return nil
}

// visible is the configuration as the caller may see it. An operator edits
// it and saves it back whole, so needs it as it is. A viewer may only
// look, and the keys, passphrases and passwords in it are not for looking
// at: they get the copy a shared backup carries.
func (a *api) visible(r *http.Request, cfg *model.Config) *model.Config {
	if p, ok := a.authenticate(r); ok && p.Role.Allows(auth.RoleOperator) {
		return cfg
	}
	return cfg.Redacted()
}

func (a *api) revisions(w http.ResponseWriter, _ *http.Request) error {
	revs, err := a.engine.Store().Revisions()
	if err != nil {
		return err
	}
	if revs == nil {
		revs = []store.Revision{}
	}
	writeJSON(w, http.StatusOK, revs)
	return nil
}

func (a *api) revision(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.engine.Store().LoadRevision(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a.visible(r, cfg))
	return nil
}

func (a *api) ruleset(w http.ResponseWriter, _ *http.Request) error {
	rs, err := a.engine.Store().LoadRuleset()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, err = io.WriteString(w, rs)
	return err
}

func (a *api) counters(w http.ResponseWriter, r *http.Request) error {
	c, err := a.engine.Counters(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}

// systemHosts describes the names a configuration makes the DNS server
// answer on its own: the static leases with hostnames. Like systemRules
// it takes the configuration in the body, so the overrides tab shows them
// for the draft being edited.
func (a *api) systemHosts(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	writeJSON(w, http.StatusOK, services.SystemHosts(req.Config))
	return nil
}

// systemRules describes the rules a configuration makes Ostiole add on its
// own. It takes the configuration in the body, like check, so the rules
// page can show them for the draft being edited rather than what is saved.
func (a *api) systemRules(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	rows, err := a.engine.SystemRules(req.Config)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []nft.SystemRule{}
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

// systemNAT describes the outbound NAT rules a configuration makes Ostiole
// write on its own, for the draft the NAT page is editing, like
// systemRules.
func (a *api) systemNAT(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	rows, err := a.engine.SystemNAT(req.Config)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []nft.SystemNAT{}
	}
	writeJSON(w, http.StatusOK, rows)
	return nil
}

func (a *api) liveInterfaces(w http.ResponseWriter, _ *http.Request) error {
	links, err := network.Discover()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, links)
	return nil
}

type renewRequest struct {
	// Release drops the lease and starts over instead of asking the
	// server to extend it.
	Release bool `json:"release,omitempty"`
}

// renewLease asks for a fresh lease on a dynamic interface. The body is
// optional; an empty one is a plain renew.
func (a *api) renewLease(w http.ResponseWriter, r *http.Request) error {
	var req renewRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			return err
		}
	}
	if err := a.engine.RenewLease(r.Context(), r.PathValue("name"), req.Release); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type starterRequest struct {
	Hostname          string `json:"hostname"`
	LAN               string `json:"lan"`
	LANAddress        string `json:"lanAddress"`
	WAN               string `json:"wan"`
	ManagementFromWAN bool   `json:"managementFromWan"`
	// Services enables DHCP and DNS on the LAN with a pool derived from
	// the LAN address and DNS forwarded to Quad9.
	Services bool `json:"services"`
}

// starter builds (but does not save) a first configuration from the
// wizard's answers, so the UI can show and then apply it.
func (a *api) starter(w http.ResponseWriter, r *http.Request) error {
	var req starterRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.LAN == "" || req.LANAddress == "" {
		return &badRequest{errors.New("lan and lanAddress are required")}
	}
	// How sshd lets people in now is what the first configuration keeps:
	// the wizard is not the place to discover that SSH stopped working.
	passwords, _ := sshd.System{}.State(r.Context())
	cfg := model.Starter(model.StarterOptions{
		Hostname:          req.Hostname,
		LAN:               req.LAN,
		LANAddress:        req.LANAddress,
		WAN:               req.WAN,
		ManagementFromWAN: req.ManagementFromWAN,
		Services:          req.Services,
		SSHPasswords:      passwords,
		WebPort:           uiPort(r),
	})
	if err := cfg.Validate(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
}

// uiPort is the port this request reached the UI on, which is the one the
// first configuration's anti-lockout rule has to keep open.
func uiPort(r *http.Request) uint16 {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0
	}
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(port, 10, 16)
	return uint16(n)
}

type configRequest struct {
	Config *model.Config `json:"config"`
}

type applyRequest struct {
	Config *model.Config `json:"config"`
	// ConfirmTimeoutSeconds of zero commits immediately.
	ConfirmTimeoutSeconds int `json:"confirmTimeoutSeconds"`
}

func (a *api) check(w http.ResponseWriter, r *http.Request) error {
	var req configRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	if err := a.mayChange(r, req.Config); err != nil {
		return err
	}
	plan, err := a.engine.Check(r.Context(), req.Config)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plan)
	return nil
}

// mayChange refuses an operator a configuration that changes what only an
// administrator may: an operator may apply, but a command cron would make
// them root and a remote backup would hand them the accounts.
func (a *api) mayChange(r *http.Request, next *model.Config) error {
	if p, ok := a.authenticate(r); ok && p.Role.Allows(auth.RoleAdmin) {
		return nil
	}
	old, err := a.engine.Store().Load()
	if errors.Is(err, store.ErrNotFound) {
		old = nil
	} else if err != nil {
		return err
	}
	if parts := model.AdminChanges(old, next); len(parts) > 0 {
		return fmt.Errorf("%w: only an administrator can change %s", errForbidden, strings.Join(parts, ", "))
	}
	return nil
}

func (a *api) apply(w http.ResponseWriter, r *http.Request) error {
	var req applyRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	if req.Config == nil {
		return &badRequest{errors.New("config is required")}
	}
	if req.ConfirmTimeoutSeconds < 0 || req.ConfirmTimeoutSeconds > 3600 {
		return &badRequest{errors.New("confirmTimeoutSeconds must be 0-3600")}
	}
	if err := a.mayChange(r, req.Config); err != nil {
		return err
	}
	res, err := a.engine.Apply(r.Context(), req.Config, engine.ApplyOptions{
		ConfirmTimeout: time.Duration(req.ConfirmTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
	a.wakeFeeds()
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (a *api) confirm(w http.ResponseWriter, r *http.Request) error {
	archived, err := a.engine.Confirm(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": archived})
	return nil
}

func (a *api) revert(w http.ResponseWriter, r *http.Request) error {
	if err := a.engine.Revert(r.Context()); err != nil {
		return err
	}
	a.wakeFeeds()
	w.WriteHeader(http.StatusNoContent)
	return nil
}
