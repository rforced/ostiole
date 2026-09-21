package update

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRun struct {
	calls  [][]string
	failOn string // the one command that fails, for the paths that handle it
}

func (f *fakeRun) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name != "" && name == f.failOn {
		return []byte("Failed to start transient service unit"), errors.New("exit status 1")
	}
	return nil, nil
}

// fakeGitHub serves one release with a signed tarball.
func fakeGitHub(t *testing.T, version string, prerelease bool, tamper string) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tarball := fakeTarball("ostiole", version)
	proxyTar := fakeTarball("ostiole-proxy", version)
	name, proxyName := AssetName(version), ProxyAssetName(version)
	sum, proxySum := sha256.Sum256(tarball), sha256.Sum256(proxyTar)
	sums := fmt.Sprintf("%s  %s\n%s  %s\n",
		hex.EncodeToString(sum[:]), name, hex.EncodeToString(proxySum[:]), proxyName)
	if tamper == "sum" {
		sums = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	sig := Sign(priv, []byte(sums))
	if tamper == "sig" {
		sig = Sign(priv, []byte("something else"))
	}

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/rforced/ostiole/releases", func(w http.ResponseWriter, _ *http.Request) {
		base := srv.URL + "/dl/"
		rel := []map[string]any{{
			"tag_name": "v" + version, "draft": false, "prerelease": prerelease, "published_at": "2026-09-15T00:00:00Z",
			"body": "notes", "html_url": "https://example/release",
			"assets": []map[string]any{
				{"name": name, "browser_download_url": base + name, "size": len(tarball)},
				{"name": proxyName, "browser_download_url": base + proxyName, "size": len(proxyTar)},
				{"name": "checksums.txt", "browser_download_url": base + "checksums.txt", "size": len(sums)},
				{"name": "checksums.txt.sig", "browser_download_url": base + "checksums.txt.sig", "size": len(sig)},
			},
		}, {
			"tag_name": "v0.0.1", "draft": false, "prerelease": false, "published_at": "2026-01-01T00:00:00Z", "assets": []any{},
		}, {
			"tag_name": "not-a-version", "draft": false, "prerelease": false, "assets": []any{},
		}}
		_ = json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/dl/", func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case name:
			_, _ = w.Write(tarball)
		case proxyName:
			_, _ = w.Write(proxyTar)
		case "checksums.txt":
			_, _ = w.Write([]byte(sums))
		case "checksums.txt.sig":
			_, _ = w.Write([]byte(sig))
		default:
			http.NotFound(w, r)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pub
}

// fakeGitHubWithoutProxy serves a release that carries the binary only,
// which is what an older release looks like to a router with the sidecar.
func fakeGitHubWithoutProxy(t *testing.T, version string) (*httptest.Server, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tarball := fakeTarball("ostiole", version)
	name := AssetName(version)
	sum := sha256.Sum256(tarball)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	sig := Sign(priv, []byte(sums))

	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/repos/rforced/ostiole/releases", func(w http.ResponseWriter, _ *http.Request) {
		base := srv.URL + "/dl/"
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"tag_name": "v" + version, "draft": false, "published_at": "2026-09-15T00:00:00Z",
			"assets": []map[string]any{
				{"name": name, "browser_download_url": base + name, "size": len(tarball)},
				{"name": "checksums.txt", "browser_download_url": base + "checksums.txt", "size": len(sums)},
				{"name": "checksums.txt.sig", "browser_download_url": base + "checksums.txt.sig", "size": len(sig)},
			},
		}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, pub
}

// fakeTarball is one release archive holding one executable member.
func fakeTarball(member, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho " + member + " " + version + "\n")
	_ = tw.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestNewer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cur, cand string
		want      bool
	}{
		{"0.1.0", "0.1.1", true}, {"v0.1.0", "0.1.1", true}, {"0.1.1", "0.1.1", false}, {"0.2.0", "0.1.9", false},
		{"dev", "0.1.0", true}, {"abc1234-dirty", "0.1.0", true}, {"0.1.0", "junk", false}, {"0.1.0", "0.2.0-beta.1", true},
	}
	for _, c := range cases {
		if got := Newer(c.cur, c.cand); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.cur, c.cand, got)
		}
	}
}

func TestCheckDownloadInstall(t *testing.T) {
	t.Parallel()
	srv, pub := fakeGitHub(t, "0.2.0", false, "")
	c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{pub}}

	chk, err := c.Check(context.Background(), "0.1.0", Stable)
	if err != nil {
		t.Fatal(err)
	}
	if !chk.Available || chk.Latest != "0.2.0" || chk.Asset == nil {
		t.Fatalf("check = %+v", chk)
	}
	if chk, _ := c.Check(context.Background(), "0.2.0", Stable); chk.Available {
		t.Error("same version reported as available")
	}

	dir := t.TempDir()
	var stages []string
	got, err := c.Download(context.Background(), chk.Release, dir, false, func(stage string, _, _ int64) { stages = append(stages, stage) })
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxy != "" {
		t.Errorf("a router without the proxy downloaded %q", got.Proxy)
	}
	raw, _ := os.ReadFile(got.Binary)
	if !strings.Contains(string(raw), "ostiole 0.2.0") {
		t.Errorf("extracted binary = %q", raw)
	}
	if info, _ := os.Stat(got.Binary); info.Mode().Perm()&0o111 == 0 {
		t.Error("extracted binary not executable")
	}
	if strings.Join(stages, ",") == "" || stages[0] != "verifying" {
		t.Errorf("stages = %v", stages)
	}

	// Install: previous kept, new in place, restart scheduled with a probe and rollback.
	bin := filepath.Join(dir, "ostiole")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &fakeRun{}
	inst := &Installer{Binary: bin, Unit: "ostiole.service", HealthURL: "https://127.0.0.1:443/api/v1/health", Run: run}
	if err := inst.Install(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(bin); !strings.Contains(string(raw), "0.2.0") {
		t.Error("new binary not in place")
	}
	if raw, _ := os.ReadFile(bin + ".previous"); string(raw) != "old" {
		t.Error("previous binary not kept")
	}
	last := strings.Join(run.calls[len(run.calls)-1], " ")
	if !strings.Contains(last, "systemd-run") || !strings.Contains(last, " install >/dev/null") || !strings.Contains(last, "systemctl restart ostiole.service") || !strings.Contains(last, "update --probe") || !strings.Contains(last, ".previous") {
		t.Errorf("restart command = %q", last)
	}
}

// A router that runs the sidecar gets both halves of the release, and
// both go back when the restart cannot be scheduled.
func TestUpdateSwapsTheProxyWithTheBinary(t *testing.T) {
	t.Parallel()
	srv, pub := fakeGitHub(t, "0.2.0", false, "")
	c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{pub}}
	chk, err := c.Check(context.Background(), "0.1.0", Stable)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	bin, proxy := filepath.Join(dir, "ostiole"), filepath.Join(dir, "ostiole-proxy")
	for _, p := range []string{bin, proxy} {
		if err := os.WriteFile(p, []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inst := &Installer{
		Binary: bin, Unit: "ostiole.service", HealthURL: "x", Run: &fakeRun{},
		Proxy: proxy, ProxyUnit: "ostiole-proxy.service",
	}
	if !inst.WantsProxy() {
		t.Fatal("a router with the sidecar in place does not want it")
	}
	got, err := c.Download(context.Background(), chk.Release, dir, inst.WantsProxy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Proxy == "" {
		t.Fatal("the sidecar was not downloaded")
	}
	if err := inst.Install(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(proxy); !strings.Contains(string(raw), "ostiole-proxy 0.2.0") {
		t.Errorf("proxy = %q", raw)
	}
	if raw, _ := os.ReadFile(proxy + ".previous"); string(raw) != "old" {
		t.Errorf("the previous proxy was not kept: %q", raw)
	}
	run := inst.Run.(*fakeRun)
	last := strings.Join(run.calls[len(run.calls)-1], " ")
	for _, want := range []string{"systemctl try-restart ostiole-proxy.service", proxy + ".previous"} {
		if !strings.Contains(last, want) {
			t.Errorf("%q missing from the restart script: %q", want, last)
		}
	}

	// A release without the sidecar is refused rather than half-applied.
	bare, barePub := fakeGitHubWithoutProxy(t, "0.3.0")
	c2 := &Client{Repo: "rforced/ostiole", BaseURL: bare.URL, PublicKeys: []ed25519.PublicKey{barePub}}
	rel, err := c2.Latest(context.Background(), Stable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Download(context.Background(), rel, t.TempDir(), true, nil); err == nil ||
		!strings.Contains(err.Error(), ProxyAssetName("0.3.0")) {
		t.Errorf("err = %v", err)
	}
}

// A swap that nothing will restart into is a binary the next reboot starts
// without a probe. When systemd-run refuses, the old one goes back.
func TestInstallPutsTheOldBinaryBackWhenNothingWillRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ostiole")
	if err := os.WriteFile(bin, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "ostiole.new")
	if err := os.WriteFile(next, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	proxy := filepath.Join(dir, "ostiole-proxy")
	if err := os.WriteFile(proxy, []byte("old proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	nextProxy := filepath.Join(dir, "ostiole-proxy.new")
	if err := os.WriteFile(nextProxy, []byte("new proxy"), 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &Installer{
		Binary: bin, Unit: "ostiole.service", HealthURL: "x", Run: &fakeRun{failOn: "systemd-run"},
		Proxy: proxy, ProxyUnit: "ostiole-proxy.service",
	}
	err := inst.Install(context.Background(), Downloaded{Binary: next, Proxy: nextProxy})
	if err == nil || !strings.Contains(err.Error(), "schedule restart") {
		t.Fatalf("err = %v", err)
	}
	if raw, _ := os.ReadFile(bin); string(raw) != "old" {
		t.Errorf("binary = %q, want the old one back", raw)
	}
	if _, err := os.Stat(bin + ".previous"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("previous copy left behind: %v", err)
	}
	if raw, _ := os.ReadFile(proxy); string(raw) != "old proxy" {
		t.Errorf("proxy = %q, want the old one back", raw)
	}
}

func TestDownloadRejectsTampering(t *testing.T) {
	t.Parallel()
	for _, tamper := range []string{"sig", "sum"} {
		srv, pub := fakeGitHub(t, "0.2.0", false, tamper)
		c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{pub}}
		chk, err := c.Check(context.Background(), "0.1.0", Stable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Download(context.Background(), chk.Release, t.TempDir(), false, nil); err == nil {
			t.Errorf("tampered %s accepted", tamper)
		}
	}
	// Wrong key.
	srv, _ := fakeGitHub(t, "0.2.0", false, "")
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{other}}
	chk, _ := c.Check(context.Background(), "0.1.0", Stable)
	if _, err := c.Download(context.Background(), chk.Release, t.TempDir(), false, nil); err == nil {
		t.Error("signature from another key accepted")
	}
}

func TestChannels(t *testing.T) {
	t.Parallel()
	srv, pub := fakeGitHub(t, "0.3.0-beta.1", true, "")
	c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{pub}}
	stable, _ := c.Check(context.Background(), "0.1.0", Stable)
	if stable.Available || stable.Latest != "0.0.1" {
		t.Errorf("stable saw the prerelease: %+v", stable)
	}
	beta, _ := c.Check(context.Background(), "0.1.0", Beta)
	if !beta.Available || beta.Latest != "0.3.0-beta.1" {
		t.Errorf("beta missed the prerelease: %+v", beta)
	}
}

func TestManagerRunsToRestart(t *testing.T) {
	t.Parallel()
	srv, pub := fakeGitHub(t, "0.2.0", false, "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "ostiole")
	_ = os.WriteFile(bin, []byte("old"), 0o755)
	m := &Manager{
		Client:    &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{pub}},
		Installer: &Installer{Binary: bin, Unit: "ostiole.service", HealthURL: "x", Run: &fakeRun{}},
		Current:   "0.1.0",
	}
	if m.Status().State != Idle {
		t.Fatal("not idle")
	}
	if err := m.Start(Stable); err != nil {
		t.Fatal(err)
	}
	deadline := 50
	for m.Status().State != Restarting && m.Status().State != Failed && deadline > 0 {
		deadline--
		<-timeAfter()
	}
	if st := m.Status(); st.State != Restarting || st.Version != "0.2.0" {
		t.Fatalf("status = %+v", st)
	}
	if err := m.Start(Stable); err == nil {
		t.Error("second start while restarting should be refused")
	}
}

func TestRotationAcceptsEitherKey(t *testing.T) {
	t.Parallel()
	srv, pub := fakeGitHub(t, "0.2.0", false, "")
	other, _, _ := ed25519.GenerateKey(rand.Reader)

	// During a rotation both keys are trusted and the release is signed by
	// one of them; order must not matter.
	for _, keys := range [][]ed25519.PublicKey{{pub, other}, {other, pub}} {
		c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: keys}
		rel, err := c.Latest(context.Background(), Stable)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Download(context.Background(), rel, t.TempDir(), false, nil); err != nil {
			t.Errorf("download with keys %d: %v", len(keys), err)
		}
	}

	// A build that trusts neither key refuses the release.
	c := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL, PublicKeys: []ed25519.PublicKey{other}}
	rel, err := c.Latest(context.Background(), Stable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), rel, t.TempDir(), false, nil); err == nil {
		t.Error("download accepted a signature from an untrusted key")
	}

	// A build with no key at all says so rather than trusting anything.
	empty := &Client{Repo: "rforced/ostiole", BaseURL: srv.URL}
	if _, err := empty.Download(context.Background(), rel, t.TempDir(), false, nil); err == nil ||
		!strings.Contains(err.Error(), "signing key") {
		t.Errorf("err = %v", err)
	}
}

func TestTrustedKeysDecodesTheEmbeddedKey(t *testing.T) {
	t.Parallel()
	keys := TrustedKeys()
	if len(keys) != len(TrustedKeysHex) {
		t.Fatalf("decoded %d of %d embedded keys", len(keys), len(TrustedKeysHex))
	}
	for i, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			t.Errorf("key %d has %d bytes", i, len(k))
		}
	}
}

// releaseServer serves a list of releases with whatever notes a test
// wants, which is where the security marker lives.
func releaseServer(t *testing.T, rels []map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/rforced/ostiole/releases", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(rels)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func release(tag, notes string) map[string]any {
	return map[string]any{
		"tag_name": tag, "draft": false, "prerelease": false, "published_at": "2026-09-15T00:00:00Z",
		"body": notes, "html_url": "https://example/" + tag,
		"assets": []map[string]any{
			{"name": AssetName(strings.TrimPrefix(tag, "v")), "browser_download_url": "https://example/x", "size": 1},
		},
	}
}

func TestIsSecurityRelease(t *testing.T) {
	t.Parallel()
	for _, notes := range []string{
		"Security-Release: yes",
		"## Fixed\n\nsecurity-release: true\n",
		"Security Release: yes\n",
		"> Security-Release: yes",
		"blah\n\n  Security-Release:   yes  \n\nmore",
	} {
		if !IsSecurityRelease(notes) {
			t.Errorf("marker not found in %q", notes)
		}
	}
	for _, notes := range []string{
		"", "Fixed a security bug in the rule renderer",
		"Security-Release: no", "see the security policy",
		"This is not a Security-Release: maybe",
	} {
		if IsSecurityRelease(notes) {
			t.Errorf("marker found in %q, which does not carry one", notes)
		}
	}
}

func TestCheckFindsSecurityReleasesSinceTheRunningVersion(t *testing.T) {
	t.Parallel()
	srv := releaseServer(t, []map[string]any{
		release("v0.4.0", "## Added\n\nA new page.\n"),
		release("v0.3.0", "Security-Release: yes\n\nFixes an authentication bypass.\n"),
		release("v0.2.0", "## Fixed\n\nSomething small.\n"),
		release("v0.1.0", "Security-Release: yes\n"),
	})
	c := &Client{Repo: DefaultRepo, BaseURL: srv.URL}

	// Three releases behind, one of which was a security fix: the router
	// installs the newest, and the marker is why it does so unasked.
	chk, err := c.Check(t.Context(), "0.2.0", Stable)
	if err != nil {
		t.Fatal(err)
	}
	if chk.Latest != "0.4.0" || !chk.Available {
		t.Fatalf("check = %+v", chk)
	}
	if !chk.Security {
		t.Error("a security release published since 0.2.0 was not noticed")
	}
	if len(chk.SecurityReleases) != 1 || chk.SecurityReleases[0] != "v0.3.0" {
		t.Errorf("securityReleases = %v", chk.SecurityReleases)
	}

	// Already past it: the older marker is not this router's problem.
	chk, err = c.Check(t.Context(), "0.3.0", Stable)
	if err != nil {
		t.Fatal(err)
	}
	if chk.Security || len(chk.SecurityReleases) != 0 {
		t.Errorf("check = %+v, want no security release waiting", chk)
	}

	// Up to date: nothing is available, so nothing is a security update.
	chk, err = c.Check(t.Context(), "0.4.0", Stable)
	if err != nil {
		t.Fatal(err)
	}
	if chk.Available || chk.Security {
		t.Errorf("check = %+v", chk)
	}
}

func TestRunScheduledObeysTheMode(t *testing.T) {
	t.Parallel()
	srv := releaseServer(t, []map[string]any{
		release("v0.4.0", "## Added\n\nA new page.\n"),
		release("v0.3.0", "Security-Release: yes\n"),
	})
	m := &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: srv.URL}, Current: "0.2.0"}

	out, err := m.RunScheduled(t.Context(), ModeManual, Stable)
	if err != nil || !strings.Contains(out, "by hand") {
		t.Errorf("manual = %q, %v", out, err)
	}
	if m.Status().State != Idle {
		t.Error("manual mode started an update")
	}

	// Nothing since this version carries the marker, so security mode
	// waits even though a release is out.
	quiet := releaseServer(t, []map[string]any{release("v0.4.0", "## Added\n\nA new page.\n")})
	m = &Manager{Client: &Client{Repo: DefaultRepo, BaseURL: quiet.URL}, Current: "0.3.0"}
	out, err = m.RunScheduled(t.Context(), ModeSecurity, Stable)
	if err != nil || !strings.Contains(out, "not marked a security release") {
		t.Errorf("security = %q, %v", out, err)
	}
	if m.Status().State != Idle {
		t.Error("security mode installed a release that carried no marker")
	}
}
