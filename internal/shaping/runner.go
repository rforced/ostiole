package shaping

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts the tc binary so the reconciler can be tested without
// one, the same way the nft runner does for the firewall.
type Runner interface {
	// Batch runs a script of tc commands as one invocation. tc stops at
	// the first line it cannot carry out, so a half-applied batch is an
	// error rather than a silent partial success.
	Batch(ctx context.Context, script string) error
	// QdiscsJSON returns `tc -s -j qdisc show` for every device. Asking
	// for all of them at once is one exec instead of one per interface,
	// and each entry names the device it came from.
	QdiscsJSON(ctx context.Context) ([]byte, error)
	// FiltersJSON returns the filters on one parent of one device.
	FiltersJSON(ctx context.Context, dev, parent string) ([]byte, error)
	// Version is the tc version string.
	Version(ctx context.Context) (string, error)
}

// Error carries tc's stderr, which is where the useful diagnostics live.
type Error struct {
	Op     string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("tc %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("tc %s: %s", e.Op, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Exec runs the real tc binary.
type Exec struct {
	// Bin is the tc executable; empty means "tc" on PATH.
	Bin string
	// Wrap, if set, is prepended to every command, which is how the
	// kernel tests run inside an unprivileged namespace.
	Wrap []string
}

var _ Runner = (*Exec)(nil)

func (x *Exec) bin() string {
	if x.Bin == "" {
		return "tc"
	}
	return x.Bin
}

func (x *Exec) run(ctx context.Context, op, stdin string, args ...string) ([]byte, error) {
	argv := append(append([]string{}, x.Wrap...), x.bin())
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is fixed by the caller, never user input
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &Error{Op: op, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Batch implements Runner. The comments that make a rendered file
// readable are taken out first: tc has no notion of one and would stop at
// the first.
func (x *Exec) Batch(ctx context.Context, script string) error {
	script = stripComments(script)
	if script == "" {
		return nil
	}
	_, err := x.run(ctx, "batch", script, "-batch", "-")
	return err
}

// QdiscsJSON implements Runner.
func (x *Exec) QdiscsJSON(ctx context.Context) ([]byte, error) {
	return x.run(ctx, "qdisc show", "", "-s", "-j", "qdisc", "show")
}

// FiltersJSON implements Runner.
func (x *Exec) FiltersJSON(ctx context.Context, dev, parent string) ([]byte, error) {
	return x.run(ctx, "filter show", "", "-j", "filter", "show", "dev", dev, "parent", parent)
}

// Version implements Runner.
func (x *Exec) Version(ctx context.Context) (string, error) {
	out, err := x.run(ctx, "version", "", "-V")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// stripComments leaves only the commands.
func stripComments(script string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(script, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Available reports where the tc binary is, if it is anywhere. On Red Hat
// family distributions it ships in a package of its own, so a router can
// have a complete iproute2 and still not have it.
func Available(bin string) (string, bool) {
	if bin == "" {
		bin = "tc"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", false
	}
	return path, true
}

// TCPackages names the package tc comes in, per package manager. The same
// table backs the error an apply is refused with and the warning the
// dashboard shows, so the operator is told to install the same thing
// twice rather than two different things once.
var TCPackages = map[string]string{
	"dnf":     "iproute-tc",
	"apt-get": "iproute2",
	"pacman":  "iproute2",
	"zypper":  "iproute2",
}

// TCPackage is what to install to get tc on a host with the given package
// manager, or a generic name when the host has none Ostiole knows.
func TCPackage(pm string) string {
	if name, ok := TCPackages[pm]; ok {
		return name
	}
	return "iproute2"
}

// MissingMessage is what to tell the operator when tc is not installed.
// It is one sentence because it appears in an apply error and on the
// dashboard, and neither has room for a paragraph.
func MissingMessage(pm string) string {
	return fmt.Sprintf("traffic shaping needs the tc command, which is not installed; install the %s package", TCPackage(pm))
}
