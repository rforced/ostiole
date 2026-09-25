package host

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rforced/ostiole/internal/iptables"
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
