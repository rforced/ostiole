package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/diff"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/version"
)

func (a *api) registerBackup(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/config/backup", a.guard(a.downloadBackup))
	mux.HandleFunc("POST /api/v1/config/restore", a.guard(a.restoreBackup))
	mux.HandleFunc("POST /api/v1/config/diff", a.guard(a.diffConfigs))
}

// downloadBackup hands over the saved configuration as a file. Accounts
// come along only when asked for, because their hashes are the one thing
// in here worth stealing on their own.
func (a *api) downloadBackup(w http.ResponseWriter, r *http.Request) error {
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return err
	}
	opts := backup.Options{Ostiole: version.Version, Note: r.URL.Query().Get("note")}
	if r.URL.Query().Get("users") == "true" && a.auth != nil {
		opts.Users = a.auth.Users()
	}
	archive, err := backup.Create(cfg, opts)
	if err != nil {
		return err
	}
	raw, err := archive.Marshal()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", archive.Filename()))
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
	return nil
}

// restoreResponse is what the browser gets back: the configuration to load
// into the draft, what it contains, and how it differs from what is
// running, so nobody applies a backup blind.
type restoreResponse struct {
	Summary backup.Summary `json:"summary"`
	Config  *model.Config  `json:"config"`
	Changes []diff.Change  `json:"changes"`
}

// restoreBackup parses an uploaded file. It deliberately does not apply
// anything: the configuration goes into the draft and through the same
// commit-confirmed apply as any other change.
func (a *api) restoreBackup(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBackupBytes))
	if err != nil {
		return &badRequest{fmt.Errorf("could not read the uploaded file: %w", err)}
	}
	archive, err := backup.Parse(raw)
	if err != nil {
		return &badRequest{err}
	}
	res := restoreResponse{Summary: archive.Summary(), Config: archive.Config, Changes: []diff.Change{}}
	if current, err := a.engine.Store().Load(); err == nil {
		if changes, derr := diff.Compare(current, archive.Config); derr == nil {
			res.Changes = changes
		}
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// maxBackupBytes bounds an upload. A configuration with thousands of rules
// is still a fraction of this.
const maxBackupBytes = 8 << 20

// diffRequest compares two configurations. Each side is a revision id, the
// word "current" for the saved configuration, or an inline configuration
// for a draft that has not been applied.
type diffRequest struct {
	From       string        `json:"from,omitempty"`
	To         string        `json:"to,omitempty"`
	FromConfig *model.Config `json:"fromConfig,omitempty"`
	ToConfig   *model.Config `json:"toConfig,omitempty"`
}

func (a *api) diffConfigs(w http.ResponseWriter, r *http.Request) error {
	var req diffRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	from, err := a.configSide(req.From, req.FromConfig)
	if err != nil {
		return err
	}
	to, err := a.configSide(req.To, req.ToConfig)
	if err != nil {
		return err
	}
	changes, err := diff.Compare(from, to)
	if err != nil {
		return err
	}
	if changes == nil {
		changes = []diff.Change{}
	}
	writeJSON(w, http.StatusOK, changes)
	return nil
}

// configSide resolves one end of a comparison.
func (a *api) configSide(ref string, inline *model.Config) (*model.Config, error) {
	if inline != nil {
		return inline, nil
	}
	switch strings.TrimSpace(ref) {
	case "", "current":
		return a.engine.Store().Load()
	case "running":
		cfg := a.engine.Effective()
		if cfg == nil {
			return nil, errors.New("nothing is running yet")
		}
		return cfg, nil
	default:
		return a.engine.Store().LoadRevision(ref)
	}
}
