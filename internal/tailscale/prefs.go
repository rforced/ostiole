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

// pref is one preference and whether `tailscale up` defines it as well.
// up refuses a flag it has never heard of, so the three it does not take
// are pushed with `set` once the login is through.
type pref struct {
	arg string
	up  bool
}

// prefs is the whole preference list, every flag every time. `tailscale
// set` leaves a flag it was not given alone and `up --reset` puts one back
// to its default, so a list that only wrote what changed would drift;
// writing the lot makes an apply deterministic and a revert a replay.
//
// Ostiole renders every rule, so the daemon touches no netfilter and no
// resolv.conf of its own. --exit-node is cleared rather than omitted for
// the same reason: using one is shelved (ADR-0014), and a node pointed at
// one by hand would otherwise keep it.
func prefs(t model.Tailscale) []pref {
	return []pref{
		{"--netfilter-mode=off", true},
		{"--accept-dns=false", true},
		{"--snat-subnet-routes=false", true},
		{"--stateful-filtering=false", true},
		{"--ssh=false", true},
		{"--auto-update=false", false},
		{"--update-check=false", false},
		{"--webclient=false", false},
		{"--shields-up=false", true},
		{"--advertise-connector=false", true},
		{"--report-posture=false", true},
		{"--exit-node=", true},
		{"--hostname=" + t.Hostname, true},
		{"--advertise-routes=" + strings.Join(t.AdvertiseRoutes, ","), true},
		{fmt.Sprintf("--advertise-exit-node=%t", t.AdvertiseExitNode), true},
		{fmt.Sprintf("--accept-routes=%t", t.AcceptRoutes), true},
	}
}

// PrefArgs is what `tailscale set` is given: everything.
func PrefArgs(t model.Tailscale) []string {
	args := make([]string, 0, len(prefs(t)))
	for _, p := range prefs(t) {
		args = append(args, p.arg)
	}
	return args
}

// UpArgs is what `tailscale up` is given: the flags it defines. The rest
// follow in a `set` once the node is up.
func UpArgs(t model.Tailscale) []string {
	var args []string
	for _, p := range prefs(t) {
		if p.up {
			args = append(args, p.arg)
		}
	}
	return args
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
