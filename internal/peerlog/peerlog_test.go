package peerlog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"testing"
	"time"

	"ostiole/internal/logsearch/logsearchtest"
	"ostiole/internal/model"
	"ostiole/internal/netlink"
	"ostiole/internal/tailscale"
)

// events is what a log holds, oldest first.
func events(l *Log) string {
	recent := l.Recent(0)
	var out []string
	for _, e := range slices.Backward(recent) {

		out = append(out, fmt.Sprintf("%s %s %s", e.Event, e.Peer, e.Endpoint))
	}
	return fmt.Sprint(out)
}

func infoConfig() *model.Config {
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	return cfg
}

var phoneKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

// A WireGuard peer connects when it shakes hands after a silence, goes
// quiet three minutes after its last handshake, and roams when it is
// heard from elsewhere. The first read only learns where it stands.
func TestAWireGuardPeersComingAndGoing(t *testing.T) {
	t.Parallel()
	cfg := infoConfig()
	cfg.Interfaces = []model.Interface{{Name: "wg0", Enabled: true, WireGuard: &model.WireGuard{
		Peers: []model.WireGuardPeer{{Name: "phone", PublicKey: phoneKey}},
	}}}
	// The log ages entries out by the real clock, so the poller's starts there.
	now := time.Now()
	peer := netlink.WireGuardPeer{PublicKey: make([]byte, 32), LastHandshake: now.Add(-time.Minute),
		Endpoint: netip.MustParseAddrPort("203.0.113.5:51820")}
	read := func(string) (netlink.WireGuardDevice, bool, error) {
		return netlink.WireGuardDevice{Name: "wg0", Peers: []netlink.WireGuardPeer{peer}}, true, nil
	}
	l := New()
	p := &Poller{Log: l, Kind: WireGuard, Source: func() *model.Config { return cfg }, Read: ReadWireGuard(read),
		Changes: WireGuardChanges, Now: func() time.Time { return now }}
	step := func(d time.Duration) {
		now = now.Add(d)
		p.Tick(context.Background())
	}
	step(0)
	step(3 * time.Minute)
	peer.LastHandshake = now.Add(time.Minute)
	step(2 * time.Minute)
	peer.Endpoint = netip.MustParseAddrPort("198.51.100.7:40000")
	step(10 * time.Second)
	if got := events(l); got != "[quiet phone 203.0.113.5:51820 connected phone 203.0.113.5:51820 roamed phone 198.51.100.7:40000]" {
		t.Errorf("logged %s", got)
	}
	if l.Recent(1)[0].Tunnel != "wg0" {
		t.Errorf("tunnel %q", l.Recent(1)[0].Tunnel)
	}
}

// A Tailscale peer goes online and offline, and its traffic's path is
// logged when it changes; traffic that stops and starts again on the same
// path is no news.
func TestATailscalePeersComingAndGoing(t *testing.T) {
	t.Parallel()
	cfg := infoConfig()
	cfg.Interfaces = []model.Interface{{Name: "tailscale0", Enabled: true, Tailscale: &model.Tailscale{}}}
	var peer tailscale.Peer
	status := func(context.Context) (*tailscale.Status, error) {
		return &tailscale.Status{BackendState: tailscale.StateRunning, Peer: map[string]tailscale.Peer{"nodekey:1": peer}}, nil
	}
	l := New()
	p := &Poller{Log: l, Kind: Tailscale, Source: func() *model.Config { return cfg }, Read: ReadTailscale(status),
		Changes: TailscaleChanges}
	for _, next := range []tailscale.Peer{
		{HostName: "laptop"},
		{HostName: "laptop", Online: true, Active: true, CurAddr: "203.0.113.5:41641"},
		{HostName: "laptop", Online: true},
		{HostName: "laptop", Online: true, Active: true, CurAddr: "203.0.113.5:41641"},
		{HostName: "laptop", Online: true, Active: true, Relay: "nyc"},
		{HostName: "laptop"},
	} {
		peer = next
		p.Tick(context.Background())
	}
	want := "[online laptop 203.0.113.5:41641 direct laptop 203.0.113.5:41641 relayed laptop nyc offline laptop nyc]"
	if got := events(l); got != want {
		t.Errorf("logged %s\nwant   %s", got, want)
	}
}

// Below Info the log is emptied once and nothing is read; back at Info
// the first read only learns where the peers stand.
func TestPeersAreReadOnlyWhileTheLevelKeepsThem(t *testing.T) {
	t.Parallel()
	cfg := infoConfig()
	cfg.Interfaces = []model.Interface{{Name: "tailscale0", Enabled: true, Tailscale: &model.Tailscale{}}}
	online := false
	reads := 0
	status := func(context.Context) (*tailscale.Status, error) {
		reads++
		return &tailscale.Status{BackendState: tailscale.StateRunning,
			Peer: map[string]tailscale.Peer{"k": {HostName: "laptop", Online: online}}}, nil
	}
	l := New()
	p := &Poller{Log: l, Kind: Tailscale, Source: func() *model.Config { return cfg }, Read: ReadTailscale(status),
		Changes: TailscaleChanges}
	p.Tick(context.Background())
	online = true
	p.Tick(context.Background())
	cfg.System.Logging.Level = model.LogWarning
	p.Tick(context.Background())
	p.Tick(context.Background())
	if n, _ := l.Held(); n != 0 || reads != 2 {
		t.Fatalf("at Warning: %d held after %d reads", n, reads)
	}
	cfg.System.Logging.Level = model.LogDebug
	online = false
	p.Tick(context.Background())
	if n, _ := l.Held(); n != 0 {
		t.Errorf("the first read back at Debug logged %s", events(l))
	}
}

// The router searches what the Log tabs show, value for value.
func TestSearchValuesAreThePages(t *testing.T) {
	t.Parallel()
	for _, c := range logsearchtest.Cases(t, "peers") {
		var e Event
		if err := json.Unmarshal(c.Entry, &e); err != nil {
			t.Fatal(err)
		}
		var got logsearchtest.Recorder
		e.Search(&got, nil)
		if !slices.Equal(got, c.Values) {
			t.Errorf("%s: %q, want %q", c.Why, got, c.Values)
		}
	}
}
