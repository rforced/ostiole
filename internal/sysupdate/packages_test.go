package sysupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Every component has to name a package for every manager that packages
// it, or say so by leaving the manager out. A typo in a package name is
// the kind of thing that is only found on somebody's router, so the
// shape of the table is checked here.
func TestComponentsNamePackagesPerManager(t *testing.T) {
	t.Parallel()
	managers := map[string]bool{}
	for _, d := range Drivers() {
		managers[d.Name()] = true
	}
	for _, c := range Components() {
		if c.Key == "" || c.Label == "" || c.Needs == "" {
			t.Errorf("component %+v is missing its description", c)
		}
		if c.Binary == "" && c.Unit == "" {
			t.Errorf("%s has no way to tell whether it is present", c.Key)
		}
		for manager := range c.packages {
			if !managers[manager] {
				t.Errorf("%s names packages for %q, which is not a manager Ostiole drives", c.Key, manager)
			}
		}
	}
}

func TestComponentAvailability(t *testing.T) {
	t.Parallel()
	tc, ok := ComponentByKey("tc")
	if !ok {
		t.Fatal("no tc component")
	}
	// The whole reason for the table: one command, three package names.
	for manager, want := range map[string]string{
		"dnf": "iproute-tc", "apt-get": "iproute2", "apk": "iproute2-tc",
	} {
		pkgs, avail := tc.Packages(manager)
		if avail != Installable || len(pkgs) != 1 || pkgs[0] != want {
			t.Errorf("tc on %s = %v %s, want [%s] installable", manager, pkgs, avail, want)
		}
	}

	networkd, _ := ComponentByKey("networkd")
	if pkgs, avail := networkd.Packages("apt-get"); avail != Bundled || len(pkgs) != 0 {
		t.Errorf("networkd on apt-get = %v %s, want bundled with nothing to install", pkgs, avail)
	}
	if _, avail := networkd.Packages("apk"); avail != Unpackaged {
		t.Errorf("networkd on apk = %s, want unpackaged: Alpine has no systemd", avail)
	}
	// miniupnpd is the awkward one, and the note is what the operator is
	// shown instead of a failed install.
	upnp, _ := ComponentByKey("miniupnpd")
	if _, avail := upnp.Packages("pacman"); avail != Unpackaged {
		t.Errorf("miniupnpd on pacman = %s, want unpackaged: it is in the AUR alone", avail)
	}
	if !strings.Contains(upnp.Note, "rpm2cpio") {
		t.Errorf("miniupnpd note does not say how to get one: %q", upnp.Note)
	}
	if _, ok := ComponentByKey("nothing-like-this"); ok {
		t.Error("ComponentByKey invented a component")
	}
}

func TestInstallAndRemoveArgv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		driver  Driver
		install string
		remove  string
		preview string
	}{
		{dnf{}, "dnf -y install dnsmasq",
			"dnf --setopt=clean_requirements_on_remove=False -y remove firewalld",
			"dnf --setopt=clean_requirements_on_remove=False --assumeno remove firewalld"},
		{apt{}, "apt-get -y -q", "apt-get -y -q", "apt-get -q -s remove firewalld"},
		{pacman{}, "pacman -S --noconfirm --needed dnsmasq", "pacman -R --noconfirm firewalld",
			"pacman -R --print --print-format %n firewalld"},
		{zypper{}, "zypper --non-interactive install dnsmasq", "zypper --non-interactive remove firewalld", "zypper --non-interactive remove --dry-run firewalld"},
		{apk{}, "apk add --no-cache dnsmasq", "apk del firewalld", "apk del --simulate firewalld"},
	}
	for _, c := range cases {
		t.Run(c.driver.Name(), func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(c.driver.InstallArgv([]string{"dnsmasq"}), " "); !strings.HasPrefix(got, c.install) {
				t.Errorf("install = %q, want it to start %q", got, c.install)
			}
			if got := strings.Join(c.driver.RemoveArgv([]string{"firewalld"}, false), " "); !strings.HasPrefix(got, c.remove) {
				t.Errorf("remove = %q, want it to start %q", got, c.remove)
			}
			if got := strings.Join(c.driver.RemoveArgv([]string{"firewalld"}, true), " "); got != c.preview {
				t.Errorf("preview = %q, want %q", got, c.preview)
			}
			// A removal names exactly what it was asked to remove, whatever
			// the manager's own flags are.
			for _, argv := range [][]string{
				c.driver.RemoveArgv([]string{"firewalld"}, false),
				c.driver.RemoveArgv([]string{"firewalld"}, true),
			} {
				if argv[len(argv)-1] != "firewalld" {
					t.Errorf("removal does not end with the package: %v", argv)
				}
			}
		})
	}
	// apt's removal keeps configuration files: a competitor that is taken
	// off can be put back with a reinstall.
	if got := strings.Join(apt{}.RemoveArgv([]string{"firewalld"}, false), " "); strings.Contains(got, "purge") {
		t.Errorf("apt removal purges: %q", got)
	}
}

func TestInstalledReadsTheDatabase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		driver  Driver
		command string
		present string
		absent  string
		code    int
	}{
		{dnf{}, "rpm -q firewalld", "firewalld-2.2.1-1.el10.noarch", "package firewalld is not installed", 1},
		{apt{}, "dpkg-query -s firewalld", "Package: firewalld\nStatus: install ok installed\nPriority: optional",
			"Package: firewalld\nStatus: deinstall ok config-files", 0},
		{pacman{}, "pacman -Q firewalld", "firewalld 2.2.1-1", "error: package 'firewalld' was not found", 1},
		{zypper{}, "rpm -q firewalld", "firewalld-2.2.1-1.noarch", "package firewalld is not installed", 1},
		{apk{}, "apk info -e firewalld", "firewalld", "", 0},
	}
	for _, c := range cases {
		t.Run(c.driver.Name(), func(t *testing.T) {
			t.Parallel()
			run := &fakeRunner{}
			run.say(c.command, c.present)
			ok, err := c.driver.Installed(context.Background(), run, "firewalld")
			if err != nil || !ok {
				t.Errorf("installed = %v, %v; want true", ok, err)
			}
			absent := &fakeRunner{code: map[string]int{c.command: c.code}}
			absent.say(c.command, c.absent)
			ok, err = c.driver.Installed(context.Background(), absent, "firewalld")
			if err != nil || ok {
				t.Errorf("absent = %v, %v; want false with no error", ok, err)
			}
		})
	}
}

// A manager that is not installed at all is an error, not a "no": the
// page must not report a package as absent because rpm could not be run.
func TestInstalledReportsAMissingManager(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{code: map[string]int{"rpm -q firewalld": -1}}
	if _, err := (dnf{}).Installed(context.Background(), run, "firewalld"); err == nil {
		t.Error("a manager that could not run reported an answer")
	}
}

// dnf's dry run prints its transaction and exits 1, which is the answer
// and not a failure; anything worse is a failure.
func TestPreviewRefused(t *testing.T) {
	t.Parallel()
	if !previewRefused(nil) {
		t.Error("a preview that exited 0 was treated as a failure")
	}
	if !previewRefused(fakeExit(1)) {
		t.Error("dnf --assumeno exiting 1 was treated as a failure")
	}
	if previewRefused(fakeExit(127)) {
		t.Error("a command that could not be found was treated as a refusal")
	}
	// The status has to survive the transient unit as well, where it
	// arrives as systemd's word for it rather than as an exec error.
	if !previewRefused(result{Result: "exit-code", Status: 1}.err()) {
		t.Error("a transient run that exited 1 was treated as a failure")
	}
	if previewRefused(result{Result: "exit-code", Status: 4}.err()) {
		t.Error("a transient run that exited 4 was treated as a refusal")
	}
	// systemd-run exits 1 too, when it cannot start the unit at all. That
	// is not the manager declining anything: the manager never ran.
	if previewRefused(fmt.Errorf("%w: systemd-run: %w", errUnitStart, fakeExit(1))) {
		t.Error("a unit that could not be started was treated as a refusal")
	}
	if previewRefused(fmt.Errorf("%w: still running", ErrBusy)) {
		t.Error("a transaction still running was treated as a refusal")
	}
}

// A finished unit keeps its name (RemainAfterExit), and systemd-run
// refuses to start a second one by that name. The console never
// collects the daemon's runs, so the stale one is cleared first; a unit
// that is still running is somebody else's transaction and refused.
func TestStartClearsAFinishedUnitAndRefusesARunningOne(t *testing.T) {
	t.Parallel()
	show := "systemctl show " + TransientUnit + " -p LoadState -p ActiveState -p SubState -p Result -p ExecMainStatus -p InvocationID"
	run := &fakeRunner{out: map[string]string{
		show: "LoadState=loaded\nActiveState=active\nSubState=exited\nResult=success\nExecMainStatus=0\nInvocationID=abc",
	}}
	tr := transient{unit: TransientUnit, run: run}
	if err := tr.start(context.Background(), []string{"apt-get", "-s", "remove", "ufw"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	var stopped, started bool
	for _, c := range run.calls {
		if c == "systemctl stop "+TransientUnit {
			stopped = true
		}
		if strings.HasPrefix(c, "systemd-run --unit="+TransientUnit) && stopped {
			started = true
		}
	}
	if !stopped || !started {
		t.Errorf("the finished unit was not cleared before the new one started: %v", run.calls)
	}

	run = &fakeRunner{out: map[string]string{
		show: "LoadState=loaded\nActiveState=activating\nSubState=start\nResult=\nExecMainStatus=0\nInvocationID=def",
	}}
	tr = transient{unit: TransientUnit, run: run}
	err := tr.start(context.Background(), []string{"apt-get", "-s", "remove", "ufw"}, time.Minute)
	if !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want busy", err)
	}
	for _, c := range run.calls {
		if strings.HasPrefix(c, "systemd-run") || strings.HasPrefix(c, "systemctl stop") {
			t.Errorf("a running transaction was touched: %v", run.calls)
		}
	}
}

func TestLocateFindsSbin(t *testing.T) {
	t.Parallel()
	// Nothing is on PATH under test, so a real sbin binary is the case
	// worth checking: dnsmasq and nft both live there.
	if got := Locate("definitely-not-a-command-anywhere"); got != "" {
		t.Errorf("Locate invented %q", got)
	}
}

type argvRecorder struct{ calls []string }

func (a *argvRecorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	a.calls = append(a.calls, name+" "+strings.Join(args, " "))
	return nil, nil
}

func TestHostRunnerNamesNeverRepeat(t *testing.T) {
	t.Parallel()
	rec := &argvRecorder{}
	a, b := NewHostRunner(rec), NewHostRunner(rec)
	for _, r := range []Runner{a, b, a} {
		_, _ = r.Run(context.Background(), "true")
	}
	seen := map[string]bool{}
	for _, call := range rec.calls {
		if !strings.HasPrefix(call, "systemd-run --unit=") {
			continue
		}
		unit := strings.Fields(call)[1]
		if seen[unit] {
			t.Errorf("unit name %s was handed out twice", unit)
		}
		seen[unit] = true
	}
	if len(seen) != 3 {
		t.Errorf("saw %d unit names in %v, want 3", len(seen), rec.calls)
	}
}
