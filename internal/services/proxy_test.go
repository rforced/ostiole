package services

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/certs"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

// The proxy's configuration is golden-tested on the inputs that enable
// it; the rest must produce no files at all.
func TestRenderProxyGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			p := &Proxy{
				Dir:      "/etc/ostiole/proxy",
				SelfCert: "/etc/ostiole/tls/cert.pem",
				SelfKey:  "/etc/ostiole/tls/key.pem",
			}
			files, err := p.Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".proxy"
			if !cfg.ProxyEnabled() {
				if len(files) != 0 {
					t.Fatalf("no proxy but rendered %v", files.Names())
				}
				return
			}
			got := files.String()
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("mismatch (run with -update to accept)\n--- got ---\n%s", got)
			}
		})
	}
}

// The sidecar is the arbiter of its own field names, so when it has been
// built the golden is run past it.
func TestProxyGoldenValidates(t *testing.T) {
	t.Parallel()
	bin, err := filepath.Abs("../../bin/ostiole-proxy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("no bin/ostiole-proxy; run `task build:proxy`")
	}
	dir := t.TempDir()
	p := &Proxy{Dir: dir, SelfCert: filepath.Join(dir, "self.pem"), SelfKey: filepath.Join(dir, "self.key")}
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The certificates are copies an apply places; write a pair where the
	// configuration says they will be.
	for _, id := range certIDs(files[proxyCertList]) {
		certDir := filepath.Join(dir, proxyCertsDir, id)
		if err := os.MkdirAll(certDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestPair(t, filepath.Join(certDir, "fullchain.pem"), filepath.Join(certDir, "key.pem"))
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range crsFiles() {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), bin, "validate", "--config", filepath.Join(dir, proxyConfName))
	cmd.Env = append(os.Environ(), "XDG_DATA_HOME="+dir, "XDG_CONFIG_HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the sidecar refused the rendered configuration: %v\n%s", err, out)
	}
}

// fakeCerts answers as the certificate store does, from memory.
type fakeCerts struct {
	files map[string]*certs.Files
	subs  []func(string)
}

func (f *fakeCerts) Read(id string) (*certs.Files, error) {
	if c, ok := f.files[id]; ok {
		return c, nil
	}
	return nil, certs.ErrNoCertificate
}

func (f *fakeCerts) Subscribe(fn func(string)) { f.subs = append(f.subs, fn) }

func (f *fakeCerts) notify(id string) {
	for _, fn := range f.subs {
		fn(id)
	}
}

func proxyBackend(t *testing.T) (*Proxy, *fakeCmd, *fakeCerts) {
	t.Helper()
	cmd := &fakeCmd{installed: true}
	store := &fakeCerts{files: map[string]*certs.Files{
		"shop": {FullChain: []byte("chain\n"), Key: []byte("key\n")},
	}}
	dir := t.TempDir()
	self := filepath.Join(dir, "tls")
	if err := os.MkdirAll(self, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		if err := os.WriteFile(filepath.Join(self, name), []byte("self "+name+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &Proxy{
		Dir: filepath.Join(dir, "proxy"), Cmd: cmd, Certs: store,
		SelfCert: filepath.Join(self, "cert.pem"), SelfKey: filepath.Join(self, "key.pem"),
	}, cmd, store
}

func TestProxyApplyStartsTheUnitAndPlacesTheCertificates(t *testing.T) {
	t.Parallel()
	p, cmd, _ := proxyBackend(t)
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "enable", "--now", ProxyUnit) {
		t.Errorf("the unit was not started: %v", cmd.calls)
	}
	for _, name := range []string{
		proxyConfName,
		filepath.Join(proxyCertsDir, "shop", "fullchain.pem"),
		filepath.Join(proxyCertsDir, model.SelfCertificate, "key.pem"),
		filepath.Join(crsDirName, "wordpress-before.conf"),
		poolCAName("api"),
	} {
		if _, err := os.Stat(filepath.Join(p.Dir, name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(p.Dir, proxyCertsDir, "shop", "fullchain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "chain\n" {
		t.Errorf("the certificate the manager holds was not copied: %q", raw)
	}

	// A second apply of the same plan changes nothing, so the proxy is
	// left alone rather than reloaded.
	cmd.calls = nil
	if err := p.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if cmd.has("systemctl", "reload", ProxyUnit) {
		t.Errorf("an apply that changed nothing reloaded the proxy: %v", cmd.calls)
	}

	// A change to the configuration reloads rather than restarts.
	cfg := loadConfig(t, "testdata/proxy.json")
	cfg.Services.Proxy.Sites[0].Hosts = []string{"shop.example.com"}
	changed, err := p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cmd.calls = nil
	if err := p.Apply(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "reload", ProxyUnit) {
		t.Errorf("a changed configuration did not reload the proxy: %v", cmd.calls)
	}
	if cmd.has("systemctl", "restart", ProxyUnit) {
		t.Errorf("the proxy was restarted, which drops every connection: %v", cmd.calls)
	}

	// A pool that drops its issuer leaves no file behind, and the rule
	// set files, which are static, are left alone by an apply that finds
	// them in place.
	ca := filepath.Join(p.Dir, poolCAName("api"))
	rules := filepath.Join(p.Dir, crsDirName, "wordpress-before.conf")
	before, err := os.Stat(rules)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Services.Proxy.Pools[1].TLSCAPEM = ""
	changed, err = p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), changed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ca); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dropped issuer is still on disk: %v", err)
	}
	after, err := os.Stat(rules)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an apply rewrote a rule set file that had not changed")
	}
}

func TestProxyApplyStopsTheUnitWhenNothingIsServed(t *testing.T) {
	t.Parallel()
	p, cmd, _ := proxyBackend(t)
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", ProxyUnit) {
		t.Errorf("the unit was not stopped: %v", cmd.calls)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, proxyConfName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the configuration is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, proxyCertsDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the certificates are still there: %v", err)
	}
	// The rule set is static and belongs to the release, not the apply.
	if _, err := os.Stat(filepath.Join(p.Dir, crsDirName, "wordpress-before.conf")); err != nil {
		t.Errorf("the rule set files were taken away: %v", err)
	}

	// And a router that never ran a proxy needs no systemctl at all.
	fresh, cmd2, _ := proxyBackend(t)
	if err := fresh.Apply(t.Context(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if len(cmd2.calls) != 0 {
		t.Errorf("a router with no proxy ran %v", cmd2.calls)
	}
}

func TestProxyPreflight(t *testing.T) {
	t.Parallel()
	p, cmd, store := proxyBackend(t)
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Preflight(t.Context(), files); err != nil {
		t.Fatal(err)
	}

	delete(store.files, "shop")
	var pe *PreflightError
	if err := p.Preflight(t.Context(), files); !errors.As(err, &pe) {
		t.Errorf("an unissued certificate was accepted: %v", err)
	} else if !strings.Contains(pe.Message, "shop") {
		t.Errorf("the refusal does not name the certificate: %q", pe.Message)
	}

	store.files["shop"] = &certs.Files{FullChain: []byte("chain\n"), Key: []byte("key\n")}
	if err := os.Remove(p.SelfCert); err != nil {
		t.Fatal(err)
	}
	if err := p.Preflight(t.Context(), files); !errors.As(err, &pe) {
		t.Errorf("a missing built-in certificate was accepted: %v", err)
	} else if !strings.Contains(pe.Message, "built-in") {
		t.Errorf("the refusal does not name the built-in certificate: %q", pe.Message)
	}

	cmd.installed = false
	if err := p.Preflight(t.Context(), files); !errors.Is(err, errProxyMissing) {
		t.Errorf("an apply without the daemon was accepted: %v", err)
	}
	// A plan with no proxy in it is not refused for a daemon it does not need.
	if err := p.Preflight(t.Context(), network.Files{}); err != nil {
		t.Errorf("an empty plan was refused: %v", err)
	}
}

// A renewal arrives outside an apply, so the copy and the reload happen
// there too.
func TestProxyWatchReloadsOnRenewal(t *testing.T) {
	t.Parallel()
	p, cmd, store := proxyBackend(t)
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	p.Watch(t.Context())

	cmd.calls = nil
	store.files["shop"] = &certs.Files{FullChain: []byte("renewed\n"), Key: []byte("key\n")}
	store.notify("shop")
	raw, err := os.ReadFile(filepath.Join(p.Dir, proxyCertsDir, "shop", "fullchain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "renewed\n" {
		t.Errorf("the renewed certificate was not copied: %q", raw)
	}
	// The certificates that did not renew are still there: placing the
	// renewed one must not take the others away.
	if _, err := os.Stat(filepath.Join(p.Dir, proxyCertsDir, model.SelfCertificate, "fullchain.pem")); err != nil {
		t.Errorf("the built-in certificate went with the renewal: %v", err)
	}
	if !cmd.has("systemctl", "reload", ProxyUnit) {
		t.Errorf("the proxy was not reloaded: %v", cmd.calls)
	}

	// A certificate nothing serves, and one that has just been deleted,
	// are both nothing to do.
	cmd.calls = nil
	store.notify("other")
	delete(store.files, "shop")
	store.notify("shop")
	if cmd.has("systemctl", "reload", ProxyUnit) {
		t.Errorf("a certificate the proxy does not serve reloaded it: %v", cmd.calls)
	}
}

// Caddy tells systemd it is reloading before it is ready again, and the
// apply has to wait for that rather than read it as a failed start.
func TestProxyApplyWaitsForAReload(t *testing.T) {
	t.Parallel()
	p, cmd, _ := proxyBackend(t)
	slow := &reloadingCmd{fakeCmd: cmd}
	p.Cmd, p.Poll = slow, time.Millisecond
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), files); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(t, "testdata/proxy.json")
	cfg.Services.Proxy.Sites[0].Hosts = []string{"watch.example.com"}
	changed, err := p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), changed); err != nil {
		t.Fatalf("a reload in progress was read as a failed start: %v", err)
	}
	if !cmd.has("systemctl", "reload", ProxyUnit) {
		t.Errorf("the proxy was not reloaded: %v", cmd.calls)
	}

	// A unit that is down once the reload is over is still a failed apply.
	slow.dies = true
	cfg.Services.Proxy.Sites[0].Hosts = []string{"other.example.com"}
	changed, err = p.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(t.Context(), changed); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Errorf("a unit that died after the reload was accepted: %v", err)
	}
}

// reloadingCmd answers "reloading" to the first looks after a reload, as
// systemd does while Caddy is between its two notifications.
type reloadingCmd struct {
	*fakeCmd
	pending int
	dies    bool
}

func (c *reloadingCmd) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) >= 2 {
		switch args[0] {
		case "reload":
			c.pending = 2
			if c.dies {
				c.active = false
			}
		case "is-active":
			if c.pending > 0 {
				c.pending--
				return []byte("reloading\n"), nil
			}
		}
	}
	return c.fakeCmd.Run(ctx, name, args...)
}

// The rendered JSON is what the sidecar reads, so the shape of it is
// pinned here as well as in the golden.
func TestProxyRenderShape(t *testing.T) {
	t.Parallel()
	p := &Proxy{Dir: "/etc/ostiole/proxy"}
	files, err := p.Render(loadConfig(t, "testdata/proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Admin struct {
			Listen string `json:"listen"`
			Config struct {
				Persist bool `json:"persist"`
			} `json:"config"`
		} `json:"admin"`
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen           []string         `json:"listen"`
					Protocols        []string         `json:"protocols"`
					ListenerWrappers []map[string]any `json:"listener_wrappers"`
					Routes           []struct {
						Match  []map[string]any `json:"match"`
						Handle []map[string]any `json:"handle"`
					} `json:"routes"`
					Errors *struct {
						Routes []struct {
							Handle []map[string]any `json:"handle"`
						} `json:"routes"`
					} `json:"errors"`
				} `json:"servers"`
			} `json:"http"`
			TLS struct {
				DisableOCSPStapling bool `json:"disable_ocsp_stapling"`
			} `json:"tls"`
			Layer4 struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
					Routes []struct {
						Handle []struct {
							Upstreams []struct {
								Dial []string `json:"dial"`
							} `json:"upstreams"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"layer4"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(files[proxyConfName]), &conf); err != nil {
		t.Fatal(err)
	}
	if conf.Admin.Listen != "unix/"+ProxyAdminSocket {
		t.Errorf("admin socket = %q", conf.Admin.Listen)
	}
	https := conf.Apps.HTTP.Servers["https"]
	if got, want := strings.Join(https.Protocols, ","), "h1,h2,h3"; got != want {
		t.Errorf("protocols = %q, want %q", got, want)
	}
	if len(https.ListenerWrappers) != 2 {
		t.Errorf("a route on the HTTPS port needs the layer4 and tls wrappers, got %v", https.ListenerWrappers)
	}
	// The challenge route comes first on the plain port, whatever else is
	// served: the solver is behind it.
	plain := conf.Apps.HTTP.Servers["http"]
	if len(plain.Routes) < 2 || plain.Routes[1].Match[0]["path"] == nil {
		t.Fatalf("the challenge route is not ahead of the sites: %v", plain.Routes)
	}
	// Nothing names the software: the header comes off on the ordinary
	// path and on an error's, and the response to an error is written by
	// the route, which is what Caddy expects of one.
	for name, srv := range conf.Apps.HTTP.Servers {
		if len(srv.Routes) == 0 || srv.Routes[0].Handle[0]["handler"] != "headers" {
			t.Errorf("%s: the Server header is not taken off: %v", name, srv.Routes)
		}
		if srv.Errors == nil || len(srv.Errors.Routes) != 1 || len(srv.Errors.Routes[0].Handle) != 2 ||
			srv.Errors.Routes[0].Handle[1]["handler"] != "static_response" {
			t.Errorf("%s: an error keeps the Server header: %+v", name, srv.Errors)
		}
	}
	if !conf.Apps.TLS.DisableOCSPStapling {
		t.Error("OCSP stapling is on: the router would ask a certificate's issuer about it")
	}
	if conf.Admin.Config.Persist {
		t.Error("the proxy keeps a copy of its configuration it never reads")
	}
	for _, name := range []string{"tcp-993", "udp-5353"} {
		if _, ok := conf.Apps.Layer4.Servers[name]; !ok {
			t.Errorf("no layer4 server %q: %v", name, conf.Apps.Layer4.Servers)
		}
	}
	if got := conf.Apps.Layer4.Servers["udp-5353"].Listen; len(got) != 1 || got[0] != "udp/:5353" {
		t.Errorf("udp listener = %v", got)
	}
	// A bare host:port is dialled over TCP, so a UDP route names its
	// network on the way out as well as on the way in.
	if !strings.Contains(files[proxyConfName], `"udp/192.168.1.40:53"`) {
		t.Error("the UDP route dials its upstream over TCP")
	}
	if strings.Contains(files[proxyConfName], `"udp/192.168.1.30:993"`) {
		t.Error("a TCP route named a network it does not use")
	}
	if _, ok := conf.Apps.Layer4.Servers["tcp-443"]; ok {
		t.Error("a route on the HTTPS port belongs in the listener wrapper, not its own server")
	}
	// Two upstreams are two entries, each dialled on its own: one entry
	// with two addresses is dialled on both and the stream copied to each.
	imap := conf.Apps.Layer4.Servers["tcp-993"].Routes
	if len(imap) != 1 || len(imap[0].Handle) != 1 || len(imap[0].Handle[0].Upstreams) != 2 {
		t.Fatalf("tcp-993 routes = %+v", imap)
	}
	for _, u := range imap[0].Handle[0].Upstreams {
		if len(u.Dial) != 1 {
			t.Errorf("an upstream dials %v; one address each", u.Dial)
		}
	}
}

// Named routes keep their configuration order, and the one that takes
// the whole port comes last whatever its position.
func TestProxyLayer4RoutesKeepTheirOrder(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/proxy.json")
	up := []model.ProxyUpstream{{Address: "192.168.1.50:8443"}}
	cfg.Services.Proxy.Routes = []model.L4Route{
		{ID: "all", Enabled: true, Protocol: "tcp", Port: 8443, Upstreams: up},
		{ID: "one", Enabled: true, Protocol: "tcp", Port: 8443, SNI: []string{"a.example.com"}, Upstreams: up},
		{ID: "any", Enabled: true, Protocol: "tcp", Port: 8443, SNI: []string{"*.example.com"}, Upstreams: up},
	}
	files, err := (&Proxy{Dir: "/etc/ostiole/proxy"}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Apps struct {
			Layer4 struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							TLS struct {
								SNI []string `json:"sni"`
							} `json:"tls"`
						} `json:"match"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"layer4"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(files[proxyConfName]), &conf); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range conf.Apps.Layer4.Servers["tcp-8443"].Routes {
		if len(r.Match) == 0 {
			got = append(got, "*")
			continue
		}
		got = append(got, strings.Join(r.Match[0].TLS.SNI, ","))
	}
	if want := "a.example.com *.example.com *"; strings.Join(got, " ") != want {
		t.Errorf("routes = %q, want %q", strings.Join(got, " "), want)
	}
}

// The placeholder Caddy fills in is the name without its port, so a
// proxy that is not on 443 has to put its own back.
func TestProxyRedirectCarriesTheHTTPSPort(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		port uint16
		want string
	}{
		{"the default port", 0, "https://{http.request.host}{http.request.uri}"},
		{"a port of its own", 8443, "https://{http.request.host}:8443{http.request.uri}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, "testdata/proxy.json")
			cfg.Services.Proxy.HTTPSPort = tc.port
			// The routes on 443 move with it, and a name is what they take.
			cfg.Services.Proxy.Routes = nil
			files, err := (&Proxy{Dir: "/etc/ostiole/proxy"}).Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(files[proxyConfName], tc.want) {
				t.Errorf("%q missing from the redirect", tc.want)
			}
		})
	}
}

// The sidecar warns about the environment it was started without before
// it prints anything, and the commander hands back stderr with stdout.
func TestProxyReleaseIsTheLastLine(t *testing.T) {
	t.Parallel()
	p := &Proxy{Cmd: noisyCmd{}}
	if got := p.Release(t.Context()); got != "v1.2.3" {
		t.Errorf("release = %q", got)
	}
}

// The answer is kept: the strip polls, and starting Caddy for each poll
// is not free. The admin client is one for the same reason.
func TestProxyReleaseIsAskedOnce(t *testing.T) {
	t.Parallel()
	cmd := &countingCmd{}
	p := &Proxy{Cmd: cmd}
	for range 3 {
		if got := p.Release(t.Context()); got != "v1.2.3" {
			t.Errorf("release = %q", got)
		}
	}
	if cmd.runs != 1 {
		t.Errorf("the sidecar was asked %d times", cmd.runs)
	}
	first := p.adminClient()
	if first != p.adminClient() {
		t.Error("each poll got a client of its own")
	}
}

type countingCmd struct{ runs int }

func (c *countingCmd) Run(context.Context, string, ...string) ([]byte, error) {
	c.runs++
	return []byte("v1.2.3\n"), nil
}

// The proxy has to get through the configuration directory, which is
// root's, to reach its own.
func TestProxySetupGrantsTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := grantTraversal(dir, os.Getgid()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o710 {
		t.Errorf("mode = %o, want 710", got)
	}
}

type noisyCmd struct{}

func (noisyCmd) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte("{\"level\":\"warn\",\"msg\":\"neither $XDG_CONFIG_HOME nor $HOME are defined\"}\nv1.2.3\n"), nil
}

func TestProxyUnitContent(t *testing.T) {
	t.Parallel()
	unit := ProxyUnitContent("/usr/local/bin/ostiole-proxy", "/etc/ostiole/proxy")
	for _, want := range []string{
		"Type=notify",
		"User=ostiole-proxy",
		"ExecStartPre=/usr/local/bin/ostiole-proxy validate --config /etc/ostiole/proxy/caddy.json",
		"ExecReload=/bin/kill -USR1 $MAINPID",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE",
		"ProtectSystem=strict",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("%q missing from the unit:\n%s", want, unit)
		}
	}
}

// writeTestPair leaves a self-signed certificate and its key on disk.
func writeTestPair(t *testing.T, certPath, keyPath string) {
	t.Helper()
	cert, key, err := certs.GenerateSelfSigned([]string{"shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, cert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
}
