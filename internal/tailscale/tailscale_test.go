package tailscale

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// A preference the model does not mention is still written, because
// `tailscale set` leaves what it is not given alone.
func TestPrefArgsWritesEveryFlag(t *testing.T) {
	t.Parallel()
	args := PrefArgs(model.Tailscale{
		Hostname:          "gateway",
		AdvertiseRoutes:   []string{"192.168.1.0/24", "10.0.0.0/8"},
		AdvertiseExitNode: true,
	})
	want := []string{
		"--netfilter-mode=off",
		"--accept-dns=false",
		"--hostname=gateway",
		"--advertise-routes=192.168.1.0/24,10.0.0.0/8",
		"--advertise-exit-node=true",
		"--accept-routes=false",
		"--exit-node=",
		"--ssh=false",
	}
	got := strings.Join(args, " ")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in %s", w, got)
		}
	}

	// An empty model still clears everything, so a revert is a replay.
	bare := strings.Join(PrefArgs(model.Tailscale{}), " ")
	for _, w := range []string{"--hostname=", "--advertise-routes=", "--advertise-exit-node=false"} {
		if !strings.Contains(bare, w) {
			t.Errorf("missing %s in %s", w, bare)
		}
	}
}

// `tailscale up` refuses a flag it does not define, so the three that are
// `set` only must not reach it. They are pushed again after the login,
// because up --reset puts them back to their defaults.
func TestUpArgsLeaveOutTheFlagsUpDoesNotTake(t *testing.T) {
	t.Parallel()
	setOnly := []string{"--auto-update=", "--update-check=", "--webclient="}
	up := strings.Join(UpArgs(model.Tailscale{}), " ")
	for _, flag := range setOnly {
		if strings.Contains(up, flag) {
			t.Errorf("up was given %s, which it does not define: %s", flag, up)
		}
	}
	all := strings.Join(PrefArgs(model.Tailscale{}), " ")
	for _, flag := range setOnly {
		if !strings.Contains(all, flag) {
			t.Errorf("set was not given %s: %s", flag, all)
		}
	}
	// Everything else is in both, so a login and an apply agree.
	for _, arg := range UpArgs(model.Tailscale{}) {
		if !strings.Contains(all, arg) {
			t.Errorf("%s is on up but not on set", arg)
		}
	}
}

func TestDaemonEnvDefaultsThePortAndKeepsLogsHere(t *testing.T) {
	t.Parallel()
	if got, want := DaemonEnv(model.Tailscale{}), "PORT=41641\nFLAGS=--no-logs-no-support\n"; got != want {
		t.Errorf("DaemonEnv = %q, want %q", got, want)
	}
	got := DaemonEnv(model.Tailscale{Port: 3478, LogUploads: true})
	if want := "PORT=3478\nFLAGS=\n"; got != want {
		t.Errorf("DaemonEnv with uploads = %q, want %q", got, want)
	}
}

func TestParseStatusReadsWhatThePageShows(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseStatus(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.BackendState != StateRunning {
		t.Errorf("BackendState = %q", s.BackendState)
	}
	if len(s.TailscaleIPs) != 2 || s.TailscaleIPs[0] != "100.101.102.103" {
		t.Errorf("TailscaleIPs = %v", s.TailscaleIPs)
	}
	if s.Self == nil || s.Self.DNSName != "ostiole-fw.tail1234.ts.net." {
		t.Fatalf("Self = %+v", s.Self)
	}
	if s.Self.KeyExpiry == nil || s.Self.KeyExpiry.Year() != 2027 {
		t.Errorf("KeyExpiry = %v", s.Self.KeyExpiry)
	}
	if len(s.Self.PrimaryRoutes) != 1 || s.Self.PrimaryRoutes[0] != "192.168.1.0/24" {
		t.Errorf("PrimaryRoutes = %v", s.Self.PrimaryRoutes)
	}
	if s.CurrentTailnet == nil || s.CurrentTailnet.Name != "example.com" {
		t.Fatalf("CurrentTailnet = %+v", s.CurrentTailnet)
	}
	if len(s.Peer) != 2 {
		t.Fatalf("peers = %d", len(s.Peer))
	}
	for _, p := range s.Peer {
		switch p.HostName {
		case "laptop":
			if !p.Online || p.CurAddr == "" {
				t.Errorf("laptop = %+v, want a direct connection", p)
			}
		case "phone":
			if p.Online || p.LastSeen == nil {
				t.Errorf("phone = %+v, want offline with a last seen", p)
			}
		default:
			t.Errorf("unexpected peer %q", p.HostName)
		}
	}
}

// fakeRunner answers with canned output and records what it was asked.
type fakeRunner struct {
	out    []byte
	err    error
	stream string
	calls  [][]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.out, f.err
}

func (f *fakeRunner) Stream(_ context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.err != nil {
		return nil, nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.stream)), func() error { return nil }, nil
}

func TestLoginReturnsTheAuthURLWithoutWaiting(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{stream: `{"BackendState":"NeedsLogin"}
{"AuthURL":"https://login.tailscale.com/a/abc123"}
{"BackendState":"Running"}
`}
	c := &Client{Bin: "tailscale", Run: run}
	url, done, err := c.Login(t.Context(), []string{"--hostname=gw"}, "https://control.example", "tskey-secret")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://login.tailscale.com/a/abc123" {
		t.Errorf("auth URL = %q", url)
	}
	if err := <-done; err != nil {
		t.Errorf("up finished with %v", err)
	}
	argv := strings.Join(run.calls[0], " ")
	for _, want := range []string{"up --reset --json", "--hostname=gw",
		"--login-server=https://control.example", "--auth-key=tskey-secret"} {
		if !strings.Contains(argv, want) {
			t.Errorf("missing %s in %s", want, argv)
		}
	}
}

func TestLoginReportsTheDaemonsError(t *testing.T) {
	t.Parallel()
	c := &Client{Run: &fakeRunner{stream: `{"Error":"invalid key: not found"}` + "\n"}}
	if _, _, err := c.Login(t.Context(), nil, "", "tskey-wrong"); err == nil ||
		!strings.Contains(err.Error(), "invalid key") {
		t.Errorf("error = %v, want the daemon's own text", err)
	}
}

// A login that ends without printing anything usable is the exit status.
func TestLoginFallsBackToTheExitStatus(t *testing.T) {
	t.Parallel()
	c := &Client{Run: &fakeRunner{stream: "not json at all\n"}}
	url, done, err := c.Login(t.Context(), nil, "", "")
	if err != nil || url != "" {
		t.Fatalf("Login = %q, %v", url, err)
	}
	if err := <-done; err != nil {
		t.Errorf("done = %v", err)
	}
}

func TestSetAndLogoutCarryTheCommandsOutput(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{out: []byte("nope"), err: errors.New("exit status 1")}
	c := &Client{Run: run}
	if err := c.Set(t.Context(), []string{"--ssh=false"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("Set error = %v", err)
	}
	if err := c.Logout(t.Context()); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("Logout error = %v", err)
	}
	if got := strings.Join(run.calls[0], " "); got != "tailscale set --ssh=false" {
		t.Errorf("argv = %q", got)
	}
}
