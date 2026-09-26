package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/backup"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/s3"
	"github.com/rforced/ostiole/internal/s3/s3test"
	"github.com/rforced/ostiole/internal/store"
)

// backupServer is a signed-in admin with a configuration to back up: a
// starter with a tunnel, so there is a secret in it worth redacting.
func backupServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _, _ := roleServer(t)
	cfg := starter()
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "vpn"})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name:      "wg0",
		Zone:      "vpn",
		Enabled:   true,
		IPv4:      model.IPv4{Mode: model.AddrStatic, Address: "10.66.0.1/24"},
		IPv6:      model.IPv6{Mode: model.AddrNone},
		WireGuard: &model.WireGuard{PrivateKey: "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}, // gitleaks:allow
	})
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	return srv
}

// A passphrase travels in the body, so the download is a POST and the
// file that comes back opens only with that passphrase.
func TestBackupDownloadEncrypts(t *testing.T) {
	t.Parallel()
	srv := backupServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/backup",
		map[string]any{"passphrase": "correct horse battery"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backup: %d %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("content type = %q", got)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, ".json.age") {
		t.Errorf("content disposition = %q", cd)
	}
	if !backup.IsEncrypted(raw) {
		t.Fatal("the file is not encrypted")
	}
	if _, err := backup.Decrypt(raw, "correct horse battery"); err != nil {
		t.Errorf("decrypt: %v", err)
	}
}

func TestBackupDownloadRedacts(t *testing.T) {
	t.Parallel()
	srv := backupServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/backup", map[string]any{"redact": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backup: %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=") {
		t.Error("the tunnel key is in a redacted backup")
	}
	// Accounts and redaction do not go together.
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/backup",
		map[string]any{"redact": true, "users": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("redacted backup with accounts = %d %s, want 400", resp.StatusCode, raw)
	}
}

// Restoring an encrypted backup needs its passphrase, and says so rather
// than reporting that the file is not a backup at all.
func TestRestoreNeedsThePassphrase(t *testing.T) {
	t.Parallel()
	srv := backupServer(t)
	_, file := do(t, srv, http.MethodPost, "/api/v1/config/backup",
		map[string]any{"passphrase": "one"})

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/restore", map[string]any{"data": file})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "passphrase is needed") {
		t.Errorf("no passphrase = %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/restore",
		map[string]any{"data": file, "passphrase": "two"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "does not open") {
		t.Errorf("wrong passphrase = %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/restore",
		map[string]any{"data": file, "passphrase": "one"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("right passphrase = %d %s", resp.StatusCode, raw)
	}
	var res restoreResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Config == nil || res.Config.System.Hostname != "fw" {
		t.Errorf("config = %+v", res.Config)
	}
}

// remoteServer is a signed-in admin whose saved configuration names a
// bucket, with two copies already in it. The bucket is the in-memory one
// and is reached through Deps rather than through the endpoint: the
// configuration can only name an https service, and this one is not.
func remoteServer(t *testing.T) (*httptest.Server, *s3test.Bucket, *auth.Service) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	bucket := s3test.New(t, "0055abc", "not-a-real-key")
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng,
		Auth:   as,
		Bucket: func(r model.RemoteBackup, hostname string) (*backup.Remote, error) {
			return &backup.Remote{
				S3:       bucket.Client(),
				Prefix:   r.PrefixOr(),
				Hostname: backup.SafeHostname(hostname),
				Keep:     r.Keep,
				Days:     r.Days,
			}, nil
		},
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup",
		credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}

	cfg := starter()
	cfg.Backup.Remote = model.RemoteBackup{
		Enabled:    true,
		Endpoint:   "https://s3.us-west-004.backblazeb2.com",
		Bucket:     bucket.Name,
		KeyID:      "0055abc",
		Secret:     "not-a-real-key",
		Passphrase: "correct horse",
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	for _, when := range []time.Time{
		time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC),
	} {
		a, err := backup.Create(cfg, backup.Options{Passphrase: "correct horse", Now: func() time.Time { return when }})
		if err != nil {
			t.Fatal(err)
		}
		body, err := a.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		bucket.Store(model.DefaultBackupPrefix+a.Filename(), body, when)
	}
	return srv, bucket, as
}

func TestRemoteCopiesAreListedNewestFirst(t *testing.T) {
	t.Parallel()
	srv, _, _ := remoteServer(t)

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/config/backup/remote", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var res remoteCopiesResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Enabled || len(res.Copies) != 2 {
		t.Fatalf("response = %+v", res)
	}
	if !strings.Contains(res.Copies[0].Name, "20260920") {
		t.Errorf("copies are not newest first: %+v", res.Copies)
	}
	if res.Copies[0].Hostname != "fw" || res.Copies[0].Size == 0 {
		t.Errorf("first copy = %+v", res.Copies[0])
	}
}

func TestRemoteCopiesAreEmptyWhenTheCopiesAreOff(t *testing.T) {
	t.Parallel()
	srv, bucket, _ := remoteServer(t)
	cfg := starter()
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	before := len(bucket.Seen())

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/config/backup/remote", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var res remoteCopiesResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Enabled || len(res.Copies) != 0 {
		t.Errorf("response = %+v, want it switched off and empty", res)
	}
	if n := len(bucket.Seen()) - before; n != 0 {
		t.Errorf("the bucket saw %d requests while the copies were off", n)
	}
}

func TestRemoteRestoreReadsOneCopy(t *testing.T) {
	t.Parallel()
	srv, _, _ := remoteServer(t)
	key := model.DefaultBackupPrefix + "fw-20260920-030000.json.age"

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/backup/remote/restore",
		map[string]any{"key": key})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.StatusCode, raw)
	}
	var res restoreResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Config == nil || res.Config.System.Hostname != "fw" {
		t.Errorf("config = %+v", res.Config)
	}
	if res.Summary.Hostname != "fw" {
		t.Errorf("summary = %+v", res.Summary)
	}
	// The copy is of the configuration that is saved, so there is
	// nothing to change — an empty list, never a missing one.
	if res.Changes == nil || len(res.Changes) != 0 {
		t.Errorf("changes = %v, want an empty list", res.Changes)
	}

	// A passphrase in the body wins, and a wrong one says so rather than
	// reporting that the file is not a backup.
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/backup/remote/restore",
		map[string]any{"key": key, "passphrase": "wrong"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "does not open") {
		t.Errorf("wrong passphrase = %d %s", resp.StatusCode, raw)
	}

	// Nothing but a backup under the prefix is fetched at all.
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/config/backup/remote/restore",
		map[string]any{"key": "../etc/shadow"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "not one of the router's backups") {
		t.Errorf("a key that is not a backup = %d %s", resp.StatusCode, raw)
	}
}

func TestRemoteCopiesReportWhatTheBucketRefused(t *testing.T) {
	t.Parallel()
	srv, bucket, _ := remoteServer(t)
	bucket.FailOnce(s3.OpList, http.StatusForbidden, "AccessDenied")

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/config/backup/remote", nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(raw), "the key may not list") {
		t.Errorf("refused list = %d %s", resp.StatusCode, raw)
	}
}

func TestRemoteBackupRoutesNeedAnOperator(t *testing.T) {
	t.Parallel()
	srv, _, as := remoteServer(t)
	// Sign in as somebody who may only look, which replaces the admin's
	// cookie in the jar.
	if err := as.CreateUser("watcher", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	login(t, srv, "watcher")
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/config/backup/remote", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer listing the copies = %d", resp.StatusCode)
	}
	resp, _ := do(t, srv, http.MethodPost, "/api/v1/config/backup/remote/restore", map[string]any{"key": "x"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a viewer restoring a copy = %d", resp.StatusCode)
	}
}

// A redacted backup restores as a draft whose validation names each
// secret it is missing.
func TestRestoreOfARedactedBackupSaysWhatIsMissing(t *testing.T) {
	t.Parallel()
	srv := backupServer(t)
	_, file := do(t, srv, http.MethodPost, "/api/v1/config/backup", map[string]any{"redact": true})

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/restore", map[string]any{"data": file})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restore: %d %s", resp.StatusCode, raw)
	}
	var res restoreResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Summary.Redacted {
		t.Error("the summary does not say the secrets are missing")
	}
	err := res.Config.Validate()
	if err == nil || !strings.Contains(err.Error(), "wireguard.privateKey") {
		t.Errorf("validation = %v, want it to name the tunnel key", err)
	}
}

// A viewer comparing the saved configuration with anything reads no
// secret in the changes, the way it reads none in the configuration.
func TestAViewersDiffCarriesNoSecret(t *testing.T) {
	t.Parallel()
	var as *auth.Service
	srv := newTestServerWith(t, func(d *Deps) { as = d.Auth })
	cfg := starter()
	cfg.Backup.Remote = model.RemoteBackup{
		Endpoint: "https://s3.example.net", Bucket: "router-backups", KeyID: "0055abc", Secret: "hunter2", Passphrase: "correct horse",
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", applyRequest{Config: (*draftConfig)(cfg)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}
	if err := as.CreateUser("eyes", testPassword, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "eyes", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	for _, from := range []string{"current", "running"} {
		resp, raw := do(t, srv, http.MethodPost, "/api/v1/config/diff", map[string]any{"from": from, "toConfig": starter()})
		if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), "hunter2") {
			t.Errorf("%s: %d %s", from, resp.StatusCode, raw)
		}
	}
}
