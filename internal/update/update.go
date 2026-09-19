// Package update checks GitHub Releases for newer versions, downloads and
// verifies them (ed25519-signed checksums), and swaps the binary in with a
// health-checked restart.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// DefaultRepo is the GitHub repository releases come from.
const DefaultRepo = "rforced/ostiole"

// PublicKeyHex is the ed25519 key that signs checksums.txt of every
// release today.
const PublicKeyHex = "e1adeb7f46c275035328edca383c6a32b753d7e4f59eaf6df8a34dd9ff432745"

// TrustedKeysHex are all the keys whose signature this binary accepts.
//
// Rotation, which has to work for routers that update from an old release:
//  1. Add the new key here, keep signing with the old one, and release.
//     Every router that updates now trusts both.
//  2. Once that release is the oldest one still in the field, switch the
//     signing secret to the new key and release again.
//  3. A release later, drop the old key from this list.
//
// Skipping step 1 strands every router that has not updated yet, because it
// cannot verify the release that would teach it the new key.
var TrustedKeysHex = []string{PublicKeyHex}

// Channel selects which releases count.
type Channel string

// Channels.
const (
	Stable Channel = "stable" // releases only
	Beta   Channel = "beta"   // releases and prereleases
)

// Release is a published version.
type Release struct {
	Version     string    `json:"version"` // without the leading v
	Tag         string    `json:"tag"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"publishedAt"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	Assets      []Asset   `json:"assets"`
	// Security is set when the notes carry the marker. It is what makes
	// a router on the security update mode install this one.
	Security bool `json:"security,omitempty"`
}

// SecurityMarker is the line a release carries to say it fixes
// something. It goes in the release notes, which is the only place both
// a scripted release and a hand-edited one can put it:
//
//	Security-Release: yes
//
// The release workflow lifts the annotated tag's message into the notes,
// so tagging with that line in the message is enough.
var SecurityMarker = regexp.MustCompile(`(?im)^[\s>*_-]*Security[ -]Release:\s*(yes|true)\b`)

// IsSecurityRelease reports whether release notes carry the marker.
func IsSecurityRelease(notes string) bool { return SecurityMarker.MatchString(notes) }

// Asset is a downloadable release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// Client talks to the GitHub Releases API.
type Client struct {
	Repo    string
	BaseURL string // default https://api.github.com
	HTTP    *http.Client
	// PublicKeys are accepted signers of checksums.txt; any one of them
	// verifying is enough, which is what makes rotation possible.
	PublicKeys []ed25519.PublicKey
}

// NewClient returns a client for DefaultRepo with the embedded keys.
func NewClient() *Client {
	return &Client{Repo: DefaultRepo, HTTP: &http.Client{Timeout: 60 * time.Second}, PublicKeys: TrustedKeys()}
}

// TrustedKeys decodes the embedded public keys, skipping anything
// malformed rather than failing the whole binary.
func TrustedKeys() []ed25519.PublicKey {
	var keys []ed25519.PublicKey
	for _, h := range TrustedKeysHex {
		raw, err := hex.DecodeString(h)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return "https://api.github.com"
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

func (c *Client) get(ctx context.Context, url string, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ostiole-updater")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

// Releases lists published releases, newest first.
func (c *Client) Releases(ctx context.Context) ([]Release, error) {
	resp, err := c.get(ctx, c.base()+"/repos/"+c.Repo+"/releases?per_page=30", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var raw []struct {
		TagName     string    `json:"tag_name"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || !semver.IsValid(r.TagName) {
			continue
		}
		rel := Release{
			Version: strings.TrimPrefix(r.TagName, "v"), Tag: r.TagName, Prerelease: r.Prerelease,
			PublishedAt: r.PublishedAt, Notes: r.Body, URL: r.HTMLURL, Security: IsSecurityRelease(r.Body),
		}
		for _, a := range r.Assets {
			rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		}
		out = append(out, rel)
	}
	return out, nil
}

// Latest returns the newest release on the channel, or nil.
func (c *Client) Latest(ctx context.Context, ch Channel) (*Release, error) {
	rels, err := c.Releases(ctx)
	if err != nil {
		return nil, err
	}
	return latestOf(rels, ch), nil
}

func latestOf(rels []Release, ch Channel) *Release {
	var best *Release
	for i := range rels {
		r := &rels[i]
		if r.Prerelease && ch != Beta {
			continue
		}
		if best == nil || semver.Compare(r.Tag, best.Tag) > 0 {
			best = r
		}
	}
	return best
}

// Newer reports whether candidate is a newer version than current. A
// current version that is not semver (a dev build) counts as older than
// any release.
func Newer(current, candidate string) bool {
	cur := "v" + strings.TrimPrefix(current, "v")
	cand := "v" + strings.TrimPrefix(candidate, "v")
	if !semver.IsValid(cand) {
		return false
	}
	if !semver.IsValid(cur) {
		return true
	}
	return semver.Compare(cand, cur) > 0
}

// Check is the result of comparing the running version with the channel.
type Check struct {
	Current   string   `json:"current"`
	Channel   Channel  `json:"channel"`
	Latest    string   `json:"latest,omitempty"`
	Available bool     `json:"available"`
	Release   *Release `json:"release,omitempty"`
	Asset     *Asset   `json:"asset,omitempty"`
	// Security is true when anything published since the running version
	// carries the security marker. A router three releases behind still
	// installs the newest one; the marker only decides whether it does so
	// without being asked.
	Security bool `json:"security"`
	// SecurityReleases names those releases, newest first, so the page
	// can say which one mattered.
	SecurityReleases []string `json:"securityReleases,omitempty"`
}

// AssetName is the tarball for this platform.
func AssetName(version string) string {
	return fmt.Sprintf("ostiole_%s_%s_%s.tar.gz", strings.TrimPrefix(version, "v"), runtime.GOOS, runtime.GOARCH)
}

// Check compares current with the latest release on the channel.
func (c *Client) Check(ctx context.Context, current string, ch Channel) (*Check, error) {
	rels, err := c.Releases(ctx)
	if err != nil {
		return nil, err
	}
	out := &Check{Current: current, Channel: ch}
	rel := latestOf(rels, ch)
	if rel == nil {
		return out, nil
	}
	out.Latest = rel.Version
	out.Release = rel
	if a := rel.asset(AssetName(rel.Version)); a != nil {
		out.Asset = a
	}
	out.Available = Newer(current, rel.Version) && out.Asset != nil
	for i := range rels {
		r := &rels[i]
		if (r.Prerelease && ch != Beta) || !r.Security || !Newer(current, r.Version) {
			continue
		}
		out.SecurityReleases = append(out.SecurityReleases, r.Tag)
	}
	out.Security = out.Available && len(out.SecurityReleases) > 0
	return out, nil
}

func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// Progress reports download progress.
type Progress func(stage string, done, total int64)

// Download fetches the platform tarball for rel into dir, verifying the
// signed checksums, and returns the path of the extracted binary.
func (c *Client) Download(ctx context.Context, rel *Release, dir string, progress Progress) (string, error) {
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	tarball := rel.asset(AssetName(rel.Version))
	sums := rel.asset("checksums.txt")
	sig := rel.asset("checksums.txt.sig")
	if tarball == nil || sums == nil || sig == nil {
		return "", errors.New("release is missing the tarball, checksums.txt, or checksums.txt.sig for this platform")
	}

	progress("verifying", 0, 0)
	sumsRaw, err := c.fetchSmall(ctx, sums.URL, 1<<20)
	if err != nil {
		return "", err
	}
	sigRaw, err := c.fetchSmall(ctx, sig.URL, 4096)
	if err != nil {
		return "", err
	}
	if err := VerifyAny(c.PublicKeys, sumsRaw, sigRaw); err != nil {
		return "", err
	}
	want, err := expectedSum(sumsRaw, tarball.Name)
	if err != nil {
		return "", err
	}

	progress("downloading", 0, tarball.Size)
	tmp, err := os.CreateTemp(dir, ".ostiole-update-*.tar.gz")
	if err != nil {
		if errors.Is(err, os.ErrPermission) || strings.Contains(err.Error(), "read-only") {
			return "", fmt.Errorf("cannot write to %s from the service (unit lacks write access; run `ostiole install` once to refresh the unit, or update with `ostiole update` on the command line): %w", dir, err)
		}
		return "", err
	}
	resp, err := c.get(ctx, tarball.URL, "application/octet-stream")
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				_ = tmp.Close()
				return "", err
			}
			_, _ = h.Write(buf[:n])
			done += int64(n)
			progress("downloading", done, tarball.Size)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = tmp.Close()
			return "", rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("checksum mismatch for %s: got %s, want %s", tarball.Name, got, want)
	}

	progress("extracting", 0, 0)
	out := filepath.Join(dir, ".ostiole.new")
	if err := extractBinary(tmpName, out); err != nil {
		return "", err
	}
	return out, nil
}

func (c *Client) fetchSmall(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := c.get(ctx, url, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// VerifyAny accepts the signature when any trusted key verifies it, so a
// release signed by either side of a rotation is installable.
func VerifyAny(keys []ed25519.PublicKey, data, sig []byte) error {
	if len(keys) == 0 {
		return errors.New("no release signing key is embedded in this build")
	}
	var lastErr error
	for _, k := range keys {
		err := VerifySignature(k, data, sig)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// VerifySignature checks a base64 ed25519 signature over data.
func VerifySignature(pub ed25519.PublicKey, data, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("no release signing key embedded")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("malformed signature: %w", err)
	}
	if !ed25519.Verify(pub, data, raw) {
		return errors.New("checksums.txt signature does not verify; refusing the update")
	}
	return nil
}

// Sign produces the base64 signature Verify expects.
func Sign(priv ed25519.PrivateKey, data []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data))
}

func expectedSum(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("%s not listed in checksums.txt", name)
}

func extractBinary(tarball, out string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("tarball contains no ostiole binary")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "ostiole" {
			continue
		}
		dst, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // it is an executable
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, io.LimitReader(tr, 512<<20)); err != nil {
			_ = dst.Close()
			return err
		}
		return dst.Close()
	}
}

// Runner runs commands (systemd-run); swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Installer swaps the service binary and restarts the service with a
// health check that rolls back on failure.
type Installer struct {
	Binary    string // service binary, e.g. /usr/local/bin/ostiole
	Unit      string // ostiole.service
	HealthURL string // probed after restart, e.g. https://127.0.0.1:443/api/v1/health
	Run       Runner
}

// Install moves newBinary over Binary, keeping the old one as
// Binary.previous, and schedules a restart plus health check.
func (i *Installer) Install(ctx context.Context, newBinary string) error {
	previous := i.Binary + ".previous"
	_ = os.Remove(previous)
	if err := os.Rename(i.Binary, previous); err != nil {
		return fmt.Errorf("keep previous binary: %w", err)
	}
	if err := os.Rename(newBinary, i.Binary); err != nil {
		_ = os.Rename(previous, i.Binary)
		return fmt.Errorf("install new binary: %w", err)
	}
	if err := os.Chmod(i.Binary, 0o755); err != nil { //nolint:gosec // executable
		return err
	}
	// The restart runs detached so the daemon can answer the request that
	// triggered it. If the new binary does not come up healthy, the
	// previous one is put back.
	// `install` first, so unit files written by the new version (hardening,
	// paths) are in place before the restart; it is idempotent.
	script := fmt.Sprintf(`sleep 1; %[2]s install >/dev/null 2>&1; systemctl restart %[1]s; sleep 4;
if %[2]s update --probe %[3]s; then rm -f %[4]s; else mv -f %[4]s %[2]s && systemctl restart %[1]s; fi`,
		i.Unit, i.Binary, i.HealthURL, previous)
	_, _ = i.Run.Run(ctx, "systemctl", "reset-failed", "ostiole-update-restart.service")
	if out, err := i.Run.Run(ctx, "systemd-run", "--unit=ostiole-update-restart", "--collect", "--quiet", "sh", "-c", script); err != nil {
		// Nothing is going to restart into the new binary, and nothing
		// would probe it if the next reboot did. Put the old one back.
		_ = os.Rename(previous, i.Binary)
		return fmt.Errorf("schedule restart: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
