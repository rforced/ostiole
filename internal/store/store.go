// Package store persists the configuration under a directory (normally
// /etc/ostiole) with atomic writes, a revision history, and the last
// confirmed nftables ruleset for early boot.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
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
	// OffsetFile is the UTC offset the ruleset in the kernel was loaded
	// at, which its schedules' hours were converted with.
	OffsetFile = "offset.json"
	// NotifyFile lists the conditions a notice went out for, so a restart
	// does not send them again.
	NotifyFile = "notify.json"
)

// DefaultDir is the production location.
const DefaultDir = "/etc/ostiole"

// ErrNotFound means the store has no configuration yet.
var ErrNotFound = errors.New("no configuration found")

// ErrStaleRuleset means the saved ruleset was not written with the saved
// configuration: a crash came between the two renames of a Save.
var ErrStaleRuleset = errors.New("the saved ruleset belongs to another configuration")

// rulesetNote heads a saved ruleset with the SHA-256 of the config.json
// saved with it. nft reads it as a comment.
const rulesetNote = "# config.json sha256 "

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

// LoadRuleset returns the last confirmed ruleset, ErrNotFound, or
// ErrStaleRuleset when it was not saved with the configuration beside it.
func (s *Store) LoadRuleset() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, RulesetFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	rest, noted := strings.CutPrefix(string(raw), rulesetNote)
	if !noted {
		// The install's bootstrap, or a ruleset saved before the note.
		return string(raw), nil
	}
	want, ruleset, _ := strings.Cut(rest, "\n")
	cfg, err := os.ReadFile(filepath.Join(s.Dir, ConfigFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if sum := sha256.Sum256(cfg); hex.EncodeToString(sum[:]) != want {
		return "", ErrStaleRuleset
	}
	return ruleset, nil
}

// WriteState atomically writes one of the state files kept beside the
// configuration, such as PendingFile.
func (s *Store) WriteState(name string, data []byte) error {
	if err := s.Init(); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.Dir, filepath.Base(name)), data, 0o600)
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
	// Two renames are not one. The ruleset goes first and names the
	// configuration it belongs to, so a crash between them leaves one that
	// LoadRuleset refuses rather than one that loads under the wrong
	// configuration.
	sum := sha256.Sum256(raw)
	note := rulesetNote + hex.EncodeToString(sum[:]) + "\n"
	if err := atomicfile.Write(filepath.Join(s.Dir, RulesetFile), []byte(note+ruleset), 0o600); err != nil {
		return nil, err
	}
	if err := atomicfile.Write(current, raw, 0o600); err != nil {
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
	if err := atomicfile.Write(filepath.Join(dir, id+".json"), raw, 0o600); err != nil {
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
