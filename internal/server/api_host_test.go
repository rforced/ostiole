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

// The report answers on a router with nothing on it, which is what the
// end-to-end server and a dev run are.
func TestHostReportAnswers(t *testing.T) {
	t.Parallel()
	srv := hostServer(t)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/host", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	body := string(raw)
	for _, want := range []string{`"units"`, `"present"`, `"network"`, `"legacy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the report has no %s: %s", want, body)
		}
	}
}
