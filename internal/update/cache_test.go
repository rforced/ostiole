package update

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A check has to leave something behind: the page that asks "is there an
// update?" on every load must be answered by this router, not by GitHub.
func TestCheckIsRemembered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := releaseServer(t, []map[string]any{
		release("v0.4.0", "## Added\n\nA new page.\n"),
		release("v0.3.0", "Security-Release: yes\n"),
	})
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.2.0", Cache: NewCache(dir)}

	if got := m.Cached(); !got.LastCheck.IsZero() {
		t.Errorf("a router that has never checked claimed it had: %+v", got)
	}
	if _, err := m.Check(t.Context(), Stable); err != nil {
		t.Fatal(err)
	}
	got := m.Cached()
	if got.LastCheck.IsZero() || got.CheckError != "" {
		t.Errorf("snapshot = %+v", got)
	}
	if got.Latest != "0.4.0" || !got.Available || got.Current != "0.2.0" || got.Channel != Stable {
		t.Errorf("snapshot = %+v", got)
	}
	if !got.Security || len(got.SecurityReleases) != 1 {
		t.Errorf("the security release since 0.2.0 was not recorded: %+v", got)
	}
	// The release is kept whole, because the card shows its notes and link.
	if got.Release == nil || got.Release.Tag != "v0.4.0" || got.Release.Notes == "" {
		t.Errorf("release = %+v", got.Release)
	}

	// It survives the restart an update itself causes.
	raw, err := os.ReadFile(filepath.Join(dir, "ostiole.json"))
	if err != nil {
		t.Fatalf("nothing was written beside the package manager's state: %v", err)
	}
	var onDisk Snapshot
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Latest != "0.4.0" || !onDisk.Available {
		t.Errorf("on disk = %+v", onDisk)
	}
	if reread := NewCache(dir).Snapshot(); reread.Latest != "0.4.0" {
		t.Errorf("reread = %+v", reread)
	}
	// 0600: what a firewall is behind on is nobody else's business.
	info, err := os.Stat(filepath.Join(dir, "ostiole.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v", perm)
	}
}

// Installing what the check found restarts the daemon into that release,
// and the process that comes back reads a file still saying one is
// waiting. It must not offer the version it is.
func TestCachedStopsOfferingTheReleaseNowRunning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := releaseServer(t, []map[string]any{release("v0.4.0", "Security-Release: yes\n")})
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.3.0", Cache: NewCache(dir)}
	if _, err := m.Check(t.Context(), Stable); err != nil {
		t.Fatal(err)
	}
	if got := m.Cached(); !got.Available || !got.Security {
		t.Fatalf("snapshot = %+v, want 0.4.0 waiting", got)
	}

	// The same file, read by the version it was telling us to install.
	after := &Manager{Client: m.Client, Current: "0.4.0", Cache: NewCache(dir)}
	got := after.Cached()
	if got.Available || got.Security || got.SecurityReleases != nil {
		t.Errorf("snapshot = %+v, want nothing waiting", got)
	}
	// What the check found is still worth showing, against the version
	// that is running now rather than the one that asked.
	if got.Latest != "0.4.0" || got.Current != "0.4.0" || got.LastCheck.IsZero() {
		t.Errorf("snapshot = %+v", got)
	}

	// A router that landed short of the latest is still behind it.
	behind := &Manager{Client: m.Client, Current: "0.3.5", Cache: NewCache(dir)}
	if got := behind.Cached(); !got.Available {
		t.Errorf("snapshot = %+v, want 0.4.0 still waiting", got)
	}
}

// A check that could not reach GitHub says so rather than quietly leaving
// last week's answer looking fresh.
func TestAFailedCheckIsRecorded(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.2.0", Cache: NewCache(dir)}

	if _, err := m.Check(t.Context(), Stable); err == nil {
		t.Fatal("a failing check reported success")
	}
	got := m.Cached()
	if got.LastCheck.IsZero() || got.CheckError == "" {
		t.Errorf("snapshot = %+v, want the failure written down", got)
	}
	// And which channel it failed to ask about, so the card can tell
	// whether the answer is even about the channel it is showing.
	if got.Channel != Stable {
		t.Errorf("channel = %q", got.Channel)
	}
}

// A manager with nowhere to write still works; it simply remembers
// nothing, which is what a test and a locked-down router both get.
func TestACacheLessManagerStillChecks(t *testing.T) {
	t.Parallel()
	srv := releaseServer(t, []map[string]any{release("v0.4.0", "")})
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.4.0"}
	if _, err := m.Check(t.Context(), Stable); err != nil {
		t.Fatal(err)
	}
	if got := m.Cached(); got.Latest != "" {
		t.Errorf("snapshot = %+v, want nothing remembered", got)
	}
}

func TestCheckScheduledSaysWhatItFound(t *testing.T) {
	t.Parallel()
	srv := releaseServer(t, []map[string]any{
		release("v0.4.0", "## Added\n\nA new page.\n"),
		release("v0.3.0", "Security-Release: yes\n"),
	})
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.2.0", Cache: NewCache(t.TempDir())}

	// Behind, with a security release in between: the line says both.
	out, err := m.CheckScheduled(t.Context(), Stable)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0.4.0 is available") || !strings.Contains(out, "v0.3.0") {
		t.Errorf("out = %q", out)
	}
	// Checking installs nothing, whatever it finds.
	if m.Status().State != Idle {
		t.Error("a check started an update")
	}

	// Up to date, and a channel with nothing on it at all.
	m = &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.4.0", Cache: NewCache(t.TempDir())}
	if out, err = m.CheckScheduled(t.Context(), Stable); err != nil || !strings.Contains(out, "up to date") {
		t.Errorf("out = %q, err = %v", out, err)
	}
	empty := releaseServer(t, nil)
	m = &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: empty.URL}, Current: "0.4.0", Cache: NewCache(t.TempDir())}
	if out, err = m.CheckScheduled(t.Context(), Beta); err != nil || !strings.Contains(out, "no release on the beta channel") {
		t.Errorf("out = %q, err = %v", out, err)
	}
}
