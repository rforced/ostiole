package logging

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"ostiole/internal/model"
)

type fakeRunner struct {
	calls []string
	// missing are the units `systemctl cat` cannot find.
	missing []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if len(args) == 2 && args[0] == "cat" && slices.Contains(f.missing, args[1]) {
		return []byte("No files found for " + args[1]), errors.New("exit status 1")
	}
	return nil, nil
}

// dirs creates a drop-in directory for every unit under a temporary
// root, the way `ostiole install` does on a router.
func dirs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, u := range Units {
		if err := os.MkdirAll(u.Dir(root), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestApplyWritesTheCapAndRestartsOnlyOnChange(t *testing.T) {
	t.Parallel()
	root := dirs(t)
	run := &fakeRunner{}
	lvl := new(slog.LevelVar)
	sys := System{Run: run, Root: root, Slog: lvl}

	if err := sys.Apply(context.Background(), model.LogWarning); err != nil {
		t.Fatal(err)
	}
	if lvl.Level() != slog.LevelWarn {
		t.Errorf("own level = %v, want warn", lvl.Level())
	}
	for _, u := range Units {
		raw, err := os.ReadFile(filepath.Join(u.Dir(root), DropInName))
		if err != nil {
			t.Fatalf("%s: %v", u.Name, err)
		}
		if !strings.Contains(string(raw), "LogLevelMax=warning") {
			t.Errorf("%s drop-in = %q", u.Name, raw)
		}
		// A daemon that says everything twice has its stderr copy sunk.
		if got := strings.Contains(string(raw), "SyslogLevel=debug"); got != u.StderrIsDuplicate {
			t.Errorf("%s SyslogLevel = %v, want %v", u.Name, got, u.StderrIsDuplicate)
		}
	}
	// One reload, then a look and a restart per unit.
	if want := 1 + 2*len(Units); len(run.calls) != want {
		t.Fatalf("calls = %v, want %d", run.calls, want)
	}
	if run.calls[0] != "systemctl daemon-reload" {
		t.Errorf("first call = %q", run.calls[0])
	}
	// Templated units are restarted by pattern: systemd will not act on
	// the template itself.
	if !strings.Contains(strings.Join(run.calls, "\n"), "try-restart ostiole-hostapd@*.service") {
		t.Errorf("calls = %v, want the hostapd instances", run.calls)
	}

	// The same level again writes nothing and restarts nothing.
	run.calls = nil
	if err := sys.Apply(context.Background(), model.LogWarning); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 0 {
		t.Errorf("an unchanged cap restarted units: %v", run.calls)
	}

	// A new level does.
	if err := sys.Apply(context.Background(), model.LogDebug); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1+2*len(Units) {
		t.Errorf("calls = %v", run.calls)
	}
	if lvl.Level() != slog.LevelDebug {
		t.Errorf("own level = %v, want debug", lvl.Level())
	}
}

// A unit that is not set up on this router has no drop-in directory, and
// writing one would leave a directory systemd reads for a unit that does
// not exist.
func TestApplySkipsAUnitThatIsNotSetUp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	only := Units[0]
	if err := os.MkdirAll(only.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	if err := (System{Run: run, Root: root}).Apply(context.Background(), model.LogInfo); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 3 {
		t.Errorf("calls = %v, want a reload, a look and one restart", run.calls)
	}
	for _, u := range Units[1:] {
		if _, err := os.Stat(u.Dir(root)); err == nil {
			t.Errorf("%s got a drop-in directory it never had", u.Name)
		}
	}
}

// An empty level is the default, not a cap of "".
func TestApplyTreatsAnEmptyLevelAsTheDefault(t *testing.T) {
	t.Parallel()
	root := dirs(t)
	if err := (System{Root: root}).Apply(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Units[0].Dir(root), DropInName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "LogLevelMax=warning") {
		t.Errorf("drop-in = %q", raw)
	}
}

func TestPriorityAndSlogCoverEveryLevel(t *testing.T) {
	t.Parallel()
	want := map[model.LogLevel]struct {
		priority string
		level    slog.Level
	}{
		model.LogError:   {"err", slog.LevelError},
		model.LogWarning: {"warning", slog.LevelWarn},
		model.LogInfo:    {"info", slog.LevelInfo},
		model.LogDebug:   {"debug", slog.LevelDebug},
	}
	for _, l := range model.LogLevels {
		w, ok := want[l]
		if !ok {
			t.Fatalf("%q has no priority or slog level", l)
		}
		if got := Priority(l); got != w.priority {
			t.Errorf("Priority(%q) = %q, want %q", l, got, w.priority)
		}
		if got := Slog(l); got != w.level {
			t.Errorf("Slog(%q) = %v, want %v", l, got, w.level)
		}
	}
}

// Install creates every drop-in directory, so a unit that was never set
// up still gets a cap; it must not get a restart, which systemctl would
// refuse and the engine would then report as a failure of the whole cap.
func TestApplyDoesNotRestartAUnitWithNoFile(t *testing.T) {
	t.Parallel()
	root := dirs(t)
	run := &fakeRunner{missing: []string{"ostiole-miniupnpd.service"}}
	if err := (System{Run: run, Root: root}).Apply(context.Background(), model.LogInfo); err != nil {
		t.Fatalf("a unit with no file failed the apply: %v", err)
	}
	joined := strings.Join(run.calls, "\n")
	if strings.Contains(joined, "try-restart ostiole-miniupnpd.service") {
		t.Errorf("restarted a unit that is not set up: %v", run.calls)
	}
	if !strings.Contains(joined, "try-restart ostiole-dnsmasq.service") {
		t.Errorf("the units that are set up were not restarted: %v", run.calls)
	}
}

// What Always marks is written at every level, and nothing else changes:
// the level still holds back the rest, attributes and groups included.
func TestAlwaysIsWrittenAtEveryLevel(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelError)
	log := slog.New(Handler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: lvl}))).With("unit", "ostiole")
	ctx := context.Background()
	log.InfoContext(Always(ctx), "signed in", "user", "admin")
	log.WarnContext(ctx, "held back")
	log.WithGroup("g").DebugContext(Always(ctx), "grouped")
	log.ErrorContext(ctx, "failed")
	got := buf.String()
	for _, want := range []string{`msg="signed in" unit=ostiole user=admin`, `msg=grouped unit=ostiole`, `msg=failed`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "held back") {
		t.Errorf("a warning got past the error level:\n%s", got)
	}
}

// Under journald each line starts with its level's syslog priority, which
// journald takes and strips, in loggers made from the first too; a line
// never gets another's, however many are written at once.
func TestTheJournalGetsEachLinesPriority(t *testing.T) {
	t.Parallel()
	var buf lockedBuilder
	lvl := new(slog.LevelVar)
	lvl.Set(slog.LevelDebug)
	log := slog.New(Handler(Journal(&buf, &slog.HandlerOptions{Level: lvl})))
	log.Debug("d")
	log.Info("i")
	log.With("unit", "ostiole").Warn("w")
	log.WithGroup("g").Error("e", "k", "v")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	want := []string{"<7>", "<6>", "<4>", "<3>"}
	if len(lines) != len(want) {
		t.Fatalf("wrote %q", lines)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, want[i]+"time=") {
			t.Errorf("line %d is %q, want it to start %s", i, l, want[i])
		}
	}

	buf = lockedBuilder{}
	levels := map[string]slog.Level{"<7>": slog.LevelDebug, "<6>": slog.LevelInfo, "<4>": slog.LevelWarn, "<3>": slog.LevelError}
	var wg sync.WaitGroup
	for _, l := range levels {
		wg.Go(func() {
			for range 200 {
				log.Log(context.Background(), l, "concurrent")
			}
		})
	}
	wg.Wait()
	for l := range strings.SplitSeq(strings.TrimSuffix(buf.String(), "\n"), "\n") {
		pri, rest, _ := strings.Cut(l, "time=")
		if lv, ok := levels[pri]; !ok || !strings.Contains(rest, " level="+lv.String()+" ") {
			t.Fatalf("%q carries another line's priority", l)
		}
	}
}

// lockedBuilder is a strings.Builder several goroutines write.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
