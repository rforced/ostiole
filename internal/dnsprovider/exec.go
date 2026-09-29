package dnsprovider

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/rforced/ostiole/internal/dnsclient"
	"github.com/rforced/ostiole/internal/model"
)

// program writes records by running the operator's own program, as root.
// Only an admin may save one (ADR-0030).
type program struct {
	id, path string
	// raw hands the program the challenge rather than the record.
	raw bool
}

func newProgram(p model.DNSProvider, _ Options) (Client, error) {
	path := strings.TrimSpace(p.Settings["program"])
	if path == "" {
		return nil, errors.New("the program provider names no program")
	}
	return &program{id: p.ID, path: path, raw: strings.TrimSpace(p.Settings["mode"]) == "RAW"}, nil
}

func (p *program) AddTXT(ctx context.Context, r Record) error {
	return p.run(ctx, "present", r)
}

func (p *program) RemoveTXT(ctx context.Context, r Record) error {
	return p.run(ctx, "cleanup", r)
}

// run calls the program with the arguments scripts written for lego
// expect: present or cleanup, then the record's name, fully qualified,
// and its value; in RAW mode --, the domain, the token and the key
// authorisation. What it prints goes to the log.
func (p *program) run(ctx context.Context, command string, r Record) error {
	args := []string{command, dnsclient.FQDN(r.Name), r.Value}
	if p.raw {
		args = []string{command, "--", r.Domain, r.Token, r.KeyAuth}
	}
	cmd := exec.CommandContext(ctx, p.path, args...) //nolint:gosec // an admin's own program (ADR-0030)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("running %s: %w", p.path, err)
	}
	last := ""
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		if line := strings.TrimSpace(lines.Text()); line != "" {
			slog.Info("DNS provider program", "provider", p.id, "command", command, "output", line)
			last = line
		}
	}
	if err := cmd.Wait(); err != nil {
		if last != "" {
			return fmt.Errorf("%s %s: %w: %s", p.path, command, err, oneLine(last))
		}
		return fmt.Errorf("%s %s: %w", p.path, command, err)
	}
	return nil
}
