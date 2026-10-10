package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/model"
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

	rev, err := s.Save(starter("one"), "ruleset-one\n", Author{})
	if err != nil {
		t.Fatal(err)
	}
	if rev != nil {
		t.Errorf("first save archived %v, want nil", rev)
	}
	if !s.Exists() {
		t.Fatal("store should exist after save")
	}

	rev, err = s.Save(starter("two"), "ruleset-two\n", Author{})
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

// A crash between Save's two renames leaves the ruleset of one save beside
// the configuration of another. Loading that ruleset would run the old
// policy under the new configuration, so it is refused.
func TestLoadRulesetRefusesOneSavedWithAnotherConfiguration(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(starter("one"), "ruleset-one\n", Author{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, RulesetFile)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(starter("two"), "ruleset-two\n", Author{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.LoadRuleset(); !errors.Is(err, ErrStaleRuleset) {
		t.Errorf("LoadRuleset = %q, %v, want ErrStaleRuleset", rs, err)
	}

	// The install's bootstrap carries no note and loads as it is.
	if err := os.WriteFile(path, []byte("table inet ostiole {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.LoadRuleset(); err != nil || rs != "table inet ostiole {}\n" {
		t.Errorf("LoadRuleset of the bootstrap = %q, %v", rs, err)
	}
}

// A file the release before saved loads as the current version and is left
// as it was: the next save writes the new one, so until then the saved
// ruleset still matches it and a binary put back can still read it.
func TestLoadBringsAnOlderFileUpToDate(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	cfg := starter("gateway")
	cfg.Services.DNS.Resolver = model.ResolverRecursive
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"version": `+strconv.Itoa(model.SchemaVersion)), []byte(`"version": 6`), 1)
	raw = bytes.Replace(raw, []byte(`"resolver": "recursive"`), []byte(`"resolver": "validate"`), 1)
	sum := sha256.Sum256(raw)
	for name, data := range map[string][]byte{
		ConfigFile: raw,
		filepath.Join(RevisionsDir, "older.json"): raw,
		RulesetFile: []byte(rulesetNote + hex.EncodeToString(sum[:]) + "\nruleset\n"),
	} {
		if err := os.WriteFile(filepath.Join(s.Dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for name, load := range map[string]func() (*model.Config, error){
		"config":   s.Load,
		"revision": func() (*model.Config, error) { return s.LoadRevision("older") },
	} {
		got, err := load()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Version != model.SchemaVersion || got.Services.DNS.Resolver != model.ResolverRecursive {
			t.Errorf("%s: version %d, resolver %q; want %d, recursive", name, got.Version, got.Services.DNS.Resolver, model.SchemaVersion)
		}
		if err := got.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if onDisk, _ := os.ReadFile(filepath.Join(s.Dir, ConfigFile)); !bytes.Equal(onDisk, raw) {
		t.Error("loading rewrote the file")
	}
	if rs, err := s.LoadRuleset(); err != nil || rs != "ruleset\n" {
		t.Errorf("LoadRuleset = %q, %v; want the ruleset saved with the file", rs, err)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(&model.Config{}, "", Author{}); err == nil {
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
		if _, err := s.Save(keeping("h"+string(rune('a'+i)), 3), "rs", Author{}); err != nil {
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
		if _, err := s.Save(keeping("h", keep), "rs", Author{}); err != nil {
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

func TestLoadSumIsTheSumOfTheSavedBytes(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, sum, err := s.LoadSum(); !errors.Is(err, ErrNotFound) || sum != NoSum {
		t.Fatalf("LoadSum with nothing saved = %q, %v", sum, err)
	}
	if sum, err := s.Sum(); err != nil || sum != NoSum {
		t.Fatalf("Sum with nothing saved = %q, %v", sum, err)
	}
	for _, hostname := range []string{"first", "second"} {
		if _, err := s.Save(starter(hostname), "ruleset\n", Author{}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(s.Dir, ConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256(raw)
		cfg, sum, err := s.LoadSum()
		if err != nil || cfg.System.Hostname != hostname || sum != hex.EncodeToString(want[:]) {
			t.Fatalf("LoadSum after saving %s = %q, %q, %v", hostname, cfg.System.Hostname, sum, err)
		}
		if again, err := s.Sum(); err != nil || again != sum {
			t.Errorf("Sum = %q, %v; LoadSum gave %q", again, err, sum)
		}
	}
}

var (
	alice = audit.Actor{Name: "alice", Kind: audit.Account, Role: "admin", Address: "192.0.2.5"}
	bob   = audit.Actor{Name: "bob", Kind: audit.Account, Role: "operator", Address: "192.0.2.6"}
	carol = audit.Actor{Name: "carol", Kind: audit.Shell}
)

func TestEachRevisionNamesWhoAppliedIt(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(starter("one"), "rs", Author{By: alice}); err != nil {
		t.Fatal(err)
	}
	archived, err := s.Save(starter("two"), "rs", Author{By: bob, ConfirmedBy: &carol})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Applied == nil || archived.Applied.By != alice {
		t.Fatalf("the archived revision says %+v, want alice", archived.Applied)
	}
	revs, err := s.Revisions()
	if err != nil || len(revs) != 1 || revs[0].Applied == nil || revs[0].Applied.By != alice {
		t.Fatalf("Revisions = %+v, %v", revs, err)
	}
	now, err := s.Applied()
	if err != nil {
		t.Fatal(err)
	}
	if now == nil || now.By != bob || now.ConfirmedBy == nil || *now.ConfirmedBy != carol || now.Outside {
		t.Fatalf("the saved configuration says %+v, want bob confirmed by carol", now)
	}
	if !now.Time.After(revs[0].Applied.Time) {
		t.Errorf("applied at %v, before the one it replaced at %v", now.Time, revs[0].Applied.Time)
	}
}

func TestAnEditByHandReadsAsChangedOutside(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(starter("one"), "rs", Author{By: alice}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Dir, ConfigFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(`"one"`), []byte(`"edited"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	now, err := s.Applied()
	if err != nil || now == nil || !now.Outside || now.By != (audit.Actor{}) {
		t.Fatalf("after an edit by hand: %+v, %v", now, err)
	}
	archived, err := s.Save(starter("two"), "rs", Author{By: bob})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Applied == nil || !archived.Applied.Outside {
		t.Errorf("the edited revision says %+v, want changed outside", archived.Applied)
	}
	if now, err := s.Applied(); err != nil || now == nil || now.By != bob {
		t.Errorf("after the next save: %+v, %v", now, err)
	}
}

func TestRevisionsFromBeforeTheRecordsNameNobody(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(starter("one"), "rs", Author{By: alice}); err != nil {
		t.Fatal(err)
	}
	// What a release that kept no records leaves.
	if err := os.Remove(filepath.Join(s.Dir, AppliedFile)); err != nil {
		t.Fatal(err)
	}
	if now, err := s.Applied(); err != nil || now != nil {
		t.Fatalf("with no records: %+v, %v", now, err)
	}
	archived, err := s.Save(starter("two"), "rs", Author{By: bob})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Applied != nil {
		t.Errorf("a revision from before the records says %+v", archived.Applied)
	}
	if now, err := s.Applied(); err != nil || now == nil || now.By != bob {
		t.Errorf("the save after: %+v, %v", now, err)
	}
}

// A crash after the records were written but before config.json leaves the
// configuration before in place: it is still the one its author applied.
func TestASaveCutShortKeepsWhoAppliedWhatIsInPlace(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	if _, err := s.Save(starter("one"), "rs", Author{By: alice}); err != nil {
		t.Fatal(err)
	}
	archived, err := s.Save(starter("two"), "rs", Author{By: bob})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.Dir, RevisionsDir, archived.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir, ConfigFile), before, 0o600); err != nil {
		t.Fatal(err)
	}
	if now, err := s.Applied(); err != nil || now == nil || now.By != alice || now.Outside {
		t.Fatalf("after a save cut short: %+v, %v", now, err)
	}
	next, err := s.Save(starter("three"), "rs", Author{By: carol})
	if err != nil {
		t.Fatal(err)
	}
	if next.Applied == nil || next.Applied.By != alice || next.Applied.Outside {
		t.Errorf("the revision archived after it says %+v, want alice", next.Applied)
	}
}

func TestRecordsGoWithTheirRevisions(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())
	for i := range 5 {
		if _, err := s.Save(keeping("h"+strconv.Itoa(i), 2), "rs", Author{By: alice}); err != nil {
			t.Fatal(err)
		}
	}
	revs, err := s.Revisions()
	if err != nil || len(revs) != 2 {
		t.Fatalf("Revisions = %v, %v", revs, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, AppliedFile))
	if err != nil {
		t.Fatal(err)
	}
	var f appliedFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Revisions) != 2 || f.Revisions[revs[0].ID] == nil || f.Revisions[revs[1].ID] == nil {
		t.Errorf("records kept for %v, want %s and %s", keys(f.Revisions), revs[0].ID, revs[1].ID)
	}
}

func keys(m map[string]*Applied) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
