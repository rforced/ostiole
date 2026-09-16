package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/model"
)

// DefaultCommandTimeout bounds a command job that names no timeout of its
// own. A job that has not finished in five minutes is stuck.
const DefaultCommandTimeout = 5 * time.Minute

// Jobs runs the scheduled work. Each dependency is optional: a box
// without a feed refresher simply cannot schedule one, and says so
// instead of failing obscurely.
type Jobs struct {
	// Config reads the configuration a backup is taken of.
	Config func() *model.Config
	// Users supplies accounts when a backup asks for them.
	Users func() []auth.User
	// Refresh fetches the blocklists and country ranges.
	Refresh func(ctx context.Context) error
	// Restart restarts a service unit.
	Restart func(ctx context.Context, unit string) error
	// Version is recorded in the backups this takes.
	Version string
	// Exec runs a command; swapped for a fake in tests.
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

var _ Executor = (*Jobs)(nil)

// Run implements Executor.
func (j *Jobs) Run(ctx context.Context, job model.Cron) (string, error) {
	switch job.Job {
	case model.CronBackup:
		return j.backup(job)
	case model.CronRefreshAliases:
		if j.Refresh == nil {
			return "", errors.New("nothing on this box refreshes aliases")
		}
		return "refreshed", j.Refresh(ctx)
	case model.CronRestartService:
		return j.restart(ctx, job)
	case model.CronCommand:
		return j.command(ctx, job)
	}
	return "", fmt.Errorf("unknown job %q", job.Job)
}

// backup writes a configuration backup and prunes the old ones, so a
// directory of them does not grow without limit on a small disk.
func (j *Jobs) backup(job model.Cron) (string, error) {
	if j.Config == nil {
		return "", errors.New("no configuration to back up")
	}
	cfg := j.Config()
	if cfg == nil {
		return "", errors.New("nothing is configured yet")
	}
	opts := backup.Options{Ostiole: j.Version, Note: "scheduled backup " + job.ID}
	if job.WithUsers && j.Users != nil {
		opts.Users = j.Users()
	}
	archive, err := backup.Create(cfg, opts)
	if err != nil {
		return "", err
	}
	raw, err := archive.Marshal()
	if err != nil {
		return "", err
	}
	// 0700 and 0600: a backup holds tunnel keys, and possibly password
	// hashes.
	if err := os.MkdirAll(job.Directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(job.Directory, archive.Filename())
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", err
	}
	removed, err := prune(job.Directory, keepOf(job))
	if err != nil {
		return fmt.Sprintf("wrote %s (%d bytes)", path, len(raw)), err
	}
	out := fmt.Sprintf("wrote %s (%d bytes)", path, len(raw))
	if removed > 0 {
		out += fmt.Sprintf("; removed %d older backup(s)", removed)
	}
	return out, nil
}

func keepOf(job model.Cron) int {
	if job.Keep <= 0 {
		return model.DefaultBackupsKept
	}
	return job.Keep
}

// prune removes all but the newest keep backups from a directory. Only
// files this job could have written are considered.
func prune(dir string, keep int) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return 0, nil
	}
	// The names carry a timestamp, so sorting them sorts by age.
	sort.Strings(names)
	removed := 0
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (j *Jobs) restart(ctx context.Context, job model.Cron) (string, error) {
	if j.Restart == nil {
		return "", errors.New("this box cannot restart services from here")
	}
	if err := j.Restart(ctx, job.Service); err != nil {
		return "", err
	}
	return "restarted " + job.Service, nil
}

// command runs a command without a shell, so there is nothing to quote
// and nothing to inject. It is bounded by a timeout, because a scheduled
// job that hangs is a job that never runs again.
func (j *Jobs) command(ctx context.Context, job model.Cron) (string, error) {
	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := j.Exec
	if run == nil {
		run = execRun
	}
	out, err := run(ctx, job.Command, job.Args...)
	text := strings.TrimSpace(string(out))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return text, fmt.Errorf("gave up after %s", timeout)
	}
	return text, err
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
