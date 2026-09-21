package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/tailscale"
)

// tailscaled paths and names.
const (
	TailscaleUnit = "ostiole-tailscaled.service"
	// TailnetDomain and MagicDNSAddress are where tailnet names are
	// answered. The daemon runs with --accept-dns=false, so dnsmasq is
	// what points at them.
	TailnetDomain   = model.TailnetDomain
	MagicDNSAddress = "100.100.100.100"
	// TailscaleCGNAT is the range every tailnet address comes out of, and
	// the reverse zone MagicDNS answers for.
	TailscaleCGNAT = "100.64.0.0/10"

	tailscaleDistroSvc = "tailscaled.service"
	tailscaleEnvName   = "env"
	tailscalePrefsName = "prefs"
)

// Tailscale runs tailscaled and pushes the preferences an apply decided
// (ADR-0014). The interface, the addresses and the routes are the daemon's;
// what is here is the unit's environment and the preference list.
//
// Logging in is not part of an apply: the node's key lives in tailscaled's
// own state file and never in the model, so an apply never runs `up` and
// never runs `logout`.
type Tailscale struct {
	// Dir holds the generated files, under the configuration directory.
	Dir string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
	// Client pushes the preferences; nil drives the installed binary.
	Client *tailscale.Client
}

var (
	_ network.Backend = (*Tailscale)(nil)
	_ Preflighter     = (*Tailscale)(nil)
)

// NewTailscale returns a backend writing under configDir.
func NewTailscale(configDir string) *Tailscale {
	return &Tailscale{Dir: TailscaleDir(configDir), Cmd: execCommander{}}
}

// TailscaleDir is where the generated files go for a given configuration
// directory. The unit reads the environment file from here.
func TailscaleDir(configDir string) string {
	if configDir == "" {
		configDir = store.DefaultDir
	}
	return filepath.Join(configDir, "tailscale")
}

func (t *Tailscale) cmd() network.Commander {
	if t.Cmd == nil {
		return execCommander{}
	}
	return t.Cmd
}

func (t *Tailscale) client() *tailscale.Client {
	if t.Client == nil {
		return tailscale.New()
	}
	return t.Client
}

func (t *Tailscale) path(name string) string { return filepath.Join(t.Dir, name) }

// Name implements network.Backend.
func (t *Tailscale) Name() string { return "tailscale" }

// EnvPath is the EnvironmentFile the unit reads.
func (t *Tailscale) EnvPath() string { return t.path(tailscaleEnvName) }

// Render implements network.Backend. The preferences are written out as
// well as pushed, so a snapshot can tell an apply that changed them from
// one that did not, and the golden test sees them.
func (t *Tailscale) Render(cfg *model.Config) (network.Files, error) {
	files := network.Files{}
	if !nft.TailscaleEnabled(cfg) {
		return files, nil
	}
	in, _ := cfg.TailscaleInterface()
	files[tailscaleEnvName] = tailscale.DaemonEnv(*in.Tailscale)
	files[tailscalePrefsName] = strings.Join(tailscale.PrefArgs(*in.Tailscale), "\n") + "\n"
	return files, nil
}

// Snapshot implements network.Backend.
func (t *Tailscale) Snapshot() (network.Files, error) {
	files := network.Files{}
	for _, name := range []string{tailscaleEnvName, tailscalePrefsName} {
		raw, err := os.ReadFile(t.path(name))
		switch {
		case err == nil:
			files[name] = string(raw)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, err
		}
	}
	return files, nil
}

// errTailscaleMissing is what an apply says when the daemon is not here.
// The script is what installs it, because a router that never asked for a
// tailnet has no reason to poll Tailscale's repository (ADR-0013).
var errTailscaleMissing = errors.New(
	"Tailscale is not on this router: run `ostiole repair --tailscale` once as root")

// Preflight implements Preflighter, so an apply that cannot work is
// refused before the ruleset has been touched.
func (t *Tailscale) Preflight(ctx context.Context, files network.Files) error {
	if _, wanted := files[tailscaleEnvName]; wanted && !t.Installed(ctx) {
		return errTailscaleMissing
	}
	return nil
}

// Apply implements network.Backend: write what changed, keep the unit in
// step, and push the preferences.
func (t *Tailscale) Apply(ctx context.Context, files network.Files) error {
	env, wanted := files[tailscaleEnvName]
	current, err := t.Snapshot()
	if err != nil {
		return err
	}
	if !wanted {
		// A router that never joined a tailnet gets no directory, no file
		// and no systemctl.
		if len(current) == 0 {
			return nil
		}
		if _, err := t.cmd().Run(ctx, "systemctl", "disable", "--now", TailscaleUnit); err != nil {
			return fmt.Errorf("stop %s: %w", TailscaleUnit, err)
		}
		for _, name := range []string{tailscaleEnvName, tailscalePrefsName} {
			if err := os.Remove(t.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	if !t.Installed(ctx) {
		return errTailscaleMissing
	}
	if err := os.MkdirAll(t.Dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{tailscaleEnvName, tailscalePrefsName} {
		if current[name] == files[name] {
			continue
		}
		if err := writeFile(t.path(name), files[name]); err != nil {
			return err
		}
	}
	if out, err := t.cmd().Run(ctx, "systemctl", "enable", "--now", TailscaleUnit); err != nil {
		return fmt.Errorf("enable %s: %w: %s", TailscaleUnit, err, strings.TrimSpace(string(out)))
	}
	// The port and the log flag are the unit's own arguments, so only a
	// change to those is worth dropping the tunnel for.
	if current[tailscaleEnvName] != env {
		if out, err := t.cmd().Run(ctx, "systemctl", "restart", TailscaleUnit); err != nil {
			return fmt.Errorf("restart %s: %w: %s", TailscaleUnit, err, strings.TrimSpace(string(out)))
		}
	}
	// `tailscale set` works before a login, so this does not wait for one.
	return t.client().Set(ctx, prefLines(files[tailscalePrefsName]))
}

// prefLines reads back the file Render wrote: one flag per line, so a value
// with a space in it stays one argument.
func prefLines(s string) []string {
	var args []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			args = append(args, line)
		}
	}
	return args
}

// Installed reports whether the ostiole-tailscaled unit exists.
func (t *Tailscale) Installed(ctx context.Context) bool {
	_, err := t.cmd().Run(ctx, "systemctl", "cat", TailscaleUnit)
	return err == nil
}

// Active reports whether the unit is running.
func (t *Tailscale) Active(ctx context.Context) bool {
	out, err := t.cmd().Run(ctx, "systemctl", "is-active", TailscaleUnit)
	return err == nil && strings.TrimSpace(string(out)) == "active"
}
