package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/update"
)

// updaterServer serves an updater that has already checked once. It is
// given no client, so anything reaching for GitHub from these endpoints
// would fail here rather than quietly work.
func updaterServer(t *testing.T) (*httptest.Server, *auth.Service) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	updater := &update.Manager{Current: "0.3.0", Cache: update.NewCache(dir)}
	updater.Cache.Update(func(s *update.Snapshot) {
		*s = update.Snapshot{
			LastCheck: time.Now().Add(-2 * time.Hour), Channel: update.Stable,
			Current: "0.3.0", Latest: "0.4.0", Available: true,
			Release: &update.Release{Version: "0.4.0", Tag: "v0.4.0"},
		}
	})
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Updater: updater}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, as
}

// The dashboard draws its "an update is waiting" line from here on every
// page load: it has to be answerable by a viewer, and out of what the
// last scheduled check wrote down rather than by asking GitHub again.
func TestUpdateStatusServesTheCachedCheck(t *testing.T) {
	t.Parallel()
	srv, as := updaterServer(t)
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
