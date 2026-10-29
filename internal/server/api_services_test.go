package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/audit"
	"ostiole/internal/auth"
	"ostiole/internal/engine"
	"ostiole/internal/install"
	"ostiole/internal/model"
	"ostiole/internal/nft/nfttest"
	"ostiole/internal/services"
	"ostiole/internal/store"
)

// showingUnits answers ReadUnits as fakeUnits does, and show with the
// blocks systemd would print, one per unit asked, in the order asked.
type showingUnits struct {
	fakeUnits
	show map[string]string
}

func (s showingUnits) Run(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 || args[0] != "show" {
		return s.fakeUnits.Run(ctx, args...)
	}
	var blocks []string
	for _, u := range args[slices.Index(args, "--")+1:] {
		blocks = append(blocks, "Id="+u+"\n"+s.show[u])
	}
	return strings.Join(blocks, "\n\n"), nil
}

// controlCalls keeps every restart and start it is asked for, and fails
// the units in fail with what systemctl says.
type controlCalls struct {
	mu    sync.Mutex
	calls []string
	fail  map[string]string
}

func (c *controlCalls) Run(_ context.Context, args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, strings.Join(args, " "))
	if out, ok := c.fail[args[len(args)-1]]; ok {
		return out, errors.New("exit status 1")
	}
	return "", nil
}

func (c *controlCalls) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

type servicesRig struct {
	srv     *httptest.Server
	log     *audit.Log
	control *controlCalls
}

// newServicesRig is a signed-in server whose router runs DHCP and DNS,
// started at 2026-10-29 12:00 UTC and restarted twice by systemd since,
// dials a PPPoE session on ppp0, and whose network service is down.
func newServicesRig(t *testing.T, dnsmasq string, control bool) *servicesRig {
	t.Helper()
	dir := t.TempDir()
	eng := engine.New(store.New(dir), &nfttest.Fake{}, nil, slog.New(slog.DiscardHandler))
	cfg := model.Starter(model.StarterOptions{LAN: "eth1", LANAddress: "192.168.1.1/24", WAN: "eth0", Services: true})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: "ppp0", Zone: "wan", Enabled: true,
		IPv4:  model.IPv4{Mode: model.AddrPPP},
		IPv6:  model.IPv6{Mode: model.AddrNone},
		PPPoE: &model.PPPoE{Parent: "eth9", Username: "someone", Password: "secret"},
	})
	if _, err := eng.Store().Save(cfg, "", store.Author{}); err != nil {
		t.Fatal(err)
	}
	as, err := auth.NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	rig := &servicesRig{log: audit.Open(dir), control: &controlCalls{fail: map[string]string{}}}
	deps := Deps{
		Engine:   eng,
		Auth:     as,
		Audit:    rig.log,
		Services: &services.Dnsmasq{},
		Units: showingUnits{
			fakeUnits: fakeUnits{state: map[string][2]string{
				services.Unit:                 {dnsmasq, "enabled"},
				services.PPPoEUnit:            {"", "static"},
				services.PPPoEUnitFor("ppp0"): {"active", "enabled"},
				"systemd-networkd.service":    {"inactive", "disabled"},
			}},
			show: map[string]string{
				services.Unit:      "ActiveEnterTimestamp=@1793275200\nResult=success\nNRestarts=2",
				install.DaemonUnit: "ActiveEnterTimestamp=@1793270000\nResult=success\nNRestarts=0",
			},
		},
	}
	if control {
		deps.Control = rig.control
	}
	rig.srv = httptest.NewServer(Handler(deps))
	jar, _ := cookiejar.New(nil)
	rig.srv.Client().Jar = jar
	t.Cleanup(rig.srv.Close)
	if resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/setup", credentials{Username: "admin", Password: testPassword}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup: %d %s", resp.StatusCode, raw)
	}
	return rig
}

func (rig *servicesRig) list(t *testing.T) ServicesReply {
	t.Helper()
	resp, raw := do(t, rig.srv, http.MethodGet, "/api/v1/system/services", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	var out ServicesReply
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func unitNamed(t *testing.T, units []ServiceUnit, unit string) ServiceUnit {
	t.Helper()
	i := slices.IndexFunc(units, func(u ServiceUnit) bool { return u.Unit == unit })
	if i < 0 {
		t.Fatalf("no row for %s in %+v", unit, units)
	}
	return units[i]
}

func (rig *servicesRig) lastAudit(t *testing.T) audit.Event {
	t.Helper()
	evs, err := rig.log.Events()
	if err != nil || len(evs) == 0 {
		t.Fatalf("audit = %v, %v", evs, err)
	}
	return evs[len(evs)-1]
}

func TestServicesListsTheUnitsWithWhatMayBeDone(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	got := rig.list(t)
	if !got.Control {
		t.Error("a daemon with control says it has none")
	}
	if got.Units[0].Unit != install.DaemonUnit || !slices.Equal(got.Units[0].Actions, []string{unitRestart}) {
		t.Errorf("first row = %+v, want Ostiole, restartable", got.Units[0])
	}
	d := unitNamed(t, got.Units, services.Unit)
	if d.Name != "DHCP and DNS" || d.State != stateActive || !slices.Equal(d.Actions, []string{unitRestart}) {
		t.Errorf("dnsmasq = %+v", d)
	}
	if d.Since == nil || !d.Since.Equal(time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)) || d.Restarts != 2 {
		t.Errorf("dnsmasq since %v, restarts %d", d.Since, d.Restarts)
	}
	if d.Effect == "" {
		t.Error("a restart says nothing of what it interrupts")
	}
	if n := unitNamed(t, got.Units, install.NetworkdUnit); len(n.Actions) != 0 {
		t.Errorf("the network service can be acted on: %v", n.Actions)
	}
	if slices.ContainsFunc(got.Units, func(u ServiceUnit) bool { return u.Unit == "" }) {
		t.Error("a row with no unit is listed")
	}
}

func TestServicesWithoutControlAreReadOnly(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", false)
	got := rig.list(t)
	if got.Control || slices.ContainsFunc(got.Units, func(u ServiceUnit) bool { return len(u.Actions) > 0 }) {
		t.Errorf("a daemon without control offers actions: %+v", got)
	}
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+services.Unit+"/restart", nil)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "cannot restart services") {
		t.Errorf("restart = %d %s", resp.StatusCode, raw)
	}
}

func TestServicesRestartRunsSystemctlAndIsAudited(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+services.Unit+"/restart", nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `"error"`) {
		t.Fatalf("restart = %d %s", resp.StatusCode, raw)
	}
	if want := []string{"--no-ask-password restart " + services.Unit}; !slices.Equal(rig.control.seen(), want) {
		t.Errorf("systemctl = %q, want %q", rig.control.seen(), want)
	}
	if ev := rig.lastAudit(t); ev.Action != audit.ServiceRestart || ev.Target != "DHCP and DNS" || ev.Detail != "" {
		t.Errorf("audit = %+v", ev)
	}
}

func TestServicesRestartThatFailsSaysWhy(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	rig.control.fail[services.Unit] = "Job for ostiole-dnsmasq.service failed because the control process exited with error code.\nSee \"systemctl status ostiole-dnsmasq.service\" for details."
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+services.Unit+"/restart", nil)
	var reply serviceActionReply
	_ = json.Unmarshal(raw, &reply)
	if resp.StatusCode != http.StatusOK || reply.Error != "Job for ostiole-dnsmasq.service failed because the control process exited with error code." {
		t.Errorf("restart = %d %s", resp.StatusCode, raw)
	}
	if ev := rig.lastAudit(t); ev.Action != audit.ServiceRestart || ev.Detail != "failed" {
		t.Errorf("audit = %+v", ev)
	}
}

func TestServicesStartOnlyAWantedUnitThatIsDown(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "failed", true)
	if d := unitNamed(t, rig.list(t).Units, services.Unit); !slices.Equal(d.Actions, []string{unitStart}) || d.Since != nil {
		t.Errorf("a wanted unit that is down = %+v, want start and no since", d)
	}
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+services.Unit+"/restart", nil)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(raw), "start it instead") {
		t.Errorf("restart of a stopped unit = %d %s", resp.StatusCode, raw)
	}
	resp, raw = do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+services.Unit+"/start", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start = %d %s", resp.StatusCode, raw)
	}
	if want := []string{"--no-ask-password start " + services.Unit}; !slices.Equal(rig.control.seen(), want) {
		t.Errorf("systemctl = %q, want %q", rig.control.seen(), want)
	}
	if ev := rig.lastAudit(t); ev.Action != audit.ServiceStart || ev.Target != "DHCP and DNS" {
		t.Errorf("audit = %+v", ev)
	}
}

func TestServicesRefuseWhatTheListDoesNotOffer(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	for _, c := range []struct {
		unit, action string
		status       int
	}{
		{"sshd.service", unitRestart, http.StatusNotFound},
		{install.NetworkdUnit, unitRestart, http.StatusBadRequest},
		{install.NetworkdUnit, unitStart, http.StatusBadRequest},
		{services.Unit, unitStart, http.StatusBadRequest},
	} {
		resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+c.unit+"/"+c.action, nil)
		if resp.StatusCode != c.status {
			t.Errorf("%s %s = %d %s, want %d", c.action, c.unit, resp.StatusCode, raw, c.status)
		}
	}
	if calls := rig.control.seen(); len(calls) != 0 {
		t.Errorf("systemctl was asked: %q", calls)
	}
}

func TestServicesRestartOstioleAfterTheReply(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+install.DaemonUnit+"/restart", nil)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(string(raw), `"restarting":true`) {
		t.Fatalf("restart = %d %s", resp.StatusCode, raw)
	}
	want := "--no-ask-password --no-block restart " + install.DaemonUnit
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if slices.Contains(rig.control.seen(), want) {
			break
		}
	}
	if got := rig.control.seen(); !slices.Equal(got, []string{want}) {
		t.Errorf("systemctl = %q, want %q", got, want)
	}
	if ev := rig.lastAudit(t); ev.Action != audit.ServiceRestart || ev.Target != "Ostiole" {
		t.Errorf("audit = %+v", ev)
	}
}

func TestServicesListsPPPoESessionsBeforeTheNetwork(t *testing.T) {
	t.Parallel()
	rig := newServicesRig(t, "active", true)
	units := rig.list(t).Units
	unit := services.PPPoEUnitFor("ppp0")
	p := unitNamed(t, units, unit)
	if p.Name != "PPPoE ppp0" || p.State != stateActive || !slices.Equal(p.Actions, []string{unitRestart}) {
		t.Errorf("session = %+v", p)
	}
	if !strings.Contains(p.Effect, "connection through it breaks") {
		t.Errorf("effect = %q, want the warning that connections break", p.Effect)
	}
	at := slices.IndexFunc(units, func(u ServiceUnit) bool { return u.Unit == unit })
	if at+1 >= len(units) || units[at+1].Unit != install.NetworkdUnit {
		t.Errorf("the session is not right before the network: %+v", units)
	}
	resp, raw := do(t, rig.srv, http.MethodPost, "/api/v1/system/services/"+unit+"/restart", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart = %d %s", resp.StatusCode, raw)
	}
	if want := []string{"--no-ask-password restart " + unit}; !slices.Equal(rig.control.seen(), want) {
		t.Errorf("systemctl = %q, want %q", rig.control.seen(), want)
	}
	if ev := rig.lastAudit(t); ev.Action != audit.ServiceRestart || ev.Target != "PPPoE ppp0" {
		t.Errorf("audit = %+v", ev)
	}
}

func TestRestartEffectCoversEveryRestartableUnit(t *testing.T) {
	t.Parallel()
	for _, u := range []string{install.DaemonUnit, services.Unit, services.UnboundUnit, services.UPnPUnit,
		services.TailscaleUnit, services.ProxyUnit, services.NTPUnit,
		services.OpenVPNUnitFor("work"), services.WirelessUnitFor("wlan0"), services.PPPoEUnitFor("ppp0")} {
		if restartEffect(u) == "" {
			t.Errorf("%s has no effect line, so it cannot be restarted", u)
		}
	}
	if restartEffect(install.NetworkdUnit) != "" {
		t.Error("the network service has an effect line")
	}
}
