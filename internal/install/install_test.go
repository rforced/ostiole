package install

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/network"
)

type fakeSystemctl struct {
	calls   [][]string
	enabled map[string]string
	active  map[string]string
}

// fakeRoutes stands in for the kernel's routing table, which a unit test
// on a workstation has no business reading.
type fakeRoutes struct{ swept bool }

func (f *fakeRoutes) Defaults() ([]network.DefaultRoute, error) { return nil, nil }

func (f *fakeRoutes) SweepStale(context.Context, []network.DefaultRoute, time.Duration) ([]network.DefaultRoute, error) {
	f.swept = true
	return nil, nil
}

func (f *fakeSystemctl) Run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	// Both take many units, as systemd does: list-unit-files leaves out a
	// unit with no file, and is-active answers a line for every unit.
	if len(args) > 3 && args[0] == "list-unit-files" {
		var lines []string
		for _, u := range args[3:] {
			if v, ok := f.enabled[u]; ok {
				lines = append(lines, u+" "+v+" enabled")
			}
		}
		if len(lines) == 0 {
			return "", errors.New("exit status 1")
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(args) > 2 && args[0] == "is-active" {
		var lines []string
		for _, u := range args[1:] {
			lines = append(lines, cmp.Or(f.active[u], "inactive"))
		}
		return strings.Join(lines, "\n"), nil
	}
	if len(args) == 2 {
		switch args[0] {
		case "cat":
			if f.enabled[args[1]] == "" {
				return "No files found for " + args[1], os.ErrNotExist
			}
			return "[Unit]", nil
		case "is-enabled":
			v, ok := f.enabled[args[1]]
			if !ok {
				return "not-found", os.ErrNotExist
			}
			return v, nil
		case "is-active":
			return f.active[args[1]], nil
		}
	}
	return "", nil
}

func (f *fakeSystemctl) has(args ...string) bool {
	for _, c := range f.calls {
		if strings.Join(c, " ") == strings.Join(args, " ") {
			return true
		}
	}
	return false
}

func tempLayout(t *testing.T) Layout {
	t.Helper()
	root := t.TempDir()
	return Layout{BinDir: filepath.Join(root, "bin"), UnitDir: filepath.Join(root, "units"), ConfigDir: filepath.Join(root, "etc"),
		BackupDir: filepath.Join(root, "backups"), LogDir: filepath.Join(root, "log"),
		NetworkdConfDir: filepath.Join(root, "networkd.conf.d")}
}

func TestInstallAndUninstall(t *testing.T) {
	t.Parallel()
	lay := tempLayout(t)
	src := filepath.Join(t.TempDir(), "ostiole-src")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho fake\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	sc := &fakeSystemctl{
		enabled: map[string]string{"firewalld.service": "enabled", "NetworkManager.service": "enabled", "ufw.service": "masked"},
		active:  map[string]string{"firewalld.service": "active", "NetworkManager.service": "active", "ufw.service": "inactive"},
	}
	log := slog.New(slog.DiscardHandler)

	run := &fakeRunner{}
	sysctlFile := filepath.Join(t.TempDir(), "99-ostiole.conf")
	btFile := filepath.Join(t.TempDir(), "ostiole-bluetooth.conf")
	rep, err := Install(context.Background(), sc, lay,
		Options{Source: src, Listen: ":8443", Run: run, SysctlFile: sysctlFile, BluetoothFile: btFile}, log)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(sysctlFile); err != nil || !strings.Contains(string(raw), "ip_forward = 1") {
		t.Errorf("sysctl file = %q, %v", raw, err)
	}
	// A router has no use for Bluetooth, wireless or not.
	if raw, err := os.ReadFile(btFile); err != nil || !strings.Contains(string(raw), "install bluetooth /bin/false") {
		t.Errorf("bluetooth file = %q, %v", raw, err)
	}
	if rep.Bluetooth != btFile || !run.ran("modprobe", "-r") {
		t.Errorf("Bluetooth = %q, ran %v", rep.Bluetooth, run.calls)
	}
	// An install takes the clock to UTC along with everything else it
	// takes over, and runs nothing else on the router.
	if rep.Timezone != "UTC" {
		t.Errorf("Timezone = %q, want UTC", rep.Timezone)
	}
	if !run.ran("timedatectl", "set-timezone", "UTC") {
		t.Errorf("commands run = %v", run.calls)
	}
	// networkd would drop policy routing's rules on every reconfigure.
	if raw, err := os.ReadFile(filepath.Join(lay.NetworkdConfDir, NetworkdConfFile)); err != nil ||
		!strings.Contains(string(raw), "ManageForeignRoutingPolicyRules=no") {
		t.Errorf("networkd drop-in = %q, %v", raw, err)
	}
	if info, err := os.Stat(lay.Binary()); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("binary = %v, %v", info, err)
	}
	if info, err := os.Stat(lay.ConfigDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir = %v, %v", info, err)
	}
	if info, err := os.Stat(lay.BackupDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup dir = %v, %v", info, err)
	}
	for _, u := range []string{FirewallUnit, DaemonUnit} {
		raw, err := os.ReadFile(filepath.Join(lay.UnitDir, u))
		if err != nil {
			t.Fatalf("%s missing: %v", u, err)
		}
		if !strings.Contains(string(raw), lay.Binary()) || !strings.Contains(string(raw), lay.ConfigDir) {
			t.Errorf("%s does not reference layout:\n%s", u, raw)
		}
	}
	daemon, _ := os.ReadFile(filepath.Join(lay.UnitDir, DaemonUnit))
	if !strings.Contains(string(daemon), "--network-backend auto") || !strings.Contains(string(daemon), "--listen :8443") {
		t.Errorf("daemon unit flags wrong:\n%s", daemon)
	}
	if !sc.has("daemon-reload") || !sc.has("enable", FirewallUnit) || !sc.has("enable", "--now", DaemonUnit) {
		t.Errorf("systemctl calls = %v", sc.calls)
	}
	comp, err := Competitors(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	conflicting := 0
	for _, c := range comp {
		if c.Conflicts() {
			conflicting++
		}
	}
	if len(comp) != 3 || conflicting != 2 {
		t.Errorf("competitors = %+v, want 3 with 2 conflicting (firewalld, NetworkManager)", comp)
	}

	// Re-install from the installed path is a no-op copy.
	if _, err := Install(context.Background(), sc, lay, Options{Source: lay.Binary(), Run: &fakeRunner{}, SysctlFile: "-", Timezone: "-", BluetoothFile: "-"}, log); err != nil {
		t.Fatal(err)
	}

	// systemd makes the log directory, and the daemon writes it.
	if err := os.MkdirAll(filepath.Join(lay.LogDir, "firewall"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background(), sc, lay, true, log); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(lay.UnitDir, DaemonUnit), filepath.Join(lay.UnitDir, FirewallUnit), lay.Binary(), lay.ConfigDir, lay.LogDir,
		filepath.Join(lay.NetworkdConfDir, NetworkdConfFile)} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists after purge", p)
		}
	}
	// The backups are what a rebuilt router is restored from.
	if _, err := os.Stat(lay.BackupDir); err != nil {
		t.Errorf("purge removed the backups: %v", err)
	}
	if !sc.has("disable", "--now", DaemonUnit) {
		t.Errorf("daemon not disabled: %v", sc.calls)
	}
}

func TestUnits(t *testing.T) {
	t.Parallel()
	units := Units(DefaultLayout(), Options{Listen: ":443"})
	d := units[DaemonUnit]
	// The firewall unit must not wait for sysinit.target: cloud-init's
	// network stage runs before sysinit and waits for networkd, which
	// waits for network-pre.target, which waits for this unit.
	for _, want := range []string{"DefaultDependencies=no", "Before=network-pre.target shutdown.target", "RequiresMountsFor=/etc/ostiole"} {
		if !strings.Contains(units[FirewallUnit], want) {
			t.Errorf("firewall unit lacks %q:\n%s", want, units[FirewallUnit])
		}
	}
	if !strings.Contains(d, "--network-backend auto") || !strings.Contains(d, "ReadWritePaths=/etc/ostiole /etc/systemd/network /usr/local/bin -/etc/dnsmasq.d -/etc/unbound -/etc/resolv.conf -/etc/ppp -/etc/miniupnpd -/etc/chrony -/etc/ssh/sshd_config.d -/etc/cloud/cloud.cfg.d -/etc/systemd/journald.conf.d") {
		t.Errorf("daemon unit:\n%s", d)
	}
	// Backup crons write here, and nowhere else outside /etc.
	if !strings.Contains(d, " -/var/backups/ostiole\n") {
		t.Errorf("daemon unit does not open the backup directory:\n%s", d)
	}
	// systemd makes the log files' directory and opens it in the sandbox,
	// where a ReadWritePaths entry would bind only if it already existed.
	if !strings.Contains(d, "\nLogsDirectory=ostiole\nLogsDirectoryMode=0700\n") {
		t.Errorf("daemon unit has no log directory:\n%s", d)
	}
	f := units[FirewallUnit]
	// A missing ruleset must not skip the unit: `load` puts the fallback in.
	if !strings.Contains(f, "Before=network-pre.target") || strings.Contains(f, "ConditionPathExists") {
		t.Errorf("firewall unit:\n%s", f)
	}
}

func TestTakeover(t *testing.T) {
	t.Parallel()
	sc := &fakeSystemctl{}
	if err := Takeover(context.Background(), sc, []string{"firewalld", "ufw"}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	// Masked and stopped first, in one call that goes through systemd
	// itself; the disable is the tidy-up that can fail on a unit with an
	// init script, and it comes after.
	want := [][]string{
		{"mask", "--now", "firewalld.service"}, {"disable", "firewalld.service"},
		{"mask", "--now", "ufw.service"}, {"disable", "ufw.service"},
	}
	for i, w := range want {
		if strings.Join(sc.calls[i], " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v, want %v (all: %v)", i, sc.calls[i], w, sc.calls)
		}
	}
	// A socket or a timer is named with its suffix and taken as it is.
	sc = &fakeSystemctl{}
	if err := Takeover(context.Background(), sc, []string{"snapd.socket", "apt-daily.timer"}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if !sc.has("mask", "--now", "snapd.socket") || !sc.has("mask", "--now", "apt-daily.timer") {
		t.Errorf("calls = %v", sc.calls)
	}
}

type fakeRunner struct {
	calls [][]string
	after func()
}

func (f *fakeRunner) ran(parts ...string) bool {
	for _, c := range f.calls {
		if len(c) >= len(parts) && strings.Join(c[:len(parts)], " ") == strings.Join(parts, " ") {
			return true
		}
	}
	return false
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.after != nil {
		f.after()
	}
	return nil, nil
}

func TestNetworkTakeover(t *testing.T) {
	t.Parallel()
	sc := &fakeSystemctl{}
	if err := NetworkTakeover(context.Background(), sc, []string{"NetworkManager", "NetworkManager-wait-online"}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mask", "--now", "NetworkManager.service"}, {"disable", "NetworkManager.service"},
		{"mask", "--now", "NetworkManager-wait-online.service"}, {"disable", "NetworkManager-wait-online.service"},
		{"enable", "--now", NetworkdSocket, NetworkdUnit},
	}
	for i, w := range want {
		if strings.Join(sc.calls[i], " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v, want %v (all: %v)", i, sc.calls[i], w, sc.calls)
		}
	}
}

// The install script removes the old manager after the handover, so a
// revert that fires late has nothing to bring back and must not take
// networkd down with it.
func TestNetworkRevertKeepsNetworkdWhenTheOldManagerIsGone(t *testing.T) {
	t.Parallel()
	sc := &fakeSystemctl{}
	sweeper := &fakeRoutes{}
	err := NetworkRevert(context.Background(), sc, sweeper, []string{"NetworkManager"}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if sc.has("disable", "--now", NetworkdSocket, NetworkdUnit) || sweeper.swept {
		t.Errorf("networkd was stopped with nothing to replace it: %v", sc.calls)
	}
	if got := Restorable(context.Background(), sc, nil); len(got) != 0 {
		t.Errorf("Restorable(nil) = %v", got)
	}
}

func TestNetworkRevertAndRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if rec, err := LoadTakeoverRecord(dir); err != nil || rec != nil {
		t.Fatalf("empty record = %v, %v", rec, err)
	}
	if err := SaveTakeoverRecord(dir, TakeoverRecord{Managers: []string{"NetworkManager", "NetworkManager-wait-online"}}); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadTakeoverRecord(dir)
	if err != nil || rec == nil || len(rec.Managers) != 2 {
		t.Fatalf("record = %v, %v", rec, err)
	}
	sc := &fakeSystemctl{enabled: map[string]string{
		"NetworkManager.service": "enabled", "NetworkManager-wait-online.service": "enabled",
	}}
	sweeper := &fakeRoutes{}
	if err := NetworkRevert(context.Background(), sc, sweeper, rec.Managers, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if !sweeper.swept {
		t.Error("the revert never swept the routes networkd left behind")
	}
	want := [][]string{
		{"cat", "NetworkManager.service"}, {"cat", "NetworkManager-wait-online.service"},
		{"disable", "--now", NetworkdSocket, NetworkdUnit},
		{"unmask", "NetworkManager.service"}, {"enable", "--now", "NetworkManager.service"},
		{"unmask", "NetworkManager-wait-online.service"}, {"enable", "NetworkManager-wait-online.service"},
	}
	for i, w := range want {
		if strings.Join(sc.calls[i], " ") != strings.Join(w, " ") {
			t.Fatalf("call %d = %v, want %v", i, sc.calls[i], w)
		}
	}
	run := &fakeRunner{}
	if err := ScheduleNetworkRevert(context.Background(), run, "/usr/local/bin/ostiole", "/etc/ostiole", 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(run.calls[len(run.calls)-1], " ")
	if !strings.Contains(last, "systemd-run") || !strings.Contains(last, "--on-active=180") || !strings.Contains(last, "AccuracySec=1s") || !strings.HasSuffix(last, "takeover --network --revert --in-unit") {
		t.Errorf("systemd-run call = %q", last)
	}
	if CancelNetworkRevert(context.Background(), &fakeRunner{}) {
		t.Error("cancel reported an armed timer on a fake that never arms one")
	}
}

func TestServiceBinaryPrefersInstalled(t *testing.T) {
	// Not parallel, and it empties the list of system bin directories:
	// this test is about a router with no installed copy, and on one that
	// has Ostiole in /usr/local/bin the real list would answer for it.
	saved := SystemBinDirs
	SystemBinDirs = nil
	t.Cleanup(func() { SystemBinDirs = saved })

	lay := tempLayout(t)
	self, _ := os.Executable()
	if got, err := ServiceBinary(lay); err != nil || got != self {
		t.Fatalf("without install: %q, %v; want the running executable %q", got, err, self)
	}
	if err := os.MkdirAll(lay.BinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lay.Binary(), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ServiceBinary(lay); err != nil || got != lay.Binary() {
		t.Fatalf("with install: %q, %v; want %q", got, err, lay.Binary())
	}
}

func TestInSystemBinDir(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]bool{"/usr/bin/ostiole": true, "/usr/local/bin/ostiole": true, "/root/ostiole": false, "/tmp/x/ostiole": false} {
		if got := InSystemBinDir(path); got != want {
			t.Errorf("InSystemBinDir(%q) = %v", path, got)
		}
	}
}

// An install that is not told where to listen keeps the unit's address:
// the updater runs `ostiole install` with no flags, and moving the UI under
// its operator would lock them out. With no unit yet, it takes the default.
func TestInstallKeepsWhereTheUIListens(t *testing.T) {
	t.Parallel()
	lay := tempLayout(t)
	src := filepath.Join(t.TempDir(), "ostiole-src")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho fake\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	opts := func(listen string) Options {
		return Options{Source: src, Listen: listen, Run: &fakeRunner{}, SysctlFile: "-", Timezone: "-", BluetoothFile: "-"}
	}
	install := func(o Options) {
		t.Helper()
		if _, err := Install(context.Background(), &fakeSystemctl{}, lay, o, slog.New(slog.DiscardHandler)); err != nil {
			t.Fatal(err)
		}
	}
	install(opts(""))
	if got := Listen(lay); got != DefaultListen || got != ":9443" {
		t.Errorf("a first install listens on %q, want :9443", got)
	}
	install(opts(":8443"))
	install(opts(""))
	if got := Listen(lay); got != ":8443" {
		t.Errorf("a reinstall moved the UI to %q, want :8443", got)
	}
}

// scriptedSystemctl answers each call with what a router's systemctl said.
type scriptedSystemctl struct {
	calls  [][]string
	answer func(args []string) (string, error)
}

func (s *scriptedSystemctl) Run(_ context.Context, args ...string) (string, error) {
	s.calls = append(s.calls, args)
	return s.answer(args)
}

// The dashboard asks about some thirty units. Two calls answer them all,
// in the shapes systemd 259 gave on the router; a template is only looked
// for on disk, since is-active refuses the whole call over one.
func TestReadUnitsAsksAboutEveryUnitInTwoCalls(t *testing.T) {
	t.Parallel()
	sc := &scriptedSystemctl{answer: func(args []string) (string, error) {
		switch args[0] {
		case "list-unit-files":
			return "firewalld.service          masked   enabled\n" +
				"ostiole-dnsmasq.service    enabled  disabled\n" +
				"ostiole-hostapd@.service   disabled disabled", nil
		case "is-active":
			return "active\ninactive\ninactive", errors.New("exit status 3")
		}
		return "", errors.New("unexpected call")
	}}
	u := ReadUnits(t.Context(), sc, "ostiole-dnsmasq.service", "firewalld.service", "ufw.service",
		"ostiole-hostapd@.service", "firewalld.service")
	want := [][]string{
		{"list-unit-files", "--no-legend", "--plain", "ostiole-dnsmasq.service", "firewalld.service", "ufw.service", "ostiole-hostapd@.service"},
		{"is-active", "ostiole-dnsmasq.service", "firewalld.service", "ufw.service"},
	}
	if !slices.EqualFunc(sc.calls, want, slices.Equal) {
		t.Fatalf("calls = %q, want %q", sc.calls, want)
	}
	if !u.Read() {
		t.Error("the answer was not taken as read")
	}
	for unit, file := range map[string]string{"ostiole-dnsmasq.service": "enabled", "firewalld.service": "masked", "ostiole-hostapd@.service": "disabled"} {
		if got, ok := u.File(unit); !ok || got != file {
			t.Errorf("File(%s) = %q, %v, want %q", unit, got, ok, file)
		}
	}
	if _, ok := u.File("ufw.service"); ok {
		t.Error("ufw has a file")
	}
	if u.Active("ostiole-dnsmasq.service") != "active" || u.Active("firewalld.service") != "inactive" || u.Active("ostiole-hostapd@.service") != "" {
		t.Errorf("active = %v", u.active)
	}
}

// Anything besides a state in is-active's answer, a warning say, and the
// units are asked one at a time. A systemctl that cannot reach systemd
// leaves the states unread, where no unit file must not read as missing.
func TestReadUnitsFallsBackAndSaysWhenItCouldNotRead(t *testing.T) {
	t.Parallel()
	sc := &scriptedSystemctl{answer: func(args []string) (string, error) {
		switch {
		case args[0] == "list-unit-files":
			return "", errors.New("exit status 1")
		case args[0] == "is-active" && len(args) > 2:
			return "Warning: the unit file changed on disk\nactive\nfailed", errors.New("exit status 3")
		case args[0] == "is-active" && args[1] == "a.service":
			return "active", nil
		}
		return "failed", errors.New("exit status 3")
	}}
	u := ReadUnits(t.Context(), sc, "a.service", "b.service")
	if len(sc.calls) != 4 || u.Active("a.service") != "active" || u.Active("b.service") != "failed" {
		t.Errorf("calls = %q, active = %v", sc.calls, u.active)
	}
	if !u.Read() {
		t.Error("no unit with a file was taken for no answer")
	}

	down := &scriptedSystemctl{answer: func([]string) (string, error) {
		return "System has not been booted with systemd as init system (PID 1). Can't operate.", errors.New("exit status 1")
	}}
	if u := ReadUnits(t.Context(), down, "a.service"); u.Read() {
		t.Error("a systemctl that could not reach systemd was taken as an answer")
	}
}

// A configuration directory that was there before is closed to everybody
// else, and so is each directory in it, whatever mode it had; the way
// through that the proxy's group has stays, and nothing past a link moves.
func TestInstallClosesTheConfigurationToOthers(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "ostiole")
	outside := t.TempDir()
	for path, mode := range map[string]os.FileMode{dir: 0o755, filepath.Join(dir, "wireless"): 0o755,
		filepath.Join(dir, "proxy"): 0o750, filepath.Join(dir, "certs"): 0o700} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := closeToOthers(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o750, filepath.Join(dir, "wireless"): 0o750,
		filepath.Join(dir, "proxy"): 0o750, filepath.Join(dir, "certs"): 0o700, outside: 0o755} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v, want %v", path, info.Mode().Perm(), err, want)
		}
	}
}
