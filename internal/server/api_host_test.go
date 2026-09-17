package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/store"
)

// quietRunner is a router on which every command prints nothing, so the
// report never asks the machine the test runs on.
type quietRunner struct{}

func (quietRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

// hostServer is a signed-in admin on a router whose daemon is root, so
// the host actions are offered rather than refused out of hand. The
// router has nothing installed and no leftovers, whatever the machine
// running the test has.
func hostServer(t *testing.T) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := host.Deps{
		Root: true, Dir: dir, Run: quietRunner{}, Proc: t.TempDir(),
		Locate: func(string) string { return "" },
	}
	srv := httptest.NewServer(Handler(Deps{Engine: eng, Auth: as, Host: h}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv
}

// The component keys become the argv of a command run as root, so they
// are checked here before anything is driven, not only by the command.
func TestHostSetupRejectsAnUnknownComponent(t *testing.T) {
	t.Parallel()
	srv := hostServer(t)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/host/setup",
		map[string]any{"components": []string{"--all"}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "not a component") {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/host/setup", map[string]any{"components": []string{}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

// A skipped step is recorded with who skipped it, and the report says
// so. A step that is done is never shown as skipped, and a step that was
// never skipped carries no timestamp at all.
func TestHostSkipRecordsWho(t *testing.T) {
	t.Parallel()
	srv := hostServer(t)
	// Nothing is installed on this router, so the packages step is the
	// one that is outstanding.
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/host/steps/packages", map[string]any{"skip": true})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	body := string(raw)
	if !strings.Contains(body, `"step":"packages","state":"skipped"`) || !strings.Contains(body, `"by":"admin"`) {
		t.Errorf("the skip is not on the packages step with who did it: %s", body)
	}
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("a step that was never skipped carries a zero time: %s", body)
	}
	if !strings.Contains(body, `"prepared":true`) {
		t.Errorf("a router whose only outstanding step is skipped should be prepared: %s", body)
	}
	resp, raw = do(t, srv, http.MethodPost, "/api/v1/host/steps/nonsense", map[string]any{"skip": true})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}
