package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWriteReplacesTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "new" {
		t.Errorf("content = %q, %v", raw, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v", info, err)
	}
	onlyThere(t, dir, "conf")
}

// A write that fails partway, or is abandoned, leaves the old file and no
// temporary one.
func TestCloseBeforeCommitKeepsTheOldFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := Create(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("half"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Commit(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("commit after close = %v", err)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "old" {
		t.Errorf("content = %q", raw)
	}
	onlyThere(t, dir, "conf")
}

func TestFailedRenameLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A file cannot be renamed over a directory.
	path := filepath.Join(dir, "conf")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o644); err == nil {
		t.Fatal("wrote over a directory")
	}
	onlyThere(t, dir, "conf")
}

// A process that stops between Create and Commit leaves its temporary file
// behind, which is how a restart during a save left five of them on a
// router. Nothing else is taken, dot files included.
func TestSweepRemovesWhatWritersLeft(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "certs", "web")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"crons.json", ".serve.lock", ".crons.json.tmp", ".json.x.tmp", "crons.json.123.tmp", ".crons.json..tmp",
		filepath.Join("certs", "web", "state.json"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var left []string
	for _, path := range []string{filepath.Join(dir, "crons.json"), filepath.Join(sub, "state.json")} {
		f, err := Create(path, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.File.Close() // as a process that stopped here would
		left = append(left, f.Name())
	}
	removed := Sweep(dir)
	slices.Sort(removed)
	slices.Sort(left)
	if !slices.Equal(removed, left) {
		t.Errorf("removed %v, want %v", removed, left)
	}
	onlyThere(t, dir, ".crons.json..tmp", ".crons.json.tmp", ".json.x.tmp", ".serve.lock", "certs", "crons.json", "crons.json.123.tmp")
	onlyThere(t, sub, "state.json")
}

func onlyThere(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s holds %v, want %v", dir, got, want)
	}
}
