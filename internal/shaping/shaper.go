package shaping

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// DirName is where the rendered batches live under the configuration
// directory, beside the dialled sessions' secrets and the feed cache.
const DirName = "shaping"

// SyncTimeout bounds one reconcile from the monitor's tick. A healthy
// pass runs no commands at all, so anything that takes longer than this
// is a kernel that is not answering.
const SyncTimeout = 10 * time.Second

// Shaper installs the queues and takes them away again. It is the
// engine's fourth backend and the gateway monitor's reconciler at once,
// which is why it holds a lock: an apply and a tick must not both be
// talking to the kernel about the same device.
type Shaper struct {
	// Dir is where the rendered batches are kept.
	Dir string
	// Bin is the tc executable, for the message when there is not one.
	Bin string
	// PackageManager names the host's package manager, so the message
	// that tc is missing can name the package to install.
	PackageManager string
	// Run drives tc; Kernel is the netlink half.
	Run    Runner
	Kernel Kernel
	Log    *slog.Logger

	mu sync.Mutex
}

var _ network.Backend = (*Shaper)(nil)

// New returns a shaper with production defaults, writing under configDir.
func New(configDir, bin, pm string, log *slog.Logger) *Shaper {
	return &Shaper{
		Dir:            filepath.Join(configDir, DirName),
		Bin:            bin,
		PackageManager: pm,
		Run:            &Exec{Bin: bin},
		Kernel:         Netlink{},
		Log:            log,
	}
}

func (s *Shaper) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// Name implements network.Backend.
func (s *Shaper) Name() string { return "shaping" }

// Render implements network.Backend.
func (s *Shaper) Render(cfg *model.Config) (network.Files, error) { return Render(cfg) }

// Snapshot implements network.Backend: what the kernel was last told,
// which is what a revert puts back.
func (s *Shaper) Snapshot() (network.Files, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

// Apply implements network.Backend. The files are written first so that a
// crash between here and the next tick leaves the tick something to
// converge on, and the kernel is then made to agree with them whatever it
// had before.
func (s *Shaper) Apply(ctx context.Context, files network.Files) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	had, err := s.read()
	if err != nil {
		return err
	}
	if err := s.write(files); err != nil {
		return err
	}
	return s.reconcile(ctx, files, had, true)
}

// Preflight refuses an apply that cannot work. Without it a configuration
// asking for shaping on a router with no tc would be accepted, saved, and
// quietly do nothing.
func (s *Shaper) Preflight(_ context.Context, files network.Files) error {
	if len(files) == 0 {
		return nil
	}
	if _, missing := s.Missing(); !missing {
		return nil
	}
	return errors.New(MissingMessage(s.PackageManager))
}

// Missing reports whether the command that installs the queues is absent
// and, when it is, the package that carries it on this host. It is a path
// lookup, which is cheap enough to ask on every dashboard poll.
func (s *Shaper) Missing() (pkg string, missing bool) {
	if _, ok := Available(s.Bin); ok {
		return "", false
	}
	return TCPackage(s.PackageManager), true
}

// Sync puts back anything that has gone missing since the last apply. It
// is what covers a link that did not exist yet at apply time: a dialled
// session that had not come up, an interface that was unplugged, a helper
// device somebody removed by hand. A pass with nothing to do runs no
// commands and only asks netlink what is already there.
func (s *Shaper) Sync(cfg *model.Config) error {
	files, err := Render(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(files) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), SyncTimeout)
	defer cancel()
	return s.reconcile(ctx, files, files, false)
}

// Clear takes every queue and helper device away and forgets the files.
// It is the uninstall path.
func (s *Shaper) Clear(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	had, err := s.read()
	if err != nil {
		return err
	}
	rerr := s.reconcile(ctx, nil, had, true)
	return errors.Join(rerr, s.write(nil))
}

// ---- reconcile ---------------------------------------------------------

// reconcile makes the kernel agree with want, given that had is what it
// was last told.
//
// Taking down comes before putting up, and within that the ingress hook
// comes before the helper device it feeds: a mirred redirect whose target
// has gone does not fall back to delivering the packet, it drops it, so
// removing the device first would take every arriving packet with it for
// as long as the hook survived.
func (s *Shaper) reconcile(ctx context.Context, want, had network.Files, force bool) error {
	var errs []error
	// Helper devices that must stay because something still points at
	// them, whatever the configuration says.
	blocked := map[string]bool{}
	for _, dev := range Devices(had) {
		wantEgress, wantIngress := wanted(want, dev)
		hadEgress, hadIngress := wanted(had, dev)
		if hadIngress && !wantIngress {
			if err := s.dropIngress(ctx, dev); err != nil {
				errs = append(errs, err)
				blocked[IFBName(dev)] = true
			}
		}
		if hadEgress && !wantEgress {
			if err := s.dropRoot(ctx, dev); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// Sweeping means dumping every link, so it is done when something has
	// actually been taken away rather than on every tick.
	if force {
		if err := s.sweepIFBs(want, had, blocked); err != nil {
			errs = append(errs, err)
		}
	}
	for _, dev := range Devices(want) {
		if err := s.install(ctx, want, dev, force); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// install puts one interface's queues in place, if they are not already.
// A link that does not exist yet is not an error: a dialled session comes
// up minutes after boot, and the next tick will find it.
func (s *Shaper) install(ctx context.Context, want network.Files, dev string, force bool) error {
	exists, err := s.Kernel.LinkExists(dev)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	_, wantIngress := wanted(want, dev)
	if wantIngress {
		if err := s.Kernel.EnsureIFB(IFBName(dev)); err != nil {
			return err
		}
	}
	if !force {
		need, err := s.missing(want, dev)
		if err != nil || !need {
			return err
		}
	}
	if err := s.Run.Batch(ctx, BatchFor(want, dev)); err != nil {
		return fmt.Errorf("install shaping on %s: %w", dev, err)
	}
	s.log().Info("traffic shaping installed", "interface", dev)
	return nil
}

// missing reports whether any part of an interface's shaping is absent.
// Every command in a batch replaces rather than adds, so the cheapest
// correct answer to "is it still there" is to put the whole batch back.
func (s *Shaper) missing(want network.Files, dev string) (bool, error) {
	wantEgress, wantIngress := wanted(want, dev)
	qs, err := s.Kernel.Qdiscs(dev)
	if err != nil {
		return false, err
	}
	if wantEgress && !hasOurRoot(qs) {
		return true, nil
	}
	if !wantIngress {
		return false, nil
	}
	if !hasIngress(qs) {
		return true, nil
	}
	helper, err := s.Kernel.Qdiscs(IFBName(dev))
	if err != nil {
		return false, err
	}
	return !hasOurRoot(helper), nil
}

// dropIngress removes the hook arriving packets are taken off, when it is
// still ours to remove.
func (s *Shaper) dropIngress(ctx context.Context, dev string) error {
	qs, err := s.Kernel.Qdiscs(dev)
	if err != nil {
		return err
	}
	if !hasIngress(qs) {
		return nil
	}
	ours, err := s.ingressIsOurs(ctx, dev)
	if err != nil {
		return err
	}
	if !ours {
		s.log().Warn("leaving an ingress hook alone: something else is using it now", "interface", dev)
		return nil
	}
	if err := s.Run.Batch(ctx, fmt.Sprintf("qdisc del dev %s handle ffff: ingress\n", dev)); err != nil {
		return fmt.Errorf("remove the ingress hook on %s: %w", dev, err)
	}
	return nil
}

// dropRoot removes our own queue from a device, leaving whatever the
// kernel puts there by default to come back.
func (s *Shaper) dropRoot(ctx context.Context, dev string) error {
	qs, err := s.Kernel.Qdiscs(dev)
	if err != nil {
		return err
	}
	if !hasOurRoot(qs) {
		return nil
	}
	if err := s.Run.Batch(ctx, fmt.Sprintf("qdisc del dev %s root\n", dev)); err != nil {
		return fmt.Errorf("remove the queue on %s: %w", dev, err)
	}
	return nil
}

// ingressIsOurs reports whether the ingress hook on dev still hands
// packets to our helper device, or hands them to nobody. A hook feeding
// somebody else's device is theirs and is left where it is.
func (s *Shaper) ingressIsOurs(ctx context.Context, dev string) (bool, error) {
	raw, err := s.Run.FiltersJSON(ctx, dev, "ffff:")
	if err != nil {
		return false, fmt.Errorf("read the ingress filters on %s: %w", dev, err)
	}
	var doc []struct {
		Options struct {
			Actions []map[string]any `json:"actions"`
		} `json:"options"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("parse the ingress filters on %s: %w", dev, err)
	}
	ifb, redirects := IFBName(dev), false
	for _, f := range doc {
		for _, action := range f.Options.Actions {
			if kind, _ := action["kind"].(string); kind != "mirred" {
				continue
			}
			redirects = true
			// tc has named the target device differently between
			// releases, so any string in the action that is our helper
			// counts; nothing else on the box carries that name.
			for _, v := range action {
				if name, ok := v.(string); ok && name == ifb {
					return true, nil
				}
			}
		}
	}
	return !redirects, nil
}

// sweepIFBs removes the helper devices nobody wants any more. Ownership
// takes all three markers: the name, the kind of device, and the handle
// on its root. A device from a half-finished apply carries only the first
// two, so the interfaces we have written files for are swept as well.
func (s *Shaper) sweepIFBs(want, had network.Files, blocked map[string]bool) error {
	keep := map[string]bool{}
	for _, dev := range Devices(want) {
		if _, ingress := wanted(want, dev); ingress {
			keep[IFBName(dev)] = true
		}
	}
	candidates := map[string]bool{}
	owned, err := s.Kernel.IFBs()
	if err != nil {
		return err
	}
	for _, name := range owned {
		candidates[name] = true
	}
	for _, dev := range Devices(had) {
		if _, ingress := wanted(had, dev); ingress {
			candidates[IFBName(dev)] = true
		}
	}

	var errs []error
	for _, name := range sortedKeys(candidates) {
		if keep[name] || blocked[name] {
			continue
		}
		if err := s.Kernel.DeleteLink(name); err != nil {
			errs = append(errs, err)
			continue
		}
		s.log().Info("shaping helper device removed", "device", name)
	}
	return errors.Join(errs...)
}

// ---- files -------------------------------------------------------------

func (s *Shaper) dir() string {
	if s.Dir == "" {
		return DirName
	}
	return s.Dir
}

// read returns the batches on disk.
func (s *Shaper) read() (network.Files, error) {
	entries, err := os.ReadDir(s.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return network.Files{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.dir(), err)
	}
	out := network.Files{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tc") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir(), e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out, nil
}

// write installs exactly these files, removing the ones left over from
// before. The directory is only created when there is something to put in
// it, so a router that shapes nothing has nothing to explain.
func (s *Shaper) write(files network.Files) error {
	var errs []error
	if len(files) > 0 {
		if err := os.MkdirAll(s.dir(), 0o700); err != nil {
			return fmt.Errorf("create %s: %w", s.dir(), err)
		}
		for _, name := range files.Names() {
			path := filepath.Join(s.dir(), name)
			if err := os.WriteFile(path, []byte(files[name]), 0o600); err != nil {
				errs = append(errs, fmt.Errorf("write %s: %w", path, err))
			}
		}
	}
	had, err := s.read()
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, name := range had.Names() {
		if _, keep := files[name]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir(), name)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(files) == 0 {
		// Nothing is shaped: leave no empty directory behind. A directory
		// that still has something in it is somebody else's.
		_ = os.Remove(s.dir())
	}
	return errors.Join(errs...)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
