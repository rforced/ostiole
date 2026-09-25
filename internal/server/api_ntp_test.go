package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/services"
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

// chronyStub answers like chronyc from the captures the chrony package
// keeps. An older build answers authdata and serverstats with 501.
type chronyStub struct {
	mu       sync.Mutex
	calls    int
	tracking string // a file in the chrony package's testdata
	auth     string // authdata rows; empty reads the capture
	older    bool
}

func (c *chronyStub) set(change func(c *chronyStub)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	change(c)
}

func (c *chronyStub) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	names, command := args[2], args[4]
	if c.older && (command == "authdata" || command == "serverstats") {
		return []byte("501 Not authorised\n"), errors.New("exit status 1")
	}
	file := map[string]string{
		"tracking -n": c.tracking, "sources -n": "sources-n.csv", "sources -N": "sources-N.csv",
		"authdata -n": "authdata-n.csv", "authdata -N": "authdata-N.csv", "serverstats -n": "serverstats.csv",
	}[command+" "+names]
	if command == "authdata" && c.auth != "" {
		return []byte(c.auth), nil
	}
	return os.ReadFile(filepath.Join("..", "chrony", "testdata", file))
}

func (c *chronyStub) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func newNTPServer(t *testing.T, unit *ntpUnit, stub *chronyStub) *httptest.Server {
	t.Helper()
	f := services.NTPFeatures{Version: chrony.Version{Major: 4, Minor: 9, NTS: true}}
	return newTestServerWith(t, func(d *Deps) {
		d.NTP = &services.NTP{Cmd: unit, Features: &f}
		d.Chrony = &chrony.Client{Bin: "chronyc", Run: stub.run}
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

// Before `ostiole repair` writes the unit, the page says so, and chronyc
// is not asked: the daemon it would reach is the distribution's.
func TestNTPStatusBeforeSetUp(t *testing.T) {
	t.Parallel()
	stub := &chronyStub{tracking: "tracking.csv"}
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{}, stub))
	if st.SetUp || st.Running || st.Read || st.Sources == nil || len(st.Sources) != 0 {
		t.Errorf("status = %+v", st)
	}
	// The page shows the list a router with none of its own would use.
	if !slices.Equal(st.Defaults, model.DefaultNTPServers) {
		t.Errorf("defaults = %+v", st.Defaults)
	}
	if stub.count() != 0 {
		t.Errorf("chronyc was asked %d times about a service that is not ours", stub.count())
	}
}

func TestNTPStatusReadsTheService(t *testing.T) {
	t.Parallel()
	stub := &chronyStub{tracking: "tracking.csv"}
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, active: true}, stub))
	if !st.SetUp || !st.Running || !st.Read || !st.Synchronised || st.Version != "4.9" {
		t.Fatalf("status = %+v", st)
	}
	// The reference goes by the name it was configured with.
	if st.Reference != "virginia.time.system76.com" || st.Stratum != 3 || st.LastUpdate == nil {
		t.Errorf("reference = %q stratum %d at %v", st.Reference, st.Stratum, st.LastUpdate)
	}
	if st.OffsetSeconds >= 0 {
		t.Errorf("offset = %v; chronyc said slow, which is negative here", st.OffsetSeconds)
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

// Debian 13's chronyd cannot open authdata or serverstats to the command
// port. The rest is still read, and those parts are left out.
func TestNTPStatusOnAnOlderBuild(t *testing.T) {
	t.Parallel()
	stub := &chronyStub{tracking: "tracking.csv", older: true}
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, active: true}, stub))
	if !st.Read || len(st.Sources) != 7 || st.Served != nil {
		t.Fatalf("status = %+v", st)
	}
	for _, s := range st.Sources {
		if s.NTS != "" {
			t.Errorf("%s nts = %q with no way to know", s.Name, s.NTS)
		}
	}
}

func TestNTPStatusDoesNotAskAStoppedService(t *testing.T) {
	t.Parallel()
	stub := &chronyStub{tracking: "tracking.csv"}
	st := getNTPStatus(t, newNTPServer(t, &ntpUnit{installed: true, skipped: true}, stub))
	if !st.SetUp || st.Running || !st.HostClock || st.Read {
		t.Errorf("status = %+v", st)
	}
	if stub.count() != 0 {
		t.Errorf("chronyc was asked %d times about a stopped service", stub.count())
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
	srv := newNTPServer(t, unit, &chronyStub{tracking: "tracking.csv"})
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
	stub := &chronyStub{tracking: "tracking-unsynchronised.csv"}
	srv := newNTPServer(t, unit, stub)
	// A service that has just started is still finding its servers.
	if w := warning(getOverview(t, srv), "clock-unsynchronised"); w != nil {
		t.Errorf("warned two minutes after start: %+v", w)
	}

	unit.set(func(u *ntpUnit) { u.since = time.Now().Add(-11 * time.Minute) })
	w := warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || w.Level != "warn" || !strings.Contains(w.Detail, "udp/123") {
		t.Fatalf("warning = %+v", w)
	}

	// Every server asked to sign failing points at NTS being blocked.
	stub.set(func(c *chronyStub) {
		c.auth = "194.58.205.196,NTS,0,0,0,4294967295,3,0,0,0\n94.198.159.11,NTS,0,0,0,4294967295,3,0,0,0\n192.0.2.123,-,0,0,0,4294967295,0,0,0,0\n"
	})
	w = warning(getOverview(t, srv), "clock-unsynchronised")
	if w == nil || !strings.Contains(w.Detail, "signed answer") || !strings.Contains(w.Detail, "tcp/4460") {
		t.Errorf("warning = %+v", w)
	}

	stub.set(func(c *chronyStub) { c.tracking = "tracking.csv" })
	if w := warning(getOverview(t, srv), "clock-unsynchronised"); w != nil {
		t.Errorf("a synchronised clock warned: %+v", w)
	}
}
