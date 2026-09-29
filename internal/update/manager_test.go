package update

import (
	"bytes"
	"cmp"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// brokenGitHub is the way to GitHub with a fault on it until the test
// mends it.
type brokenGitHub struct {
	next   http.RoundTripper
	fault  func(*http.Request) *http.Response
	mended atomic.Bool
}

func (b *brokenGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	if b.fault != nil && !b.mended.Load() {
		if resp := b.fault(req); resp != nil {
			return resp, nil
		}
	}
	return b.next.RoundTrip(req)
}

// answering has GitHub answer a request for one file with an error.
func answering(file string, code int) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		if path.Base(req.URL.Path) != file {
			return nil
		}
		return reply(req, code, nil)
	}
}

// serving has GitHub serve something else in place of one file.
func serving(file string, body []byte) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		if path.Base(req.URL.Path) != file {
			return nil
		}
		return reply(req, http.StatusOK, body)
	}
}

// panicking panics on the way to one file.
func panicking(file string) func(*http.Request) *http.Response {
	return func(req *http.Request) *http.Response {
		if path.Base(req.URL.Path) == file {
			panic("fetching " + file)
		}
		return nil
	}
}

func reply(req *http.Request, code int, body []byte) *http.Response {
	return &http.Response{
		Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), StatusCode: code,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: req,
	}
}

// settle waits for the update under way to end, one way or the other.
func settle(t *testing.T, m *Manager) Status {
	t.Helper()
	for range 1000 {
		switch st := m.Status(); st.State {
		case Failed, Restarting:
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the update is still busy after ten seconds: %+v", m.Status())
	return Status{}
}

// However an update fails, it ends in Failed saying why, the router keeps
// the binary it had, and the manager is not left busy: once the cause is
// gone, pressing Install again installs.
func TestAFailedUpdateSaysWhyAndCanBeRetried(t *testing.T) {
	t.Parallel()
	_, stranger, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tarball := AssetName("0.2.0")
	for _, tc := range []struct {
		name string
		// release is what GitHub has, 0.2.0 when empty.
		release    string
		prerelease bool
		fault      func(*http.Request) *http.Response
		failOn     string
		// retry is the channel pressed the second time, stable when empty.
		retry   Channel
		want    []string
		version string
	}{
		{name: "GitHub does not answer", fault: answering("releases", http.StatusServiceUnavailable),
			want: []string{"check failed", "503 Service Unavailable"}},
		{name: "nothing newer on the channel", release: "0.3.0-beta.1", prerelease: true, retry: Beta,
			want: []string{"no newer release on the stable channel"}, version: "0.0.1"},
		{name: "a signature from another key", fault: serving("checksums.txt.sig", []byte(Sign(stranger, []byte("checksums")))),
			want: []string{"signature does not verify"}, version: "0.2.0"},
		{name: "a tarball that is not the one signed for", fault: serving(tarball, fakeTarball("ostiole", "evil")),
			want: []string{"checksum mismatch for " + tarball}, version: "0.2.0"},
		{name: "the download breaks off", fault: answering(tarball, http.StatusBadGateway),
			want: []string{tarball, "502 Bad Gateway"}, version: "0.2.0"},
		{name: "systemd will not schedule the restart", failOn: "systemd-run",
			want: []string{"schedule restart", "Failed to start transient service unit"}, version: "0.2.0"},
		{name: "a panic halfway", fault: panicking("checksums.txt"),
			want: []string{"update panicked"}, version: "0.2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			release := cmp.Or(tc.release, "0.2.0")
			srv, pub := fakeGitHub(t, release, tc.prerelease, "")
			github := &brokenGitHub{next: srv.Client().Transport, fault: tc.fault}
			dir := t.TempDir()
			bin := filepath.Join(dir, "ostiole")
			if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			run := &fakeRun{failOn: tc.failOn}
			m := &Manager{
				Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL, HTTP: &http.Client{Transport: github},
					PublicKeys: []ed25519.PublicKey{pub}},
				Installer: &Installer{Binary: bin, Unit: "ostiole.service", HealthURL: "x", Run: run},
				Current:   "0.1.0",
				Log:       slog.New(slog.DiscardHandler),
			}

			if err := m.Start(Stable); err != nil {
				t.Fatal(err)
			}
			st := settle(t, m)
			if st.State != Failed || st.Version != tc.version {
				t.Errorf("status = %+v, want failed with version %q", st, tc.version)
			}
			for _, want := range tc.want {
				if !strings.Contains(st.Message, want) {
					t.Errorf("message %q does not say %q", st.Message, want)
				}
			}
			if raw, _ := os.ReadFile(bin); string(raw) != "old" {
				t.Errorf("binary = %q, want the old one", raw)
			}
			if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 1 {
				t.Errorf("left beside the binary: %v", left)
			}

			github.mended.Store(true)
			run.failOn = ""
			if err := m.Start(cmp.Or(tc.retry, Stable)); err != nil {
				t.Fatalf("second press: %v", err)
			}
			if st := settle(t, m); st.State != Restarting || st.Version != release {
				t.Errorf("second press: status = %+v, want restarting into %s", st, release)
			}
			if raw, _ := os.ReadFile(bin); !strings.Contains(string(raw), "ostiole "+release) {
				t.Errorf("binary after the second press = %q", raw)
			}
		})
	}
}
