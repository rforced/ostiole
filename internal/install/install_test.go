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

	"ostiole/internal/network"
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
		enabled: map[string]string{"firewalld.service": "enabled", "NetworkManager.service": "enabled", "ufw.service": "masked",
			resolvedUnit: "disabled", "systemd-timesyncd.service": "disabled", "chronyd.service": "disabled"},
		active: map[string]string{"firewalld.service": "active", "NetworkManager.service": "active", "ufw.service": "inactive"},
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
	lay.Leftovers.Resolv = filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(lay.Leftovers.Resolv, []byte("nameserver 127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(context.Background(), sc, &fakeRunner{}, lay, UninstallOptions{Purge: true}, log); err != nil {
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
	// A purged box still resolves names and keeps time: systemd-resolved
	// behind resolv.conf, and timesyncd ahead of chrony.
	if !sc.has("enable", "--now", resolvedUnit) || !sc.has("enable", "--now", "systemd-timesyncd.service") ||
		sc.has("enable", "--now", "chronyd.service") {
		t.Errorf("systemctl calls = %v", sc.calls)
	}
	if target, err := os.Readlink(lay.Leftovers.Resolv); err != nil || target != "../run/systemd/resolve/stub-resolv.conf" {
		t.Errorf("resolv.conf links to %q, %v", target, err)
	}
}

func TestUninstallLeftovers(t *testing.T) {
	t.Parallel()
	for _, purge := range []bool{false, true} {
		lay := tempLayout(t)
		host := t.TempDir()
		at := func(p ...string) string { return filepath.Join(append([]string{host}, p...)...) }
		cfg := func(name string) string { return filepath.Join(lay.ConfigDir, name) }
		lay.Leftovers = Leftovers{
			Units:    []string{"ostiole-quokka.service", "ostiole-wombat@.service"},
			Files:    []string{at("quokka.d", "ostiole.*"), at("sysctl.d", "99-ostiole.conf")},
			Unmask:   []string{"quokka.service", "numbat-timesyncd.service"},
			Resolv:   at("resolv.conf"),
			State:    []string{at("lib", "ostiole-wombat"), at("lib", "quokka", "ostiole.leases")},
			Binaries: []string{"ostiole-wombat"},
			User:     "ostiole-wombat",
		}
		resolv := "nameserver 127.0.0.1\nnameserver ::1\n"
		if err := os.WriteFile(at("resolv.conf"), []byte(resolv), 0o600); err != nil {
			t.Fatal(err)
		}
		files := []string{at("quokka.d", "ostiole.conf"), at("quokka.d", "ostiole.hosts"), at("sysctl.d", "99-ostiole.conf"),
			filepath.Join(lay.NetworkdConfDir, NetworkdConfFile)}
		state := []string{at("lib", "ostiole-wombat", "certificates", "site.example.crt"), at("lib", "quokka", "ostiole.leases"),
			filepath.Join(lay.BinDir, "ostiole-wombat"), cfg("ruleset.nft"), cfg("audit.jsonl")}
		var units []string
		for _, u := range []string{"ostiole-quokka.service", "ostiole-wombat@.service", "ostiole-quokka.service.d/10-level.conf",
			"ostiole-wombat@.service.d/10-level.conf", "multi-user.target.wants/ostiole-quokka.service",
			"multi-user.target.wants/ostiole-wombat@radio0.service"} {
			units = append(units, filepath.Join(lay.UnitDir, u))
		}
		daemon := []string{lay.Binary(), cfg("config.json"), cfg("sessions.json"), filepath.Join(lay.UnitDir, DaemonUnit),
			filepath.Join(lay.UnitDir, DaemonUnit+".d", "10-level.conf"), filepath.Join(lay.UnitDir, "multi-user.target.wants", DaemonUnit)}
		firewall := filepath.Join(lay.UnitDir, FirewallUnit)
		nft := at("sbin", "nft")
		kept := []string{at("quokka.d", "distro.conf"), at("lib", "quokka", "distro.leases"),
			filepath.Join(lay.BinDir, "other-tool"), filepath.Join(lay.UnitDir, "multi-user.target.wants", "other.service")}
		// Each file holds its own path, so a renamed one shows where it came from.
		for _, p := range slices.Concat(files, state, units, daemon, kept, []string{firewall, nft, cfg("users.json"), cfg("users.json.bak")}) {
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(p+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		// Debian names chrony's unit chrony.service, with chronyd.service an alias.
		sc := &fakeSystemctl{enabled: map[string]string{"chronyd.service": "alias", "chrony.service": "disabled"}}
		run := &fakeRunner{}
		var logged strings.Builder
		opts := UninstallOptions{Purge: purge, Nft: nft}
		if err := Uninstall(context.Background(), sc, run, lay, opts, slog.New(slog.NewTextHandler(&logged, nil))); err != nil {
			t.Fatal(err)
		}
		if warned := strings.Contains(logged.String(), "resolv.conf points at the removed dnsmasq"); warned != purge {
			t.Errorf("purge=%v: resolv.conf warning is %v in %q", purge, warned, logged.String())
		}
		gone := slices.Concat(daemon, []string{filepath.Join(lay.UnitDir, DaemonUnit+".d")})
		if purge {
			gone = slices.Concat(gone, files, units, state, []string{firewall, lay.ConfigDir, at("lib", "ostiole-wombat"),
				filepath.Join(lay.UnitDir, "ostiole-quokka.service.d")})
		} else {
			kept = slices.Concat(kept, files, units, state, []string{firewall})
		}
		for _, p := range gone {
			if _, err := os.Lstat(p); err == nil {
				t.Errorf("purge=%v: %s is still there", purge, p)
			}
		}
		for _, p := range kept {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("purge=%v: %s went: %v", purge, p, err)
			}
		}
		if !purge {
			// The daemon goes and nothing else stops or comes back.
			if want := [][]string{{"disable", "--now", DaemonUnit}, {"daemon-reload"}}; !slices.EqualFunc(sc.calls, want, slices.Equal) {
				t.Errorf("systemctl calls = %q, want %q", sc.calls, want)
			}
			if len(run.calls) > 0 {
				t.Errorf("commands run = %v", run.calls)
			}
			if raw, _ := os.ReadFile(firewall); string(raw) != FirewallUnitUnmanaged(nft, lay.ConfigDir) ||
				!strings.Contains(string(raw), "\nExecStart="+nft+" -f "+cfg("ruleset.nft")+"\n") {
				t.Errorf("firewall unit:\n%s", raw)
			}
			// An older .bak gives way, and a file that is not there leaves none.
			for bak, was := range map[string]string{"config.json.bak": cfg("config.json"), "users.json.bak": cfg("users.json")} {
				if raw, err := os.ReadFile(cfg(bak)); err != nil || string(raw) != was+"\n" {
					t.Errorf("%s = %q, %v, want the bytes of %s", bak, raw, err, was)
				}
			}
			if _, err := os.Stat(cfg("tokens.json.bak")); err == nil {
				t.Error("a missing tokens.json left a .bak")
			}
			if raw, _ := os.ReadFile(at("resolv.conf")); string(raw) != resolv {
				t.Errorf("resolv.conf = %q", raw)
			}
			continue
		}
		for _, want := range [][]string{{"disable", "--now", DaemonUnit}, {"disable", "--now", "ostiole-quokka.service"},
			{"disable", "ostiole-wombat@.service"}, {"stop", "ostiole-wombat@*.service"},
			{"disable", "--now", FirewallUnit}, {"daemon-reload"}, {"unmask", "quokka.service"}, {"unmask", "numbat-timesyncd.service"},
			{"enable", "--now", "chrony.service"}} {
			if !sc.has(want...) {
				t.Errorf("no systemctl %v in %v", want, sc.calls)
			}
		}
		if sc.has("enable", "--now", "chronyd.service") || sc.has("enable", "--now", resolvedUnit) {
			t.Errorf("started a unit the host has no file for: %v", sc.calls)
		}
		reload := slices.IndexFunc(sc.calls, func(c []string) bool { return c[0] == "daemon-reload" })
		unmask := slices.IndexFunc(sc.calls, func(c []string) bool { return c[0] == "unmask" })
		if unmask < reload {
			t.Errorf("unmasked before the units were gone: %v", sc.calls)
		}
		// A masked unit lists a file even on a host that has none.
		unmasked := slices.IndexFunc(sc.calls, func(c []string) bool { return slices.Equal(c, []string{"unmask", "numbat-timesyncd.service"}) })
		looked := slices.IndexFunc(sc.calls, func(c []string) bool { return c[0] == "list-unit-files" })
		if looked < unmasked {
			t.Errorf("looked for a resolver and a clock before the unmask: %v", sc.calls)
		}
		daemonCall := slices.IndexFunc(sc.calls, func(c []string) bool { return slices.Contains(c, DaemonUnit) })
		service := slices.IndexFunc(sc.calls, func(c []string) bool { return slices.Contains(c, "ostiole-quokka.service") })
		if daemonCall < 0 || service < daemonCall {
			t.Errorf("the daemon is not stopped first: %v", sc.calls)
		}
		if !run.ran("userdel", "ostiole-wombat") {
			t.Errorf("commands run = %v", run.calls)
		}
	}
}

// Once the daemon is gone the firewall unit loads the saved ruleset with
// nft, in the same place in the boot, and needs no Ostiole binary.
func TestFirewallUnitUnmanaged(t *testing.T) {
	t.Parallel()
	lay := DefaultLayout()
	managed := Units(lay, Options{Listen: ":443"})[FirewallUnit]
	u := FirewallUnitUnmanaged("/usr/sbin/nft", lay.ConfigDir)
	for _, want := range []string{"\nType=oneshot\n", "\nRemainAfterExit=yes\n",
		"\nExecStart=/usr/sbin/nft -f /etc/ostiole/ruleset.nft\n", "\nExecReload=/usr/sbin/nft -f /etc/ostiole/ruleset.nft\n"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", strings.TrimSpace(want), u)
		}
	}
	if strings.Contains(u, lay.Binary()) {
		t.Errorf("unit still runs the binary:\n%s", u)
	}
	unit := func(s string) string { head, _, _ := strings.Cut(s, "[Service]"); return head }
	wanted := func(s string) string { _, foot, _ := strings.Cut(s, "[Install]"); return foot }
	if unit(u) != unit(managed) || wanted(u) != wanted(managed) {
		t.Errorf("[Unit] or [Install] differs from the managed unit:\n%s\nmanaged:\n%s", u, managed)
	}
	if got := nftPath("ostiole-no-such-nft"); got != "/usr/sbin/nft" {
		t.Errorf("nftPath of a missing name = %q", got)
	}
	exe := filepath.Join(t.TempDir(), "nft")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := nftPath(exe); got != exe {
		t.Errorf("nftPath(%q) = %q", exe, got)
	}
}

func TestDefaultLeftovers(t *testing.T) {
	t.Parallel()
	left := DefaultLayout().Leftovers
	for _, f := range []string{"/etc/sysctl.d/99-ostiole.conf", BluetoothConfFile, "/etc/sysusers.d/ostiole-proxy.conf"} {
		if !slices.Contains(left.Files, f) {
			t.Errorf("Files lacks %s: %v", f, left.Files)
		}
	}
	for _, u := range []string{"ostiole-hostapd@.service", "ostiole-pppoe@.service", "ostiole-tailscaled.service", "ostiole-proxy.service"} {
		if !slices.Contains(left.Units, u) {
			t.Errorf("Units lacks %s: %v", u, left.Units)
		}
	}
	for _, s := range []string{"/var/lib/ostiole-proxy", "/var/lib/tailscale"} {
		if slices.Contains(left.Files, s) || !slices.Contains(left.State, s) {
			t.Errorf("%s is not state alone: files %v, state %v", s, left.Files, left.State)
		}
	}
	for _, u := range []string{"systemd-resolved.service", "chronyd.service", "systemd-timesyncd.service", "bluetooth.service",
		"apt-daily.timer", "dnf-makecache.timer", "smartd.service", "smartmontools.service"} {
		if !slices.Contains(left.Unmask, u) {
			t.Errorf("Unmask lacks %s: %v", u, left.Unmask)
		}
	}
	if left.Resolv != "/etc/resolv.conf" {
		t.Errorf("Resolv = %q", left.Resolv)
	}
}

func TestScriptMaskOnly(t *testing.T) {
	t.Parallel()
	units := ScriptMaskOnly()
	if len(units) < 3 || !slices.Contains(units, "apt-daily.timer") || !slices.Contains(units, "smartd.service") {
		t.Errorf("ScriptMaskOnly() = %v", units)
	}
	if len(slices.Compact(slices.Sorted(slices.Values(units)))) != len(units) {
		t.Errorf("repeats in %v", units)
	}
}

func TestLoopbackOnly(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"nameserver 127.0.0.1\nnameserver ::1\n":     true,
		"# by Ostiole\nnameserver 127.0.0.53\n":      true,
		"nameserver 127.0.0.1\nnameserver 9.9.9.9\n": false,
		"search lan.example\n":                       false,
		"nameserver fe80::1%eth0\n":                  false,
		"":                                           false,
	} {
		if got := loopbackOnly(in); got != want {
			t.Errorf("loopbackOnly(%q) = %v", in, got)
		}
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
	// What the sandbox takes away, which systemd-analyze security scores.
	// Captures and Wake on LAN need raw frames, and nft, routes and nflog
	// need netlink; the filter is a deny list, so a call it misses fails.
	for _, want := range []string{
		"\nRestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK AF_PACKET\n",
		"\nRestrictNamespaces=yes\n", "\nMemoryDenyWriteExecute=yes\n", "\nProtectKernelLogs=yes\n", "\nProtectHostname=yes\n",
		"\nSystemCallFilter=~@obsolete @cpu-emulation @debug @swap @reboot @raw-io @mount\n", "\nSystemCallErrorNumber=EPERM\n",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("daemon unit lacks %q:\n%s", strings.TrimSpace(want), d)
		}
	}
	// sysctls, the kernel's time zone offset, the disks and the module
	// directory are the daemon's to reach.
	for _, never := range []string{"ProtectKernelTunables=", "ProtectClock=", "PrivateDevices=", "ProtectKernelModules="} {
		if strings.Contains(d, never) {
			t.Errorf("daemon unit has %s, which takes what the daemon needs:\n%s", never, d)
		}
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
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: %v, want %v", path, info.Mode().Perm(), want)
		}
	}
}

// Only a handover that holds lets the old managers' packages go: networkd
// running and none of the managers it replaced, as a revert leaves them.
func TestHandoverHoldsOnlyWithNetworkdAndNoOldManager(t *testing.T) {
	t.Parallel()
	managers := []string{"NetworkManager", "NetworkManager-wait-online"}
	for _, c := range []struct {
		name   string
		active map[string]string
		want   bool
	}{
		{"confirmed", map[string]string{NetworkdUnit: "active"}, true},
		{"reverted", map[string]string{NetworkdUnit: "inactive", "NetworkManager.service": "active"}, false},
		{"both running", map[string]string{NetworkdUnit: "active", "NetworkManager.service": "active"}, false},
		{"neither", map[string]string{}, false},
	} {
		if got := HandoverHolds(context.Background(), &fakeSystemctl{active: c.active}, managers); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
