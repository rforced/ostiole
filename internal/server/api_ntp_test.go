package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/chrony"
	"ostiole/internal/chrony/chronytest"
	"ostiole/internal/model"
	"ostiole/internal/services"
)

// ntpUnit answers systemctl about ostiole-chronyd. Tests change it
// between requests, so it is read under a lock.
type ntpUnit struct {
	mu                         sync.Mutex
	installed, active, skipped bool
	since                      time.Time
}

func (u *ntpUnit) set(change func(u *ntpUnit)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	change(u)
}

func (u *ntpUnit) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch args[0] {
	case "cat":
		if u.installed {
			return []byte("[Unit]"), nil
		}
		return nil, errors.New("no such unit")
	case "is-active":
		if u.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	case "show":
		switch {
		case slices.Contains(args, "--property=ActiveEnterTimestamp"):
			return fmt.Appendf(nil, "@%d\n", u.since.Unix()), nil
		case slices.Contains(args, "--property=ConditionResult"):
			if u.skipped {
				return []byte("no\n"), nil
			}
			return []byte("yes\n"), nil
		}
	}
	return nil, nil
}

// ntpSystemd answers the dashboard's two calls about every unit from the
// same state, as systemctl would.
type ntpSystemd struct{ u *ntpUnit }

func (s ntpSystemd) Run(_ context.Context, args ...string) (string, error) {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	switch args[0] {
	case "list-unit-files":
		if s.u.installed && slices.Contains(args, services.NTPUnit) {
			return services.NTPUnit + " enabled disabled", nil
		}
		return "", errors.New("exit status 1")
	case "is-active":
		var lines []string
		for _, unit := range args[1:] {
			if unit == services.NTPUnit && s.u.active {
				lines = append(lines, "active")
			} else {
				lines = append(lines, "inactive")
			}
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", errors.New("unexpected systemctl call")
}

var (
	// synced is what chrony 4.9 on the router said once it followed
	// nts.example.net, corrected a moment ago rather than on
	// the day, which would count as lost.
	synced = chrony.Tracking{
		Address: "198.51.100.39", Stratum: 3, RefTime: time.Now().Add(-40 * time.Second),
		Offset: -0.003059836, RootDelay: 0.049001947, RootDispersion: 0.019441204, Leap: "Normal",
	}
	unsynchronised = chrony.Tracking{Leap: "Not synchronised"}
)

// chronySource is a server as chronytest lists it.
func chronySource(name, addr, state string, stratum int, last time.Duration, auth *chrony.Auth) chronytest.Source {
	s := chrony.Source{Name: name, Address: addr, State: state, Stratum: stratum, Poll: 64 * time.Second, LastRx: last}
	if last >= 0 {
		s.Reach = 0o17
	}
	return chronytest.Source{Source: s, Auth: auth}
}

// chronyd is the router's time service following four NTS servers, an
// address that never answers and a pool, as chrony 4.9 reported it.
func chronyd(t *testing.T) *chronytest.Daemon {
	t.Helper()
	d := chronytest.New(t)
	d.SetTracking(synced)
	signed := &chrony.Auth{Mode: "NTS", LastKE: 75 * time.Second, Cookies: 8}
	d.SetSources(
		chronySource("nts.netnod.se", "194.58.205.196", "excluded", 1, 26*time.Second, signed),
		chronySource("nts.time.nl", "94.198.159.11", "excluded", 2, 27*time.Second, signed),
		chronySource("a.st1.ntp.br", "200.160.7.186", "excluded", 1, 28*time.Second, signed),
		chronySource("nts.example.net", "198.51.100.39", "selected", 2, 28*time.Second, signed),
		chronySource("192.0.2.123", "192.0.2.123", "unusable", 0, -1, nil),
		chronySource("2.pool.ntp.org", "45.63.54.13", "excluded", 2, 28*time.Second, nil),
		chronySource("2.pool.ntp.org", "137.190.2.4", "excluded", 1, 28*time.Second, nil),
	)
	d.SetServerStats(chrony.ServerStats{NTPReceived: 1234, NTPDropped: 5})
	return d
}

func newNTPServer(t *testing.T, unit *ntpUnit, daemon *chronytest.Daemon) *httptest.Server {
	t.Helper()
	f := services.NTPFeatures{Version: chrony.Version{Major: 4, Minor: 9, NTS: true}}
	return newTestServerWith(t, func(d *Deps) {
		d.NTP = &services.NTP{Cmd: unit, Features: &f}
		d.Units = ntpSystemd{unit}
		d.Chrony = &chrony.Client{Addr: daemon.Addr}
	})
}

func getNTPStatus(t *testing.T, srv *httptest.Server) ntpStatus {
	t.Helper()
	resp, raw := do(t, srv, http.MethodGet, "/api/v1/ntp/status", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d %s", resp.StatusCode, raw)
	}
	var st ntpStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// Before `ostiole repair` writes the unit, the page says so, and chronyd
// is not asked: the daemon on the port is the distribution's.
func TestNTPStatusBeforeSetUp(t *testing.T) {
	t.Parallel()
	daemon := chronyd(t)
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{}, daemon))
	if st.SetUp || st.Running || st.Read || st.Sources == nil || len(st.Sources) != 0 {
		t.Errorf("status = %+v", st)
	}
	// The page shows the list a router with none of its own would use.
	if !slices.Equal(st.Defaults, model.DefaultNTPServers) {
		t.Errorf("defaults = %+v", st.Defaults)
	}
	if n := daemon.Requests(); n != 0 {
		t.Errorf("chronyd was asked %d times about a service that is not ours", n)
	}
}

func TestNTPStatusReadsTheService(t *testing.T) {
	t.Parallel()
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, active: true}, chronyd(t)))
	if !st.SetUp || !st.Running || !st.Read || !st.Synchronised || st.Version != "4.9" {
		t.Fatalf("status = %+v", st)
	}
	// The reference goes by the name it was configured with.
	if st.Reference != "nts.example.net" || st.Stratum != 3 || st.LastUpdate == nil {
		t.Errorf("reference = %q stratum %d at %v", st.Reference, st.Stratum, st.LastUpdate)
	}
	if st.OffsetSeconds >= 0 {
		t.Errorf("offset = %v; chronyd said slow, which is negative here", st.OffsetSeconds)
	}
	if len(st.Sources) != 7 {
		t.Fatalf("sources = %+v", st.Sources)
	}
	for i, want := range []string{"signed", "signed", "signed", "signed", "", "", ""} {
		if st.Sources[i].NTS != want {
			t.Errorf("%s nts = %q, want %q", st.Sources[i].Name, st.Sources[i].NTS, want)
		}
	}
	if s := st.Sources[4]; s.State != "unusable" || s.LastSeconds != -1 || s.Reach != 0 {
		t.Errorf("a source that never answered = %+v", s)
	}
	if s := st.Sources[3]; s.PollSeconds != 64 || s.Reach != 0o17 || s.LastSeconds != 28 {
		t.Errorf("the selected source = %+v", s)
	}
	if st.Served == nil || st.Served.Packets != 1234 || st.Served.Dropped != 5 {
		t.Errorf("served = %+v", st.Served)
	}
}

// Debian 13's chronyd cannot open authdata, selectdata or serverstats to
// the command port. The rest is still read, and those parts are left out.
func TestNTPStatusOnAnOlderBuild(t *testing.T) {
	t.Parallel()
	daemon := chronyd(t)
	daemon.Refuse("authdata", "selectdata", "serverstats")
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, active: true}, daemon))
	if !st.Read || len(st.Sources) != 7 || st.Served != nil {
		t.Fatalf("status = %+v", st)
	}
	for _, s := range st.Sources {
		if s.NTS != "" {
			t.Errorf("%s nts = %q with no way to know", s.Name, s.NTS)
		}
	}
}

// The router's own case: beside one NTS server and minsources 2, chronyd
// trusted none of the pool and waited on Cloudflare for ever. The page
// says so rather than calling servers that answer silent.
func TestNTPStatusSaysWhyAServerGoesUnused(t *testing.T) {
	t.Parallel()
	daemon := chronytest.New(t)
	daemon.SetTracking(unsynchronised)
	signed := &chrony.Auth{Mode: "NTS", LastKE: 75 * time.Second, Cookies: 8}
	with := func(s chronytest.Source, letter byte) chronytest.Source {
		s.Select = letter
		return s
	}
	daemon.SetSources(
		with(chronySource("time.cloudflare.com", "162.159.200.123", "excluded", 3, 20*time.Second, signed), 'W'),
		with(chronySource("2.pool.ntp.org", "172.233.177.198", "unusable", 2, 30*time.Second, nil), 'T'),
		// One that is not answering says that, whatever selection made of it.
		with(chronySource("192.0.2.1", "192.0.2.1", "unusable", 0, -1, nil), 'M'),
	)
	srv := newNTPServer(t, &ntpUnit{installed: true, active: true}, daemon)
	states := func() []string {
		var out []string
		for _, s := range getNTPStatus(t, srv).Sources {
			out = append(out, s.State)
		}
		return out
	}
	if got, want := states(), []string{"waiting", "untrusted", "unusable"}; !slices.Equal(got, want) {
		t.Errorf("states = %q, want %q", got, want)
	}
	daemon.Refuse("selectdata")
	if got, want := states(), []string{"excluded", "unusable", "unusable"}; !slices.Equal(got, want) {
		t.Errorf("states without selectdata = %q, want %q", got, want)
	}
}

func TestNTPStatusDoesNotAskAStoppedService(t *testing.T) {
	t.Parallel()
	daemon := chronyd(t)
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, skipped: true}, daemon))
	if !st.SetUp || st.Running || !st.HostClock || st.Read {
		t.Errorf("status = %+v", st)
	}
	if n := daemon.Requests(); n != 0 {
		t.Errorf("chronyd was asked %d times about a stopped service", n)
	}
}

func timeTile(t *testing.T, srv *httptest.Server) *ServiceState {
	t.Helper()
	for _, svc := range getOverview(t, srv).Services {
		if svc.Name == "NTP" {
			return &svc
		}
	}
	return nil
}

// Until the unit is there the distribution keeps the clock, which is no
// failure: the tile says what to run, and nothing warns.
func TestOverviewTimeTile(t *testing.T) {
	t.Parallel()
	unit := &ntpUnit{}
	srv := newNTPServer(t, unit, chronyd(t))
	tile := timeTile(t, srv)
	if tile == nil || tile.State != stateMissing || tile.Want || !strings.Contains(tile.Detail, "ostiole repair") {
		t.Fatalf("tile before setup = %+v", tile)
	}
	if w := warning(getOverview(t, srv), "service-down"); w != nil {
		t.Errorf("a router the distribution keeps the time for warned: %+v", w)
	}

	unit.set(func(u *ntpUnit) { u.installed, u.active = true, true })
	if tile := timeTile(t, srv); tile == nil || tile.State != stateActive || !tile.Want {
		t.Errorf("tile when running = %+v", tile)
	}

	// Stopped is a failure; declined in a container is not.
	unit.set(func(u *ntpUnit) { u.active = false })
	if w := warning(getOverview(t, srv), "service-down"); w == nil || !strings.Contains(w.Title, "NTP") {
		t.Errorf("a stopped time service did not warn: %+v", getOverview(t, srv).Warnings)
	}
	unit.set(func(u *ntpUnit) { u.skipped = true })
	if tile := timeTile(t, srv); tile == nil || tile.Want || !strings.Contains(tile.Detail, "host") {
		t.Errorf("tile in a container = %+v", tile)
	}
	if w := warning(getOverview(t, srv), "service-down"); w != nil {
		t.Errorf("a container warned about the host's clock: %+v", w)
	}
}

func TestOverviewWarnsAboutAnUnsynchronisedClock(t *testing.T) {
	t.Parallel()
	unit := &ntpUnit{installed: true, active: true, since: time.Now().Add(-2 * time.Minute)}
	daemon := chronyd(t)
	daemon.SetTracking(unsynchronised)
	srv := newNTPServer(t, unit, daemon)
	// A service that has just started is still finding its servers.
	if w := warning(getOverview(t, srv), "clock-unsynchronised"); w != nil {
		t.Errorf("warned two minutes after start: %+v", w)
	}

	unit.set(func(u *ntpUnit) { u.since = time.Now().Add(-11 * time.Minute) })
	w := warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || w.Level != "warn" || !strings.Contains(w.Detail, "Servers answer") || strings.Contains(w.Detail, "udp/123") {
		t.Fatalf("warning with servers answering = %+v", w)
	}

	daemon.SetSources(chronySource("192.0.2.123", "192.0.2.123", "unusable", 0, -1, nil))
	w = warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "udp/123") {
		t.Fatalf("warning with no server answering = %+v", w)
	}

	// Unsigned servers set the clock without a signed one beside them, so
	// when they answer and do not, they do not agree.
	signed := &chrony.Auth{Mode: "NTS", LastKE: 75 * time.Second, Cookies: 8}
	daemon.SetSources(
		chronySource("time.cloudflare.com", "162.159.200.123", "unusable", 0, -1, signed),
		chronySource("2.pool.ntp.org", "172.233.177.198", "unusable", 2, 30*time.Second, nil),
	)
	w = warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "Servers answer") {
		t.Fatalf("warning with only unsigned servers answering = %+v", w)
	}

	// Every server asked to sign failing points at NTS being blocked.
	failing := &chrony.Auth{Mode: "NTS", LastKE: -1, Attempts: 3}
	daemon.SetSources(
		chronySource("nts.netnod.se", "194.58.205.196", "unusable", 0, -1, failing),
		chronySource("nts.time.nl", "94.198.159.11", "unusable", 0, -1, failing),
		chronySource("192.0.2.123", "192.0.2.123", "unusable", 0, -1, nil),
	)
	w = warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "signed answer") || !strings.Contains(w.Detail, "tcp/4460") ||
		!strings.Contains(w.Detail, "nts.netnod.se, nts.time.nl") {
		t.Errorf("warning = %+v", w)
	}

	daemon.SetTracking(synced)
	if w := warning(getOverview(t, srv), "clock-unsynchronised"); w != nil {
		t.Errorf("a synchronised clock warned: %+v", w)
	}
}

// chronyd goes on calling the clock synchronised after every source has
// stopped answering, and once the local directive has it answer from its
// own clock it calls that synchronised too.
func TestOverviewWarnsWhenEverySourceIsLost(t *testing.T) {
	t.Parallel()
	unit := &ntpUnit{installed: true, active: true, since: time.Now().Add(-48 * time.Hour)}
	daemon := chronyd(t)
	lost := synced
	lost.RefTime = time.Now().Add(-lostAfter - time.Minute)
	daemon.SetTracking(lost)
	// The last answer is the one that set the clock; none came since.
	gone := chronySource("nts.example.net", "198.51.100.39", "selected", 2, lostAfter, nil)
	gone.Reach = 0
	daemon.SetSources(gone, chronySource("192.0.2.123", "192.0.2.123", "unusable", 0, -1, nil))
	srv := newNTPServer(t, unit, daemon)
	w := warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "udp/123") || strings.Contains(w.Detail, "own clock") {
		t.Errorf("warning with the last correction over %v old = %+v", lostAfter, w)
	}
	// The page says when a source last set the clock, and names none.
	if st := getNTPStatus(t, srv); st.Synchronised || st.Reference != "" || st.LastUpdate == nil {
		t.Errorf("status = %+v", st)
	}

	daemon.SetTracking(chrony.Tracking{Local: true, Stratum: 10, RefTime: time.Now(), Leap: "Normal"})
	w = warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "own clock") {
		t.Errorf("warning on its own clock = %+v", w)
	}
	// Its own clock is stamped with the time of asking, which is no update.
	if st := getNTPStatus(t, srv); st.Synchronised || st.LastUpdate != nil || st.Stratum != 10 {
		t.Errorf("status on its own clock = %+v", st)
	}
}

// With no signed server to be had the unsigned ones keep the clock, and
// the dashboard says so without sending a notice.
func TestOverviewSaysWhenTheClockFollowsUnsignedServers(t *testing.T) {
	t.Parallel()
	unit := &ntpUnit{installed: true, active: true, since: time.Now().Add(-11 * time.Minute)}
	daemon := chronyd(t)
	srv := newNTPServer(t, unit, daemon)
	if w := warning(getOverview(t, srv), "clock-unsigned"); w != nil {
		t.Errorf("warned while a signed server sets the clock: %+v", w)
	}

	signed := &chrony.Auth{Mode: "NTS", LastKE: 75 * time.Second, Cookies: 8}
	failing := &chrony.Auth{Mode: "NTS", LastKE: -1, Attempts: 3}
	pool := chronySource("2.pool.ntp.org", "198.51.100.39", "selected", 2, 30*time.Second, nil)
	for _, tc := range []struct {
		name   string
		cf     chronytest.Source
		detail string
	}{
		{"silent", chronySource("time.cloudflare.com", "162.159.200.123", "unusable", 0, -1, signed), "No signed server can be used"},
		{"failing", chronySource("time.cloudflare.com", "162.159.200.123", "unusable", 0, -1, failing), "tcp/4460"},
		{"standby", chronySource("time.cloudflare.com", "162.159.200.123", "excluded", 3, 30*time.Second, signed), ""},
	} {
		daemon.SetSources(tc.cf, pool)
		w := warning(getOverview(t, srv), "clock-unsigned")
		switch {
		case tc.detail == "" && w != nil:
			t.Errorf("%s: warned while a signed server bounds the clock: %+v", tc.name, w)
		case tc.detail != "" && (w == nil || w.Level != "info" || !strings.Contains(w.Detail, tc.detail)):
			t.Errorf("%s: warning = %+v", tc.name, w)
		}
	}

	// Unsigned servers alone are what was asked for.
	daemon.SetSources(pool)
	if w := warning(getOverview(t, srv), "clock-unsigned"); w != nil {
		t.Errorf("warned with no signed server listed: %+v", w)
	}
}
