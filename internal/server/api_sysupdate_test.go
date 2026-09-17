package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// scriptedRunner answers the package manager commands from a table, so
// the API tests never touch the box they run on.
type scriptedRunner struct{ out map[string]string }

func (s scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return []byte(s.out[strings.TrimSpace(name+" "+strings.Join(args, " "))]), nil
}

func updateServer(t *testing.T, root bool) (*httptest.Server, *engine.Engine, *auth.Service) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := scriptedRunner{out: map[string]string{
		"dnf -q --refresh check-update":                   "bash.x86_64 5.2.26-4.el10 baseos\nopenssl-libs.x86_64 1:3.2.2-12.el10 baseos\n",
		"dnf -q --cacheonly check-update --security":      "openssl-libs.x86_64 1:3.2.2-12.el10 baseos\n",
		"rpm -q --qf %{NAME} %{EVR}\\n openssl-libs bash": "openssl-libs 1:3.2.2-11.el10\nbash 5.2.26-3.el10\n",
		"dnf needs-restarting -r":                         "No core libraries or services have been updated since boot-up.",
	}}
	packages := sysupdate.New(sysupdate.Options{
		PackageManager: "dnf", StateDir: dir, Run: run, Root: root, Log: slog.New(slog.DiscardHandler),
	})
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Packages: packages}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, eng, as
}

func TestSystemUpdatesReportsTheModeInForce(t *testing.T) {
	t.Parallel()
	srv, eng, _ := updateServer(t, true)

	// Nothing has been configured, so the defaults are what the page
	// shows: security fixes, weekly.
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/system/updates", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var st sysupdate.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != string(model.UpdateSecurity) || st.Schedule != model.DefaultUpdateSchedule {
		t.Errorf("mode = %q, schedule = %q", st.Mode, st.Schedule)
	}
	if st.Manager != "dnf" || !st.Available || !st.SecurityCapable {
		t.Errorf("status = %+v", st)
	}

	// A check fills in what is waiting.
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/system/updates/check", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check: %d %s", resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Pending.Packages) != 2 || st.Pending.Security != 1 {
		t.Errorf("pending = %+v", st.Pending)
	}
	if st.RebootRequired {
		t.Error("a reboot was reported that dnf did not ask for")
	}

	// A configured mode is what the page shows next.
	cfg := starter()
	cfg.Updates.System.Mode = model.UpdateManual
	cfg.Updates.System.Schedule = "0 3 * * 1"
	if _, err := eng.Store().Save(cfg, ""); err != nil {
		t.Fatal(err)
	}
	_, raw = do(t, srv, http.MethodGet, "/api/v1/system/updates", nil)
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != string(model.UpdateManual) || st.Schedule != "0 3 * * 1" {
		t.Errorf("mode = %q, schedule = %q", st.Mode, st.Schedule)
	}
}

func TestSystemUpdatesExplainsAnUnprivilegedBox(t *testing.T) {
	t.Parallel()
	srv, _, _ := updateServer(t, false)
	_, raw := do(t, srv, http.MethodGet, "/api/v1/system/updates", nil)
	var st sysupdate.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.Available || !strings.Contains(st.Unavailable, "root") {
		t.Errorf("status = %+v, want an honest explanation", st)
	}
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/system/updates/check", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("check: %d %s, want it refused with a reason", resp.StatusCode, raw)
	}
}

func TestSystemUpdateEndpointsNeedTheirRoles(t *testing.T) {
	t.Parallel()
	srv, _, as := updateServer(t, true)
	// Sign in as somebody who may only look, which replaces the admin's
	// cookie in the jar.
	if err := as.SetPassword("watcher", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := as.SetRole("watcher", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login", credentials{Username: "watcher", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	for _, path := range []string{"/api/v1/system/updates/apply", "/api/v1/system/reboot"} {
		if resp, raw := do(t, srv, http.MethodPost, path, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403 for a viewer", path, resp.StatusCode, raw)
		}
	}
	// Reading is still allowed: knowing the box is behind is not a
	// privileged act.
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/system/updates", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("read: %d, want a viewer to see what is waiting", resp.StatusCode)
	}
}

func TestSystemUpdatesAreHiddenWithoutAPackageManager(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/system/updates", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 where nothing drives a package manager", resp.StatusCode)
	}
}
