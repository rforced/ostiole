package nft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts the nft binary so the engine can be tested without it.
type Runner interface {
	// Check validates a ruleset without applying it (nft -c -f -).
	Check(ctx context.Context, ruleset string) error
	// Apply loads a ruleset atomically (nft -f -).
	Apply(ctx context.Context, ruleset string) error
	// ListTableJSON returns `nft -j list table inet ostiole` output, or
	// ErrNoTable if the table does not exist.
	ListTableJSON(ctx context.Context) ([]byte, error)
}

// ErrNoTable is returned by ListTableJSON when Ostiole's table is absent.
var ErrNoTable = errors.New("nftables table " + Table + " does not exist")

// Error carries nft's stderr, which is where the useful diagnostics live.
type Error struct {
	Op     string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("nft %s: %v", e.Op, e.Err)
	}
	return fmt.Sprintf("nft %s: %s", e.Op, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Exec runs the real nft binary.
type Exec struct {
	// Bin is the nft executable; empty means "nft" on PATH.
	Bin string
	// Wrap, if set, is prepended to every command (used by tests to run
	// inside an unprivileged namespace, e.g. []string{"unshare","-Urn"}).
	Wrap []string
}

func (x *Exec) bin() string {
	if x.Bin == "" {
		return "nft"
	}
	return x.Bin
}

func (x *Exec) run(ctx context.Context, op string, stdin string, args ...string) ([]byte, error) {
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

// Check implements Runner.
func (x *Exec) Check(ctx context.Context, ruleset string) error {
	_, err := x.run(ctx, "check", ruleset, "-c", "-f", "-")
	return err
}

// Apply implements Runner.
func (x *Exec) Apply(ctx context.Context, ruleset string) error {
	_, err := x.run(ctx, "apply", ruleset, "-f", "-")
	return err
}

// ListTableJSON implements Runner.
func (x *Exec) ListTableJSON(ctx context.Context) ([]byte, error) {
	out, err := x.run(ctx, "list", "", "-j", "list", "table", "inet", "ostiole")
	if err != nil {
		var nerr *Error
		if errors.As(err, &nerr) && strings.Contains(nerr.Stderr, "No such file or directory") {
			return nil, ErrNoTable
		}
		return nil, err
	}
	return out, nil
}

// Version returns the nft version string, e.g. "nftables v1.1.6 (…)".
func (x *Exec) Version(ctx context.Context) (string, error) {
	out, err := x.run(ctx, "version", "", "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
