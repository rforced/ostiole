package services

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/chrony"
	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/network"
)

var (
	chrony49  = NTPFeatures{Version: chrony.Version{Major: 4, Minor: 9, NTS: true}, LeapList: true}
	chrony461 = NTPFeatures{Version: chrony.Version{Major: 4, Minor: 6, Patch: 1, NTS: true}, LeapList: true}
)

// The configuration is golden-tested for the fixture that sets servers
// and serving, on the newest build and on Debian 13's, and for one with no
// time block at all, which gets the defaults.
func TestRenderNTPGolden(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture, golden string
		features        NTPFeatures
	}{
		{"full", "full.chrony", chrony49},
		{"ntp", "ntp.chrony", chrony49},
		{"ntp", "ntp-4.6.chrony", chrony461},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, filepath.Join("testdata", tc.fixture+".json"))
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			got := renderNTP(cfg, tc.features)
			golden := filepath.Join("testdata", tc.golden)
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

// A router that has no chronyd, or one that would not say what it is,
// gets nothing a build might not know, and 4.5 (Ubuntu 24.04) nothing
// newer than it. The list of leap seconds is on disk either way.
func TestRenderNTPForAnOldOrUnknownBuild(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/ntp.json")
	for _, f := range []NTPFeatures{
		{LeapList: true},
		{Version: chrony.Version{Major: 4, Minor: 5, NTS: true}, LeapList: true},
	} {
		got := renderNTP(cfg, f)
		for _, line := range []string{"opencommands", "local stratum", "leapseclist"} {
			if strings.Contains(got, line) {
				t.Errorf("build %q was given %s:\n%s", f.Version, line, got)
			}
		}
		if !strings.Contains(got, "allow all\n") {
			t.Errorf("serving was dropped:\n%s", got)
		}
	}
}

func TestRenderNTPWithoutServingOpensNothing(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/ntp.json")
	cfg.Services.NTP.Serve = false
	got := renderNTP(cfg, chrony49)
	if strings.Contains(got, "allow") || strings.Contains(got, "local ") {
		t.Errorf("a router that serves nobody answers on udp/123:\n%s", got)
	}
}

// One or two servers cannot outvote each other, and with minsources 2 a
// single server would never set the clock at all.
func TestRenderNTPMinSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		servers []model.NTPServer
		want    bool
	}{
		{[]model.NTPServer{{Host: "a.example"}}, false},
		{[]model.NTPServer{{Host: "a.example"}, {Host: "b.example"}}, false},
		{[]model.NTPServer{{Host: "a.example"}, {Host: "b.example"}, {Host: "c.example"}}, true},
		{[]model.NTPServer{{Host: "pool.ntp.org", Pool: true}}, true},
	} {
		cfg := &model.Config{}
		cfg.Services.NTP.Servers = tc.servers
		if got := strings.Contains(renderNTP(cfg, chrony49), "minsources 2\n"); got != tc.want {
			t.Errorf("%+v: minsources = %v, want %v", tc.servers, got, tc.want)
		}
	}
}

// ntpCmd answers like systemctl and chronyd on a router with the given
// other time services.
type ntpCmd struct {
	calls     [][]string
	installed bool
	active    bool
	skipped   bool
	// units is what `systemctl show --property=Id,LoadState` prints for
	// the competitors.
	units string
	// check is what `chronyd -p` answers.
	check func(conf string) ([]byte, error)
}

func (f *ntpCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "chronyd" {
		if f.check == nil {
			return nil, nil
		}
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		return f.check(string(raw))
	}
	switch args[0] {
	case "cat":
		if f.installed {
			return []byte("[Unit]"), nil
		}
		return nil, errors.New("no such unit")
	case "is-active":
		if f.active {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	case "show":
		if args[1] == "--property=ConditionResult" {
			if f.skipped {
				return []byte("no\n"), nil
			}
			return []byte("yes\n"), nil
		}
		return []byte(f.units), nil
	case "enable", "restart":
		f.active = !f.skipped
	}
	return nil, nil
}

func (f *ntpCmd) ran(parts ...string) bool {
	for _, c := range f.calls {
		if strings.Join(c, " ") == strings.Join(parts, " ") {
			return true
		}
	}
	return false
}

func (f *ntpCmd) count(verb string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) > 1 && c[0] == "systemctl" && c[1] == verb {
			n++
		}
	}
	return n
}

func newTestNTP(t *testing.T, cmd *ntpCmd) *NTP {
	t.Helper()
	f := chrony49
	return &NTP{Dir: t.TempDir(), Cmd: cmd, Binary: "chronyd", Features: &f}
}

func ntpFiles(t *testing.T, n *NTP, cfg *model.Config) network.Files {
	t.Helper()
	files, err := n.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Until `ostiole repair` writes the unit, the distribution's time service
// keeps the clock: nothing is written, masked or started.
func TestNTPApplyIsInertUntilSetUp(t *testing.T) {
	t.Parallel()
	cmd := &ntpCmd{units: "Id=chronyd.service\nLoadState=loaded\n"}
	n := newTestNTP(t, cmd)
	if err := n.Apply(context.Background(), ntpFiles(t, n, &model.Config{})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(n.ConfPath()); !os.IsNotExist(err) {
		t.Errorf("configuration written on a router that is not set up: %v", err)
	}
	for _, c := range cmd.calls {
		if c[1] != "cat" {
			t.Errorf("ran %v on a router that is not set up", c)
		}
	}
}

func TestNTPApplyTakesTheClockOver(t *testing.T) {
	t.Parallel()
	// Fedora: chronyd runs, timesyncd is there too, the rest are not.
	cmd := &ntpCmd{installed: true, units: strings.Join([]string{
		"Id=chronyd.service\nLoadState=loaded",
		"Id=chrony.service\nLoadState=not-found",
		"Id=chronyd-restricted.service\nLoadState=loaded",
		"Id=systemd-timesyncd.service\nLoadState=masked",
		"Id=ntpd.service\nLoadState=not-found",
		"Id=ntpsec.service\nLoadState=not-found",
		"Id=openntpd.service\nLoadState=not-found",
		"Id=ntpd-rs.service\nLoadState=not-found",
	}, "\n\n") + "\n"}
	n := newTestNTP(t, cmd)
	files := ntpFiles(t, n, &model.Config{})
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(n.ConfPath())
	if err != nil || string(raw) != files[ntpConfName] {
		t.Fatalf("configuration not written: %v", err)
	}
	if !cmd.ran("systemctl", "mask", "--now", "chronyd.service") ||
		!cmd.ran("systemctl", "mask", "--now", "chronyd-restricted.service") {
		t.Errorf("the distribution's chronyd was left running: %v", cmd.calls)
	}
	if cmd.count("mask") != 2 {
		t.Errorf("masked %d units, want 2 (not the missing or already masked ones): %v", cmd.count("mask"), cmd.calls)
	}
	// Disabling a unit with an init script fails inside the daemon's
	// sandbox, and masking is enough.
	if cmd.count("disable") != 0 {
		t.Errorf("disabled a unit: %v", cmd.calls)
	}
	if !cmd.ran("systemctl", "enable", "--now", NTPUnit) {
		t.Errorf("the time service was not started: %v", cmd.calls)
	}
	// A unit that was not running has just been started on the new file.
	if cmd.count("restart") != 0 {
		t.Errorf("restarted a unit that was just started: %v", cmd.calls)
	}
}

// Debian's chronyd.service is an alias of chrony.service: both names lead
// to one unit, which is masked once, by its own name.
func TestNTPTakeOverMasksDebiansUnitByItsName(t *testing.T) {
	t.Parallel()
	cmd := &ntpCmd{installed: true, units: "Id=chrony.service\nLoadState=loaded\n\nId=chrony.service\nLoadState=loaded\n\nId=chronyd-restricted.service\nLoadState=not-found\n"}
	n := newTestNTP(t, cmd)
	if err := n.Apply(context.Background(), ntpFiles(t, n, &model.Config{})); err != nil {
		t.Fatal(err)
	}
	if !cmd.ran("systemctl", "mask", "--now", "chrony.service") || cmd.count("mask") != 1 {
		t.Errorf("want chrony.service masked once: %v", cmd.calls)
	}
}

func TestNTPApplyRestartsOnlyForAChange(t *testing.T) {
	t.Parallel()
	cmd := &ntpCmd{installed: true, active: true}
	n := newTestNTP(t, cmd)
	cfg := &model.Config{}
	if err := n.Apply(context.Background(), ntpFiles(t, n, cfg)); err != nil {
		t.Fatal(err)
	}
	if cmd.count("restart") != 1 {
		t.Fatalf("a new file did not restart the running unit: %v", cmd.calls)
	}
	if err := n.Apply(context.Background(), ntpFiles(t, n, cfg)); err != nil {
		t.Fatal(err)
	}
	if cmd.count("restart") != 1 {
		t.Errorf("an apply that changed nothing restarted the time service: %v", cmd.calls)
	}
	cfg.Services.NTP.Servers = []model.NTPServer{{Host: "time.example.lan"}}
	if err := n.Apply(context.Background(), ntpFiles(t, n, cfg)); err != nil {
		t.Fatal(err)
	}
	if cmd.count("restart") != 2 {
		t.Errorf("a changed server list did not restart the time service: %v", cmd.calls)
	}
}

func TestNTPPreflight(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	refused := "pool 2.pool.ntp.org iburst\nopencommandz tracking\n" +
		"2026-09-24T22:09:37Z Fatal error : Invalid directive opencommandz at line 2 in file /etc/chrony/.ostiole-check.conf\n"
	cmd := &ntpCmd{installed: true, check: func(string) ([]byte, error) {
		return []byte(refused), errors.New("exit status 1")
	}}
	n := newTestNTP(t, cmd)
	err := n.Preflight(ctx, ntpFiles(t, n, &model.Config{}))
	var pe *PreflightError
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "Invalid directive opencommandz at line 2") {
		t.Errorf("err = %v, want chronyd's reason", err)
	}
	if _, err := os.Stat(filepath.Join(n.Dir, ntpCheckName)); !os.IsNotExist(err) {
		t.Errorf("the file checked was left behind: %v", err)
	}

	// A configuration chronyd reads is let through, and what it read was
	// the rendered one.
	var read string
	cmd = &ntpCmd{installed: true, check: func(conf string) ([]byte, error) { read = conf; return nil, nil }}
	n = newTestNTP(t, cmd)
	files := ntpFiles(t, n, &model.Config{})
	if err := n.Preflight(ctx, files); err != nil {
		t.Errorf("err = %v", err)
	}
	if read != files[ntpConfName] {
		t.Errorf("chronyd read %q", read)
	}

	// A build without NTS cannot keep a server asked to sign. The
	// defaults are not asked to.
	cmd = &ntpCmd{installed: true}
	n = newTestNTP(t, cmd)
	n.Features = &NTPFeatures{Version: chrony.Version{Major: 4, Minor: 5}}
	signed := &model.Config{}
	signed.Services.NTP.Servers = []model.NTPServer{{Host: "nts.example", NTS: true}}
	if err := n.Preflight(ctx, ntpFiles(t, n, signed)); !errors.As(err, &pe) || !strings.Contains(pe.Message, "without NTS") {
		t.Errorf("err = %v, want the missing NTS named", err)
	}
	if err := n.Preflight(ctx, ntpFiles(t, n, &model.Config{})); err != nil {
		t.Errorf("a build without NTS refused the defaults: %v", err)
	}

	// Not set up: nothing to check against, and the apply leaves the
	// distribution's service alone anyway.
	cmd = &ntpCmd{check: func(string) ([]byte, error) { return nil, errors.New("must not run") }}
	n = newTestNTP(t, cmd)
	if err := n.Preflight(ctx, ntpFiles(t, n, &model.Config{})); err != nil {
		t.Errorf("err = %v", err)
	}
}

// At daemon start the clock is handed over once, and a unit already
// running is left alone.
func TestNTPStart(t *testing.T) {
	t.Parallel()
	cmd := &ntpCmd{installed: true, active: true}
	n := newTestNTP(t, cmd)
	if err := n.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if cmd.count("enable") != 0 || cmd.count("show") != 0 {
		t.Errorf("a running time service was touched: %v", cmd.calls)
	}

	cmd = &ntpCmd{installed: true, units: "Id=chronyd.service\nLoadState=loaded\n"}
	n = newTestNTP(t, cmd)
	if err := n.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !cmd.ran("systemctl", "mask", "--now", "chronyd.service") || !cmd.ran("systemctl", "enable", "--now", NTPUnit) {
		t.Errorf("the clock was not handed over: %v", cmd.calls)
	}
	raw, _ := os.ReadFile(n.ConfPath())
	if !strings.Contains(string(raw), "pool 2.pool.ntp.org iburst\n") {
		t.Errorf("a router with no configuration did not get the default servers:\n%s", raw)
	}
}

// In a container systemd declines to start the unit, and the host keeps
// the clock.
func TestNTPSkippedInAContainer(t *testing.T) {
	t.Parallel()
	cmd := &ntpCmd{installed: true, skipped: true}
	n := newTestNTP(t, cmd)
	if err := n.Start(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if n.Active(context.Background()) || !n.Skipped(context.Background()) {
		t.Errorf("active %v, skipped %v", n.Active(context.Background()), n.Skipped(context.Background()))
	}
}

func TestNTPUnitContent(t *testing.T) {
	t.Parallel()
	unit := NTPUnitContent("/usr/sbin/chronyd", "/etc/chrony/ostiole.conf")
	for _, want := range []string{
		"ExecStart=/usr/sbin/chronyd -n -f /etc/chrony/ostiole.conf\n",
		"ConditionCapability=CAP_SYS_TIME\n",
		"ConditionVirtualization=!container\n",
		"Conflicts=chronyd.service chrony.service chronyd-restricted.service systemd-timesyncd.service",
		"ReadWritePaths=/run /var/lib/chrony\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	// chronyd drops to the user its build names; the unit must not guess.
	if strings.Contains(unit, "User=") || strings.Contains(unit, " -u ") {
		t.Errorf("unit names a user:\n%s", unit)
	}
}

func TestSetupWritesTheTimeUnit(t *testing.T) {
	t.Parallel()
	unitDir, dir := t.TempDir(), t.TempDir()
	run := &fakeCmd{}
	o := SetupOptions{
		NTP: true, ChronydBinary: "/usr/sbin/chronyd", NTPBackend: &NTP{Dir: filepath.Join(dir, "chrony")},
		UnitDir: unitDir, Run: run, NoRestart: true,
	}
	if err := Setup(context.Background(), New(), o, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(unitDir, NTPUnit))
	if err != nil || !strings.Contains(string(raw), "-f "+filepath.Join(dir, "chrony", "ostiole.conf")) {
		t.Fatalf("unit = %q, %v", raw, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "chrony")); err != nil || !info.IsDir() {
		t.Errorf("chrony's directory was not made: %v", err)
	}
	// Nothing is masked here: the distribution keeps the time until ours
	// starts.
	for _, c := range run.calls {
		if len(c) > 1 && (c[1] == "mask" || c[1] == "disable") {
			t.Errorf("Setup ran %v", c)
		}
	}
}

// The chronyd this machine has, when it has one, reads what is rendered
// for it: a line a build does not know stops the daemon starting at all.
// The lines go to it as arguments rather than in a file: the AppArmor
// profile Debian and Ubuntu ship lets chronyd read configuration from
// /etc/chrony only. CI reads what the installer writes with every
// distribution's own build.
func TestChronydReadsWhatIsRenderedForIt(t *testing.T) {
	t.Parallel()
	bin := lookPath("chronyd")
	if bin == "" {
		t.Skip("no chronyd here")
	}
	n := &NTP{Binary: bin}
	f := n.features()
	for _, fixture := range []string{"full", "ntp"} {
		conf := renderNTP(loadConfig(t, filepath.Join("testdata", fixture+".json")), f)
		args := append([]string{"-p"}, strings.Split(strings.TrimSuffix(conf, "\n"), "\n")...)
		out, err := n.cmd().Run(context.Background(), bin, args...)
		if err != nil {
			t.Errorf("chronyd %s refuses %s as rendered for it: %s", f.Version, fixture, chronydFatal(string(out), err))
		}
	}
}

// chronyd is asked what it is once per binary: again after it failed to
// say, and again once a package update replaced it.
func TestNTPFeaturesAreAskedOncePerBinary(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "chronyd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	asked, fail := 0, true
	cmd := commanderFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != bin || len(args) != 1 || args[0] != "-v" {
			t.Fatalf("ran %s %v", name, args)
		}
		asked++
		if fail {
			return nil, errors.New("timed out")
		}
		return []byte("chronyd (chrony) version 4.6.1 (+CMDMON +NTS +IPV6)\n"), nil
	})
	n := &NTP{Binary: bin, Cmd: cmd}
	if v := n.Version(); v.Major != 0 || asked != 1 {
		t.Fatalf("version %v after %d asks", v, asked)
	}
	fail = false
	if v := n.Version(); v.String() != "4.6.1" || !v.NTS || asked != 2 {
		t.Fatalf("version %v after %d asks, want 4.6.1 asked again", v, asked)
	}
	n.Version()
	if asked != 2 {
		t.Errorf("asked %d times for one binary", asked)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	n.Version()
	if asked != 3 {
		t.Errorf("an upgraded binary was not asked again (%d asks)", asked)
	}
}

type commanderFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func (f commanderFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}
