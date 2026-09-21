package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Snapshot is what the router last learned about Ostiole's own releases.
// It is written to disk so the page can answer "is there an update?"
// without asking GitHub on every load, and so the answer survives the
// restart an update itself causes.
type Snapshot struct {
	// LastCheck is when GitHub was last asked. omitzero, not omitempty: a
	// struct is never empty, so a router that has never checked would
	// otherwise report the year 1 as a timestamp.
	LastCheck time.Time `json:"lastCheck,omitzero"`
	// CheckError is why the last check failed, if it did.
	CheckError string `json:"checkError,omitempty"`
	// Channel is the channel that was asked about.
	Channel Channel `json:"channel,omitempty"`
	// Current and Latest are the versions that were compared.
	Current string `json:"current,omitempty"`
	Latest  string `json:"latest,omitempty"`
	// Available says whether Latest is newer and installable here.
	Available bool `json:"available"`
	// Security and SecurityReleases say whether anything published since
	// Current carried the marker, and which releases did.
	Security         bool     `json:"security,omitempty"`
	SecurityReleases []string `json:"securityReleases,omitempty"`
	// Release is kept whole because the card shows its tag, date, notes
	// and link.
	Release *Release `json:"release,omitempty"`
}

// Cache holds the snapshot and keeps a copy on disk. It is the same idea
// as the package manager's state file, one directory over; the name
// differs only because State here is already the progress of an install.
type Cache struct {
	Dir string

	mu   sync.Mutex
	data Snapshot
}

// NewCache reads what is on disk, if anything. A router that has never
// checked simply starts empty.
func NewCache(dir string) *Cache {
	c := &Cache{Dir: dir}
	raw, err := os.ReadFile(c.path())
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c.data)
	return c
}

// path sits beside the package manager's state.json, because both are
// the same question asked of a different source.
func (c *Cache) path() string { return filepath.Join(c.Dir, "ostiole.json") }

// Snapshot returns a copy of what is known. A manager built without a
// cache — a test, or a binary with nowhere to write — remembers nothing
// and says so rather than panicking.
func (c *Cache) Snapshot() Snapshot {
	if c == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data
}

// Update applies a change and writes it out.
func (c *Cache) Update(change func(*Snapshot)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	change(&c.data)
	raw, err := json.MarshalIndent(c.data, "", "  ")
	c.mu.Unlock()
	if err != nil || c.Dir == "" {
		return
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return
	}
	// 0600: what a firewall is behind on is nobody else's business.
	_ = os.WriteFile(c.path(), raw, 0o600)
}
