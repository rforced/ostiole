// Package store persists the configuration under a directory (normally
// /etc/ostiole) with atomic writes, a revision history, and the last
// confirmed nftables ruleset for early boot.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// File names inside the store directory.
const (
	ConfigFile   = "config.json"
	RulesetFile  = "ruleset.nft"
	RevisionsDir = "revisions"
	lockFile     = ".lock"
	// PendingFile records an apply that has not been committed or undone
	// yet, so the next start can undo one a crash or a reboot cut short.
	PendingFile = "pending.json"
	// FallbackFile says the kernel runs the fallback ruleset, and why.
	FallbackFile = "fallback.json"
)

// DefaultDir is the production location.
const DefaultDir = "/etc/ostiole"

// ErrNotFound means the store has no configuration yet.
var ErrNotFound = errors.New("no configuration found")

// Store is a directory-backed configuration store. It is safe for use from
// multiple processes when callers hold Lock around read-modify-write.
type Store struct {
	Dir string
}

// New returns a store rooted at dir.
func New(dir string) *Store {
	return &Store{Dir: dir}
}

// Init creates the directory layout with restrictive permissions.
func (s *Store) Init() error {
	for _, d := range []string{s.Dir, filepath.Join(s.Dir, RevisionsDir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// Exists reports whether a configuration has been saved.
func (s *Store) Exists() bool {
	_, err := os.Stat(filepath.Join(s.Dir, ConfigFile))
	return err == nil
}

// Load reads the current configuration.
func (s *Store) Load() (*model.Config, error) {
	return readConfig(filepath.Join(s.Dir, ConfigFile))
}

// LoadRuleset returns the last confirmed ruleset, or ErrNotFound.
func (s *Store) LoadRuleset() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, RulesetFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// WriteState atomically writes one of the state files kept beside the
// configuration, such as PendingFile.
func (s *Store) WriteState(name string, data []byte) error {
	if err := s.Init(); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.Dir, filepath.Base(name)), data)
}

// ReadState reads a state file, or returns ErrNotFound.
func (s *Store) ReadState(name string) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, filepath.Base(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return raw, err
}

// RemoveState deletes a state file; one that is not there is not an error.
func (s *Store) RemoveState(name string) error {
	err := os.Remove(filepath.Join(s.Dir, filepath.Base(name)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Revision identifies an archived configuration.
type Revision struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// Save archives the current configuration as a revision, then atomically
// writes cfg and ruleset as the new current state. The returned revision
// is the archived previous config, or nil on first save.
func (s *Store) Save(cfg *model.Config, ruleset string) (*Revision, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := s.Init(); err != nil {
		return nil, err
	}
	var archived *Revision
	current := filepath.Join(s.Dir, ConfigFile)
	if prev, err := os.ReadFile(current); err == nil {
		rev, err := s.archive(prev)
		if err != nil {
			return nil, err
		}
		archived = rev
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := writeAtomic(current, raw); err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(s.Dir, RulesetFile), []byte(ruleset)); err != nil {
		return nil, err
	}
	if err := s.prune(cfg.System.RevisionsKept()); err != nil {
		return archived, err
	}
	return archived, nil
}

// Revisions lists archived configurations, newest first.
func (s *Store) Revisions() ([]Revision, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, RevisionsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revs := make([]Revision, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		ts, err := parseRevisionTime(id)
		if err != nil {
			ts = info.ModTime()
		}
		revs = append(revs, Revision{ID: id, Time: ts, Size: info.Size()})
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].ID > revs[j].ID })
	return revs, nil
}

// LoadRevision reads an archived configuration by ID.
func (s *Store) LoadRevision(id string) (*model.Config, error) {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return nil, fmt.Errorf("invalid revision id %q", id)
	}
	return readConfig(filepath.Join(s.Dir, RevisionsDir, id+".json"))
}

// Lock takes an exclusive advisory lock on the store, blocking until it is
// available. The returned function releases it.
func (s *Store) Lock() (func(), error) {
	if err := s.Init(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock store: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// revisionTimeLayout gives IDs that sort chronologically as strings.
const revisionTimeLayout = "20060102T150405.000000000Z"

func parseRevisionTime(id string) (time.Time, error) {
	return time.Parse(revisionTimeLayout, id)
}

// archive writes raw as a new revision named by its UTC timestamp. If two
// archives land in the same nanosecond the later one is bumped forward so
// IDs stay unique and strictly increasing even after older ones are pruned.
func (s *Store) archive(raw []byte) (*Revision, error) {
	dir := filepath.Join(s.Dir, RevisionsDir)
	ts := time.Now().UTC()
	if latest, err := s.Revisions(); err == nil && len(latest) > 0 && !latest[0].Time.Before(ts) {
		ts = latest[0].Time.Add(time.Nanosecond)
	}
	var id string
	for {
		id = ts.Format(revisionTimeLayout)
		if _, err := os.Stat(filepath.Join(dir, id+".json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		ts = ts.Add(time.Nanosecond)
	}
	if err := writeAtomic(filepath.Join(dir, id+".json"), raw); err != nil {
		return nil, err
	}
	return &Revision{ID: id, Time: ts, Size: int64(len(raw))}, nil
}

// prune deletes the oldest archived configurations beyond keep, which is
// how many the configuration being saved asks to hold on to.
func (s *Store) prune(keep int) error {
	if keep <= 0 {
		keep = model.DefaultKeepRevisions
	}
	revs, err := s.Revisions()
	if err != nil {
		return err
	}
	for _, r := range revs[min(keep, len(revs)):] {
		if err := os.Remove(filepath.Join(s.Dir, RevisionsDir, r.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readConfig(path string) (*model.Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// writeAtomic writes data to a temp file in the same directory, fsyncs it,
// and renames it over path so readers never see a partial file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
