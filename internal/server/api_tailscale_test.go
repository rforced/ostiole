package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/auth"
	"github.com/rforced/ostiole/internal/engine"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/nft/nfttest"
	"github.com/rforced/ostiole/internal/services"
	"github.com/rforced/ostiole/internal/store"
	"github.com/rforced/ostiole/internal/tailscale"
)

// tsUnit answers `systemctl cat` and `is-active` for the Tailscale unit.
type tsUnit struct {
	installed bool
	active    bool
}

func (u tsUnit) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch {
	case len(args) >= 1 && args[0] == "cat":
		if u.installed {
			return []byte("[Unit]"), nil
		}
		return nil, errors.New("no such unit")
	case len(args) >= 1 && args[0] == "is-active":
		if u.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	}
	return nil, nil
}

// tsCLI is the tailscale command line, answering with canned output. A
// login goes on in its own goroutine, so the record is locked.
type tsCLI struct {
	status  string
	statErr error
	stream  string

	mu    sync.Mutex
	calls [][]string
}

func (c *tsCLI) note(name string, args []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, append([]string{name}, args...))
}

func (c *tsCLI) ran(verb string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if len(call) > 1 && call[1] == verb {
			return strings.Join(call, " "), true
		}
	}
	return "", false
}

func (c *tsCLI) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c.note(name, args)
	if len(args) > 0 && args[0] == "status" {
		return []byte(c.status), c.statErr
	}
	return nil, nil
}

func (c *tsCLI) Stream(_ context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	c.note(name, args)
	return io.NopCloser(strings.NewReader(c.stream)), func() error { return nil }, nil
}

const tsRunningStatus = `{"BackendState":"Running","TailscaleIPs":["100.101.102.103"],
"Self":{"HostName":"fw","DNSName":"fw.tail1.ts.net."},
"CurrentTailnet":{"Name":"example.com","MagicDNSSuffix":"tail1.ts.net"},
"Peer":{"a":{"HostName":"phone","Online":false},"b":{"HostName":"laptop","Online":true}}}`

func newTailscaleServer(t *testing.T, unit tsUnit, cli *tsCLI, cfg *model.Config) (*httptest.Server, *auth.Service) {
	t.Helper()
	dir := t.TempDir()
	st := store.New(dir)
	if cfg != nil {
		if _, err := st.Save(cfg, "table inet ostiole {}\n"); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.New(st, &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Engine: eng, Auth: as,
		Tailscale: &services.Tailscale{Dir: dir, Cmd: unit},
	}
	if cli != nil {
		deps.TSClient = &tailscale.Client{Bin: "tailscale", Run: cli}
	}
	srv := httptest.NewServer(Handler(deps))
	jar, _ := cookiejar.New(nil)
	srv.Client().Jar = jar
	t.Cleanup(srv.Close)
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return srv, as
}

func tailscaleConfig() *model.Config {
	return &model.Config{
		Version: model.SchemaVersion,
		System:  model.System{Hostname: "fw", Management: model.Management{WebPort: 443, SSHPort: 22}},
		Zones:   []model.Zone{{Name: "lan", AntiLockout: true}, {Name: "tailnet"}},
		Interfaces: []model.Interface{
			{Name: "eth1", Zone: "lan", Enabled: true,
				IPv4: model.IPv4{Mode: model.AddrStatic, Address: "192.168.1.1/24"},
				IPv6: model.IPv6{Mode: model.AddrNone}},
			{Name: model.TailscaleDevice, Zone: "tailnet", Enabled: true,
				IPv4:      model.IPv4{Mode: model.AddrNone},
				IPv6:      model.IPv6{Mode: model.AddrNone},
				Tailscale: &model.Tailscale{Port: 41641, Hostname: "fw"}},
		},
		NAT: model.NAT{Outbound: model.OutboundNAT{Mode: model.OutboundAutomatic}},
	}
}

func getTailscaleStatus(t *testing.T, srv *httptest.Server) tailscaleStatus {
	t.Helper()
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/tailscale/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}
	var got tailscaleStatus
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// A router without the daemon answers the status route rather than
// failing it: the page has a line for every one of these.
func TestTailscaleStatusIsNeverAnError(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		unit    tsUnit
		cli     *tsCLI
		setUp   bool
		running bool
	}{
		{"no unit", tsUnit{}, &tsCLI{status: tsRunningStatus}, false, false},
		{"unit but stopped", tsUnit{installed: true}, &tsCLI{status: tsRunningStatus}, true, false},
		{"no client", tsUnit{installed: true, active: true}, nil, true, false},
		{"daemon will not answer", tsUnit{installed: true, active: true},
			&tsCLI{statErr: errors.New("exit 1")}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := newTailscaleServer(t, tc.unit, tc.cli, nil)
			got := getTailscaleStatus(t, srv)
			if got.SetUp != tc.setUp || got.Running != tc.running {
				t.Errorf("setUp/running = %v/%v, want %v/%v", got.SetUp, got.Running, tc.setUp, tc.running)
			}
			if got.Peers == nil || got.IPs == nil || got.Health == nil {
				t.Errorf("a list came back null: %+v", got)
			}
		})
	}
}

func TestTailscaleStatusReportsTheTailnet(t *testing.T) {
	t.Parallel()
	srv, _ := newTailscaleServer(t, tsUnit{installed: true, active: true}, &tsCLI{status: tsRunningStatus}, nil)
	got := getTailscaleStatus(t, srv)
	if !got.Running || got.State != tailscale.StateRunning {
		t.Fatalf("status = %+v", got)
	}
	if got.DNSName != "fw.tail1.ts.net." || got.Tailnet != "example.com" {
		t.Errorf("node = %q on %q", got.DNSName, got.Tailnet)
	}
	// The ones that are up come first, so the table does not reshuffle.
	if len(got.Peers) != 2 || got.Peers[0].HostName != "laptop" || got.Peers[1].HostName != "phone" {
		t.Errorf("peers = %+v", got.Peers)
	}
}

func TestTailscaleLoginReturnsTheAuthURL(t *testing.T) {
	t.Parallel()
	cli := &tsCLI{
		status: tsRunningStatus,
		stream: `{"AuthURL":"https://login.tailscale.com/a/abc"}` + "\n",
	}
	srv, _ := newTailscaleServer(t, tsUnit{installed: true, active: true}, cli, tailscaleConfig())

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/tailscale/login", map[string]string{"authKey": "tskey-x"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, raw)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["authUrl"] != "https://login.tailscale.com/a/abc" {
		t.Errorf("answer = %v", got)
	}
	argv, ok := cli.ran("up")
	if !ok {
		t.Fatal("up was never run")
	}
	for _, want := range []string{"--reset", "--hostname=fw", "--netfilter-mode=off", "--auth-key=tskey-x"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %s in %s", want, argv)
		}
	}
	// up does not define these, so giving them to it would fail the login.
	for _, never := range []string{"--auto-update", "--update-check", "--webclient"} {
		if strings.Contains(argv, never) {
			t.Errorf("up was given %s: %s", never, argv)
		}
	}
	// They are pushed once the node is up, because --reset took them back
	// to defaults and one of those phones home.
	waitFor(t, func() bool {
		argv, ok := cli.ran("set")
		return ok && strings.Contains(argv, "--update-check=false")
	}, "the preferences up does not take were never pushed")
}

// waitFor polls until cond holds, for work a handler left running behind
// it.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}

// Nothing to log in until the interface is in the saved configuration.
func TestTailscaleLoginNeedsAnAppliedInterface(t *testing.T) {
	t.Parallel()
	srv, _ := newTailscaleServer(t, tsUnit{installed: true, active: true}, &tsCLI{status: tsRunningStatus}, nil)
	resp, raw := do(t, srv, http.MethodPost, "/api/v1/tailscale/login", map[string]string{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("login without an interface: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "apply the Tailscale interface first") {
		t.Errorf("error = %s", raw)
	}
}

func TestTailscaleLogoutIsAdminOnly(t *testing.T) {
	t.Parallel()
	cli := &tsCLI{status: tsRunningStatus}
	srv, as := newTailscaleServer(t, tsUnit{installed: true, active: true}, cli, tailscaleConfig())
	if err := as.CreateUser("hand", testPassword, auth.RoleOperator); err != nil {
		t.Fatal(err)
	}

	resp, raw := do(t, srv, http.MethodPost, "/api/v1/tailscale/logout", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout as admin: %d %s", resp.StatusCode, raw)
	}
	if _, ok := cli.ran("logout"); !ok {
		t.Error("logout not run")
	}

	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/auth/login",
		credentials{Username: "hand", Password: testPassword}); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in as the operator: %d %s", resp.StatusCode, raw)
	}
	if resp, raw := do(t, srv, http.MethodPost, "/api/v1/tailscale/logout", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("logout as an operator: %d %s", resp.StatusCode, raw)
	}
}

// The services strip says whether the daemon is there, so the page can
// tell "not installed" from "not joined" before anything is applied.
func TestServicesStatusReportsTailscale(t *testing.T) {
	t.Parallel()
	srv, _ := newTailscaleServer(t, tsUnit{installed: true, active: true}, &tsCLI{status: tsRunningStatus}, nil)
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/services/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("services status: %d %s", resp.StatusCode, raw)
	}
	var got struct {
		TailscaleSetUp   bool `json:"tailscaleSetUp"`
		TailscaleRunning bool `json:"tailscaleRunning"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.TailscaleSetUp || !got.TailscaleRunning {
		t.Errorf("services status = %+v", got)
	}
}
