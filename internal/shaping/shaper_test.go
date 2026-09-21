package shaping

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/network"
)

// events records what the fakes were asked to do, in order, so a test can
// say not just that something happened but that it happened first.
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, fmt.Sprintf(format, args...))
}

func (e *events) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.list...)
}

func (e *events) index(prefix string) int {
	for i, s := range e.all() {
		if strings.HasPrefix(s, prefix) {
			return i
		}
	}
	return -1
}

type fakeRunner struct {
	ev *events
	// filters answers FiltersJSON per device; a device with no entry
	// answers with a redirect to its own helper, which is the usual case.
	filters map[string]string
	err     error
}

func (f *fakeRunner) Batch(_ context.Context, script string) error {
	for _, line := range strings.Split(strings.TrimSpace(stripComments(script)), "\n") {
		if line != "" {
			f.ev.add("tc %s", line)
		}
	}
	return f.err
}

func (f *fakeRunner) QdiscsJSON(context.Context) ([]byte, error) { return []byte("[]"), nil }

func (f *fakeRunner) FiltersJSON(_ context.Context, dev, _ string) ([]byte, error) {
	if raw, ok := f.filters[dev]; ok {
		return []byte(raw), nil
	}
	return fmt.Appendf(nil,
		`[{"options":{"actions":[{"kind":"mirred","to_dev":%q}]}}]`, IFBName(dev)), nil
}

func (f *fakeRunner) Version(context.Context) (string, error) {
	return "tc utility, iproute2-6.17.0", nil
}

type fakeKernel struct {
	ev      *events
	links   map[string]bool
	qdiscs  map[string][]Qdisc
	ifbs    []string
	failIFB error
}

func (k *fakeKernel) LinkExists(name string) (bool, error) { return k.links[name], nil }

func (k *fakeKernel) Qdiscs(dev string) ([]Qdisc, error) { return k.qdiscs[dev], nil }

func (k *fakeKernel) EnsureIFB(name string) error {
	if k.failIFB != nil {
		return k.failIFB
	}
	k.ev.add("ifb up %s", name)
	k.links[name] = true
	return nil
}

func (k *fakeKernel) DeleteLink(name string) error {
	k.ev.add("ifb del %s", name)
	delete(k.links, name)
	return nil
}

func (k *fakeKernel) IFBs() ([]string, error) { return k.ifbs, nil }

// ours is what a device looks like once the batch has run.
var ours = Qdisc{Type: "cake", Handle: rootHandle, Parent: rootParent}
var ingress = Qdisc{Type: "ingress", Handle: ingressHandle, Parent: ingressParent}

func newShaper(t *testing.T) (*Shaper, *fakeRunner, *fakeKernel, *events) {
	t.Helper()
	ev := &events{}
	run := &fakeRunner{ev: ev, filters: map[string]string{}}
	kern := &fakeKernel{ev: ev, links: map[string]bool{}, qdiscs: map[string][]Qdisc{}}
	s := &Shaper{
		Dir: t.TempDir(), Bin: "tc", Run: run, Kernel: kern,
		Log: slog.New(slog.DiscardHandler),
	}
	return s, run, kern, ev
}

func render(t *testing.T, path string) network.Files {
	t.Helper()
	files, err := Render(loadConfig(t, path))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// A first apply on a bare router installs everything and creates the
// helper device for the direction that needs one.
func TestApplyInstallsEverything(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true

	if err := s.Apply(t.Context(), render(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ifb up ifb-eth0",
		"tc qdisc replace dev eth0 root handle 571: cake bandwidth 20000000bit",
		"tc qdisc replace dev ifb-eth0 root handle 571: cake bandwidth 200000000bit ingress",
		"tc qdisc replace dev eth1 root handle 571: cake bandwidth 50000000bit",
	} {
		if ev.index(want) < 0 {
			t.Errorf("never happened: %q\ngot %v", want, ev.all())
		}
	}
	// eth1 is shaped one way only, so it needs no helper.
	if ev.index("ifb up ifb-eth1") >= 0 {
		t.Errorf("a helper was made for an interface shaped in one direction: %v", ev.all())
	}
	// The files are the snapshot a revert puts back.
	files, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(files), 3; got != want {
		t.Errorf("snapshot has %d files, want %d: %v", got, want, files.Names())
	}
}

// A tick with nothing wrong runs no commands at all: it is the price of
// every five seconds for the life of the router.
func TestSyncOnAHealthyRouterRunsNothing(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true
	kern.qdiscs["eth0"] = []Qdisc{ours, ingress}
	kern.qdiscs["ifb-eth0"] = []Qdisc{ours}
	kern.qdiscs["eth1"] = []Qdisc{ours}

	if err := s.Sync(loadConfig(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	for _, e := range ev.all() {
		if strings.HasPrefix(e, "tc ") {
			t.Errorf("a healthy tick ran a command: %v", ev.all())
			break
		}
	}
}

// A router with nothing shaped never asks the kernel anything.
func TestSyncWithNothingShapedIsFree(t *testing.T) {
	t.Parallel()
	s, _, _, ev := newShaper(t)
	if err := s.Sync(loadConfig(t, "../nft/testdata/minimal.json")); err != nil {
		t.Fatal(err)
	}
	if len(ev.all()) != 0 {
		t.Errorf("a router that shapes nothing did work: %v", ev.all())
	}
}

// A link that is not there yet is not an error. A dialled session comes up
// minutes after boot and the next tick finds it.
func TestLinkThatArrivesLaterIsInstalledThen(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	files := render(t, "testdata/pair.json")
	kern.links["eth1"] = true

	if err := s.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if ev.index("tc qdisc replace dev eth0") >= 0 {
		t.Errorf("shaping was installed on a link that does not exist: %v", ev.all())
	}
	if ev.index("tc qdisc replace dev eth1") < 0 {
		t.Errorf("the link that is here was left unshaped: %v", ev.all())
	}

	kern.links["eth0"] = true
	kern.qdiscs["eth1"] = []Qdisc{ours}
	if err := s.Sync(loadConfig(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	if ev.index("tc qdisc replace dev eth0") < 0 {
		t.Errorf("the link that turned up was never shaped: %v", ev.all())
	}
}

// Dropping the shaped direction that needs a helper takes the hook down
// before the device it feeds. The other order would leave a redirect
// pointing at nothing, and the kernel drops every packet such a redirect
// touches.
func TestIngressHookGoesBeforeItsHelper(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"], kern.links["ifb-eth0"] = true, true, true
	kern.qdiscs["eth0"] = []Qdisc{ours, ingress}
	kern.qdiscs["ifb-eth0"] = []Qdisc{ours}
	kern.ifbs = []string{"ifb-eth0"}

	if err := s.Apply(t.Context(), render(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	// Now the WAN is shaped outwards only: the hook and the helper both go.
	if err := s.Apply(t.Context(), render(t, "testdata/upload-only.json")); err != nil {
		t.Fatal(err)
	}
	hook := ev.index("tc qdisc del dev eth0 handle ffff: ingress")
	helper := ev.index("ifb del ifb-eth0")
	switch {
	case hook < 0:
		t.Errorf("the ingress hook was left behind: %v", ev.all())
	case helper < 0:
		t.Errorf("the helper device was left behind: %v", ev.all())
	case hook > helper:
		t.Errorf("the helper went before the hook that feeds it: %v", ev.all())
	}
}

// An ingress hook somebody else is now using is left where it is, and so
// is the device it feeds.
func TestAForeignIngressHookIsLeftAlone(t *testing.T) {
	t.Parallel()
	s, run, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true
	kern.qdiscs["eth0"] = []Qdisc{ours, ingress}
	if err := s.Apply(t.Context(), render(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	run.filters["eth0"] = `[{"options":{"actions":[{"kind":"mirred","to_dev":"ifb0"}]}}]`

	if err := s.Apply(t.Context(), render(t, "testdata/upload-only.json")); err != nil {
		t.Fatal(err)
	}
	if ev.index("tc qdisc del dev eth0 handle ffff: ingress") >= 0 {
		t.Errorf("an ingress hook feeding somebody else's device was removed: %v", ev.all())
	}
}

// Taking an interface out of the configuration takes its queue with it,
// leaving whatever the kernel puts there by default to come back.
func TestDroppedInterfaceLosesItsQueue(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true
	if err := s.Apply(t.Context(), render(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	kern.qdiscs["eth0"] = []Qdisc{ours, ingress}
	kern.qdiscs["ifb-eth0"] = []Qdisc{ours}
	kern.qdiscs["eth1"] = []Qdisc{ours}
	kern.ifbs = []string{"ifb-eth0"}

	if err := s.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tc qdisc del dev eth0 handle ffff: ingress",
		"tc qdisc del dev eth0 root",
		"tc qdisc del dev eth1 root",
		"ifb del ifb-eth0",
	} {
		if ev.index(want) < 0 {
			t.Errorf("never happened: %q\ngot %v", want, ev.all())
		}
	}
	files, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("files left behind: %v", files.Names())
	}
	if _, err := os.Stat(s.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an empty directory was left behind at %s", s.Dir)
	}
}

// A revert is an apply of what was there before, so the files have to
// round trip through the disk unchanged.
func TestRevertPutsTheOldFilesBack(t *testing.T) {
	t.Parallel()
	s, _, kern, _ := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true
	before := render(t, "testdata/pair.json")
	if err := s.Apply(t.Context(), before); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(t.Context(), render(t, "testdata/upload-only.json")); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	back, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != before.String() {
		t.Errorf("revert did not restore the files\n--- got ---\n%s\n--- want ---\n%s", back, before)
	}
}

// Uninstalling leaves the router as it was found.
func TestClearRemovesEverything(t *testing.T) {
	t.Parallel()
	s, _, kern, ev := newShaper(t)
	kern.links["eth0"], kern.links["eth1"] = true, true
	if err := s.Apply(t.Context(), render(t, "testdata/pair.json")); err != nil {
		t.Fatal(err)
	}
	kern.qdiscs["eth0"] = []Qdisc{ours, ingress}
	kern.qdiscs["eth1"] = []Qdisc{ours}
	kern.ifbs = []string{"ifb-eth0"}

	if err := s.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ev.index("ifb del ifb-eth0") < 0 {
		t.Errorf("the helper device survived an uninstall: %v", ev.all())
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "eth0.egress.tc")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the batches survived an uninstall")
	}
}

// An apply that asks for shaping on a router with no tc is refused before
// anything is saved, rather than accepted and quietly doing nothing.
func TestPreflightNamesThePackage(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newShaper(t)
	s.Bin, s.PackageManager = "ostiole-no-such-binary", "dnf"

	if err := s.Preflight(t.Context(), nil); err != nil {
		t.Errorf("an apply that shapes nothing was refused: %v", err)
	}
	err := s.Preflight(t.Context(), render(t, "testdata/pair.json"))
	if err == nil {
		t.Fatal("an apply that needs tc was allowed without it")
	}
	if !strings.Contains(err.Error(), "iproute-tc") {
		t.Errorf("the error does not say what to install: %v", err)
	}
}
