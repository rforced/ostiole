package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
)

// releaseNet is a service whose render a test changes, the way a new
// release renders the same configuration differently. It leaves on disk
// what it is given, and refuses files holding refuse after writing them,
// as the proxy refuses a configuration on reload.
type releaseNet struct {
	mu      sync.Mutex
	render  network.Files
	files   network.Files
	applies int
	refuse  string
}

func (r *releaseNet) Name() string { return "proxy" }

func (r *releaseNet) Render(*model.Config) (network.Files, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.render), nil
}

func (r *releaseNet) Snapshot() (network.Files, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := network.Files{}
	maps.Copy(out, r.files)
	return out, nil
}

func (r *releaseNet) Apply(_ context.Context, files network.Files) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applies++
	r.files = maps.Clone(files)
	for _, content := range files {
		if r.refuse != "" && strings.Contains(content, r.refuse) {
			return errors.New("the service refused its files")
		}
	}
	return nil
}

func (r *releaseNet) release(files network.Files) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.render = files
}

func (r *releaseNet) state() (network.Files, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.files), r.applies
}

// newDriftEngine is an engine with one service, applied once and
// confirmed, as a router is between updates.
func newDriftEngine(t *testing.T) (*Engine, *releaseNet, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	rel := &releaseNet{render: network.Files{"caddy.json": "{\n  \"site\": \"one\"\n}\n"}}
	e := New(st, &fakeRunner{}, nil, slog.New(slog.DiscardHandler)).WithServices(services.NewBundleOf(rel))
	if _, err := e.Apply(context.Background(), cfg("a"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	return e, rel, st
}

func mustDrift(t *testing.T, e *Engine) *Drift {
	t.Helper()
	d, err := e.Drift()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestNothingDiffersRightAfterAnApply(t *testing.T) {
	t.Parallel()
	e, _, _ := newDriftEngine(t)
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift after an apply: %+v", d)
	}
	if s := e.DriftStatus(); s != nil {
		t.Errorf("status after an apply: %+v", s)
	}
}

// A release that renders the same configuration differently lists what an
// apply would change, line by line, until the apply.
func TestARenderThatChangedIsListed(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	rel.release(network.Files{"caddy.json": "{\n  \"site\": \"two\"\n}\n", "certificates": "self\n"})
	e.drifted = nil // what a minute, or a commit elsewhere, does
	d := mustDrift(t, e)
	if d == nil || len(d.Parts) != 1 || d.Parts[0] != "Reverse proxy" {
		t.Fatalf("drift = %+v", d)
	}
	want := []DriftChange{
		{Path: "proxy/caddy.json", Kind: "removed", Before: `  "site": "one"`},
		{Path: "proxy/caddy.json", Kind: "added", After: `  "site": "two"`},
		{Path: "proxy/certificates", Kind: "added", After: "self"},
	}
	if fmt.Sprint(d.Changes) != fmt.Sprint(want) {
		t.Errorf("changes:\n%v\nwant\n%v", d.Changes, want)
	}
	if s := e.DriftStatus(); s == nil || s.Changes != 3 || s.Parts[0] != "Reverse proxy" {
		t.Errorf("status = %+v", s)
	}
	if _, err := e.Apply(context.Background(), cfg("a"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift after applying again: %+v", d)
	}
}

// The firewall's half is the saved ruleset, and a commit elsewhere, the
// command line's, is seen without waiting out the minute.
func TestTheSavedRulesetIsTheFirewallsHalf(t *testing.T) {
	t.Parallel()
	e, _, st := newDriftEngine(t)
	if d := mustDrift(t, e); d != nil {
		t.Fatalf("drift = %+v", d)
	}
	saved, err := st.LoadRuleset()
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	// What an earlier release would have saved: the same ruleset bar a line.
	older := strings.Replace(saved, "\n", "\n# an older release's line\n", 1)
	time.Sleep(10 * time.Millisecond) // a new modification time on coarse clocks
	if _, err := st.Save(c, older); err != nil {
		t.Fatal(err)
	}
	d := mustDrift(t, e)
	if d == nil || len(d.Parts) != 1 || d.Parts[0] != "Firewall" {
		t.Fatalf("drift = %+v", d)
	}
	if len(d.Changes) != 1 || d.Changes[0] != (DriftChange{Path: "firewall", Kind: "removed", Before: "# an older release's line"}) {
		t.Errorf("changes = %+v", d.Changes)
	}
}

// Fetched set elements reach the kernel with every refresh, not by an
// apply, so a list that moved since the apply is not a difference.
func TestARefreshedListIsNotADifference(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	feeds := &fakeFeeds{entries: map[string][]string{"drop": {"192.0.2.0/24"}}}
	e := New(st, &fakeRunner{}, nil, slog.New(slog.DiscardHandler)).WithFeeds(feeds)
	c := cfg("a")
	c.Aliases = append(c.Aliases, model.Alias{Name: "drop", Type: model.AliasHosts, URL: "https://lists.example.com/drop.txt"})
	if _, err := e.Apply(context.Background(), c, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	saved, err := st.LoadRuleset()
	if err != nil || !strings.Contains(saved, "192.0.2.0/24") {
		t.Fatalf("the list is not in the saved ruleset (%v):\n%s", err, saved)
	}
	feeds.set(map[string][]string{"drop": {"198.51.100.0/24"}})
	e.drifted = nil
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift = %+v", d)
	}
}

// While an apply awaits confirmation the router runs it, not the confirmed
// configuration, so nothing is set against the confirmed one; a revert
// brings back what differed before it.
func TestNothingIsListedWhileAnApplyWaits(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	rel.release(network.Files{"caddy.json": "{}\n"})
	e.drifted = nil
	if d := mustDrift(t, e); d == nil {
		t.Fatal("no drift before the apply")
	}
	if _, err := e.Apply(context.Background(), cfg("b"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift while pending: %+v", d)
	}
	if err := e.Revert(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := mustDrift(t, e); d == nil {
		t.Error("the drift before the apply did not come back after its revert")
	}
}

// A commit by an earlier release left no record of its render, so only the
// firewall can be set against it until the next apply.
func TestWithoutARecordOnlyTheFirewallIsCompared(t *testing.T) {
	t.Parallel()
	e, rel, st := newDriftEngine(t)
	if err := os.Remove(filepath.Join(st.Dir, store.RenderedFile)); err != nil {
		t.Fatal(err)
	}
	rel.release(network.Files{"caddy.json": "{}\n"})
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift = %+v", d)
	}
}

// A backend the committing process did not drive was not rendered, so it
// has nothing to be set against.
func TestABackendTheCommitDidNotDriveIsLeftOut(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	plain := New(st, &fakeRunner{}, nil, slog.New(slog.DiscardHandler))
	if _, err := plain.Apply(context.Background(), cfg("a"), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	rel := &releaseNet{render: network.Files{"caddy.json": "{}\n"}}
	e := New(st, &fakeRunner{}, nil, slog.New(slog.DiscardHandler)).WithServices(services.NewBundleOf(rel))
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift = %+v", d)
	}
}

func TestALongDriftIsCounted(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	var b strings.Builder
	for i := range maxDriftChanges + 50 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	rel.release(network.Files{"caddy.json": b.String()})
	e.drifted = nil
	d := mustDrift(t, e)
	// Three lines go and 250 come, the final newline among them.
	if d == nil || len(d.Changes) != maxDriftChanges || d.More != 3+maxDriftChanges+50-maxDriftChanges {
		t.Fatalf("changes %d, more %d", len(d.Changes), d.More)
	}
	if s := e.DriftStatus(); s.Changes != 3+maxDriftChanges+50 {
		t.Errorf("status counts %d", s.Changes)
	}
}

func TestALongLineIsCut(t *testing.T) {
	t.Parallel()
	if got := clip(strings.Repeat("é", maxDriftLine)); len(got) > maxDriftLine+len("…") || !strings.HasSuffix(got, "é…") {
		t.Errorf("clip = %d bytes, ends %q", len(got), got[len(got)-8:])
	}
}

// Follow puts in what this release renders for one service and notes it
// as rendered, so the drift stops listing it.
func TestFollowBringsAServiceInLine(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	next := network.Files{"caddy.json": "{\n  \"site\": \"two\"\n}\n"}
	rel.release(next)
	changed, err := e.Follow(context.Background(), rel)
	if err != nil || !changed {
		t.Fatalf("follow = %v, %v", changed, err)
	}
	if files, _ := rel.state(); !maps.Equal(files, next) {
		t.Errorf("files = %v", files)
	}
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift after following: %+v", d)
	}
	// Following again finds nothing to do.
	if changed, err := e.Follow(context.Background(), rel); err != nil || changed {
		t.Errorf("second follow = %v, %v", changed, err)
	}
}

// Files the service refuses go back to what they were, the drift still
// lists them, and the same render is not tried again.
func TestFollowPutsBackWhatTheServiceRefuses(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	before, _ := rel.state()
	rel.release(network.Files{"caddy.json": "{\"bad\": true}\n"})
	rel.refuse = "bad"
	if changed, err := e.Follow(context.Background(), rel); err == nil || changed {
		t.Fatalf("follow = %v, %v", changed, err)
	}
	files, applies := rel.state()
	if !maps.Equal(files, before) {
		t.Errorf("files after the refusal = %v, want %v", files, before)
	}
	e.drifted = nil
	if d := mustDrift(t, e); d == nil || d.Parts[0] != "Reverse proxy" {
		t.Errorf("drift = %+v", d)
	}
	if changed, err := e.Follow(context.Background(), rel); err != nil || changed {
		t.Errorf("second follow = %v, %v", changed, err)
	}
	if _, again := rel.state(); again != applies {
		t.Errorf("the refused render was tried again: %d applies, was %d", again, applies)
	}
	// Another render is.
	rel.release(network.Files{"caddy.json": "{\"good\": true}\n"})
	if changed, err := e.Follow(context.Background(), rel); err != nil || !changed {
		t.Errorf("follow of a new render = %v, %v", changed, err)
	}
}

func TestFollowWaitsForAPendingApply(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	if _, err := e.Apply(context.Background(), cfg("b"), ApplyOptions{ConfirmTimeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	rel.release(network.Files{"caddy.json": "{}\n"})
	_, applies := rel.state()
	if changed, err := e.Follow(context.Background(), rel); err != nil || changed {
		t.Errorf("follow = %v, %v", changed, err)
	}
	if _, after := rel.state(); after != applies {
		t.Error("followed while an apply waited")
	}
}

// A service the confirmed configuration does not run renders nothing, and
// following must not take away what is there.
func TestFollowLeavesAServiceThatRendersNothing(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	rel.release(network.Files{})
	if changed, err := e.Follow(context.Background(), rel); err != nil || changed {
		t.Errorf("follow = %v, %v", changed, err)
	}
	if files, _ := rel.state(); len(files) == 0 {
		t.Error("the service's files went")
	}
}

// fakeFeeds hands the renderer a list a test can move.
type fakeFeeds struct {
	mu      sync.Mutex
	entries map[string][]string
}

func (f *fakeFeeds) Entries() map[string][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.entries
}

func (f *fakeFeeds) set(entries map[string][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = entries
}

// A part is named by the pages its settings are on, once each, and one
// with no page keeps its own name.
func TestPartsAreNamedByTheirPages(t *testing.T) {
	t.Parallel()
	var b driftBuilder
	b.file("dnsmasq", "dnsmasq/ostiole.conf", "a\n", "b\n")
	b.file("unbound", "unbound/unbound.conf", "a\n", "b\n")
	b.file("spare", "spare/x", "a\n", "b\n")
	if got := fmt.Sprint(b.done().Parts); got != "[DHCP DNS spare]" {
		t.Errorf("parts = %s", got)
	}
}

// Files that already match this release, when the record says otherwise,
// are noted as rendered without being written again.
func TestFollowNotesFilesThatAlreadyMatch(t *testing.T) {
	t.Parallel()
	e, rel, _ := newDriftEngine(t)
	next := network.Files{"caddy.json": "{}\n"}
	rel.release(next)
	rel.mu.Lock()
	rel.files = maps.Clone(next)
	rel.mu.Unlock()
	e.drifted = nil
	if d := mustDrift(t, e); d == nil {
		t.Fatal("no drift against the record")
	}
	_, applies := rel.state()
	if changed, err := e.Follow(context.Background(), rel); err != nil || changed {
		t.Fatalf("follow = %v, %v", changed, err)
	}
	if _, after := rel.state(); after != applies {
		t.Error("files that matched were written again")
	}
	if d := mustDrift(t, e); d != nil {
		t.Errorf("drift after noting = %+v", d)
	}
}
