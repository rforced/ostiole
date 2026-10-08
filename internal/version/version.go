// Package version holds build metadata injected at link time.
package version

import "fmt"

// These are overridden via -ldflags "-X ostiole/internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Agent is the User-Agent every request leaving the router carries. The
// version stays off it: like the one /health keeps from strangers, it
// would tell a publisher's or a CA's logs which bugs this router has.
const Agent = "ostiole"

// String returns a human-readable version line.
func String() string {
	return fmt.Sprintf("ostiole %s (commit %s, built %s)", Version, Commit, Date)
}
