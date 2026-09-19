package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/update"
)

// updaterServer serves the updater built for its directory behind an
// admin session.
func updaterServer(t *testing.T, build func(dir string) *update.Manager) (*httptest.Server, *auth.Service) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Updater: build(dir)}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, as
}

// checkedUpdater has already checked once. It is given no client, so
// anything reaching for GitHub from these endpoints would fail here rather
// than quietly work.
func checkedUpdater(dir string) *update.Manager {
	updater := &update.Manager{Current: "0.3.0", Cache: update.NewCache(dir)}
	updater.Cache.Update(func(s *update.Snapshot) {
		*s = update.Snapshot{
			LastCheck: time.Now().Add(-2 * time.Hour), Channel: update.Stable,
			Current: "0.3.0", Latest: "0.4.0", Available: true,
			Release: &update.Release{Version: "0.4.0", Tag: "v0.4.0"},
		}
	})
	return updater
}

// The dashboard draws its "an update is waiting" line from here on every
// page load: it has to be answerable by a viewer, and out of what the
// last scheduled check wrote down rather than by asking GitHub again.
func TestUpdateStatusServesTheCachedCheck(t *testing.T) {
	t.Parallel()
	srv, as := updaterServer(t, checkedUpdater)
	if err := as.SetPassword("watcher", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := as.SetRole("watcher", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "watcher", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/update/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s, want a viewer to see what is waiting", resp.StatusCode, raw)
	}
	var body struct {
		Check  update.Snapshot `json:"check"`
		Status update.Status   `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Check.Latest != "0.4.0" || !body.Check.Available || body.Check.LastCheck.IsZero() {
		t.Errorf("check = %+v", body.Check)
	}
	if body.Check.Release == nil || body.Check.Release.Tag != "v0.4.0" {
		t.Errorf("release = %+v, want the card's tag, date and notes", body.Check.Release)
	}
	// And the other half of the answer: nothing is installing.
	if body.Status.State != update.Idle {
		t.Errorf("status = %+v", body.Status)
	}

	// Asking GitHub is still the admin's to do.
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/update/check", nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("check: %d %s, want 403 for a viewer", resp.StatusCode, raw)
	}
}

// Pressing Install is one request that hands the work to the background.
// The answer is the progress so far, a second press while it runs is
// refused, and a channel the server does not know is a bad request rather
// than a download from nowhere.
func TestUpdateApplyStartsOnceAndReportsItsProgress(t *testing.T) {
	t.Parallel()
	// GitHub, holding the door: the check never finishes until the test
	// lets it go, so the update stays in its first state throughout.
	release := make(chan struct{})
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(github.Close)
	t.Cleanup(func() { close(release) })
	srv, _ := updaterServer(t, func(dir string) *update.Manager {
		return &update.Manager{
			Client:    &update.Client{Repo: update.DefaultRepo, BaseURL: github.URL},
			Installer: &update.Installer{Binary: filepath.Join(dir, "ostiole")},
			Current:   "0.3.0",
			Cache:     update.NewCache(dir),
		}
	})

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/update/apply", map[string]string{"channel": "nightly"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown channel: %d %s, want 400", resp.StatusCode, raw)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/update/apply", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("apply: %d %s, want 202", resp.StatusCode, raw)
	}
	var st update.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != update.Checking {
		t.Errorf("status = %+v, want the first state of the run", st)
	}

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/update/apply", nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("second apply: %d %s, want 409 while one runs", resp.StatusCode, raw)
	}
	// And the page polling meanwhile sees the same run.
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/update/status", nil)
	var body struct {
		Status update.Status `json:"status"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || body.Status.State != update.Checking {
		t.Errorf("status: %d %+v", resp.StatusCode, body.Status)
	}
}

// A binary the distro installed is the package manager's to update; the
// button says so rather than overwriting a file dnf owns.
func TestUpdateApplyRefusesAPackagedBinary(t *testing.T) {
	t.Parallel()
	srv, _ := updaterServer(t, func(dir string) *update.Manager {
		return &update.Manager{Current: "0.3.0", PackageManaged: true, Cache: update.NewCache(dir)}
	})
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/update/apply", nil)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "package") {
		t.Errorf("apply: %d %s, want 422 naming the package manager", resp.StatusCode, raw)
	}
}

// A server with no updater wired in, which is how the dev and e2e servers
// run, answers the button with a 503 rather than a panic.
func TestUpdateApplyWithoutAnUpdater(t *testing.T) {
	t.Parallel()
	srv, _, _ := roleServer(t)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/update/apply", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("apply: %d %s, want 503", resp.StatusCode, raw)
	}
}
