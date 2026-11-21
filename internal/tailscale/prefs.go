// Package tailscale drives the tailscale command line: the preference list
// an apply pushes, the environment the daemon starts with, and the status
// the UI shows. Nothing here imports tailscale.com; the binary is pure Go
// and the daemon is the distribution's (ADR-0014).
package tailscale

import (
	"fmt"
	"strings"

	"ostiole/internal/model"
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
// resolv.conf of its own. Source NAT stays on: with netfilter off the flag
// writes nothing, and off makes tailscaled warn on every node that offers
// an exit node.
//
// When exitNode is empty, --exit-node and --exit-node-allow-lan-access are
// cleared explicitly so removing a gateway, reverting an apply, or a node
// pointed at an exit node by hand never leaves a stale exit node behind.
// When exitNode is set, both flags are omitted here: the gateway monitor
// sets --exit-node=<name> and --exit-node-allow-lan-access=true together
// once tailscaled is Running (tailscale refuses allow-lan-access=true when
// no exit node is set yet), and `tailscale set` leaves unspecified flags
// alone on later applies.
func prefs(t model.Tailscale, exitNode string) []pref {
	out := []pref{
		{"--netfilter-mode=off", true},
		{"--accept-dns=false", true},
		{"--snat-subnet-routes=true", true},
		{"--stateful-filtering=false", true},
		{"--ssh=false", true},
		{"--auto-update=false", false},
		{"--update-check=false", false},
		{"--webclient=false", false},
		{"--shields-up=false", true},
		{"--advertise-connector=false", true},
		{"--report-posture=false", true},
	}
	if exitNode == "" {
		out = append(out,
			pref{"--exit-node=", true},
			pref{"--exit-node-allow-lan-access=false", true},
		)
	}
	return append(out,
		pref{"--hostname=" + t.Hostname, true},
		pref{"--advertise-routes=" + strings.Join(t.AdvertiseRoutes, ","), true},
		pref{fmt.Sprintf("--advertise-exit-node=%t", t.AdvertiseExitNode), true},
		pref{fmt.Sprintf("--accept-routes=%t", t.AcceptRoutes), true},
	)
}

// PrefArgs is what `tailscale set` is given: everything.
func PrefArgs(t model.Tailscale, exitNode string) []string {
	ps := prefs(t, exitNode)
	args := make([]string, 0, len(ps))
	for _, p := range ps {
		args = append(args, p.arg)
	}
	return args
}

// UpArgs is what `tailscale up` is given: the flags it defines. The rest
// follow in a `set` once the node is up.
func UpArgs(t model.Tailscale, exitNode string) []string {
	var args []string
	for _, p := range prefs(t, exitNode) {
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
