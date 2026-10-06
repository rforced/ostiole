package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rforced/ostiole/internal/linediff"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
)

// Drift is what applying the confirmed configuration again would change:
// this release's render of it set against what the last apply rendered.
// Nothing renders at start, so a release that renders the same
// configuration differently waits for the next apply (ADR-0038).
type Drift struct {
	// Parts name what differs by the pages its settings are on, in the
	// order an apply takes them: Firewall, Interfaces, the services,
	// Traffic shaping.
	Parts []string `json:"parts"`
	// Changes are the lines that differ, file by file, up to
	// maxDriftChanges of them.
	Changes []DriftChange `json:"changes"`
	// More counts the changes past those.
	More int `json:"more,omitempty"`
}

// DriftChange is one line an apply would take out or put in.
type DriftChange struct {
	// Path is "firewall" for the ruleset, else the part and the file,
	// "proxy/caddy.json".
	Path string `json:"path"`
	// Kind is "removed" or "added".
	Kind   string `json:"kind"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// DriftStatus is the size of a drift, for the status every page reads.
type DriftStatus struct {
	Parts   []string `json:"parts"`
	Changes int      `json:"changes"`
}

const (
	// maxDriftChanges bounds the lines a drift lists. The rest are counted.
	maxDriftChanges = 200
	// maxDriftLine bounds one line: caddy.json carries each site's rules
	// in a single string.
	maxDriftLine = 400
	// driftEvery is how long a worked-out drift stands while none of the
	// files it came from changed. Renders also read the router, the radios
	// and chrony's version, and those move on their own.
	driftEvery = time.Minute
)

// partNames are the pages a part's settings are on, which is how the apply
// bar and `ostiole status` name it. The services are the bundle's names.
var partNames = map[string][]string{
	"firewall":  {"Firewall"},
	"network":   {"Interfaces"},
	"pppoe":     {"Interfaces"},
	"wireless":  {"Wireless"},
	"unbound":   {"DNS"},
	"dnsblock":  {"DNS"},
	"dnsmasq":   {"DHCP", "DNS"},
	"ntp":       {"NTP"},
	"tailscale": {"Tailscale"},
	"proxy":     {"Reverse proxy"},
	"upnp":      {"UPnP"},
	"shaping":   {"Traffic shaping"},
}

// driftState is a drift worked out, when, and from which files.
type driftState struct {
	drift *Drift
	at    time.Time
	stamp string
}

// rendered is what a commit rendered for the backends (store.RenderedFile).
// The firewall's half is the saved ruleset. A backend the committing
// process did not drive is null and not compared, and neither is anything
// when the file is missing, as a commit by an earlier release leaves it.
type rendered struct {
	Network  network.Files `json:"network"`
	Services network.Files `json:"services"`
	Shaping  network.Files `json:"shaping"`
}

// saveRendered keeps what a commit rendered. Without it a later release
// cannot say what it would change, so one that cannot be written goes
// rather than staying to be compared against.
func (e *Engine) saveRendered(plan *Plan) {
	driven := func(b network.Backend, files network.Files) network.Files {
		switch {
		case b == nil:
			return nil
		case files == nil:
			return network.Files{}
		}
		return files
	}
	err := e.writeRendered(&rendered{
		Network:  driven(e.net, plan.Network),
		Services: driven(e.svc, plan.Services),
		Shaping:  driven(e.shape, plan.Shaping),
	})
	if err != nil {
		e.log.Warn("could not keep what this apply rendered; what a later release changes in it goes unshown until the next apply",
			"err", err)
		if err := e.store.RemoveState(store.RenderedFile); err != nil {
			e.log.Warn("could not remove what an earlier apply rendered", "err", err)
		}
	}
}

func (e *Engine) writeRendered(r *rendered) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return e.store.WriteState(store.RenderedFile, raw)
}

func (e *Engine) readRendered() (*rendered, error) {
	raw, err := e.store.ReadState(store.RenderedFile)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r rendered
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", store.RenderedFile, err)
	}
	return &r, nil
}

// Drift is what applying the confirmed configuration again would change,
// nil when nothing would. It is worked out again when the configuration,
// the saved ruleset or what the last commit rendered has changed, or a
// minute on. Nothing is reported while an apply is being made or awaits
// confirmation, before the first apply, or when a crash split the last
// save: the next apply sees to those.
func (e *Engine) Drift() (*Drift, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return nil, nil
	}
	stamp := e.store.Stamp()
	if s := e.drifted; s != nil && s.stamp == stamp && time.Since(s.at) < driftEvery {
		return s.drift, nil
	}
	d, err := e.drift()
	if err != nil {
		return nil, err
	}
	e.drifted = &driftState{drift: d, at: time.Now(), stamp: stamp}
	return d, nil
}

// DriftStatus is the size of the drift, nil when nothing differs or it
// cannot be worked out.
func (e *Engine) DriftStatus() *DriftStatus {
	d, err := e.Drift()
	if err != nil {
		e.log.Debug("could not work out what an apply would change", "err", err)
		return nil
	}
	if d == nil {
		return nil
	}
	return &DriftStatus{Parts: d.Parts, Changes: len(d.Changes) + d.More}
}

// drift renders the confirmed configuration and sets it against what the
// last apply rendered. The caller holds e.mu.
func (e *Engine) drift() (*Drift, error) {
	// An apply another process is making, or one a crash left unfinished.
	if r, err := e.readRecord(); err != nil || r != nil {
		return nil, err
	}
	cfg, err := e.store.Load()
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	saved, err := e.store.LoadRuleset()
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrStaleRuleset):
		return nil, nil
	case err != nil:
		return nil, err
	}
	ruleset, err := nft.Render(cfg)
	if err != nil {
		return nil, err
	}
	var d driftBuilder
	d.file("firewall", "firewall", nft.WithoutFetched(cfg, saved), nft.WithoutFetched(cfg, ruleset))
	last, err := e.readRendered()
	if err != nil || last == nil {
		return d.done(), err
	}
	for _, part := range []struct {
		name string
		b    network.Backend
		was  network.Files
	}{
		{"network", e.net, last.Network},
		{"services", e.svc, last.Services},
		{"shaping", e.shape, last.Shaping},
	} {
		if part.b == nil || part.was == nil {
			continue
		}
		now, err := part.b.Render(cfg)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", part.name, err)
		}
		d.files(part.name, part.was, now)
	}
	return d.done(), nil
}

// driftBuilder gathers what differs, part by part.
type driftBuilder struct {
	d Drift
}

// files compares a backend's files. A service's carry its name,
// "proxy/caddy.json", and that is its part.
func (b *driftBuilder) files(part string, was, now network.Files) {
	all := maps.Clone(was)
	maps.Copy(all, now)
	for _, name := range all.Names() {
		p, path := part, part+"/"+name
		if part == "services" {
			p, _, _ = strings.Cut(name, "/")
			path = name
		}
		b.file(p, path, was[name], now[name])
	}
}

// file adds the lines a file differs by, as the last apply rendered it and
// as this release does.
func (b *driftBuilder) file(part, path, was, now string) {
	if was == now {
		return
	}
	names, ok := partNames[part]
	if !ok {
		names = []string{part}
	}
	for _, name := range names {
		if !slices.Contains(b.d.Parts, name) {
			b.d.Parts = append(b.d.Parts, name)
		}
	}
	for _, l := range linediff.Diff(was, now) {
		if len(b.d.Changes) == maxDriftChanges {
			b.d.More++
			continue
		}
		c := DriftChange{Path: path, Kind: "added", After: clip(l.Text)}
		if l.Kind == linediff.Removed {
			c = DriftChange{Path: path, Kind: "removed", Before: clip(l.Text)}
		}
		b.d.Changes = append(b.d.Changes, c)
	}
}

func (b *driftBuilder) done() *Drift {
	if len(b.d.Parts) == 0 {
		return nil
	}
	return &b.d
}

// clip cuts a line past maxDriftLine bytes, on a rune boundary.
func clip(s string) string {
	if len(s) <= maxDriftLine {
		return s
	}
	cut := maxDriftLine
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Follow brings one service's files in line with what this release renders
// from the confirmed configuration, without an apply, and notes them as
// what was last rendered. It is for the proxy, whose files follow the
// binary an update put in (ADR-0038). Nothing happens while an apply is
// being made or awaits confirmation, when the service renders no files, or
// when its files already match. Files it refuses go back to what they
// were, and the same render is not tried again. It reports whether the
// files changed.
func (e *Engine) Follow(ctx context.Context, b network.Backend) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending != nil {
		return false, nil
	}
	// The store's lock holds off an apply in another process until this
	// is done, as an apply's own record does once it has begun.
	unlock, err := e.store.Lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	if r, err := e.readRecord(); err != nil || r != nil {
		return false, err
	}
	cfg, err := e.store.Load()
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	want, err := b.Render(cfg)
	if err != nil || len(want) == 0 {
		return false, err
	}
	have, err := b.Snapshot()
	if err != nil {
		return false, err
	}
	if maps.Equal(want, have) {
		e.noteRendered(b.Name(), want)
		return false, nil
	}
	sum := filesDigest(want)
	if e.refused[b.Name()] == sum {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), applyTimeout)
	defer cancel()
	if err := b.Apply(ctx, want); err != nil {
		if e.refused == nil {
			e.refused = map[string]string{}
		}
		e.refused[b.Name()] = sum
		if rerr := b.Apply(ctx, have); rerr != nil {
			return false, fmt.Errorf("%w, and putting the files before it back failed: %w", err, rerr)
		}
		return false, err
	}
	delete(e.refused, b.Name())
	e.noteRendered(b.Name(), want)
	return true, nil
}

// noteRendered records one service's files as what was last rendered, so
// that what followed the release is no longer listed as differing.
// Without a record of the last commit's render there is nothing to note
// them in, and a record that already says so is left alone.
func (e *Engine) noteRendered(name string, files network.Files) {
	last, err := e.readRendered()
	if err != nil || last == nil || last.Services == nil {
		return
	}
	next := network.Files{}
	for k, v := range last.Services {
		if !strings.HasPrefix(k, name+"/") {
			next[k] = v
		}
	}
	for k, v := range files {
		next[name+"/"+k] = v
	}
	if maps.Equal(next, last.Services) {
		return
	}
	last.Services = next
	e.drifted = nil
	if err := e.writeRendered(last); err != nil {
		e.log.Warn("could not note what followed this release; it is listed as differing until the next apply",
			"service", name, "err", err)
	}
}

// filesDigest identifies a set of files by their names and contents.
func filesDigest(files network.Files) string {
	h := sha256.New()
	for _, name := range files.Names() {
		fmt.Fprintf(h, "%d %s %d %s", len(name), name, len(files[name]), files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}
