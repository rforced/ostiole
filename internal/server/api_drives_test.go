package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/host"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/smart"
	"github.com/rforced/ostiole/internal/store"
)

// smartStub answers like the smartctl on the router did, from the
// documents it printed, and records every command line.
type smartStub struct {
	mu    sync.Mutex
	calls [][]string
}

func (s *smartStub) run(_ context.Context, _ string, args ...string) ([]byte, int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, slices.Clone(args))
	s.mu.Unlock()
	switch {
	case slices.Contains(args, "--scan-open"):
		return driveFixture("scan.json"), 0, nil
	case slices.Contains(args, "-t"), slices.Contains(args, "-X"):
		return []byte(`{"smartctl":{"version":[7,5],"exit_status":0}}`), 0, nil
	case !slices.Contains(args, "--json=c"):
		return []byte("smartctl 7.5 2025-04-30 r5714\nDevice Model: GOFATOO 256GB SSD\n"), 0, nil
	default:
		return driveFixture("sat-ssd.json"), 0, nil
	}
}

func (s *smartStub) ran(arg string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if slices.Contains(c, arg) {
			return true
		}
	}
	return false
}

// driveFixture reads one of the documents the smart package keeps, so
// the API is tested against what smartctl actually prints.
func driveFixture(name string) []byte {
	raw, err := os.ReadFile(filepath.Join("..", "smart", "testdata", name))
	if err != nil {
		panic(err)
	}
	return raw
}

// newDrivesServer is a logged-in server with the drive routes wired to
// the stub, and a viewer token to try them with.
func newDrivesServer(t *testing.T, client *smart.Client) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(Deps{
		Engine: eng, Auth: as, Tokens: tokens, Drives: client,
		Host: host.Deps{Root: true},
	}))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup",
		credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, mintToken(t, srv, "look", string(auth.RoleViewer))
}

// The page asks for every drive at once and gets the tool's version with
// them, because an empty list means one of two different things.
func TestDrivesList(t *testing.T) {
	t.Parallel()
	stub := &smartStub{}
	srv, _ := newDrivesServer(t, &smart.Client{Bin: "smartctl", Run: stub.run})

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var list drivesResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list.Tool != "7.5" || !list.Root {
		t.Errorf("tool = %q, root = %v", list.Tool, list.Root)
	}
	if len(list.Drives) != 1 || list.Drives[0].Name != "sda" || list.Drives[0].Health != "passed" {
		t.Fatalf("drives = %+v", list.Drives)
	}
	if len(list.Drives[0].Attributes) != 30 {
		t.Errorf("attributes = %d, want the whole table", len(list.Drives[0].Attributes))
	}

	resp, raw = do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives/sda", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("one drive: %d %s", resp.StatusCode, raw)
	}
	var one smart.Drive
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	if one.Model != "GOFATOO 256GB SSD" {
		t.Errorf("drive = %+v", one)
	}
	// A name the scan does not know is a 404, and never a device path.
	if resp, raw := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives/sdz", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown drive: %d %s", resp.StatusCode, raw)
	}
}

// Without smartctl the page still answers, so it can say what is missing
// rather than showing an error a refresh will not fix.
func TestDrivesWithoutTheTool(t *testing.T) {
	t.Parallel()
	srv, _ := newDrivesServer(t, nil)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var list drivesResponse
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list.Tool != "" || len(list.Drives) != 0 {
		t.Errorf("list = %+v", list)
	}
	if !strings.Contains(string(raw), `"drives":[]`) {
		t.Errorf("drives = %s, want an array rather than null", raw)
	}
	// One drive is a different question, and the answer is that there is
	// nothing here to ask with.
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives/sda", nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("one drive: %d, want 503", resp.StatusCode)
	}
}

// Starting a test answers with the drive as it reads afterwards, so the
// page shows the progress bar without asking again.
func TestDriveSelfTest(t *testing.T) {
	t.Parallel()
	stub := &smartStub{}
	srv, viewer := newDrivesServer(t, &smart.Client{Bin: "smartctl", Run: stub.run})

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/diagnostics/drives/sda/self-test",
		selfTestRequest{Kind: "long"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var d smart.Drive
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Name != "sda" {
		t.Errorf("drive = %+v", d)
	}
	if !stub.ran("long") {
		t.Error("the extended test never reached the command line")
	}

	if resp, _ := do(t, srv, http.MethodDelete, "/api/v1/diagnostics/drives/sda/self-test", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("abort: %d", resp.StatusCode)
	}
	if !stub.ran("-X") {
		t.Error("the abort never reached the command line")
	}

	// A test the drive does not offer, and one that is not a test at all.
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/diagnostics/drives/sda/self-test",
		selfTestRequest{Kind: "conveyance"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("conveyance: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/diagnostics/drives/sda/self-test",
		selfTestRequest{Kind: "scrub"}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown kind: %d %s", resp.StatusCode, raw)
	}

	// Reading is a viewer's business; running a test on the drive is not.
	if got := probe(t, srv, http.MethodGet, "/api/v1/diagnostics/drives", viewer); got != http.StatusOK {
		t.Errorf("viewer read: %d", got)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if got := probe(t, srv, method, "/api/v1/diagnostics/drives/sda/self-test", viewer); got != http.StatusForbidden {
			t.Errorf("viewer %s: %d, want 403", method, got)
		}
	}
}

// The full report downloads as a text file named after the drive.
func TestDriveReport(t *testing.T) {
	t.Parallel()
	stub := &smartStub{}
	srv, _ := newDrivesServer(t, &smart.Client{Bin: "smartctl", Run: stub.run})

	resp, raw := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives/sda/report", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("content type = %q", got)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="ostiole-sda-smart-`) || !strings.HasSuffix(cd, `.txt"`) {
		t.Errorf("content disposition = %q", cd)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("cache control = %q", resp.Header.Get("Cache-Control"))
	}
	if !strings.Contains(string(raw), "GOFATOO") {
		t.Errorf("report = %q", raw)
	}
	if resp, _ := do(t, srv, http.MethodGet, "/api/v1/diagnostics/drives/sdz/report", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown drive: %d, want 404", resp.StatusCode)
	}
}
