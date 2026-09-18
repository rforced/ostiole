package host

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/rforced/ostiole/internal/iptables"
)

// SetUpFunc installs components. The command line calls SetUp directly;
// the daemon drives the command line, because writing a unit file is
// what its sandbox forbids.
type SetUpFunc func(ctx context.Context, keys []string) (string, error)

// Prepare does everything the page's one button promises, in the order
// the steps have to happen: the components the configuration asks for,
// then the old firewall, then whatever it left in the kernel. Addressing
// is not here, because it is the step that can drop the session driving
// it and has a confirmation window of its own.
//
// The old firewall is only retired on a router with Ostiole's ruleset
// loaded. Before the first confirmed apply it is left running and the
// output says so; finishing the setup wizard is what retires it then.
func Prepare(ctx context.Context, d Deps, setUp SetUpFunc) (string, error) {
	if !d.Root {
		return "", ErrNotRoot
	}
	var said []string
	rep := Status(ctx, d)
	var keys []string
	for _, c := range rep.Components {
		if c.Outstanding() {
			keys = append(keys, c.Key)
		}
	}
	if len(keys) > 0 {
		out, err := setUp(ctx, keys)
		if out != "" {
			said = append(said, out)
		}
		if err != nil {
			return strings.Join(said, "\n"), err
		}
		rep = Status(ctx, d)
	}
	if outstanding(rep, StepFirewall) != "" {
		if rep.Firewalled {
			out, err := TakeoverFirewall(ctx, d)
			if out != "" {
				said = append(said, out)
			}
			if err != nil {
				return strings.Join(said, "\n"), err
			}
		} else {
			said = append(said, "the old firewall keeps filtering until a configuration is applied and confirmed; finishing the setup wizard retires it")
		}
	}
	if len(rep.Legacy.Sweepable()) > 0 {
		out, err := FlushLegacy(ctx, d, nil)
		if out != "" {
			said = append(said, out)
		}
		if err != nil && !errors.Is(err, iptables.ErrNothingToFlush) {
			return strings.Join(said, "\n"), err
		}
	}
	if len(said) == 0 {
		said = append(said, "nothing to do: this router is prepared")
	}
	return strings.Join(said, "\n"), nil
}

// Sentence says what Prepare would do on this router, in the words the
// button shows: what it would install, retire and clear, from the
// report rather than from a promise.
func Sentence(rep Report) string {
	var parts []string
	var missing []string
	for _, c := range rep.Components {
		if c.Outstanding() {
			missing = append(missing, c.Label)
		}
	}
	if len(missing) > 0 {
		parts = append(parts, "set up "+join(missing))
	}
	var firewalls []string
	for _, c := range rep.Competitors {
		if c.Kind == "firewall" && c.Conflicts {
			firewalls = append(firewalls, c.Name)
		}
	}
	if len(firewalls) > 0 && rep.Firewalled {
		parts = append(parts, "retire "+join(firewalls))
	}
	if n := len(rep.Legacy.Sweepable()); n > 0 {
		word := "leftover rulesets"
		if n == 1 {
			word = "leftover ruleset"
		}
		parts = append(parts, "clear "+strconv.Itoa(n)+" "+word)
	}
	if len(parts) == 0 {
		return ""
	}
	s := join(parts)
	return strings.ToUpper(s[:1]) + s[1:] + "."
}
