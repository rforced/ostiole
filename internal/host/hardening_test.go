package host

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/model"
	"github.com/rforced/ostiole/internal/sysupdate"
)

// aptPackages is testPackages for a Debian router.
func aptPackages(run *fakeCommands) *sysupdate.Manager {
	m := sysupdate.New(sysupdate.Options{
		PackageManager: "apt-get",
		Run:            run,
		Root:           true,
		Direct:         true,
		Log:            slog.New(slog.DiscardHandler),
	})
	m.Run = run
	return m
}

// The gate only holds for the packages step. A router with the old
// firewall still running is let through, because retiring it needs a
// ruleset that only the wizard can apply, and the page is where it
// shows rather than where the browser is sent.
func TestOnlyPackagesGate(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true, "systemd-networkd.service": true},
	}
	d := testDeps(t, &model.Config{}, units)
	// nft is there, so nothing an apply needs is missing.
	d.Locate = func(name string) string {
		if name == "nft" {
			return "/usr/sbin/nft"
		}
		return ""
	}
	rep := Status(context.Background(), d)
	if !rep.Prepared {
		t.Error("a router whose only outstanding step is the old firewall was gated")
	}
	if fw := stepOf(t, rep, StepFirewall); fw.State != StateOutstanding {
		t.Errorf("firewall step = %+v, want it still reported outstanding", fw)
	}
	if !rep.Firewalled {
		t.Error("the report does not say Ostiole's table is loaded")
	}
	// Without nft the apply would fail, and that is what gates.
	d.Locate = func(string) string { return "" }
	if Status(context.Background(), d).Prepared {
		t.Error("a router with no nft was let through")
	}
}

// Extras are read from systemd like competitors, and what a removal
// takes is the package manager's answer.
func TestExtrasAreReportedAndRemoved(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{
			"snapd.service": "enabled", "snapd.socket": "enabled",
			"unattended-upgrades.service": "enabled",
			"apt-daily.timer":             "enabled", "apt-daily-upgrade.timer": "enabled",
			"ModemManager.service": "masked",
		},
		active: map[string]string{"snapd.service": "active", "unattended-upgrades.service": "active"},
		known:  map[string]bool{},
	}
	run := &fakeCommands{out: map[string]string{
		"apt-get -q -s remove snapd":                     "Remv snapd [2.76]",
		"dpkg-query -s modemmanager":                     "Status: deinstall ok config-files",
		"apt-get -q -s remove snapd unattended-upgrades": "Remv snapd\nRemv unattended-upgrades",
		"apt-get -q -s remove unattended-upgrades":       "Remv unattended-upgrades",
	}}
	d := testDeps(t, &model.Config{}, units)
	d.Run = run
	d.Packages = aptPackages(run)
	rep := Status(context.Background(), d)
	byKey := map[string]ExtraState{}
	for _, e := range rep.Extras {
		byKey[e.Key] = e
	}
	if e := byKey["snapd"]; !e.Present || !e.Active || strings.Join(e.Packages, " ") != "snapd" {
		t.Errorf("snapd = %+v", e)
	}
	if e := byKey["apt-daily"]; !e.MaskOnly || len(e.Packages) != 0 {
		t.Errorf("apt's timers = %+v, want mask only", e)
	}
	if e := byKey["modemmanager"]; !e.Removed {
		t.Errorf("ModemManager = %+v, want removed (masked and not installed)", e)
	}
	if _, ok := byKey["fwupd"]; ok {
		t.Error("an extra systemd has never heard of is reported")
	}

	// The preview is the manager's, and changes nothing.
	out, err := RemoveExtras(context.Background(), d, []string{"snapd"}, true)
	if err != nil || !strings.Contains(out, "Remv snapd") {
		t.Fatalf("preview = %q, %v", out, err)
	}
	for _, c := range units.calls {
		if strings.HasPrefix(c, "mask") {
			t.Fatalf("a preview masked something: %v", units.calls)
		}
	}
	// The removal masks every unit first, then takes the package.
	out, err = RemoveExtras(context.Background(), d, []string{"snapd", "apt-daily"}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mask --now snapd.service", "mask --now snapd.socket", "mask --now apt-daily.timer"} {
		found := false
		for _, c := range units.calls {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("did not run %q: %v", want, units.calls)
		}
	}
	removed := false
	for _, c := range run.calls {
		if strings.HasPrefix(c, "apt-get -y -q") && strings.HasSuffix(c, "remove snapd") {
			removed = true
		}
	}
	if !removed || !strings.Contains(out, "removed: snapd") {
		t.Errorf("out = %q, calls = %v", out, run.calls)
	}
	if _, err := RemoveExtras(context.Background(), d, []string{"gnome"}, true); err == nil {
		t.Error("removed something this router does not have")
	}
}

// sshRouter is a filesystem with sshd set up the way a cloud image
// leaves it: the main file says no passwords, and the drop-in cloud-init
// wrote says yes and wins.
func sshRouter(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/ssh/sshd_config", "# comment\nInclude /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\nMatch User bob\n  PasswordAuthentication yes\n")
	write("etc/ssh/sshd_config.d/50-cloud-init.conf", "PasswordAuthentication yes\n")
	write("etc/cloud/cloud.cfg.d/95_ds-vultr.cfg", "ssh_pwauth: 1\n")
	write("etc/passwd", "root:x:0:0:root:/root:/bin/bash\ndaemon:x:1:1::/usr/sbin:/usr/sbin/nologin\nlinuxuser:x:1000:1000::/home/linuxuser:/bin/bash\nnobody:x:65534:65534::/nonexistent:/usr/sbin/nologin\nsvc:x:1001:1001::/home/svc:/bin/false\n")
	write("etc/group", "sudo:x:27:linuxuser\nwheel:x:10:\n")
	write("root/.ssh/authorized_keys", "ssh-ed25519 AAAA josh\n# a comment\n\nssh-rsa BBBB old\n")
	write("home/linuxuser/.ssh/authorized_keys", "ssh-ed25519 AAAA josh\n")
	return root
}

func TestSSHStateNamesTheFileThatDecides(t *testing.T) {
	t.Parallel()
	root := sshRouter(t)
	run := &fakeCommands{out: map[string]string{
		"/usr/sbin/sshd -T": "port 22\npasswordauthentication yes\nkbdinteractiveauthentication no\npermitrootlogin yes\n",
	}}
	units := &fakeUnits{active: map[string]string{"ssh.service": "active"}}
	d := testDeps(t, &model.Config{}, units)
	d.Run, d.FS = run, root
	d.Locate = func(name string) string {
		if name == "sshd" {
			return "/usr/sbin/sshd"
		}
		return ""
	}
	st := sshState(context.Background(), d)
	if !st.Present || !st.Passwords || st.RootLogin != "yes" || st.Managed {
		t.Errorf("state = %+v", st)
	}
	if st.SetBy != "/etc/ssh/sshd_config.d/50-cloud-init.conf" {
		t.Errorf("set by %q, want the cloud-init drop-in", st.SetBy)
	}
	if st.Unit != "ssh.service" {
		t.Errorf("unit = %q", st.Unit)
	}

	// Requiring keys writes the drop-in that sorts first, pins cloud-init,
	// checks the result with sshd and reloads it.
	said, err := SetSSHPasswords(context.Background(), d, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(said, "keys only") || !strings.Contains(said, "reloaded") || !strings.Contains(said, "cloud-init") {
		t.Errorf("said %q", said)
	}
	raw, err := os.ReadFile(filepath.Join(root, SSHDropIn))
	if err != nil || !strings.Contains(string(raw), "PasswordAuthentication no") {
		t.Fatalf("drop-in = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, CloudInitSSHDropIn)); err != nil || !strings.Contains(string(raw), "ssh_pwauth: false") {
		t.Errorf("cloud-init pin = %q, %v", raw, err)
	}
	if !sshStateManaged(d) {
		t.Error("the report does not see the drop-in")
	}
	if by := sshSetBy(filepath.Join(root, "etc/ssh/sshd_config"), root, "passwordauthentication"); by != SSHDropIn {
		t.Errorf("after requiring keys the deciding file is %q", by)
	}
	for _, want := range []string{"/usr/sbin/sshd -t"} {
		if !hasCall(run.calls, want) {
			t.Errorf("did not run %q: %v", want, run.calls)
		}
	}
	if !hasCall(units.calls, "reload ssh.service") {
		t.Errorf("sshd was not reloaded: %v", units.calls)
	}

	// Allowing passwords again takes both files away.
	if _, err := SetSSHPasswords(context.Background(), d, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{SSHDropIn, CloudInitSSHDropIn} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s is still there", p)
		}
	}

	// A configuration sshd rejects is put back rather than reloaded.
	run.fail = map[string]bool{"/usr/sbin/sshd -t": true}
	if _, err := SetSSHPasswords(context.Background(), d, false); err == nil || !strings.Contains(err.Error(), "put back") {
		t.Errorf("err = %v, want the rejection", err)
	}
	if _, err := os.Stat(filepath.Join(root, SSHDropIn)); err == nil {
		t.Error("a rejected drop-in was left in place")
	}
}

func sshStateManaged(d Deps) bool {
	_, err := os.Stat(d.fsPath(SSHDropIn))
	return err == nil
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// A main file without the Include gets one, at the top, or the drop-in
// would be decoration.
func TestRequireKeysAddsTheIncludeWhenMissing(t *testing.T) {
	t.Parallel()
	root := sshRouter(t)
	main := filepath.Join(root, "etc/ssh/sshd_config")
	if err := os.WriteFile(main, []byte("PasswordAuthentication yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := testDeps(t, &model.Config{}, &fakeUnits{})
	d.FS = root
	d.Locate = func(name string) string { return "/usr/sbin/" + name }
	if _, err := SetSSHPasswords(context.Background(), d, false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(main)
	if !strings.HasPrefix(string(raw), "# Added by ostiole") || !strings.Contains(string(raw), sshdInclude+"\nPasswordAuthentication yes") {
		t.Errorf("main file:\n%s", raw)
	}
	if by := sshSetBy(main, root, "passwordauthentication"); by != SSHDropIn {
		t.Errorf("deciding file = %q", by)
	}
}

func TestAccountsListWhoCanGetIn(t *testing.T) {
	t.Parallel()
	d := testDeps(t, &model.Config{}, &fakeUnits{})
	d.FS = sshRouter(t)
	got := accounts(d)
	want := map[string]Account{
		"root":      {Name: "root", UID: 0, Sudo: true, Keys: 2, Shell: "/bin/bash"},
		"linuxuser": {Name: "linuxuser", UID: 1000, Sudo: true, Keys: 1, Shell: "/bin/bash"},
	}
	if len(got) != len(want) {
		t.Fatalf("accounts = %+v, want root and linuxuser only", got)
	}
	for _, a := range got {
		if w, ok := want[a.Name]; !ok || w != a {
			t.Errorf("account %+v, want %+v", a, w)
		}
	}
}

// Prepare walks the steps in order and says which one it could not take.
func TestPrepareRunsTheStepsInOrder(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{"firewalld.service": "enabled"},
		active:  map[string]string{"firewalld.service": "active"},
		known:   map[string]bool{"firewalld.service": true, "systemd-networkd.service": true},
	}
	cfg := &model.Config{Services: model.Services{DHCP: model.DHCPService{Enabled: true}}}
	d := testDeps(t, cfg, units)
	var setUpKeys []string
	setUp := func(_ context.Context, keys []string) (string, error) {
		setUpKeys = keys
		return "set up " + strings.Join(keys, ","), nil
	}
	// Before the first apply the old firewall stays, and the output says
	// so instead of failing.
	d.TableLoaded = func(context.Context) bool { return false }
	out, err := Prepare(context.Background(), d, setUp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(setUpKeys, ",") != "nft,dnsmasq" {
		t.Errorf("set up %v, want the outstanding components", setUpKeys)
	}
	if !strings.Contains(out, "setup wizard retires it") {
		t.Errorf("out = %q, want it to say the firewall waits", out)
	}
	if hasCall(units.calls, "mask --now firewalld.service") {
		t.Error("the old firewall was retired with no ruleset loaded")
	}
	// After it, the firewall goes.
	d.TableLoaded = func(context.Context) bool { return true }
	if _, err := Prepare(context.Background(), d, setUp); err != nil {
		t.Fatal(err)
	}
	if !hasCall(units.calls, "mask --now firewalld.service") {
		t.Errorf("the old firewall was not retired: %v", units.calls)
	}
}

func TestSentence(t *testing.T) {
	t.Parallel()
	rep := Report{
		Firewalled: true,
		Components: []ComponentState{
			{Key: "dnsmasq", Label: "DHCP and DNS", Required: true},
			{Key: "unbound", Label: "Validating resolver", Required: true},
			{Key: "nft", Label: "nftables", Required: true, Present: true, Ready: true},
		},
		Competitors: []CompetitorState{{Conflicts: true}},
	}
	rep.Competitors[0].Name, rep.Competitors[0].Kind = "ufw", "firewall"
	got := Sentence(rep)
	if got != "Set up DHCP and DNS and Validating resolver and retire ufw." {
		t.Errorf("sentence = %q", got)
	}
	rep.Firewalled = false
	if got := Sentence(rep); strings.Contains(got, "ufw") {
		t.Errorf("sentence promises to retire a firewall it cannot: %q", got)
	}
	if Sentence(Report{}) != "" {
		t.Error("an empty report has a sentence")
	}
}

// The plan is the report read as a to-do list, with the package
// manager's preview attached and its refusal honoured.
func TestPlanInstall(t *testing.T) {
	t.Parallel()
	units := &fakeUnits{
		enabled: map[string]string{
			"ufw.service": "enabled", "nftables.service": "enabled",
			"snapd.service": "enabled", "snapd.socket": "enabled", "apt-daily.timer": "enabled",
			"ModemManager.service": "enabled", "fwupd.service": "enabled",
		},
		active: map[string]string{"ufw.service": "active", "nftables.service": "inactive"},
		known:  map[string]bool{"systemd-networkd.service": true},
	}
	run := &fakeCommands{out: map[string]string{
		"dpkg-query -s ufw":                    "Status: install ok installed",
		"apt-get -q -s remove ufw snapd fwupd": "Remv ufw\nRemv snapd\nRemv fwupd\nRemv ubuntu-server",
	}}
	d := testDeps(t, &model.Config{}, units)
	d.Run = run
	d.Packages = aptPackages(run)
	p, err := PlanInstall(context.Background(), d, []string{"modemmanager"})
	if err != nil {
		t.Fatal(err)
	}
	var install []string
	for _, c := range p.Install {
		install = append(install, c.Key)
	}
	// Everything a router gets, and nothing the page offers on demand.
	if strings.Join(install, " ") != "nft dnsmasq unbound tc" {
		t.Errorf("install = %v", install)
	}
	if len(p.Retire) != 2 || p.Retire[0].Name != "ufw" || p.Retire[1].Name != "nftables" {
		t.Errorf("retire = %+v, want ufw and nftables masked", p.Retire)
	}
	var removed []string
	for _, r := range p.Remove {
		removed = append(removed, r.Label)
	}
	if strings.Join(removed, " ") != "ufw snapd fwupd" {
		t.Errorf("remove = %v", removed)
	}
	if strings.Join(p.Mask, " ") != "apt-daily.timer apt-daily-upgrade.timer" {
		t.Errorf("mask = %v", p.Mask)
	}
	if strings.Join(p.Kept, " ") != "ModemManager" {
		t.Errorf("kept = %v", p.Kept)
	}
	if !strings.Contains(p.Preview, "ubuntu-server") || p.Refused != nil {
		t.Errorf("preview = %q, refused = %v", p.Preview, p.Refused)
	}
	var b strings.Builder
	p.Print(&b)
	for _, want := range []string{"install and set up:", "stop and mask:        ufw, nftables", "remove:", "kept (--keep):        ModemManager", "Remv ubuntu-server"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("printed plan lacks %q:\n%s", want, b.String())
		}
	}

	// A preview that would take nftables is refused, and the second half
	// refuses with it.
	run.out["apt-get -q -s remove ufw snapd fwupd"] = "Remv ufw\nRemv nftables"
	p, err = PlanInstall(context.Background(), d, []string{"modemmanager"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Refused == nil {
		t.Fatal("a plan that removes nftables was not refused")
	}
	if _, err := RetireAndRemove(context.Background(), d, p); err == nil {
		t.Error("a refused plan was carried out")
	}
}
