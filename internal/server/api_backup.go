package server

import (
	"encoding/json"
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

func (a *api) registerBackup(mux *router) {
	mux.HandleFunc("POST /api/v1/config/backup", a.admin(a.downloadBackup))
	mux.HandleFunc("POST /api/v1/config/restore", a.write(a.restoreBackup))
	mux.HandleFunc("POST /api/v1/config/diff", a.read(a.diffConfigs))
	mux.HandleFunc("GET /api/v1/config/backup/remote", a.write(a.remoteCopies))
	mux.HandleFunc("POST /api/v1/config/backup/remote/restore", a.write(a.restoreRemote))
}

// backupRequest is what the browser asks for. It is a POST body and not a
// query string because a passphrase in a URL is a passphrase in the
// access log, the history and the referrer.
type backupRequest struct {
	Note       string `json:"note,omitempty"`
	Users      bool   `json:"users,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Redact     bool   `json:"redact,omitempty"`
}

// downloadBackup hands over the saved configuration as a file. Accounts
// come along only when asked for, because their hashes are the one thing
// in here worth stealing on their own.
func (a *api) downloadBackup(w http.ResponseWriter, r *http.Request) error {
	var req backupRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return err
	}
	opts := backup.Options{
		Ostiole:    version.Version,
		Note:       req.Note,
		Passphrase: req.Passphrase,
		Redact:     req.Redact,
	}
	if req.Users && a.auth != nil {
		opts.Users = a.auth.Users()
	}
	archive, err := backup.Create(cfg, opts)
	if errors.Is(err, backup.ErrRedactedUsers) {
		return &badRequest{err}
	}
	if err != nil {
		return err
	}
	raw, err := archive.Bytes()
	if err != nil {
		return err
	}
	contentType := "application/json"
	if archive.Encrypted() {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
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

// restoreRequest carries the uploaded file. The bytes are base64 because
// an encrypted backup is not text, and the passphrase is beside them
// rather than in the URL.
type restoreRequest struct {
	Data       []byte `json:"data"`
	Passphrase string `json:"passphrase,omitempty"`
}

// restoreBackup parses an uploaded file. It deliberately does not apply
// anything: the configuration goes into the draft and through the same
// commit-confirmed apply as any other change.
func (a *api) restoreBackup(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBackupBytes))
	if err != nil {
		return &badRequest{fmt.Errorf("could not read the uploaded file: %w", err)}
	}
	var req restoreRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return &badRequest{fmt.Errorf("could not read the uploaded file: %w", err)}
	}
	raw, err := backup.Decrypt(req.Data, req.Passphrase)
	if err != nil {
		return &badRequest{err}
	}
	archive, err := backup.Parse(raw)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, a.restoreFrom(archive))
	return nil
}

// restoreFrom describes a parsed backup against the saved
// configuration, which is what both restores answer with.
func (a *api) restoreFrom(archive *backup.Archive) restoreResponse {
	res := restoreResponse{Summary: archive.Summary(), Config: archive.Config, Changes: []diff.Change{}}
	if current, err := a.engine.Store().Load(); err == nil {
		// A backup that matches has no changes, not no list: the page
		// reads the empty list and says so.
		if changes, derr := diff.Compare(current, archive.Config); derr == nil && changes != nil {
			res.Changes = changes
		}
	}
	return res
}

// remoteCopiesResponse lists what is in the bucket. A router with no
// bucket configured answers with an empty list rather than an error, so
// the page has one shape to render.
type remoteCopiesResponse struct {
	Enabled bool          `json:"enabled"`
	Copies  []backup.Copy `json:"copies"`
}

// remoteBucket builds the bucket from the saved configuration, which is
// the one the cron reads. A draft that has not been applied does not
// reach anything. A nil bucket means the copies are switched off.
func (a *api) remoteBucket() (*backup.Remote, model.RemoteBackup, error) {
	cfg, err := a.engine.Store().Load()
	if err != nil {
		return nil, model.RemoteBackup{}, err
	}
	settings := cfg.Backup.Remote
	if !settings.Enabled {
		return nil, settings, nil
	}
	build := a.bucket
	if build == nil {
		build = func(r model.RemoteBackup, hostname string) (*backup.Remote, error) {
			return backup.NewRemote(r, hostname, "ostiole/"+version.Version)
		}
	}
	remote, err := build(settings, cfg.System.Hostname)
	if err != nil {
		return nil, settings, &badRequest{err}
	}
	return remote, settings, nil
}

func (a *api) remoteCopies(w http.ResponseWriter, r *http.Request) error {
	remote, _, err := a.remoteBucket()
	if err != nil {
		return err
	}
	res := remoteCopiesResponse{Copies: []backup.Copy{}}
	if remote == nil {
		writeJSON(w, http.StatusOK, res)
		return nil
	}
	copies, err := remote.List(r.Context())
	if err != nil {
		return &upstream{err}
	}
	res.Enabled, res.Copies = true, copies
	writeJSON(w, http.StatusOK, res)
	return nil
}

// remoteRestoreRequest names one copy in the bucket. The passphrase is
// the configured one unless an older copy was locked with another.
type remoteRestoreRequest struct {
	Key        string `json:"key"`
	Passphrase string `json:"passphrase,omitempty"`
}

// restoreRemote reads one copy back out of the bucket and reports what it
// would change. Like the file restore, it applies nothing.
func (a *api) restoreRemote(w http.ResponseWriter, r *http.Request) error {
	var req remoteRestoreRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	remote, settings, err := a.remoteBucket()
	if err != nil {
		return err
	}
	if remote == nil {
		return &badRequest{errors.New("remote backup is off")}
	}
	raw, err := remote.Fetch(r.Context(), req.Key)
	switch {
	case errors.Is(err, backup.ErrNotACopy):
		return &badRequest{err}
	case err != nil:
		return &upstream{err}
	}
	passphrase := req.Passphrase
	if passphrase == "" {
		passphrase = settings.Passphrase
	}
	plain, err := backup.Decrypt(raw, passphrase)
	if err != nil {
		return &badRequest{err}
	}
	archive, err := backup.Parse(plain)
	if err != nil {
		return &badRequest{err}
	}
	writeJSON(w, http.StatusOK, a.restoreFrom(archive))
	return nil
}

// maxBackupBytes bounds an upload. A configuration with thousands of rules
// is still a fraction of this.
const maxBackupBytes = 8 << 20

// diffRequest compares two configurations. Each side is a revision id, the
// word "current" for the saved configuration, or an inline configuration
// for a draft that has not been applied.
type diffRequest struct {
	From       string       `json:"from,omitempty"`
	To         string       `json:"to,omitempty"`
	FromConfig *draftConfig `json:"fromConfig,omitempty"`
	ToConfig   *draftConfig `json:"toConfig,omitempty"`
}

func (a *api) diffConfigs(w http.ResponseWriter, r *http.Request) error {
	var req diffRequest
	if err := decodeJSON(r, &req); err != nil {
		return err
	}
	from, err := a.configSide(r, req.From, req.FromConfig.config())
	if err != nil {
		return err
	}
	to, err := a.configSide(r, req.To, req.ToConfig.config())
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

// configSide resolves one end of a comparison. A stored side comes the
// way the caller may read it: a viewer comparing against an empty
// configuration would otherwise read every secret as a change.
func (a *api) configSide(r *http.Request, ref string, inline *model.Config) (*model.Config, error) {
	if inline != nil {
		return inline, nil
	}
	var cfg *model.Config
	var err error
	switch strings.TrimSpace(ref) {
	case "", "current":
		cfg, err = a.engine.Store().Load()
	case "running":
		if cfg = a.engine.Effective(); cfg == nil {
			err = errors.New("nothing is running yet")
		}
	default:
		cfg, err = a.engine.Store().LoadRevision(ref)
	}
	if err != nil {
		return nil, err
	}
	return a.visible(r, cfg), nil
}
