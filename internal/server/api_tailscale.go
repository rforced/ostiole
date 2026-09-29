package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/rforced/ostiole/internal/tailscale"
)

// tailscaleStatus is what the page shows. A router that has not joined a
// tailnet, or whose daemon is stopped, answers with the flags false and
// the rest empty: "not set up" is a state of the page, not an error.
type tailscaleStatus struct {
	SetUp   bool   `json:"setUp"`
	Running bool   `json:"running"`
	State   string `json:"state,omitempty"`
	// AuthURL is the link to open to log this router in.
	AuthURL        string          `json:"authUrl,omitempty"`
	IPs            []string        `json:"ips"`
	HostName       string          `json:"hostName,omitempty"`
	DNSName        string          `json:"dnsName,omitempty"`
	Tailnet        string          `json:"tailnet,omitempty"`
	MagicDNSSuffix string          `json:"magicDnsSuffix,omitempty"`
	KeyExpiry      *time.Time      `json:"keyExpiry,omitempty"`
	Version        string          `json:"version,omitempty"`
	Health         []string        `json:"health"`
	Peers          []tailscalePeer `json:"peers"`
}

// tailscalePeer is one other node, in the order the page lists them.
type tailscalePeer struct {
	HostName string     `json:"hostName"`
	DNSName  string     `json:"dnsName,omitempty"`
	OS       string     `json:"os,omitempty"`
	IPs      []string   `json:"ips"`
	Routes   []string   `json:"routes"`
	Online   bool       `json:"online"`
	LastSeen *time.Time `json:"lastSeen,omitempty"`
	// Active is whether traffic is flowing. Relay and DirectAddr describe
	// the path only while it is; idle clears the direct endpoint and would
	// otherwise read as relayed.
	Active     bool   `json:"active"`
	Relay      string `json:"relay,omitempty"`
	DirectAddr string `json:"directAddr,omitempty"`
	RxBytes    int64  `json:"rxBytes,omitempty"`
	TxBytes    int64  `json:"txBytes,omitempty"`
	ExitNode   bool   `json:"exitNode,omitempty"`
	Expired    bool   `json:"expired,omitempty"`
}

func (a *api) registerTailscale(mux *router) {
	mux.HandleFunc("GET /api/v1/tailscale/status", a.readNoEngine(a.tailscaleStatus))
	mux.HandleFunc("POST /api/v1/tailscale/login", a.write(a.tailscaleLogin))
	mux.HandleFunc("POST /api/v1/tailscale/logout", a.admin(a.tailscaleLogout))
}

// tailscaleStatus asks the daemon what it is doing. Every way of having
// nothing to say — no unit, no daemon running, a daemon that will not
// answer — is a status with the flags false rather than an error, because
// the page's own text covers each of them.
func (a *api) tailscaleStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, a.readTailscale(r.Context()))
	return nil
}

func (a *api) readTailscale(ctx context.Context) tailscaleStatus {
	out := tailscaleStatus{IPs: []string{}, Health: []string{}, Peers: []tailscalePeer{}}
	if a.tailscale == nil || !a.tailscale.Installed(ctx) {
		return out
	}
	out.SetUp = true
	if !a.tailscale.Active(ctx) || a.tsClient == nil {
		return out
	}
	st, err := a.tsClient.Status(ctx)
	if err != nil {
		return out
	}
	out.Running = true
	out.State = st.BackendState
	out.AuthURL = st.AuthURL
	out.Version = st.Version
	if st.TailscaleIPs != nil {
		out.IPs = st.TailscaleIPs
	}
	if st.Health != nil {
		out.Health = st.Health
	}
	if st.Self != nil {
		out.HostName = st.Self.HostName
		out.DNSName = st.Self.DNSName
		out.KeyExpiry = whenSet(st.Self.KeyExpiry)
	}
	if st.CurrentTailnet != nil {
		out.Tailnet = st.CurrentTailnet.Name
		out.MagicDNSSuffix = st.CurrentTailnet.MagicDNSSuffix
	}
	for _, p := range st.Peer {
		out.Peers = append(out.Peers, tailscalePeer{
			HostName: p.HostName, DNSName: p.DNSName, OS: p.OS,
			IPs: orEmpty(p.TailscaleIPs), Routes: orEmpty(p.PrimaryRoutes),
			Online: p.Online, LastSeen: whenSet(p.LastSeen),
			Active: p.Active, Relay: p.Relay, DirectAddr: p.CurAddr,
			RxBytes: p.RxBytes, TxBytes: p.TxBytes,
			ExitNode: p.ExitNodeOption, Expired: p.Expired,
		})
	}
	sortPeers(out.Peers)
	return out
}

// sortPeers puts the ones that are up first, then by name, so the table
// does not reshuffle every time the map is walked.
func sortPeers(peers []tailscalePeer) {
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Online != peers[j].Online {
			return peers[i].Online
		}
		return peers[i].HostName < peers[j].HostName
	})
}

// whenSet drops a time the daemon sent as the zero value. A peer that is
// online carries "LastSeen":"0001-01-01T00:00:00Z" rather than no field at
// all, which a table would otherwise render as a date in year one.
func whenSet(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	return t
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// tailscaleLogin starts a login and answers as soon as it knows whether a
// browser is needed. The auth key, when there is one, is read from the body
// so it never reaches a URL or the request log.
func (a *api) tailscaleLogin(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		AuthKey string `json:"authKey"`
	}
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	if a.tailscale == nil || !a.tailscale.Installed(r.Context()) || a.tsClient == nil {
		return &badRequest{errors.New("tailscaled is not on this router: run `ostiole repair --tailscale` as root")}
	}
	cfg := a.engine.Effective()
	if cfg == nil {
		return &badRequest{errors.New("apply the Tailscale interface first")}
	}
	in, ok := cfg.TailscaleInterface()
	if !ok || !in.Enabled {
		return &badRequest{errors.New("apply the Tailscale interface first")}
	}

	// The login is the operator's, not the request's: a browser that gives
	// up must not take the attempt with it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	url, done, err := a.tsClient.Login(ctx, tailscale.UpArgs(*in.Tailscale), in.Tailscale.LoginServer, body.AuthKey)
	if err != nil {
		cancel()
		return err
	}
	prefs := tailscale.PrefArgs(*in.Tailscale)
	go func() {
		defer cancel()
		if err := <-done; err != nil {
			slog.Warn("tailscale login", "err", err)
			return
		}
		// `up --reset` put the preferences it does not take back to their
		// defaults, and one of those defaults phones home for updates.
		if err := a.tsClient.Set(ctx, prefs); err != nil {
			slog.Warn("tailscale preferences after login", "err", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"authUrl": url, "state": a.tsState(r.Context())})
	return nil
}

// tsState is the daemon's own word for what it is doing, for a login that
// needed no browser.
func (a *api) tsState(ctx context.Context) string {
	if a.tsClient == nil {
		return ""
	}
	st, err := a.tsClient.Status(ctx)
	if err != nil {
		return ""
	}
	return st.BackendState
}

// tailscaleLogout drops the node's key. The node stays in the tailnet's
// admin console, so logging in again puts this router back as it was.
func (a *api) tailscaleLogout(w http.ResponseWriter, r *http.Request) error {
	if a.tailscale == nil || !a.tailscale.Installed(r.Context()) || a.tsClient == nil {
		return &badRequest{errors.New("tailscaled is not on this router")}
	}
	if err := a.tsClient.Logout(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, a.readTailscale(r.Context()))
	return nil
}
