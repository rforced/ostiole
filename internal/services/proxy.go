package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/store"
)

// The sidecar's paths and names.
const (
	ProxyUnit        = "ostiole-proxy.service"
	ProxyUser        = "ostiole-proxy"
	ProxyStateDir    = "/var/lib/ostiole-proxy"
	ProxyAdminSocket = "/run/ostiole-proxy/admin.sock"
	ProxyBinaryName  = "ostiole-proxy"

	proxyConfName = "caddy.json"
	// proxyCertsDir holds a copy of every certificate a site names, one
	// directory each; proxyCertList is the plain list of their IDs, which
	// is what a preflight and an apply read.
	proxyCertsDir = "certs"
	proxyCertList = "certificates"
)

// CertSource is what the proxy reads certificates from.
type CertSource interface {
	Read(id string) (*certs.Files, error)
	Subscribe(fn func(id string))
}

// Proxy drives the ostiole-proxy sidecar: the sites and routes rendered
// to its configuration, and the certificates the manager holds copied to
// where it can read them (ADR-0022).
type Proxy struct {
	// Dir holds the generated files, under the configuration directory.
	Dir string
	// Cmd runs systemctl; default execs it.
	Cmd network.Commander
	// Certs is where a site's named certificate comes from.
	Certs CertSource
	// SelfCert and SelfKey are the built-in pair a site falls back to.
	SelfCert, SelfKey string
	Log               *slog.Logger
	// Poll is how often a reload in progress is looked at; zero is 200 ms.
	Poll time.Duration

	// The strip polls every few seconds: the release is kept until the
	// binary changes, and one client keeps one admin connection open.
	mu         sync.Mutex
	release    string
	releaseMod time.Time
	admin      *http.Client
}

var (
	_ network.Backend = (*Proxy)(nil)
	_ Preflighter     = (*Proxy)(nil)
)

// NewProxy returns a backend writing under configDir.
func NewProxy(configDir string, store CertSource) *Proxy {
	dir := configDir
	if dir == "" {
		dir = storeDefaultDir
	}
	return &Proxy{
		Dir:      ProxyDir(configDir),
		Cmd:      execCommander{},
		Certs:    store,
		SelfCert: filepath.Join(dir, "tls", "cert.pem"),
		SelfKey:  filepath.Join(dir, "tls", "key.pem"),
	}
}

// storeDefaultDir is the configuration directory when none was named.
const storeDefaultDir = store.DefaultDir

// ProxyDir is where the generated files go for a given configuration
// directory. The unit reads its configuration from here.
func ProxyDir(configDir string) string {
	if configDir == "" {
		configDir = store.DefaultDir
	}
	return filepath.Join(configDir, "proxy")
}

func (p *Proxy) cmd() network.Commander {
	if p.Cmd == nil {
		return execCommander{}
	}
	return p.Cmd
}

func (p *Proxy) log() *slog.Logger {
	if p.Log == nil {
		return slog.Default()
	}
	return p.Log
}

func (p *Proxy) path(name string) string { return filepath.Join(p.Dir, name) }

// Name implements network.Backend.
func (p *Proxy) Name() string { return "proxy" }

// ConfPath is the configuration the unit reads.
func (p *Proxy) ConfPath() string { return p.path(proxyConfName) }

// Render implements network.Backend. With nothing to serve it returns no
// files, which Apply turns into "unit stopped".
func (p *Proxy) Render(cfg *model.Config) (network.Files, error) {
	if !cfg.ProxyEnabled() {
		return network.Files{}, nil
	}
	return p.render(cfg), nil
}

// Snapshot implements network.Backend. The rule set files are static and
// the certificates are copies, so neither is worth reverting.
func (p *Proxy) Snapshot() (network.Files, error) {
	files := network.Files{}
	for _, name := range append([]string{proxyConfName, proxyCertList}, p.poolCAs()...) {
		raw, err := os.ReadFile(p.path(name))
		switch {
		case err == nil:
			files[name] = string(raw)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, err
		}
	}
	return files, nil
}

// poolCAs names the private issuer files on disk, so a snapshot sees one
// a later apply is about to replace.
func (p *Proxy) poolCAs() []string {
	entries, err := os.ReadDir(p.path(proxyCertsDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "-ca.pem") {
			out = append(out, filepath.Join(proxyCertsDir, e.Name()))
		}
	}
	return out
}

// PreflightError is a refusal the operator can act on: the daemon is not
// here, or a certificate a site names has not been issued. The API maps
// it to 422 rather than letting it read as a missing resource.
type PreflightError struct{ Message string }

func (e *PreflightError) Error() string { return e.Message }

var errProxyMissing = &PreflightError{
	Message: "The proxy is not on this router: run `ostiole repair --proxy` once as root"}

// Preflight implements Preflighter, so an apply that cannot work is
// refused before the ruleset has been touched.
func (p *Proxy) Preflight(ctx context.Context, files network.Files) error {
	if _, wanted := files[proxyConfName]; !wanted {
		return nil
	}
	if !p.Installed(ctx) {
		return errProxyMissing
	}
	for _, id := range certIDs(files[proxyCertList]) {
		if id == model.SelfCertificate {
			for _, path := range []string{p.SelfCert, p.SelfKey} {
				if _, err := os.Stat(path); err != nil {
					return &PreflightError{Message: "the built-in certificate is not on disk: the web UI is not serving TLS"}
				}
			}
			continue
		}
		if _, err := p.Certs.Read(id); err != nil {
			return &PreflightError{Message: fmt.Sprintf("certificate %q has not been issued yet", id)}
		}
	}
	return nil
}

// certIDs reads back the list Render wrote, one ID per line.
func certIDs(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Apply implements network.Backend: write what changed, place the
// certificates, and keep the unit in step.
func (p *Proxy) Apply(ctx context.Context, files network.Files) error {
	_, wanted := files[proxyConfName]
	current, err := p.Snapshot()
	if err != nil {
		return err
	}
	if !wanted {
		return p.remove(ctx, current)
	}
	if !p.Installed(ctx) {
		return errProxyMissing
	}
	gid, err := proxyGroup()
	if err != nil {
		return err
	}
	if err := p.mkdir(p.Dir, gid); err != nil {
		return err
	}
	for _, name := range []string{crsDirName, proxyCertsDir} {
		if err := p.mkdir(p.path(name), gid); err != nil {
			return err
		}
	}
	for name, content := range crsFiles() {
		if have, err := os.ReadFile(p.path(name)); err == nil && string(have) == content {
			continue
		}
		if err := p.write(name, content, gid); err != nil {
			return err
		}
	}
	changed := false
	for name, content := range files {
		if current[name] == content {
			continue
		}
		if err := p.write(name, content, gid); err != nil {
			return err
		}
		changed = true
	}
	// A pool that dropped its issuer, or was deleted, would leave the file
	// behind: nothing else here removes what a render stopped naming.
	for _, name := range p.poolCAs() {
		if _, kept := files[name]; kept {
			continue
		}
		if err := os.Remove(p.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		changed = true
	}
	placed, err := p.placeCertificates(certIDs(files[proxyCertList]), gid)
	if err != nil {
		return err
	}
	changed = changed || placed
	return p.start(ctx, changed)
}

// remove stops the proxy and takes back what an apply put there. The rule
// set files stay: they are static, and the next apply would only write
// them again.
func (p *Proxy) remove(ctx context.Context, current network.Files) error {
	if len(current) == 0 {
		return nil
	}
	if out, err := p.cmd().Run(ctx, "systemctl", "disable", "--now", ProxyUnit); err != nil {
		return fmt.Errorf("stop %s: %w: %s", ProxyUnit, err, strings.TrimSpace(string(out)))
	}
	if err := os.RemoveAll(p.path(proxyCertsDir)); err != nil {
		return err
	}
	for _, name := range []string{proxyConfName, proxyCertList} {
		if err := os.Remove(p.path(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// start brings the unit up, reloads it when anything it reads changed,
// and fails the apply if it is not running afterwards.
func (p *Proxy) start(ctx context.Context, changed bool) error {
	if !p.Active(ctx) {
		if out, err := p.cmd().Run(ctx, "systemctl", "enable", "--now", ProxyUnit); err != nil {
			return fmt.Errorf("enable %s: %w: %s", ProxyUnit, err, strings.TrimSpace(string(out)))
		}
	} else if changed {
		if err := p.reload(ctx); err != nil {
			return err
		}
	}
	return p.settle(ctx)
}

// reload has the running proxy read its files again. A configuration Caddy
// refuses fails the reload and leaves the old one serving.
func (p *Proxy) reload(ctx context.Context) error {
	since := time.Now()
	if _, err := p.cmd().Run(ctx, "systemctl", "reload", ProxyUnit); err != nil {
		return fmt.Errorf("reload %s: %w: %s", ProxyUnit, err, p.refusal(ctx, since))
	}
	return nil
}

// refusal is what Caddy said about a reload it refused. systemctl only says
// the reload failed; Caddy's reason is in the journal, among whatever the
// proxy logged since, and it is the line its command line starts with
// "Error: ".
func (p *Proxy) refusal(ctx context.Context, since time.Time) string {
	out, _ := p.cmd().Run(ctx, "journalctl", "-u", ProxyUnit, "--since=@"+strconv.FormatInt(since.Unix(), 10),
		"-o", "cat", "--no-pager")
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var said []string
	for _, line := range lines {
		if reason, ok := strings.CutPrefix(line, "Error: "); ok {
			said = append(said, reason)
		}
	}
	if len(said) == 0 {
		said = lines[max(0, len(lines)-5):]
	}
	return strings.Join(said, "\n")
}

// reloadSettle is how long a reload may take before the apply gives up
// on it. Loading the rule set for a few sites is well under a second.
const reloadSettle = 15 * time.Second

// settle waits for the unit to be running. Caddy tells systemd that it
// is reloading and then that it is ready, and between the two the unit
// reads as "reloading": a look at that moment is not a failed start.
func (p *Proxy) settle(ctx context.Context) error {
	deadline := time.Now().Add(reloadSettle)
	for {
		st := p.state(ctx)
		switch st {
		case "active":
			return nil
		case "reloading", "activating":
			if time.Now().After(deadline) {
				return fmt.Errorf("%s is still %s after %s", ProxyUnit, st, reloadSettle)
			}
		default:
			out, _ := p.cmd().Run(ctx, "journalctl", "-u", ProxyUnit, "-n", "5", "--no-pager")
			return fmt.Errorf("%s did not start: %s", ProxyUnit, strings.TrimSpace(string(out)))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.poll()):
		}
	}
}

func (p *Proxy) poll() time.Duration {
	if p.Poll > 0 {
		return p.Poll
	}
	return 200 * time.Millisecond
}

// placeCertificates copies what each site names to where the proxy can
// read it, and takes away the directories no site names any more. It
// reports whether anything changed, which is what decides a reload.
func (p *Proxy) placeCertificates(ids []string, gid int) (bool, error) {
	changed := false
	for _, id := range ids {
		cert, key, err := p.readCertificate(id)
		if err != nil {
			return changed, fmt.Errorf("certificate %q: %w", id, err)
		}
		dir := filepath.Join(p.path(proxyCertsDir), id)
		if err := p.mkdir(dir, gid); err != nil {
			return changed, err
		}
		for _, w := range []struct{ name, content string }{
			{"fullchain.pem", string(cert)}, {"key.pem", string(key)},
		} {
			path := filepath.Join(dir, w.name)
			if have, err := os.ReadFile(path); err == nil && string(have) == w.content {
				continue
			}
			if err := writeMode(path, w.content, 0o640); err != nil {
				return changed, err
			}
			if err := chown(path, gid); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	entries, err := os.ReadDir(p.path(proxyCertsDir))
	if err != nil {
		return changed, nil
	}
	for _, e := range entries {
		if !e.IsDir() || slices.Contains(ids, e.Name()) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(p.path(proxyCertsDir), e.Name())); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// readCertificate is one certificate's chain and key: from the manager,
// or the router's own self-signed pair for the built-in one.
func (p *Proxy) readCertificate(id string) (cert, key []byte, err error) {
	if id == model.SelfCertificate {
		if cert, err = os.ReadFile(p.SelfCert); err != nil {
			return nil, nil, err
		}
		key, err = os.ReadFile(p.SelfKey)
		return cert, key, err
	}
	f, err := p.Certs.Read(id)
	if err != nil {
		return nil, nil, err
	}
	return f.FullChain, f.Key, nil
}

// Watch reloads the proxy when a certificate it serves is renewed, which
// happens outside an apply.
func (p *Proxy) Watch(ctx context.Context) {
	if p.Certs == nil {
		return
	}
	p.Certs.Subscribe(func(id string) {
		raw, err := os.ReadFile(p.path(proxyCertList))
		if err != nil {
			return
		}
		ids := certIDs(string(raw))
		if !slices.Contains(ids, id) {
			return
		}
		gid, err := proxyGroup()
		if err != nil {
			return
		}
		// The whole list, not the one that renewed: placing takes away
		// every directory it was not given.
		changed, err := p.placeCertificates(ids, gid)
		if err != nil {
			// Remove notifies as Write does, so a certificate that has
			// just been deleted lands here with nothing to read. The
			// apply that stops naming it is what tidies up.
			p.log().Debug("proxy certificate not placed", "id", id, "err", err)
			return
		}
		if !changed || !p.Active(ctx) {
			return
		}
		if err := p.reload(ctx); err != nil {
			p.log().Error("proxy reload after renewal failed", "id", id, "err", err)
		}
	})
}

// mkdir creates a directory the proxy's group may read.
func (p *Proxy) mkdir(path string, gid int) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	return chown(path, gid)
}

func (p *Proxy) write(name, content string, gid int) error {
	path := p.path(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := writeMode(path, content, 0o640); err != nil {
		return err
	}
	return chown(path, gid)
}

// chown hands a file to the proxy's group, root keeping it. A test that
// is not root cannot, and does not need to.
func chown(path string, gid int) error {
	if gid < 0 || os.Geteuid() != 0 {
		return nil
	}
	return os.Chown(path, 0, gid)
}

// proxyGroup is the group the sidecar runs as. `ostiole install` creates
// it; without it the files would be unreadable to the daemon.
func proxyGroup() (int, error) {
	if os.Geteuid() != 0 {
		return -1, nil
	}
	g, err := user.LookupGroup(ProxyUser)
	if err != nil {
		return -1, errProxyMissing
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1, errProxyMissing
	}
	return gid, nil
}

// Installed reports whether the ostiole-proxy unit exists.
func (p *Proxy) Installed(ctx context.Context) bool {
	_, err := p.cmd().Run(ctx, "systemctl", "cat", ProxyUnit)
	return err == nil
}

// Active reports whether the unit is running, a reload in progress
// included.
func (p *Proxy) Active(ctx context.Context) bool {
	switch p.state(ctx) {
	case "active", "reloading":
		return true
	}
	return false
}

// state is what systemctl says the unit is doing: active, reloading,
// activating, inactive or failed.
func (p *Proxy) state(ctx context.Context) string {
	out, _ := p.cmd().Run(ctx, "systemctl", "is-active", ProxyUnit)
	return strings.TrimSpace(string(out))
}

// Release is the Ostiole version the sidecar was built beside. Caddy
// writes a line about the environment it was started without, and the
// commander gives us stderr with the rest, so the answer is the last
// line rather than all of them. It is asked once per binary: the strip
// polls every few seconds, and starting Caddy to answer each poll is not
// free on a small router.
func (p *Proxy) Release(ctx context.Context) string {
	mod := binaryModTime(ProxyBinaryName)
	p.mu.Lock()
	if p.release != "" && p.releaseMod.Equal(mod) {
		p.mu.Unlock()
		return p.release
	}
	p.mu.Unlock()
	out, err := p.cmd().Run(ctx, ProxyBinaryName, "release")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	rel := strings.TrimSpace(lines[len(lines)-1])
	if rel != "" {
		p.mu.Lock()
		p.release, p.releaseMod = rel, mod
		p.mu.Unlock()
	}
	return rel
}

// binaryModTime is when the sidecar's binary last changed, or zero when
// it is not on the path.
func binaryModTime(name string) time.Time {
	path := lookPath(name)
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// adminClient talks to the admin socket. One client for the life of the
// backend, so the connection it keeps open is reused by the next poll
// rather than left behind by this one.
func (p *Proxy) adminClient() *http.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.admin == nil {
		p.admin = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", ProxyAdminSocket)
			},
			MaxIdleConns:    2,
			IdleConnTimeout: 30 * time.Second,
		}}
	}
	return p.admin
}

// Upstream is one backend as the proxy sees it.
type Upstream struct {
	Address  string `json:"address"`
	Healthy  bool   `json:"healthy"`
	Requests int    `json:"requests"`
	Fails    int    `json:"fails"`
}

// Upstreams asks the admin socket how the backends are doing. A proxy
// that is not running has none, which is not an error.
func (p *Proxy) Upstreams(ctx context.Context) ([]Upstream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://admin/reverse_proxy/upstreams", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.adminClient().Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var raw []struct {
		Address     string `json:"address"`
		NumRequests int    `json:"num_requests"`
		Fails       int    `json:"fails"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, nil
	}
	out := make([]Upstream, 0, len(raw))
	for _, u := range raw {
		// The challenge solver is an upstream of Caddy's like any other,
		// and nobody configured it as a backend.
		if u.Address == challengeUpstream {
			continue
		}
		out = append(out, Upstream{
			Address: u.Address, Healthy: u.Fails == 0, Requests: u.NumRequests, Fails: u.Fails,
		})
	}
	return out, nil
}
