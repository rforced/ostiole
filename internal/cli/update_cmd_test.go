package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/rforced/ostiole/internal/update"
)

// The probe turns certificate verification off, so which hosts count as
// loopback is the whole of that decision's security boundary.
func TestLoopbackHost(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true}, // all of 127/8, not just the one address
		{"::1", true},
		{"localhost", true},
		{"", false},
		{"0.0.0.0", false},
		{"10.2.96.3", false},
		{"example.com", false},
		{"localhost.example.com", false},
		{"2606:4700:4700::1111", false},
	} {
		if got := loopbackHost(tc.host); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// The health URLs the installer writes have a port, and an IPv6 one would be
// bracketed; the check reads the host through url.Hostname so both forms
// arrive as a bare address.
func TestLoopbackHostFromHealthURL(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"https://127.0.0.1:443/api/v1/health", true},
		{"http://127.0.0.1:8080/api/v1/health", true},
		{"https://[::1]:443/api/v1/health", true},
		{"https://example.com:443/api/v1/health", false},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got := loopbackHost(u.Hostname()); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// releasesWith is the only GitHub these tests reach. It publishes one
// release besides an old one every router is past, signed with a key of
// the test's own.
func releasesWith(t *testing.T, version string, prerelease bool) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho ostiole " + version + "\n")
	_ = tw.WriteHeader(&tar.Header{Name: "ostiole", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	name := update.AssetName(version)
	sum := sha256.Sum256(tarball.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	files := map[string][]byte{
		name: tarball.Bytes(), "checksums.txt": []byte(sums), "checksums.txt.sig": []byte(update.Sign(priv, []byte(sums))),
	}

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/rforced/ostiole/releases", func(w http.ResponseWriter, _ *http.Request) {
		var assets []map[string]any
		for _, n := range []string{name, "checksums.txt", "checksums.txt.sig"} {
			assets = append(assets, map[string]any{"name": n, "browser_download_url": srv.URL + "/dl/" + n, "size": len(files[n])})
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"tag_name": "v" + version, "prerelease": prerelease, "published_at": "2026-09-15T00:00:00Z",
				"html_url": "https://github.com/rforced/ostiole/releases/tag/v" + version, "assets": assets},
			{"tag_name": "v0.0.1", "published_at": "2026-01-01T00:00:00Z", "assets": []any{}},
		})
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		raw, ok := files[path.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(raw)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pub
}

// fakeSystemd is systemd as a transcript. With refuse set, systemd-run
// fails the way it does when the restart unit cannot start.
type fakeSystemd struct {
	refuse bool
	calls  [][]string
}

func (s *fakeSystemd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, append([]string{name}, args...))
	if s.refuse && name == "systemd-run" {
		return []byte("Failed to start transient service unit: Unit ostiole-update-restart.service was already loaded"), errors.New("exit status 1")
	}
	return nil, nil
}

// consoleRouter is a router running 0.1.0 as `ostiole update` sees it: the
// installed binary is a file of the test's own, the daemon serves on a port
// that is not the default, systemd is a transcript, and the person at the
// console gives answer.
type consoleRouter struct {
	g       *globals
	deps    updateDeps
	bin     string
	systemd *fakeSystemd
	asked   []string
	answer  error
}

func newConsoleRouter(t *testing.T, github *httptest.Server, key ed25519.PublicKey) *consoleRouter {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ostiole")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := &consoleRouter{g: &globals{configDir: t.TempDir()}, bin: bin, systemd: &fakeSystemd{}}
	r.deps = updateDeps{
		client: func() *update.Client {
			return &update.Client{Repo: update.DefaultRepo, BaseURL: github.URL, HTTP: github.Client(),
				PublicKeys: []ed25519.PublicKey{key}}
		},
		current: "0.1.0",
		binary:  func() (string, error) { return bin, nil },
		listen:  func() string { return "0.0.0.0:8443" },
		run:     r.systemd,
		confirm: func(_ *cobra.Command, question string) error {
			r.asked = append(r.asked, question)
			return r.answer
		},
	}
	return r
}

// update runs `ostiole update` and returns what it printed and how it
// ended.
func (r *consoleRouter) update(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := updateCmd(r.g, r.deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// Never nil: cobra reads the test binary's own arguments then.
	cmd.SetArgs(append([]string{}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}

// kept fails unless the router still has the binary it had, with nothing
// beside it.
func (r *consoleRouter) kept(t *testing.T) {
	t.Helper()
	if raw, _ := os.ReadFile(r.bin); string(raw) != "old" {
		t.Errorf("binary = %q, want the old one", raw)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(r.bin), "*")); len(left) != 1 {
		t.Errorf("left beside the binary: %v", left)
	}
}

// installed fails unless the release is in place, the old binary is kept to
// fall back to, and systemd was handed a restart that probes the port the
// daemon serves on and notes a rollback where the daemon looks for one.
func (r *consoleRouter) installed(t *testing.T, version string) {
	t.Helper()
	if raw, _ := os.ReadFile(r.bin); !strings.Contains(string(raw), "ostiole "+version) {
		t.Errorf("binary = %q, want %s", raw, version)
	}
	if raw, _ := os.ReadFile(r.bin + ".previous"); string(raw) != "old" {
		t.Errorf("previous = %q, want the old binary", raw)
	}
	if len(r.systemd.calls) == 0 {
		t.Fatal("no restart was scheduled")
	}
	last := r.systemd.calls[len(r.systemd.calls)-1]
	if last[0] != "systemd-run" {
		t.Fatalf("last call to systemd = %q, want the restart", last)
	}
	note := filepath.Join(r.g.configDir, "updates", "rolled-back")
	for _, want := range []string{"systemctl restart ostiole.service", "update --probe https://127.0.0.1:8443/api/v1/health",
		"echo " + version + " >" + note} {
		if !strings.Contains(last[len(last)-1], want) {
			t.Errorf("the restart script does not have %q: %q", want, last[len(last)-1])
		}
	}
}

// --check says what is out there and stops there: nothing is asked,
// downloaded or restarted.
func TestUpdateCheckOnlyReports(t *testing.T) {
	t.Parallel()
	github, key := releasesWith(t, "0.2.0", false)
	r := newConsoleRouter(t, github, key)

	out, err := r.update(t, "--check")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current  0.1.0\n", "latest   0.2.0 (stable, 2026-09-15)\n",
		"update available: https://github.com/rforced/ostiole/releases/tag/v0.2.0\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q does not say %q", out, want)
		}
	}
	if len(r.asked) != 0 {
		t.Errorf("asked %q", r.asked)
	}
	r.kept(t)
	if len(r.systemd.calls) != 0 {
		t.Errorf("systemd was asked %q", r.systemd.calls)
	}
}

// With --yes the release goes in without a question.
func TestUpdateWithYesInstallsWithoutAsking(t *testing.T) {
	t.Parallel()
	github, key := releasesWith(t, "0.2.0", false)
	r := newConsoleRouter(t, github, key)

	out, err := r.update(t, "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "installed 0.2.0;") {
		t.Errorf("output %q does not say what was installed", out)
	}
	if len(r.asked) != 0 {
		t.Errorf("asked %q despite --yes", r.asked)
	}
	r.installed(t, "0.2.0")
}

// Without --yes the console is asked first, naming the release and the
// binary it replaces. Anything but yes fails the command and leaves the
// router as it was; yes installs.
func TestUpdateAsksBeforeInstalling(t *testing.T) {
	t.Parallel()
	github, key := releasesWith(t, "0.2.0", false)
	r := newConsoleRouter(t, github, key)

	r.answer = errors.New("aborted")
	if _, err := r.update(t); err == nil || err.Error() != "aborted" {
		t.Errorf("err = %v, want the refusal", err)
	}
	if len(r.asked) != 1 || !strings.Contains(r.asked[0], "install 0.2.0 over "+r.bin) {
		t.Errorf("asked %q", r.asked)
	}
	r.kept(t)
	if len(r.systemd.calls) != 0 {
		t.Errorf("systemd was asked %q", r.systemd.calls)
	}

	r.answer = nil
	if _, err := r.update(t); err != nil {
		t.Fatal(err)
	}
	r.installed(t, "0.2.0")
}

// An update that cannot go in fails the command with the reason, and the
// router keeps the binary it had.
func TestUpdateThatCannotGoInFailsWithTheReason(t *testing.T) {
	t.Parallel()
	github, key := releasesWith(t, "0.2.0", false)

	stranger, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := newConsoleRouter(t, github, stranger)
	if _, err := r.update(t, "--yes"); err == nil || !strings.Contains(err.Error(), "signature does not verify") {
		t.Errorf("a release signed with another key: err = %v", err)
	}
	r.kept(t)
	if len(r.systemd.calls) != 0 {
		t.Errorf("systemd was asked %q", r.systemd.calls)
	}

	r = newConsoleRouter(t, github, key)
	r.systemd.refuse = true
	_, err = r.update(t, "--yes")
	if err == nil || !strings.Contains(err.Error(), "schedule restart") || !strings.Contains(err.Error(), "Failed to start transient service unit") {
		t.Errorf("systemd refusing the restart: err = %v", err)
	}
	r.kept(t)
}

// The channel decides which releases count: a prerelease is not offered on
// stable, is on beta, and installs from there. A channel that does not
// exist is refused.
func TestUpdateFollowsTheChannel(t *testing.T) {
	t.Parallel()
	github, key := releasesWith(t, "0.3.0-beta.1", true)
	r := newConsoleRouter(t, github, key)

	if _, err := r.update(t, "--channel", "nightly"); err == nil || !strings.Contains(err.Error(), `unknown channel "nightly"`) {
		t.Errorf("nightly: err = %v", err)
	}
	out, err := r.update(t, "--check")
	if err != nil || !strings.Contains(out, "latest   0.0.1 (stable, 2026-01-01)\nup to date\n") {
		t.Errorf("stable: %q, %v", out, err)
	}
	out, err = r.update(t, "--check", "--channel", "beta")
	if err != nil || !strings.Contains(out, "latest   0.3.0-beta.1 (beta, 2026-09-15)\nupdate available") {
		t.Errorf("beta: %q, %v", out, err)
	}
	r.kept(t)

	if _, err := r.update(t, "--channel", "beta", "--yes"); err != nil {
		t.Fatal(err)
	}
	r.installed(t, "0.3.0-beta.1")
}
