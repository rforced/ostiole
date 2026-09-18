// Package tailscale drives the tailscale command line: the preference list
// an apply pushes, the environment the daemon starts with, and the status
// the UI shows. Nothing here imports tailscale.com; the binary is pure Go
// and the daemon is the distribution's (ADR-0014).
package tailscale

import (
	"fmt"
	"strings"

	"github.com/rforced/ostiole/internal/model"
)

// DefaultPort is what the unit falls back to when the model asks for none,
// so the daemon does not pick a new port at every restart.
const DefaultPort = 41641

// PrefArgs is the whole preference list, every flag every time. `tailscale
// set` leaves a flag it was not given alone, so a configuration that only
// wrote what changed would keep whatever a previous one set; writing the
// lot makes an apply deterministic and a revert a replay.
//
// Ostiole renders every rule, so the daemon touches no netfilter and no
// resolv.conf of its own. --exit-node is cleared rather than omitted for
// the same reason: using one is shelved (ADR-0014), and a node that was
// pointed at one by hand would otherwise keep it.
func PrefArgs(t model.Tailscale) []string {
	return []string{
		"--netfilter-mode=off",
		"--accept-dns=false",
		"--snat-subnet-routes=false",
		"--stateful-filtering=false",
		"--ssh=false",
		"--auto-update=false",
		"--update-check=false",
		"--webclient=false",
		"--shields-up=false",
		"--advertise-connector=false",
		"--report-posture=false",
		"--exit-node=",
		"--hostname=" + t.Hostname,
		"--advertise-routes=" + strings.Join(t.AdvertiseRoutes, ","),
		fmt.Sprintf("--advertise-exit-node=%t", t.AdvertiseExitNode),
		fmt.Sprintf("--accept-routes=%t", t.AcceptRoutes),
	}
}

// DaemonEnv is the EnvironmentFile the unit reads. The port is fixed so
// peers keep finding this router directly, and the logs stay here unless
// somebody asks for support.
func DaemonEnv(t model.Tailscale) string {
	port := t.Port
	if port == 0 {
		port = DefaultPort
	}
	flags := "--no-logs-no-support"
	if t.LogUploads {
		flags = ""
	}
	return fmt.Sprintf("PORT=%d\nFLAGS=%s\n", port, flags)
}
