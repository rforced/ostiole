package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

func starter(hostname string) *model.Config {
	return model.Starter(model.StarterOptions{Hostname: hostname, LAN: "eth1", LANAddress: "192.168.1.1/24"})
}

func keeping(hostname string, revisions int) *model.Config {
	cfg := starter(hostname)
	cfg.System.KeepRevisions = revisions
	return cfg
}

func TestSaveLoadAndRevisions(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if s.Exists() {
		t.Fatal("fresh store should not exist")
	}
	if _, err := s.Load(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load on empty store: %v, want ErrNotFound", err)
	}
	if _, err := s.LoadRuleset(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("LoadRuleset on empty store: %v, want ErrNotFound", err)
	}

	rev, err := s.Save(starter("one"), "ruleset-one\n")
	if err != nil {
		t.Fatal(err)
	}
	if rev != nil {
		t.Errorf("first save archived %v, want nil", rev)
	}
	if !s.Exists() {
		t.Fatal("store should exist after save")
	}

	rev, err = s.Save(starter("two"), "ruleset-two\n")
	if err != nil {
		t.Fatal(err)
	}
	if rev == nil {
		t.Fatal("second save should archive the first config")
	}

	cfg, err := s.Load()
	if err != nil || cfg.System.Hostname != "two" {
		t.Fatalf("Load = %v, %v", cfg, err)
	}
	rs, err := s.LoadRuleset()
	if err != nil || rs != "ruleset-two\n" {
		t.Fatalf("LoadRuleset = %q, %v", rs, err)
	}

	revs, err := s.Revisions()
	if err != nil || len(revs) != 1 || revs[0].ID != rev.ID {
		t.Fatalf("Revisions = %v, %v", revs, err)
	}
	if time.Since(revs[0].Time) > time.Minute {
		t.Errorf("revision time %v not parsed from id", revs[0].Time)
	}
	old, err := s.LoadRevision(rev.ID)
	if err != nil || old.System.Hostname != "one" {
		t.Fatalf("LoadRevision = %v, %v", old, err)
	}

	// Permissions: config is private.
	info, _ := os.Stat(filepath.Join(s.Dir, ConfigFile))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %o, want 600", info.Mode().Perm())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(s.Dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(&model.Config{}, ""); err == nil {
		t.Fatal("expected validation error")
	}
	if s.Exists() {
		t.Fatal("invalid config must not be written")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	for i := range 6 {
		if _, err := s.Save(keeping("h"+string(rune('a'+i)), 3), "rs"); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := s.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("kept %d revisions, want 3", len(revs))
	}
	// Newest first: the most recent archive holds hostname "he".
	newest, err := s.LoadRevision(revs[0].ID)
	if err != nil || newest.System.Hostname != "he" {
		t.Fatalf("newest revision = %v, %v", newest, err)
	}
}

// Retention is a setting, so lowering it has to take effect with the apply
// that lowers it rather than only on the one after.
func TestPruneFollowsConfiguredLimit(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	save := func(keep int) {
		t.Helper()
		if _, err := s.Save(keeping("h", keep), "rs"); err != nil {
			t.Fatal(err)
		}
	}
	for range 6 {
		save(4)
	}
	if revs, err := s.Revisions(); err != nil || len(revs) != 4 {
		t.Fatalf("kept %d revisions, want 4 (%v)", len(revs), err)
	}
	save(2)
	if revs, err := s.Revisions(); err != nil || len(revs) != 2 {
		t.Fatalf("after lowering the limit kept %d revisions, want 2 (%v)", len(revs), err)
	}
}

func TestLoadRevisionRejectsTraversal(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	for _, id := range []string{"", "../config", "a/b", "..", `x\y`} {
		if _, err := s.LoadRevision(id); err == nil {
			t.Errorf("LoadRevision(%q) succeeded", id)
		}
	}
}

func TestLockIsExclusive(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	unlock, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() {
		u, err := s.Lock()
		if err == nil {
			u()
		}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("second lock acquired while first held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock never acquired after release")
	}
}
