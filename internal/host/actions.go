package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/install"
	"github.com/rforced/ostiole/internal/iptables"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// ErrNotRoot means the daemon cannot change the router it is running on.
var ErrNotRoot = errors.New("this needs root; the daemon is not running as root")

// FlushLegacy clears what an older firewall left in the kernel. With no
// ids it sweeps everything with no recognisable owner; with ids it clears
// exactly those tables, which is the only way to take one that Ostiole
// would otherwise leave alone.
func FlushLegacy(ctx context.Context, d Deps, ids []string) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	deps := d.legacy()
	rep := iptables.Detect(ctx, deps)
	targets := rep.Sweepable()
	if len(ids) > 0 {
		targets = nil
		for _, id := range ids {
			t, ok := rep.Find(id)
			if !ok {
				return "", fmt.Errorf("%s is not a leftover ruleset on this router", id)
			}
			// Named explicitly, so an owner is a warning and not a veto:
			// the operator can see whose it is on the page.
			t.Owner = ""
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return "", iptables.ErrNothingToFlush
	}
	return iptables.Flush(ctx, deps, targets)
}

// NetworkTakeoverWindow is how long the handover waits to be confirmed
// before the revert timer puts the previous manager back.
const NetworkTakeoverWindow = 3 * time.Minute

// TakeNetwork hands the router's addressing to systemd-networkd. It runs
// the command line, which detaches the switch into a transient unit of
// its own and arms a revert timer: losing the session mid-switch is the
// expected case, not the surprise, and the revert is what makes that
// survivable.
func TakeNetwork(ctx context.Context, d Deps, window time.Duration) (string, error) {
	if window <= 0 {
		window = NetworkTakeoverWindow
	}
	return Drive(ctx, d, "takeover", "--network", "--yes", "--confirm-window", window.String())
}

// ConfirmNetwork keeps the handover and disarms the revert timer.
func ConfirmNetwork(ctx context.Context, d Deps) (string, error) {
	return Drive(ctx, d, "takeover", "--network", "--confirm")
}

// RevertNetwork puts the previous network manager back now, rather than
// waiting for the timer.
func RevertNetwork(ctx context.Context, d Deps) (string, error) {
	return Drive(ctx, d, "takeover", "--network", "--revert", "--yes")
}

// Drive runs the installed ostiole on the host and returns what it
// printed.
//
// This is the hinge between the two front doors. The daemon runs behind
// ProtectSystem=strict, so it cannot write a unit file or /etc/ppp; a
// transient unit has no sandbox. Rather than teach the daemon to do these
// things another way, the API runs the same command an operator would
// type, with the same globals the daemon is running with, and shows them
// its output.
func Drive(ctx context.Context, d Deps, args ...string) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	bin := d.Binary
	if bin == "" {
		found, err := install.ServiceBinary(install.DefaultLayout())
		if err != nil {
			return "", fmt.Errorf("find the installed ostiole: %w", err)
		}
		bin = found
	}
	argv := []string{"--config-dir", d.Dir}
	if d.NFT != "" {
		argv = append(argv, "--nft", d.NFT)
	}
	if d.Backend != "" {
		argv = append(argv, "--network-backend", d.Backend)
	}
	argv = append(argv, args...)
	out, err := sysupdate.NewHostRunner(nil).Run(ctx, bin, argv...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("ostiole %s: %w: %s", strings.Join(args, " "), err, tail(out))
	}
	return text, nil
}

// tail keeps the end of a failed command's output, which is where the
// reason is.
func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 800 {
		s = "…" + s[len(s)-800:]
	}
	return s
}
