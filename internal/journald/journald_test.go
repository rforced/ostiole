package journald

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type fakeRunner struct{ calls []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestApplyWritesAndRestartsOnlyOnChange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc/systemd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc/systemd/journald.conf"), []byte("[Journal]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &fakeRunner{}
	sys := System{Run: run, Root: root}
	if err := sys.Apply(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ConfFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[Journal]", "Storage=persistent", "SystemMaxUse=10G", "MaxRetentionSec=90day", "MaxFileSec=1week", "LineMax=128K"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("drop-in lacks %q:\n%s", want, raw)
		}
	}
	if len(run.calls) != 1 || run.calls[0] != "systemctl restart systemd-journald" {
		t.Errorf("calls = %v, want one restart", run.calls)
	}
	// The same limits again are not a restart.
	if err := sys.Apply(context.Background(), DefaultMaxUseGB, DefaultRetentionDays); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 1 {
		t.Errorf("an unchanged drop-in restarted journald: %v", run.calls)
	}
	// A new ceiling is.
	if err := sys.Apply(context.Background(), 25, DefaultRetentionDays); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(root, ConfFile))
	if !strings.Contains(string(raw), "SystemMaxUse=25G") || len(run.calls) != 2 {
		t.Errorf("drop-in = %q, calls = %v", raw, run.calls)
	}
	// So is a new retention.
	if err := sys.Apply(context.Background(), 25, 30); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(root, ConfFile))
	if !strings.Contains(string(raw), "MaxRetentionSec=30day") || len(run.calls) != 3 {
		t.Errorf("drop-in = %q, calls = %v", raw, run.calls)
	}
}

// systemd's own defaults live in /usr/lib, and RHEL 10 ships
// journald.conf there and nowhere else. A router looked at for the file
// in /etc alone reports no journald, and is then left with a journal
// that is neither bounded nor kept across the reboot it would explain —
// silently, because the installer had already said it would bound it.
func TestJournaldIsFoundWhereSystemdKeepsIts(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"etc/systemd", "usr/lib/systemd", "lib/systemd"} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		conf := filepath.Join(root, dir, "journald.conf")
		if err := os.WriteFile(conf, []byte("[Journal]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		sys := System{Run: &fakeRunner{}, Root: root}
		if !sys.Present() {
			t.Errorf("journald at %s was not found", conf)
		}
		if err := sys.Apply(context.Background(), 0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(root, ConfFile)); err != nil {
			t.Errorf("journald at %s was left unbounded", conf)
		}
	}
}

// A router whose image left journald out gets nothing written and
// nothing restarted.
func TestApplySkipsARouterWithoutJournald(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run := &fakeRunner{}
	sys := System{Run: run, Root: root}
	if sys.Present() {
		t.Fatal("an empty root reports journald present")
	}
	if err := sys.Apply(context.Background(), 5, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ConfFile)); err == nil {
		t.Error("a drop-in was written for a router with no journald")
	}
	if len(run.calls) != 0 {
		t.Errorf("calls = %v", run.calls)
	}
}

// Stream is true only for the file systemd named: the same device and
// inode, not another file, and nothing without JOURNAL_STREAM.
func TestStreamIsTheFileSystemdNamed(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "stream"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	other, err := os.Create(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	t.Setenv("JOURNAL_STREAM", fmt.Sprintf("%d:%d", st.Dev, st.Ino))
	if !Stream(f) || Stream(other) {
		t.Errorf("stream %v, other file %v", Stream(f), Stream(other))
	}
	t.Setenv("JOURNAL_STREAM", "")
	if Stream(f) {
		t.Error("a stream without JOURNAL_STREAM")
	}
}
