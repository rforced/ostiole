package services

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/atomicfile"
	"github.com/rforced/ostiole/internal/dnsblock"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// Names of the two files this backend owns, both in dnsmasq's own
// directory so SELinux labels them the way dnsmasq_t expects.
const (
	// BlockConfName holds the entries dnsmasq reads. It can be tens of
	// megabytes, so it is written straight from the cache and never kept
	// in the model, in a revision, or in an apply diff.
	BlockConfName = "ostiole.block.conf"
	// blockStateName holds the inputs that produced it. This is the small,
	// model-derived file that does travel through apply, so an apply can
	// be diffed, reverted, and re-rendered without the configuration
	// being to hand.
	blockStateName = "ostiole.block.json"
)

// DNSBlock installs the DNS blocklist. It is a network.Backend so the
// engine applies and reverts it with everything else, and a
// dnsblock.Loader so the refresher can install a new list between applies.
//
// It runs before the dnsmasq backend in the bundle: dnsmasq refuses to
// start if a file it is told to include is missing.
type DNSBlock struct {
	// Dir holds the generated files; default DefaultDir.
	Dir string
	// Cache holds the names each list gave us.
	Cache *dnsblock.Cache
	// Max is the ceiling on merged names; zero means the default.
	Max int
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
}

// NewDNSBlock returns a backend with production defaults.
func NewDNSBlock(cache *dnsblock.Cache) *DNSBlock {
	return &DNSBlock{Dir: DefaultDir, Cache: cache, Cmd: execCommander{}}
}

func (d *DNSBlock) dir() string {
	if d.Dir == "" {
		return DefaultDir
	}
	return d.Dir
}

func (d *DNSBlock) cmd() network.Commander {
	if d.Cmd == nil {
		return execCommander{}
	}
	return d.Cmd
}

// ConfPath is the include file dnsmasq reads.
func (d *DNSBlock) ConfPath() string { return filepath.Join(d.dir(), BlockConfName) }

func (d *DNSBlock) statePath() string { return filepath.Join(d.dir(), blockStateName) }

// Name implements network.Backend.
func (d *DNSBlock) Name() string { return "dnsblock" }

// Render implements network.Backend. It emits only the inputs: the names
// themselves are in the cache and are written by Apply.
//
// The include file exists whenever dnsmasq is answering queries, empty or
// not, so that turning blocking on and off never rewrites dnsmasq's own
// configuration and never costs a second restart.
func (d *DNSBlock) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	if !cfg.Services.DNS.Enabled {
		return files, nil
	}
	o := dnsblock.OptionsFor(cfg)
	if d.Max > 0 {
		o.Max = d.Max
	}
	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	files[blockStateName] = string(raw) + "\n"
	return files, nil
}

// Snapshot implements network.Backend.
func (d *DNSBlock) Snapshot() (network.Files, error) {
	files := network.Files{}
	raw, err := os.ReadFile(d.statePath())
	switch {
	case err == nil:
		files[blockStateName] = string(raw)
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	return files, nil
}

// Apply implements network.Backend: record the inputs, then render the
// names from the cache and reload dnsmasq if what it reads changed.
func (d *DNSBlock) Apply(ctx context.Context, files network.Files) error {
	state, wanted := files[blockStateName]
	if !wanted {
		// DNS is off. Leave nothing behind for a later dnsmasq to read.
		var err error
		for _, p := range []string{d.statePath(), d.ConfPath()} {
			if rerr := os.Remove(p); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				err = rerr
			}
		}
		return err
	}
	var o dnsblock.Options
	if err := json.Unmarshal([]byte(state), &o); err != nil {
		return fmt.Errorf("read the blocking settings: %w", err)
	}
	if err := os.MkdirAll(d.dir(), 0o755); err != nil { //nolint:gosec // dnsmasq reads these unprivileged
		return err
	}
	if err := writeFile(d.statePath(), state); err != nil {
		return err
	}
	var res dnsblock.Result
	if _, err := d.Load(ctx, func(w io.Writer) error {
		var rerr error
		res, rerr = dnsblock.Render(w, o, d.Cache)
		return rerr
	}); err != nil {
		return err
	}
	// What the merge came to is what the UI compares against the ceiling.
	return d.Cache.SaveMerged(res)
}

// Load implements dnsblock.Loader: render through fn, and if what dnsmasq
// would read has changed, put it in place and restart dnsmasq.
//
// A restart is how dnsmasq picks up configuration entries — it re-reads
// hosts files on a signal but not these. It costs about four tenths of a
// second with a quarter of a million names (ADR-0005), which is why the
// contents are compared first: a daily refresh of a list that has not moved
// must not interrupt DNS at all.
func (d *DNSBlock) Load(ctx context.Context, fn func(io.Writer) error) (bool, error) {
	if err := os.MkdirAll(d.dir(), 0o755); err != nil { //nolint:gosec // dnsmasq reads these unprivileged
		return false, err
	}
	f, err := atomicfile.Create(d.ConfPath(), 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	sum := sha256.New()
	bw := bufio.NewWriterSize(io.MultiWriter(f, sum), 64<<10)
	if err := fn(bw); err != nil {
		return false, err
	}
	if err := bw.Flush(); err != nil {
		return false, err
	}
	fresh := hex.EncodeToString(sum.Sum(nil))

	if current, err := sumFile(d.ConfPath()); err == nil && current == fresh {
		return false, nil
	}
	if err := f.Commit(); err != nil {
		return false, err
	}
	return true, d.reload(ctx)
}

// reload restarts dnsmasq, but only if it is already running: at first
// apply it has not been started yet, and the apply that follows starts it.
func (d *DNSBlock) reload(ctx context.Context) error {
	if !d.installed(ctx) {
		return nil
	}
	if out, err := d.cmd().Run(ctx, "systemctl", "try-restart", Unit); err != nil {
		return fmt.Errorf("restart %s: %w: %s", Unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *DNSBlock) installed(ctx context.Context) bool {
	_, err := d.cmd().Run(ctx, "systemctl", "cat", Unit)
	return err == nil
}

// sumFile is the checksum of a file that may not be there.
func sumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
