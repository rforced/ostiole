// Package backup reads and writes portable copies of an appliance's
// configuration. A backup is plain JSON so it can be read, diffed, and
// kept in version control; restoring it loads the configuration as a draft
// that still goes through the ordinary commit-confirmed apply.
package backup

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/model"
)

// Kind identifies the file, so restoring the wrong JSON says so plainly
// instead of failing somewhere deep in validation.
const Kind = "ostiole-backup"

// Version is the backup envelope's own version, separate from the
// configuration schema it carries.
const Version = 1

// Errors returned when parsing a file.
var (
	ErrNotABackup     = errors.New("this is not an Ostiole backup file")
	ErrUnknownVersion = errors.New("this backup was written by a newer Ostiole")
	ErrNoConfig       = errors.New("the backup contains no configuration")
)

// Archive is the file itself.
type Archive struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// Ostiole is the version that wrote the file, for support questions.
	Ostiole  string `json:"ostiole,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// Note is whatever the operator typed when taking the backup.
	Note   string        `json:"note,omitempty"`
	Config *model.Config `json:"config"`
	// Users carries administrator accounts, password hashes included, so a
	// rebuilt box can be signed into. It is left out unless asked for.
	Users []auth.User `json:"users,omitempty"`
}

// Options tune what Create puts in the archive.
type Options struct {
	Ostiole  string
	Hostname string
	Note     string
	Users    []auth.User
	Now      func() time.Time
}

// Create builds an archive around cfg.
func Create(cfg *model.Config, opts Options) (*Archive, error) {
	if cfg == nil {
		return nil, ErrNoConfig
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	hostname := opts.Hostname
	if hostname == "" {
		hostname = cfg.System.Hostname
	}
	return &Archive{
		Kind:      Kind,
		Version:   Version,
		CreatedAt: now().UTC().Truncate(time.Second),
		Ostiole:   opts.Ostiole,
		Hostname:  hostname,
		Note:      opts.Note,
		Config:    cfg,
		Users:     opts.Users,
	}, nil
}

// Marshal renders the archive as the bytes that go in the file.
func (a *Archive) Marshal() ([]byte, error) {
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Parse reads a backup file and checks that the configuration inside it is
// valid, so a restore cannot leave a draft that can never be applied.
func Parse(raw []byte) (*Archive, error) {
	var a Archive
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotABackup, err)
	}
	if a.Kind != Kind {
		return nil, ErrNotABackup
	}
	if a.Version > Version {
		return nil, ErrUnknownVersion
	}
	if a.Config == nil {
		return nil, ErrNoConfig
	}
	if err := a.Config.Validate(); err != nil {
		return nil, fmt.Errorf("the configuration in this backup is not valid: %w", err)
	}
	return &a, nil
}

var (
	unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	// A dotted hostname is fine in a filename, a run of dots is not.
	dotRun = regexp.MustCompile(`\.{2,}`)
)

// Filename is what a download is called: hostname and timestamp, so a
// directory of backups sorts and reads sensibly.
func (a *Archive) Filename() string {
	host := unsafeName.ReplaceAllString(a.Hostname, "-")
	host = dotRun.ReplaceAllString(host, ".")
	host = strings.Trim(host, "-.")
	if host == "" {
		host = "ostiole"
	}
	when := a.CreatedAt
	if when.IsZero() {
		when = time.Now()
	}
	return fmt.Sprintf("%s-%s.json", host, when.UTC().Format("20060102-150405"))
}

// Summary describes a parsed backup without handing over its contents, so
// the UI can say what is about to be restored.
type Summary struct {
	CreatedAt time.Time `json:"createdAt"`
	Ostiole   string    `json:"ostiole,omitempty"`
	Hostname  string    `json:"hostname,omitempty"`
	Note      string    `json:"note,omitempty"`
	// Counts are the parts of the configuration worth a number.
	Zones      int `json:"zones"`
	Interfaces int `json:"interfaces"`
	Rules      int `json:"rules"`
	Aliases    int `json:"aliases"`
	Gateways   int `json:"gateways"`
	Users      int `json:"users"`
}

// Summary counts what the archive holds.
func (a *Archive) Summary() Summary {
	s := Summary{
		CreatedAt: a.CreatedAt,
		Ostiole:   a.Ostiole,
		Hostname:  a.Hostname,
		Note:      a.Note,
		Users:     len(a.Users),
	}
	if a.Config != nil {
		s.Zones = len(a.Config.Zones)
		s.Interfaces = len(a.Config.Interfaces)
		s.Rules = len(a.Config.Rules)
		s.Aliases = len(a.Config.Aliases)
		s.Gateways = len(a.Config.Gateways) + len(a.Config.GatewayGroups)
	}
	return s
}
