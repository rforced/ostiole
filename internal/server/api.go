package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/feeds"
	"github.com/rforced/ostiole/internal/fwlog"
	"github.com/rforced/ostiole/internal/gateway"
	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/policy"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/update"
	"github.com/rforced/ostiole/internal/wg"
)

const maxBodyBytes = 1 << 20

type api struct {
	engine    *engine.Engine
	auth      *auth.Service
	updater   *update.Manager
	services  *services.Dnsmasq
	resolver  *services.Unbound
	pppoe     *services.PPPoE
	certs     *certs.Manager
	tokens    *auth.Tokens
	feeds     FeedRefresher
	feedCache *feeds.Cache
	crons     CronRunner
	// routes is the router the handlers were registered on, which the
	// OpenAPI description is generated from.
	routes *router
	// certHosts lists the names a regenerated certificate should cover.
	certHosts func() []string
	fwlog     *fwlog.Ring
	tables    TableLister
	units     install.Systemctl
	gateways  GatewayStatuser
}

// GatewayStatuser reports what the gateway monitor knows.
type GatewayStatuser interface {
	Statuses() []gateway.Status
}

func (a *api) register(mux *router) {
	a.registerAuth(mux)
	a.registerLog(mux)
	a.registerDiag(mux)
	a.registerBackup(mux)
	a.registerCerts(mux)
	a.registerTokens(mux)
	a.registerFeeds(mux)
	a.registerCrons(mux)
	a.registerMetrics(mux)
	a.registerOpenAPI(mux)
	mux.HandleFunc("GET /api/v1/status", a.read(a.status))
	mux.HandleFunc("GET /api/v1/overview", a.read(a.overview))
	mux.HandleFunc("GET /api/v1/config", a.read(a.getConfig))
	mux.HandleFunc("GET /api/v1/config/revisions", a.read(a.revisions))
	mux.HandleFunc("GET /api/v1/config/revisions/{id}", a.read(a.revision))
	mux.HandleFunc("GET /api/v1/ruleset", a.read(a.ruleset))
	mux.HandleFunc("GET /api/v1/counters", a.read(a.counters))
	mux.HandleFunc("GET /api/v1/interfaces/live", a.readNoEngine(a.liveInterfaces))
	mux.HandleFunc("POST /api/v1/config/starter", a.write(a.starter))
	mux.HandleFunc("POST /api/v1/check", a.write(a.check))
	mux.HandleFunc("POST /api/v1/apply", a.write(a.apply))
	mux.HandleFunc("POST /api/v1/apply/confirm", a.write(a.confirm))
	mux.HandleFunc("POST /api/v1/apply/revert", a.write(a.revert))
	mux.HandleFunc("GET /api/v1/services/status", a.readNoEngine(a.servicesStatus))
	mux.HandleFunc("GET /api/v1/dhcp/leases", a.readNoEngine(a.dhcpLeases))
	mux.HandleFunc("POST /api/v1/wireguard/keys", a.write(a.wireguardKeys))
	mux.HandleFunc("GET /api/v1/gateways", a.readNoEngine(a.gatewayStatus))
	mux.HandleFunc("GET /api/v1/policy", a.readNoEngine(a.policyStatus))
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
	writeJSON(w, http.StatusOK, st)
	return nil
}

func (a *api) dhcpLeases(w http.ResponseWriter, _ *http.Request) error {
	if a.services == nil {
		writeJSON(w, http.StatusOK, []services.Lease{})
		return nil
	}
	leases, err := a.services.ReadLeases()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, leases)
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
// gives every box the same well-seeded source.
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

func (a *api) updateStatus(w http.ResponseWriter, _ *http.Request) error {
	if a.updater == nil {
		return &unavailable{errors.New("updates not available")}
	}
	writeJSON(w, http.StatusOK, a.updater.Status())
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

// upstream marks failures talking to GitHub.
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
	var ve *model.ValidationError
	if errors.As(err, &ve) {
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
	switch {
	case errors.As(err, &be):
		return http.StatusBadRequest
	case errors.As(err, &ua):
		return http.StatusServiceUnavailable
	case errors.As(err, &up):
		return http.StatusBadGateway
	case errors.Is(err, update.ErrBusy):
		return http.StatusConflict
	case errors.Is(err, update.ErrPackageManaged):
		return http.StatusUnprocessableEntity
	case errors.Is(err, errUnauthorized), errors.Is(err, auth.ErrInvalidCredentials),
		errors.Is(err, auth.ErrInvalidToken), errors.Is(err, auth.ErrTokenExpired):
		return http.StatusUnauthorized
	case errors.Is(err, errForbidden):
		return http.StatusForbidden
	case errors.Is(err, auth.ErrTokenNotFound):
		return http.StatusNotFound
	case errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, auth.ErrSetupDone):
		return http.StatusConflict
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidUsername):
		return http.StatusUnprocessableEntity
	case errors.As(err, &ve), errors.As(err, &ne):
		return http.StatusUnprocessableEntity
	case errors.Is(err, engine.ErrPending), errors.Is(err, engine.ErrNothingPending):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotFound), errors.Is(err, nft.ErrNoTable):
		return http.StatusNotFound
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

func (a *api) getConfig(w http.ResponseWriter, _ *http.Request) error {
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
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
	writeJSON(w, http.StatusOK, cfg)
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

func (a *api) liveInterfaces(w http.ResponseWriter, _ *http.Request) error {
	links, err := network.Discover()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, links)
	return nil
}

// currentResolvers reads the nameservers the box uses today so the DNS
// service forwards to the same place. Loopback entries are skipped.
func currentResolvers() []string {
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "nameserver" {
			if ip, err := netip.ParseAddr(fields[1]); err == nil && !ip.IsLoopback() {
				out = append(out, ip.String())
			}
		}
	}
	return out
}

type starterRequest struct {
	Hostname          string `json:"hostname"`
	LAN               string `json:"lan"`
	LANAddress        string `json:"lanAddress"`
	WAN               string `json:"wan"`
	ManagementFromWAN bool   `json:"managementFromWan"`
	// Services enables DHCP and DNS on the LAN with a pool derived from
	// the LAN address; upstreams default to the box's current resolvers.
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
	cfg := model.Starter(model.StarterOptions{
		Hostname:          req.Hostname,
		LAN:               req.LAN,
		LANAddress:        req.LANAddress,
		WAN:               req.WAN,
		ManagementFromWAN: req.ManagementFromWAN,
		Services:          req.Services,
		DNSUpstreams:      currentResolvers(),
	})
	if err := cfg.Validate(); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, cfg)
	return nil
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
	plan, err := a.engine.Check(r.Context(), req.Config)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, plan)
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
	res, err := a.engine.Apply(r.Context(), req.Config, engine.ApplyOptions{
		ConfirmTimeout: time.Duration(req.ConfirmTimeoutSeconds) * time.Second,
	})
	if err != nil {
		return err
	}
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
	w.WriteHeader(http.StatusNoContent)
	return nil
}
