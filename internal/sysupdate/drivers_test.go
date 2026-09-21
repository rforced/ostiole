package sysupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeExit is a command that ran and failed, the way exec reports it.
type fakeExit int

func (f fakeExit) Error() string { return fmt.Sprintf("exit status %d", int(f)) }
func (f fakeExit) ExitCode() int { return int(f) }

// fakeRunner answers the commands a driver runs from a table keyed by the
// whole command line, so a test reads as the transcript of a session.
type fakeRunner struct {
	// mu guards the lot: a reattached update reads these from its own
	// goroutine while the test changes what the router is saying.
	mu    sync.Mutex
	out   map[string]string
	code  map[string]int
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, line)
	// A command that stepped outside the sandbox is answered by what it
	// runs, so the tables below read as the commands they are about. The
	// transcript keeps the whole line, for the tests that care how it
	// got there.
	key := unwrapped(line)
	out := []byte(f.out[key])
	if code := f.code[key]; code != 0 {
		return out, fakeExit(code)
	}
	return out, nil
}

// unwrapped strips a `systemd-run … -- ` prefix.
func unwrapped(line string) string {
	if !strings.HasPrefix(line, "systemd-run ") {
		return line
	}
	_, cmd, ok := strings.Cut(line, " -- ")
	if !ok {
		return line
	}
	return cmd
}

// say makes the router answer a command with this output from now on.
func (f *fakeRunner) say(line, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.out == nil {
		f.out = map[string]string{}
	}
	f.out[line] = out
}

func (f *fakeRunner) ran(line string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == line {
			return true
		}
	}
	return false
}

func (f *fakeRunner) ranWithout(want, guard string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, want) && !strings.Contains(c, guard) {
			return c
		}
	}
	return ""
}

// ranMatching reports whether one command line contains all of these.
func (f *fakeRunner) ranMatching(parts ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(c, p)
		}
		if all {
			return true
		}
	}
	return false
}

func (f *fakeRunner) transcript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "\n")
}

func (f *fakeRunner) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func byName(pkgs []Package) map[string]Package {
	out := map[string]Package{}
	for _, p := range pkgs {
		out[p.Name] = p
	}
	return out
}

func TestDNFCheck(t *testing.T) {
	t.Parallel()
	d := dnf{}
	run := &fakeRunner{
		out: map[string]string{
			"dnf -q --setopt=*.countme=0 --refresh check-update":              fixture(t, "dnf-check-update.txt"),
			"dnf -q --setopt=*.countme=0 --cacheonly check-update --security": fixture(t, "dnf-check-update-security.txt"),
			// The version lookup asks for every pending package at once,
			// in the order check-update listed them.
			"rpm -q --qf %{NAME} %{EVR}\\n NetworkManager-libnm bash kernel kernel-core openssl-libs tzdata": fixture(t, "rpm-installed.txt"),
		},
		code: map[string]int{
			"dnf -q --setopt=*.countme=0 --refresh check-update":              dnfPending,
			"dnf -q --setopt=*.countme=0 --cacheonly check-update --security": dnfPending,
		},
	}

	got, err := d.Check(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 6 {
		t.Fatalf("parsed %d packages, want 6: %+v", len(got.Packages), got.Packages)
	}
	if got.Security != 3 {
		t.Errorf("security = %d, want 3", got.Security)
	}
	// Security fixes sort first, so the rows that matter are visible
	// without scrolling.
	for _, p := range got.Packages[:3] {
		if !p.Security {
			t.Errorf("%s is not marked security but sorted with them", p.Name)
		}
	}
	idx := byName(got.Packages)
	// A name too long for its column is printed on a line of its own.
	if wrapped := idx["NetworkManager-libnm"]; wrapped.To != "1:1.52.0-3.el10" || wrapped.Repo != "appstream" {
		t.Errorf("wrapped row = %+v", wrapped)
	}
	if k := idx["kernel"]; k.From != "6.12.0-50.el10" || k.To != "6.12.0-55.el10" {
		t.Errorf("kernel = %+v, want the installed version filled in", k)
	}
	if _, ok := idx["grub2-tools"]; ok {
		t.Error("obsoleted packages were parsed as upgrades")
	}
	if _, ok := idx["Last"]; ok {
		t.Error("the metadata expiration line was parsed as a package")
	}
	// tzdata is not installed, so rpm says so rather than a version.
	if tz := idx["tzdata"]; tz.From != "" {
		t.Errorf("tzdata from = %q, want empty", tz.From)
	}
}

func TestDNFUpgradeArgv(t *testing.T) {
	t.Parallel()
	d := dnf{}
	all := d.UpgradeArgv(false, nil, Pending{})
	if strings.Join(all, " ") != "dnf --setopt=*.countme=0 -y upgrade" {
		t.Errorf("all = %v", all)
	}
	sec := d.UpgradeArgv(true, []string{"kernel", "kernel-core"}, Pending{})
	if want := "dnf --setopt=*.countme=0 -y upgrade --security --exclude=kernel --exclude=kernel-core"; strings.Join(sec, " ") != want {
		t.Errorf("security = %v", sec)
	}
	// A glob is dnf's own spelling, so it is handed over untouched rather
	// than expanded here — where it would only match what is waiting.
	glob := d.UpgradeArgv(false, []string{"kernel*"}, Pending{})
	if want := "dnf --setopt=*.countme=0 -y upgrade --exclude=kernel*"; strings.Join(glob, " ") != want {
		t.Errorf("glob = %v", glob)
	}
}

// The never-upgrade list may hold globs. dnf and pacman match them
// themselves; for the managers that are told which packages to install,
// the matching happens here.
func TestExcludeMatchesGlobs(t *testing.T) {
	t.Parallel()
	pending := Pending{Packages: []Package{
		{Name: "kernel"}, {Name: "kernel-core"}, {Name: "kernel-modules"},
		{Name: "bash"}, {Name: "openssl-libs"}, {Name: "gcc-c++"},
	}}
	kept := names(keepWanted(pending.Packages, false, []string{"kernel*"}))
	if strings.Join(kept, " ") != "bash openssl-libs gcc-c++" {
		t.Errorf("kept %v, want every kernel package held back", kept)
	}
	// A single character, and a plain name, still mean what they did.
	if !excluded("gcc-c++", []string{"gcc-c++"}) {
		t.Error("a name with glob-free punctuation stopped matching itself")
	}
	if !excluded("kernel6", []string{"kernel?"}) {
		t.Error("? did not match one character")
	}
	if excluded("kernel-core", []string{"kernel"}) {
		t.Error("a plain name matched more than itself")
	}
	// Nothing is silently dropped: a pattern that will not compile is
	// compared as the literal it is.
	if !excluded("kernel[", []string{"kernel["}) {
		t.Error("a malformed pattern stopped matching itself")
	}
	// apt and zypper name what they install, so the glob decides there too.
	line := strings.Join(apt{}.UpgradeArgv(false, []string{"kernel*"}, pending), " ")
	if strings.Contains(line, "kernel") {
		t.Errorf("apt upgraded a kernel package anyway: %q", line)
	}
	line = strings.Join(zypper{}.UpgradeArgv(false, []string{"kernel*"}, pending), " ")
	if strings.Contains(line, "kernel") {
		t.Errorf("zypper upgraded a kernel package anyway: %q", line)
	}
}

func TestDNFRebootRequired(t *testing.T) {
	t.Parallel()
	d := dnf{}
	run := &fakeRunner{
		out:  map[string]string{"dnf --setopt=*.countme=0 needs-restarting -r": "Core libraries or services have been updated since boot-up:\n  * systemd\n\nReboot is required to fully utilize these updates."},
		code: map[string]int{"dnf --setopt=*.countme=0 needs-restarting -r": 1},
	}
	if need, why := d.RebootRequired(t.Context(), run); !need || why == "" {
		t.Errorf("RebootRequired = %v, %q", need, why)
	}

	clean := &fakeRunner{out: map[string]string{"dnf --setopt=*.countme=0 needs-restarting -r": "No core libraries or services have been updated since boot-up."}}
	if need, _ := d.RebootRequired(t.Context(), clean); need {
		t.Error("reported a reboot when dnf said none was needed")
	}

	// A router without the plugin exits 1 as well, so the text is what
	// keeps it from claiming every router needs rebooting.
	missing := &fakeRunner{
		out:  map[string]string{"dnf --setopt=*.countme=0 needs-restarting -r": "No such command: needs-restarting. Please use /usr/bin/dnf --help"},
		code: map[string]int{"dnf --setopt=*.countme=0 needs-restarting -r": 1},
	}
	if need, _ := d.RebootRequired(t.Context(), missing); need {
		t.Error("a missing plugin was read as a reboot request")
	}
}

func TestAptCheckAndUpgrade(t *testing.T) {
	t.Parallel()
	a := apt{}
	run := &fakeRunner{out: map[string]string{
		"apt-get -q -s upgrade --with-new-pkgs": fixture(t, "apt-simulate.txt"),
	}}
	got, err := a.Check(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !run.ran("apt-get -q update") {
		t.Error("the index was not refreshed, so the answer could be stale")
	}
	if len(got.Packages) != 4 {
		t.Fatalf("parsed %d packages: %+v", len(got.Packages), got.Packages)
	}
	if got.Security != 2 {
		t.Errorf("security = %d, want 2", got.Security)
	}
	idx := byName(got.Packages)
	if p := idx["libssl3"]; !p.Security || p.From != "3.0.11-1~deb12u2" || p.To != "3.0.14-1~deb12u2" {
		t.Errorf("libssl3 = %+v", p)
	}
	if p := idx["libssl3"]; p.Repo != "Debian-Security:12/stable-security" {
		t.Errorf("libssl3 repo = %q", p.Repo)
	}
	// A package with no installed version still parses, and its several
	// origins do not swallow the architecture into the version.
	if p := idx["linux-image-amd64"]; p.From != "" || p.To != "6.1.99-1" || !p.Security {
		t.Errorf("linux-image-amd64 = %+v", p)
	}
	if p := idx["base-files"]; p.Security {
		t.Error("a stable origin was read as security")
	}

	// Nothing to exclude and everything wanted: the plain upgrade.
	if line := strings.Join(a.UpgradeArgv(false, nil, got), " "); !strings.HasSuffix(line, "upgrade --with-new-pkgs") {
		t.Errorf("all = %q", line)
	}
	// apt has no security switch, so the fixes are named.
	line := strings.Join(a.UpgradeArgv(true, nil, got), " ")
	if !strings.Contains(line, "install --only-upgrade libssl3 linux-image-amd64") {
		t.Errorf("security = %q", line)
	}
	if strings.Contains(line, "base-files") {
		t.Error("a non-security package was named in a security upgrade")
	}
	// An exclude list narrows the same command.
	line = strings.Join(a.UpgradeArgv(false, []string{"linux-image-amd64"}, got), " ")
	if strings.Contains(line, "linux-image-amd64") {
		t.Errorf("excluded package upgraded anyway: %q", line)
	}
	// Excluding everything leaves nothing to run.
	if argv := a.UpgradeArgv(true, []string{"libssl3", "linux-image-amd64"}, got); argv != nil {
		t.Errorf("argv = %v, want nothing to do", argv)
	}
}

func TestZypperCheck(t *testing.T) {
	t.Parallel()
	z := zypper{}
	run := &fakeRunner{out: map[string]string{
		"zypper --xmlout --non-interactive list-updates": fixture(t, "zypper-list-updates.xml"),
		"zypper --xmlout --non-interactive list-patches": fixture(t, "zypper-list-patches.xml"),
	}}
	got, err := z.Check(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 {
		t.Fatalf("parsed %d packages: %+v", len(got.Packages), got.Packages)
	}
	if got.Security != 1 {
		t.Errorf("security = %d, want the one security patch", got.Security)
	}
	if p := byName(got.Packages)["openssl-3"]; p.From != "3.1.4-150600.5.6.1" || p.Repo != "repo-update" {
		t.Errorf("openssl-3 = %+v", p)
	}
	if !strings.Contains(got.Note, "patches") {
		t.Errorf("note = %q, want the patch caveat", got.Note)
	}
	// A stale index reports nothing waiting, which is the one wrong answer
	// this page must never give.
	if !run.ran("zypper --non-interactive refresh") {
		t.Error("the repositories were not refreshed, so the answer could be stale")
	}

	// Tumbleweed has package updates and no patch metadata at all, so a
	// security run would install nothing. Say so rather than report a
	// quiet zero.
	rolling := &fakeRunner{out: map[string]string{
		"zypper --xmlout --non-interactive list-updates": fixture(t, "zypper-list-updates.xml"),
		"zypper --xmlout --non-interactive list-patches": `<?xml version='1.0'?><stream><update-status version="0.6"><update-list/></update-status></stream>`,
	}}
	got, err = z.Check(t.Context(), rolling)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Note, "install nothing") {
		t.Errorf("note = %q", got.Note)
	}
}

func TestZypperUpgradeArgv(t *testing.T) {
	t.Parallel()
	z := zypper{}
	if line := strings.Join(z.UpgradeArgv(true, nil, Pending{}), " "); line != "zypper --non-interactive patch --category security" {
		t.Errorf("security = %q", line)
	}
	pending := Pending{Packages: []Package{{Name: "bash"}, {Name: "openssl-3"}}}
	if line := strings.Join(z.UpgradeArgv(false, []string{"bash"}, pending), " "); line != "zypper --non-interactive update openssl-3" {
		t.Errorf("excluded = %q", line)
	}
	if z.ExcludeSupported(true) {
		t.Error("a patch cannot honour an exclude list; say so")
	}
}

func TestPacmanRefusesSecurity(t *testing.T) {
	t.Parallel()
	for _, d := range []Driver{pacman{}} {
		if d.SecurityCapable() {
			t.Errorf("%s claims a security channel it does not have", d.Name())
		}
		if argv := d.UpgradeArgv(true, nil, Pending{}); argv != nil {
			t.Errorf("%s security argv = %v, want nothing at all", d.Name(), argv)
		}
		if msg := SecurityUnavailable(d); !strings.Contains(msg, "All or Manual") {
			t.Errorf("%s explanation = %q", d.Name(), msg)
		}
	}
}

// Not parallel: it swaps lookPath, which every parallel test that detects
// a manager or locates a command reads.
func TestPacmanCheck(t *testing.T) {
	p := pacman{}
	// checkupdates is the tool an Arch router is expected to have, and the
	// one that never touches the real database.
	restore := lookPath
	lookPath = func(name string) (string, error) {
		if name == "checkupdates" {
			return "/usr/bin/checkupdates", nil
		}
		return "", os.ErrNotExist
	}
	defer func() { lookPath = restore }()

	run := &fakeRunner{out: map[string]string{"checkupdates": fixture(t, "pacman-checkupdates.txt")}}
	got, err := p.Check(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 || got.Security != 0 {
		t.Fatalf("got %+v", got)
	}
	if k := byName(got.Packages)["linux"]; k.From != "6.12.3.arch1-1" || k.To != "6.12.8.arch1-1" {
		t.Errorf("linux = %+v", k)
	}
	if line := strings.Join(p.UpgradeArgv(false, []string{"linux"}, got), " "); line != "pacman -Syu --noconfirm --ignore linux" {
		t.Errorf("argv = %q", line)
	}
	// --ignore glob-matches the way IgnorePkg does, so a pattern goes
	// over as written.
	if line := strings.Join(p.UpgradeArgv(false, []string{"linux*"}, got), " "); line != "pacman -Syu --noconfirm --ignore linux*" {
		t.Errorf("glob argv = %q", line)
	}
	// Nothing waiting exits 2, which is not a failure.
	empty := &fakeRunner{code: map[string]int{"checkupdates": 2}}
	if _, err := p.Check(t.Context(), empty); err != nil {
		t.Errorf("an up-to-date router reported an error: %v", err)
	}
}

// Not parallel: it swaps lookPath, which every parallel test that detects
// a manager or locates a command reads.
func TestPacmanCheckWithoutCheckupdates(t *testing.T) {
	restore := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = restore }()

	scratch := t.TempDir()
	p := pacman{}.useScratch(scratch)
	run := &fakeRunner{}
	if _, err := p.Check(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	// The metadata still has to be fetched, or the answer is whatever the
	// database happened to say last time.
	if !run.ranMatching("pacman -Sy", "--dbpath "+scratch) {
		t.Error("the metadata was not synced, so the answer could be stale")
	}
	// A sync that names no database is a sync of the real one, which
	// leaves the router one step from a partial upgrade: how an Arch
	// install breaks.
	if line := run.ranWithout("-Sy", "--dbpath"); line != "" {
		t.Errorf("the real package database was synced: %q", line)
	}
	if !run.ranMatching("pacman -Qu", "--dbpath "+scratch) {
		t.Error("the throwaway database was not the one queried")
	}
}

func TestKernelRebootHint(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"6.12.0-50.el10.x86_64", "6.12.0-55.el10.x86_64"} {
		if err := os.Mkdir(filepath.Join(dir, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore := moduleDir
	moduleDir = dir
	defer func() { moduleDir = restore }()

	run := &fakeRunner{out: map[string]string{"uname -r": "6.12.0-50.el10.x86_64\n"}}
	need, why := kernelRebootHint(t.Context(), run)
	if !need || !strings.Contains(why, "6.12.0-55") {
		t.Errorf("hint = %v, %q", need, why)
	}

	booted := &fakeRunner{out: map[string]string{"uname -r": "6.12.0-55.el10.x86_64\n"}}
	if need, _ := kernelRebootHint(t.Context(), booted); need {
		t.Error("asked for a reboot into the kernel already running")
	}
}

func TestStripArch(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"kernel.x86_64":        "kernel",
		"tzdata.noarch":        "tzdata",
		"python3.12.aarch64":   "python3.12",
		"python3.12":           "python3.12",
		"NetworkManager-libnm": "NetworkManager-libnm",
	} {
		if got := stripArch(in); got != want {
			t.Errorf("stripArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	t.Parallel()
	if _, err := Detect("nix-env"); err == nil {
		t.Error("accepted a package manager Ostiole cannot drive")
	}
	d, err := Detect("zypper")
	if err != nil || d.Name() != "zypper" {
		t.Errorf("Detect(zypper) = %v, %v", d, err)
	}
}
