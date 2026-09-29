// Package peerlog keeps the VPN peers' coming and going at the Info and
// Debug levels: WireGuard's read from the kernel, Tailscale's from its
// daemon, each every few seconds, with what changed logged.
package peerlog

import (
	"context"
	"encoding/base64"
	"log/slog"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/logfile"
	"github.com/rforced/ostiole/internal/logring"
	"github.com/rforced/ostiole/internal/logsearch"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/tailscale"
	"github.com/rforced/ostiole/internal/wg"
)

// What a peer did, as the Log tabs write it. A WireGuard peer connects when
// it shakes hands after a silence, goes quiet after quietAfter without one,
// and roams when it is heard from somewhere else. A Tailscale peer goes
// online and offline, and its traffic takes a direct path or a relay.
const (
	Connected = "connected"
	Quiet     = "quiet"
	Roamed    = "roamed"
	Online    = "online"
	Offline   = "offline"
	Direct    = "direct"
	Relayed   = "relayed"
)

// quietAfter is how long a WireGuard peer goes without a handshake before
// it is quiet: its session keys are refused past three minutes, so it
// shakes hands again before sending anything.
const quietAfter = 3 * time.Minute

// Event is one peer's change.
type Event struct {
	logring.Stamp
	Event string `json:"event"`
	// Tunnel is a WireGuard peer's interface.
	Tunnel string `json:"tunnel,omitempty"`
	Peer   string `json:"peer"`
	// Endpoint is where a peer was heard from, the direct path it took, or
	// the relay it went through.
	Endpoint string `json:"endpoint,omitempty"`
}

// Log is one kind of peer's coming and going in memory.
type Log = logring.Ring[Event, *Event]

// Kind is WireGuard or Tailscale: where its log is kept and how it is set.
type Kind struct {
	// Name is the log's directory under logfile.Dir.
	Name string
	// Keep is the log's setting in a configuration, and On whether its
	// peers are there to read.
	Keep func(*model.Config) model.LogKeep
	On   func(*model.Config) bool
}

// The two kinds.
var (
	WireGuard = Kind{
		Name: "wireguard",
		Keep: func(c *model.Config) model.LogKeep { return c.VPN.WireGuardLog },
		On:   (*model.Config).WireGuardEnabled,
	}
	Tailscale = Kind{
		Name: "tailscale",
		Keep: func(c *model.Config) model.LogKeep { return c.VPN.TailscaleLog },
		On:   (*model.Config).TailscaleEnabled,
	}
)

// FileVersion is the format of a line: an event as the API serves it.
const FileVersion = 1

// New returns an empty log of the default size.
func New() *Log {
	var k model.LogKeep
	return logring.New[Event, *Event](k.Size(model.DefaultPeerLogEntries), k.Retention())
}

// Kept says whether the configuration keeps the peers' logs at all: only
// the levels that keep what the daemons say about their clients do.
func Kept(c *model.Config) bool { return c.System.Logging.Records() }

// Settings sizes a kind's log in a configuration.
func (k Kind) Settings(c *model.Config) (int, time.Duration) {
	keep := k.Keep(c)
	return keep.Size(model.DefaultPeerLogEntries), keep.Retention()
}

// Files describes a kind's log to the writer that keeps it in files.
func (k Kind) Files(l *Log) logfile.Log {
	return l.Files(k.Name, FileVersion, Kept, func(c *model.Config) int {
		return int(k.Keep(c).Retention() / (24 * time.Hour))
	})
}

// ParseLine reads a line of a peer log's files.
func ParseLine(line []byte) (Event, time.Time, error) {
	return logring.Parse[Event, *Event](line)
}

// Peer is one peer as a read finds it.
type Peer struct {
	Tunnel, Name, Endpoint string
	// Up is a WireGuard peer that shook hands lately, or a Tailscale peer
	// online.
	Up bool
	// Path is the way a Tailscale peer's traffic last went, Direct or
	// Relayed, and Endpoint says where.
	Path string
}

// Poller reads a kind of peer every Interval while the configuration keeps
// its log, and logs what changed since the last read. The first read after
// the log was off only learns where each peer stands.
type Poller struct {
	Log    *Log
	Kind   Kind
	Source func() *model.Config
	// Read finds the peers as they are now, each by a key of its own.
	Read func(ctx context.Context, cfg *model.Config, now time.Time) (map[string]Peer, error)
	// Changes is what one peer did between two reads, and what to keep of
	// it for the next.
	Changes  func(was, now Peer) (events []string, keep Peer)
	Slog     *slog.Logger
	Interval time.Duration
	Now      func() time.Time

	seen    map[string]Peer
	emptied bool
}

// Run reads until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		p.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick reads once.
func (p *Poller) Tick(ctx context.Context) {
	cfg := p.Source()
	if cfg == nil || !Kept(cfg) {
		if !p.emptied {
			p.Log.Clear()
			p.seen, p.emptied = nil, true
		}
		return
	}
	p.emptied = false
	p.Log.Configure(p.Kind.Settings(cfg))
	if !p.Kind.On(cfg) {
		p.seen = nil
		return
	}
	now := time.Now()
	if p.Now != nil {
		now = p.Now()
	}
	peers, err := p.Read(ctx, cfg, now)
	if err != nil {
		if p.Slog != nil {
			p.Slog.Debug("could not read the peers", "kind", p.Kind.Name, "err", err)
		}
		return
	}
	next := make(map[string]Peer, len(peers))
	for key, cur := range peers {
		was, known := p.seen[key]
		if !known {
			next[key] = cur
			continue
		}
		events, keep := p.Changes(was, cur)
		for _, ev := range events {
			p.Log.Add(Event{Stamp: logring.Stamp{Time: now}, Event: ev, Tunnel: keep.Tunnel, Peer: keep.Name, Endpoint: keep.Endpoint})
		}
		next[key] = keep
	}
	p.seen = next
}

// WireGuardChanges is what a WireGuard peer did between two reads.
func WireGuardChanges(was, now Peer) ([]string, Peer) {
	switch {
	case now.Up && !was.Up:
		return []string{Connected}, now
	case !now.Up && was.Up:
		return []string{Quiet}, now
	case now.Up && now.Endpoint != "" && was.Endpoint != "" && now.Endpoint != was.Endpoint:
		return []string{Roamed}, now
	}
	return nil, now
}

// TailscaleChanges is what a Tailscale peer did between two reads. A peer
// with no traffic moving keeps the path it last took, so traffic taking
// it again is no news.
func TailscaleChanges(was, now Peer) ([]string, Peer) {
	var out []string
	switch {
	case now.Up && !was.Up:
		out = append(out, Online)
	case !now.Up && was.Up:
		out = append(out, Offline)
	}
	keep := now
	if now.Path == "" {
		keep.Path, keep.Endpoint = was.Path, was.Endpoint
	} else if now.Path != was.Path || now.Endpoint != was.Endpoint {
		out = append(out, now.Path)
	}
	return out, keep
}

// ReadWireGuard reads every enabled tunnel's peers through read, nil being
// the kernel. A peer is named as the configuration names it.
func ReadWireGuard(read wg.Device) func(context.Context, *model.Config, time.Time) (map[string]Peer, error) {
	return func(_ context.Context, cfg *model.Config, now time.Time) (map[string]Peer, error) {
		var tunnels []wg.Configured
		for _, in := range cfg.Interfaces {
			if !in.Enabled || in.WireGuard == nil {
				continue
			}
			c := wg.Configured{Name: in.Name, Peers: make(map[string]string, len(in.WireGuard.Peers))}
			for _, p := range in.WireGuard.Peers {
				c.Peers[p.PublicKey] = p.Name
			}
			tunnels = append(tunnels, c)
		}
		status, err := wg.Status(tunnels, read)
		if err != nil {
			return nil, err
		}
		out := map[string]Peer{}
		for _, t := range status {
			for _, p := range t.Peers {
				name := p.Name
				if name == "" {
					name = shortKey(p.PublicKey)
				}
				up := !p.LastHandshake.IsZero() && now.Sub(p.LastHandshake) < quietAfter
				out[t.Name+" "+p.PublicKey] = Peer{Tunnel: t.Name, Name: name, Endpoint: p.Endpoint, Up: up}
			}
		}
		return out, nil
	}
}

// shortKey is the start of a public key, for a peer the configuration
// does not name.
func shortKey(key string) string {
	if b, err := base64.StdEncoding.DecodeString(key); err == nil && len(key) > 8 && len(b) > 0 {
		return key[:8] + "…"
	}
	return key
}

// ReadTailscale reads the tailnet's peers through status. Traffic that is
// not moving says nothing about its path.
func ReadTailscale(status func(context.Context) (*tailscale.Status, error)) func(context.Context, *model.Config, time.Time) (map[string]Peer, error) {
	return func(ctx context.Context, _ *model.Config, _ time.Time) (map[string]Peer, error) {
		st, err := status(ctx)
		if err != nil {
			return nil, err
		}
		out := map[string]Peer{}
		if st.BackendState != tailscale.StateRunning {
			return out, nil
		}
		for key, p := range st.Peer {
			name := p.HostName
			if name == "" {
				name, _, _ = strings.Cut(p.DNSName, ".")
			}
			peer := Peer{Name: name, Up: p.Online}
			switch {
			case p.Active && p.CurAddr != "":
				peer.Path, peer.Endpoint = Direct, p.CurAddr
			case p.Active && p.Relay != "":
				peer.Path, peer.Endpoint = Relayed, p.Relay
			}
			out[key] = peer
		}
		return out, nil
	}
}

// Search hands a the values a Log tab shows for the event, as it shows
// them. buf is scratch, handed back to be used again.
func (e *Event) Search(a logsearch.Adder, buf []byte) []byte {
	a.Add(e.Event)
	a.Add(e.Tunnel)
	a.Add(e.Peer)
	a.Add(e.Endpoint)
	return buf
}

// Matcher is a search's test of an event, the same for the log's files as
// for its memory.
func Matcher(q logsearch.Query) func(*Event) bool {
	row := q.Row()
	var buf []byte
	return func(e *Event) bool {
		if q.Empty() {
			return true
		}
		row.Reset()
		buf = e.Search(&row, buf)
		return row.Match()
	}
}
