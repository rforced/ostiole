// Package network renders and applies interface configuration (addresses,
// VLANs, routes) through a pluggable backend, and reads live link state
// from netlink.
package network

import (
	"context"
	"sort"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// Files is a set of configuration files keyed by name relative to the
// backend's directory.
type Files map[string]string

// Names returns the file names in sorted order.
func (f Files) Names() []string {
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// String concatenates the files in name order; used for goldens and diffs.
func (f Files) String() string {
	var b strings.Builder
	for _, n := range f.Names() {
		b.WriteString("==> " + n + " <==\n")
		b.WriteString(f[n])
		if !strings.HasSuffix(f[n], "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Backend turns the model into system network configuration.
type Backend interface {
	// Name identifies the backend, e.g. "systemd-networkd".
	Name() string
	// Render produces the files for cfg without touching the system.
	Render(cfg *model.Config) (Files, error)
	// Snapshot returns the Ostiole-owned files currently on the system so
	// they can be restored by Apply on revert.
	Snapshot() (Files, error)
	// Apply installs exactly the given files (removing stale Ostiole-owned
	// ones) and tells the network stack to pick them up.
	Apply(ctx context.Context, files Files) error
}

// Commander runs external commands; swapped out in tests.
type Commander interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}
