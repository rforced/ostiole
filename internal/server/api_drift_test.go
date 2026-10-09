package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/store"
)

// The status says which parts an apply would change and how many lines,
// and the drift route has the lines; neither says anything right after an
// apply.
func TestDriftThroughTheAPI(t *testing.T) {
	t.Parallel()
	st := store.New(t.TempDir())
	eng := engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(st.Dir)
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
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/apply", map[string]any{"config": starter(), "baseRevision": revision(t, srv)}); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply: %d %s", resp.StatusCode, raw)
	}

	status := func() map[string]json.RawMessage {
		t.Helper()
		resp, raw := do(t, srv, http.MethodGet, "/api/v1/status", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d %s", resp.StatusCode, raw)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := status(); got["drift"] != nil || got["configured"] == nil {
		t.Errorf("status after an apply: %v", got)
	}
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/apply/drift", nil)
	if resp.StatusCode != http.StatusOK || string(raw) != `{"parts":[],"changes":[]}`+"\n" {
		t.Errorf("drift after an apply: %d %s", resp.StatusCode, raw)
	}

	// What an earlier release saved: the same ruleset bar a line.
	saved, err := st.LoadRuleset()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(cfg, strings.Replace(saved, "\n", "\n# an older release's line\n", 1)); err != nil {
		t.Fatal(err)
	}
	if got := string(status()["drift"]); got != `{"parts":["Firewall"],"changes":1}` {
		t.Errorf("status drift = %s", got)
	}
	resp, raw = do(t, srv, http.MethodGet, "/api/v1/apply/drift", nil)
	var d engine.Drift
	if err := json.Unmarshal(raw, &d); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("drift: %d %s", resp.StatusCode, raw)
	}
	if len(d.Changes) != 1 || d.Changes[0].Kind != "removed" || d.Changes[0].Before != "# an older release's line" {
		t.Errorf("drift = %+v", d)
	}
}
