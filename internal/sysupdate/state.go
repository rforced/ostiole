package sysupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/atomicfile"
)

// Snapshot is what the router last learned about its own updates. It is
// written to disk because an update can restart the daemon that started
// it, and a page that forgets what it just did is no use to anybody.
type Snapshot struct {
	// LastCheck is when the package manager was last asked. omitzero, not
	// omitempty: a struct is never empty, so a router that has never
	// checked would otherwise report the year 1 as a timestamp.
	LastCheck time.Time `json:"lastCheck,omitzero"`
	// CheckError is why the last check failed, if it did.
	CheckError string `json:"checkError,omitempty"`
	// Pending is what the last successful check found.
	Pending Pending `json:"pending"`
	// LastRun, LastMode, LastError and LastOutput describe the last
	// install attempt.
	LastRun    time.Time `json:"lastRun,omitzero"`
	LastMode   string    `json:"lastMode,omitempty"`
	LastError  string    `json:"lastError,omitempty"`
	LastOutput string    `json:"lastOutput,omitempty"`
	// RebootRequired and RebootReason are the answer to "and now what".
	RebootRequired bool   `json:"rebootRequired,omitempty"`
	RebootReason   string `json:"rebootReason,omitempty"`
}

// maxOutput is how much of a run's output is kept. Enough to see what a
// transaction did, not enough to fill a small disk with a distro
// upgrade.
const maxOutput = 16000

// State holds the snapshot and keeps a copy on disk.
type State struct {
	Dir string

	mu   sync.Mutex
	data Snapshot
}

// NewState reads what is on disk, if anything. A router that has never
// checked simply starts empty.
func NewState(dir string) *State {
	s := &State{Dir: dir}
	raw, err := os.ReadFile(s.path())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s.data)
	return s
}

func (s *State) path() string { return filepath.Join(s.Dir, "state.json") }

// Snapshot returns a copy of what is known.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// Update applies a change and writes it out.
func (s *State) Update(change func(*Snapshot)) {
	s.mu.Lock()
	change(&s.data)
	if len(s.data.LastOutput) > maxOutput {
		s.data.LastOutput = s.data.LastOutput[:maxOutput] + "\n… (truncated)"
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil || s.Dir == "" {
		return
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return
	}
	// 0600: the list of unpatched packages on a firewall is nobody
	// else's business.
	_ = atomicfile.Write(s.path(), raw, 0o600)
}
