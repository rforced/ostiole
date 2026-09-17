package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// State of an update run.
type State string

// States.
const (
	Idle        State = "idle"
	Checking    State = "checking"
	Downloading State = "downloading"
	Verifying   State = "verifying"
	Installing  State = "installing"
	Restarting  State = "restarting"
	Failed      State = "failed"
)

// Status is the observable progress of an update.
type Status struct {
	State          State     `json:"state"`
	Version        string    `json:"version,omitempty"`
	Message        string    `json:"message,omitempty"`
	Done           int64     `json:"done,omitempty"`
	Total          int64     `json:"total,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
	PackageManaged bool      `json:"packageManaged"`
}

// ErrBusy means an update is already running.
var ErrBusy = errors.New("an update is already in progress")

// ErrPackageManaged means the binary belongs to a distro package.
var ErrPackageManaged = errors.New("this binary was installed from a package; update it with your package manager")

// Manager runs updates in the background for the API.
type Manager struct {
	Client         *Client
	Installer      *Installer
	Current        string
	PackageManaged bool
	Log            *slog.Logger

	mu     sync.Mutex
	status Status
}

// Status returns the current progress.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	if st.State == "" {
		st.State = Idle
	}
	st.PackageManaged = m.PackageManaged
	return st
}

func (m *Manager) set(st State, version, msg string, done, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = Status{State: st, Version: version, Message: msg, Done: done, Total: total, UpdatedAt: time.Now()}
}

// Check looks for a newer release on the channel.
func (m *Manager) Check(ctx context.Context, ch Channel) (*Check, error) {
	return m.Client.Check(ctx, m.Current, ch)
}

// Mode is what a scheduled self-update is allowed to install. It mirrors
// the update mode in the configuration without this package having to
// know about the configuration.
type Mode string

// Modes.
const (
	ModeManual   Mode = "manual"
	ModeSecurity Mode = "security"
	ModeAll      Mode = "all"
)

// RunScheduled is the scheduled job: always check, then install only
// what the mode allows. It returns a line describing what it decided,
// which is what the job's last result shows.
func (m *Manager) RunScheduled(ctx context.Context, mode Mode, ch Channel) (string, error) {
	chk, err := m.Check(ctx, ch)
	if err != nil {
		return "", err
	}
	if !chk.Available {
		if chk.Latest == "" {
			return "no release on the " + string(ch) + " channel", nil
		}
		return "up to date (" + chk.Current + ")", nil
	}
	waiting := chk.Latest + " is available"
	switch mode {
	case ModeManual:
		return waiting + "; this box installs Ostiole updates by hand", nil
	case ModeSecurity:
		if !chk.Security {
			return waiting + ", and is not marked a security release; nothing installed", nil
		}
		waiting += " and fixes " + strings.Join(chk.SecurityReleases, ", ")
	}
	if m.PackageManaged {
		// The distro package manager owns this binary, and the system
		// update job is what upgrades it.
		return waiting, ErrPackageManaged
	}
	if err := m.Start(ch); err != nil {
		return waiting, err
	}
	return "installing " + chk.Latest + "; the service restarts when it is in place", nil
}

// Start begins downloading and installing the latest release on the
// channel. It returns once the work is running in the background.
func (m *Manager) Start(ch Channel) error {
	if m.PackageManaged {
		return ErrPackageManaged
	}
	m.mu.Lock()
	switch m.status.State {
	case Checking, Downloading, Verifying, Installing, Restarting:
		m.mu.Unlock()
		return ErrBusy
	}
	m.status = Status{State: Checking, UpdatedAt: time.Now()}
	m.mu.Unlock()

	go m.run(ch)
	return nil
}

func (m *Manager) run(ch Channel) {
	log := m.Log
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	chk, err := m.Client.Check(ctx, m.Current, ch)
	if err != nil {
		m.set(Failed, "", "check failed: "+err.Error(), 0, 0)
		return
	}
	if !chk.Available {
		m.set(Failed, chk.Latest, "no newer release on the "+string(ch)+" channel", 0, 0)
		return
	}
	version := chk.Release.Version
	m.set(Downloading, version, "", 0, chk.Asset.Size)
	dir := filepath.Dir(m.Installer.Binary)
	path, err := m.Client.Download(ctx, chk.Release, dir, func(stage string, done, total int64) {
		switch stage {
		case "downloading":
			m.set(Downloading, version, "", done, total)
		case "verifying":
			m.set(Verifying, version, "", 0, 0)
		case "extracting":
			m.set(Installing, version, "", 0, 0)
		}
	})
	if err != nil {
		log.Error("update download failed", "version", version, "err", err)
		m.set(Failed, version, err.Error(), 0, 0)
		return
	}
	m.set(Installing, version, "", 0, 0)
	if err := m.Installer.Install(ctx, path); err != nil {
		log.Error("update install failed", "version", version, "err", err)
		m.set(Failed, version, err.Error(), 0, 0)
		return
	}
	log.Info("update installed; restarting", "from", m.Current, "to", version)
	m.set(Restarting, version, fmt.Sprintf("restarting into %s", version), 0, 0)
}
