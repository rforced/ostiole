// Package version holds build metadata injected at link time.
package version

import "fmt"

// These are overridden via -ldflags "-X github.com/rforced/ostiole/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable version line.
func String() string {
	return fmt.Sprintf("ostiole %s (commit %s, built %s)", Version, Commit, Date)
}
