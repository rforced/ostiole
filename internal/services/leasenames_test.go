package services

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/network"
)

// The policy is read back from what Render wrote, so the two cannot drift:
// full.json registers on eth1 for both families and gives two static
// leases names of their own; ntp.json registers nowhere.
func TestPolicyOfReadsTheRenderedConfiguration(t *testing.T) {
	t.Parallel()
	render := func(fixture string) string {
		files, err := (&Dnsmasq{}).Render(loadConfig(t, fixture))
		if err != nil {
			t.Fatal(err)
		}
		return files[confName]
	}

	p, ok := policyOf(render("testdata/full.json"))
	if !ok {
		t.Fatal("full.json serves DHCP, policy says it does not")
	}
	if want := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}; !slices.Equal(p.nets4, want) {
		t.Errorf("nets4 = %v, want %v", p.nets4, want)
	}
	if want := []string{"eth1"}; !slices.Equal(p.ifaces6, want) {
		t.Errorf("ifaces6 = %v, want %v", p.ifaces6, want)
	}
	for _, mac := range []string{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:01"} {
		if !p.named[mac] {
			t.Errorf("static lease %s names its device, policy missed it: %v", mac, p.named)
		}
	}

	p, ok = policyOf(render("testdata/ntp.json"))
	if !ok || len(p.nets4) != 0 || len(p.ifaces6) != 0 {
		t.Errorf("ntp.json registers nothing, policy = %+v, %v", p, ok)
	}
	if _, ok := policyOf("port=0\n"); ok {
		t.Error("a configuration without DHCP reads no leases")
	}
}

func TestStripTakesOffOnlyUnregisteredNames(t *testing.T) {
	t.Parallel()
	p := namePolicy{
		nets4: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")},
		named: map[string]bool{"10:ff:e0:3a:5a:cf": true},
	}
	file := strings.Join([]string{
		"1790471918 3a:8a:33:e0:8f:75 10.130.0.185 Watch 01:3a:8a:33:e0:8f:75",
		"1790471918 10:ff:e0:3a:5a:cf 10.130.0.11 server 01:10:ff:e0:3a:5a:cf",
		"1790471918 aa:aa:aa:aa:aa:aa 192.168.1.20 laptop *",
		"0 bb:bb:bb:bb:bb:bb 10.130.0.99 * *",
		"duid 00:01:00:01:2c:5e:6a:10:84:47:09:81:de:5a",
		"1790471918 1234 2001:db8:1::150 phone 00:01:00:01:aa",
		"1790471918 5678 2001:db8:9::150 tablet 00:01:00:01:bb",
		"",
	}, "\n")
	got, changed := p.strip(file, []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")})
	want := strings.Join([]string{
		"1790471918 3a:8a:33:e0:8f:75 10.130.0.185 * 01:3a:8a:33:e0:8f:75",
		"1790471918 10:ff:e0:3a:5a:cf 10.130.0.11 server 01:10:ff:e0:3a:5a:cf",
		"1790471918 aa:aa:aa:aa:aa:aa 192.168.1.20 laptop *",
		"0 bb:bb:bb:bb:bb:bb 10.130.0.99 * *",
		"duid 00:01:00:01:2c:5e:6a:10:84:47:09:81:de:5a",
		"1790471918 1234 2001:db8:1::150 phone 00:01:00:01:aa",
		"1790471918 5678 2001:db8:9::150 * 00:01:00:01:bb",
		"",
	}, "\n")
	if !changed || got != want {
		t.Errorf("strip changed=%v\n--- got ---\n%s--- want ---\n%s", changed, got, want)
	}
	if _, changed := p.strip(want, []netip.Prefix{netip.MustParsePrefix("2001:db8:1::/64")}); changed {
		t.Error("a file with nothing left to take off reports a change")
	}
}

// With registration off, the name comes off the lease while dnsmasq is
// stopped, through a transient unit, and dnsmasq starts again. The copy is
// the only way the file changes, so the test reads what it would copy.
func TestApplyTakesUnregisteredNamesOffTheLeases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := filepath.Join(dir, "ostiole.leases")
	if err := os.WriteFile(leases, []byte("1758300000 aa:bb:cc:00:00:01 10.0.0.20 calcifer *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "conf")
	var copied string
	cmd := &fakeCmd{installed: true}
	cmd.onRun = func(name string, args []string) {
		if name == "systemd-run" {
			raw, err := os.ReadFile(args[len(args)-2])
			if err != nil {
				t.Errorf("copy source: %v", err)
			}
			copied = string(raw)
		}
	}
	d := &Dnsmasq{Dir: conf, Leases: leases, Cmd: cmd}
	files, err := d.Render(loadConfig(t, "testdata/dhcp-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if want := "1758300000 aa:bb:cc:00:00:01 10.0.0.20 * *\n"; copied != want {
		t.Errorf("copied %q, want %q", copied, want)
	}
	var verbs []string
	for _, c := range cmd.calls {
		switch c[0] {
		case "systemctl":
			verbs = append(verbs, c[1])
		case "systemd-run":
			verbs = append(verbs, "copy")
			if want := []string{"cp", "--", filepath.Join(conf, leasesName), leases}; !slices.Equal(c[len(c)-4:], want) {
				t.Errorf("copy command = %v, want it to end %v", c, want)
			}
		}
	}
	if want := []string{"cat", "enable", "stop", "copy", "start"}; !slices.Equal(verbs, want) {
		t.Errorf("commands = %v, want %v", verbs, want)
	}
	if _, err := os.Stat(filepath.Join(conf, leasesName)); !os.IsNotExist(err) {
		t.Errorf("the copy's source is left behind: %v", err)
	}
}

// A server that registers keeps its names, and dnsmasq is simply restarted.
func TestApplyRestartsWhenEveryNameIsRegistered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := filepath.Join(dir, "ostiole.leases")
	if err := os.WriteFile(leases, []byte("1758300000 aa:bb:cc:00:00:01 10.0.0.20 calcifer *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &fakeCmd{installed: true}
	d := &Dnsmasq{Dir: filepath.Join(dir, "conf"), Leases: leases, Cmd: cmd}
	cfg := loadConfig(t, "testdata/dhcp-only.json")
	cfg.Services.DHCP.Servers[0].DNSRegistration = true
	files, err := d.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "restart", Unit) || cmd.has("systemctl", "stop", Unit) {
		t.Errorf("calls = %v, want a plain restart", cmd.calls)
	}
}

// dnsmasq is started again even when the copy fails, and the apply says
// what went wrong.
func TestApplyStartsDnsmasqWhenTheCopyFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := filepath.Join(dir, "ostiole.leases")
	if err := os.WriteFile(leases, []byte("1758300000 aa:bb:cc:00:00:01 10.0.0.20 calcifer *\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &failingCopy{fakeCmd: fakeCmd{installed: true}}
	d := &Dnsmasq{Dir: filepath.Join(dir, "conf"), Leases: leases, Cmd: cmd}
	files, err := d.Render(loadConfig(t, "testdata/dhcp-only.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Apply(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "take the names off") {
		t.Errorf("Apply = %v, want the copy's failure", err)
	}
	if !cmd.has("systemctl", "start", Unit) {
		t.Errorf("dnsmasq was left stopped: %v", cmd.calls)
	}
}

type failingCopy struct{ fakeCmd }

func (f *failingCopy) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := f.fakeCmd.Run(ctx, name, args...)
	if name == "systemd-run" {
		return []byte("Failed to start transient service unit"), os.ErrPermission
	}
	return out, err
}

// An IPv6 lease keeps its name only inside a registering server's prefix,
// which comes from the kernel because the configuration names none.
func TestStrippedLeasesReadsIPv6PrefixesFromTheLinks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := filepath.Join(dir, "ostiole.leases")
	content := "duid 00:01\n1790471918 1234 2001:db8:1::150 phone 00:01:aa\n1790471918 5678 2001:db8:9::150 tablet 00:01:bb\n"
	if err := os.WriteFile(leases, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &Dnsmasq{Leases: leases, Links: func() ([]network.Link, error) {
		return []network.Link{
			{Name: "eth1", Addresses: []string{"fe80::1/64", "2001:db8:1::1/64"}},
			{Name: "eth9", Addresses: []string{"2001:db8:9::1/64"}},
		}, nil
	}}
	conf := "dhcp-range=set:s6_eth1,::100,::1ff,constructor:eth1,ra-names,64,6h\n"
	got, err := d.strippedLeases(conf)
	if err != nil {
		t.Fatal(err)
	}
	want := "duid 00:01\n1790471918 1234 2001:db8:1::150 phone 00:01:aa\n1790471918 5678 2001:db8:9::150 * 00:01:bb\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
