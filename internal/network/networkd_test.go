package network

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadConfig(t *testing.T, path string) *model.Config {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg model.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func TestNetworkdRenderGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	if len(inputs) == 0 {
		t.Fatal("no testdata")
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files, err := (&Networkd{}).Render(loadConfig(t, in))
			if err != nil {
				t.Fatal(err)
			}
			got := files.String()
			golden := strings.TrimSuffix(in, ".json") + ".networkd"
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

// An interface whose IPv6 mode is none gets no link-local address either:
// left to itself, networkd would keep the kernel's, and "none" would show
// an IPv6 address in the live state it does not admit to configuring.
func TestRenderNoneMeansNoLinkLocal(t *testing.T) {
	t.Parallel()
	n := &Networkd{}
	files, err := n.Render(loadConfig(t, "testdata/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"eth0":    false, // slaac
		"eth1":    false, // static
		"eth1.20": true,  // none
		"eth2":    true,  // none
		"wg0":     true,  // none
	} {
		unit := files["00-ostiole-"+name+".network"]
		if got := strings.Contains(unit, "LinkLocalAddressing=no"); got != want {
			t.Errorf("%s: LinkLocalAddressing=no present %v, want %v:\n%s", name, got, want, unit)
		}
	}
}

// KeepConfiguration goes on interfaces with a leased address, spelled the
// way systemd 252 parses it: it warns on dynamic-on-stop and ignores it.
func TestRenderKeepsLeasedAddressesOnStop(t *testing.T) {
	t.Parallel()
	n := &Networkd{}
	files, err := n.Render(loadConfig(t, "testdata/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"eth0":    true,  // dhcp v4, slaac v6
		"eth1":    false, // static both ways
		"eth1.20": false, // static v4, no v6
	} {
		unit := files["00-ostiole-"+name+".network"]
		if got := strings.Contains(unit, "KeepConfiguration=dhcp-on-stop"); got != want {
			t.Errorf("%s: KeepConfiguration present %v, want %v:\n%s", name, got, want, unit)
		}
		if strings.Contains(unit, "dynamic-on-stop") {
			t.Errorf("%s: wrote the systemd 257 spelling, which RHEL 9 ignores:\n%s", name, unit)
		}
	}
}

func TestRenderRouteNeedsInterface(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/full.json")
	cfg.Routes = append(cfg.Routes, model.StaticRoute{ID: "orphan", Enabled: true, Destination: "10.9.0.0/16", Gateway: "203.0.113.1"})
	_, err := (&Networkd{}).Render(cfg)
	if err == nil || !strings.Contains(err.Error(), "orphan") {
		t.Fatalf("err = %v", err)
	}
	cfg.Routes[len(cfg.Routes)-1].Enabled = false
	if _, err := (&Networkd{}).Render(cfg); err != nil {
		t.Fatalf("disabled orphan route should be ignored: %v", err)
	}
}

// allLinks stands in for netlink where every configured device exists.
func allLinks(string) bool { return true }

type fakeCmd struct {
	calls    [][]string
	err      error
	networkd string // reply to `systemctl is-active systemd-networkd.service`
}

func (f *fakeCmd) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "systemctl" && len(args) == 2 && args[0] == "is-active" {
		if f.networkd == "active" {
			return []byte("active\n"), nil
		}
		return []byte("inactive\n"), errors.New("exit 3")
	}
	if f.err != nil {
		return []byte("boom"), f.err
	}
	return nil, nil
}

func TestApplyWritesRemovesAndReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{}
	n := &Networkd{LinkExists: allLinks, Dir: dir, Cmd: cmd}

	// Pre-existing: one stale owned file, one foreign file that must survive.
	if err := os.WriteFile(filepath.Join(dir, "00-ostiole-old.network"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-wired.network"), []byte("foreign"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := n.Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}

	snap, err := n.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.String() != files.String() {
		t.Errorf("snapshot after apply differs:\n%s", snap.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "00-ostiole-old.network")); !errors.Is(err, os.ErrNotExist) {
		t.Error("stale owned file not removed")
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "20-wired.network")); string(raw) != "foreign" {
		t.Error("foreign file touched")
	}
	if len(cmd.calls) != 2 || cmd.calls[0][1] != "reload" || cmd.calls[1][1] != "reconfigure" {
		t.Fatalf("calls = %v", cmd.calls)
	}
	if got := strings.Join(cmd.calls[1][2:], ","); got != "eth0,eth1" {
		t.Errorf("reconfigured %q, want eth0,eth1", got)
	}

	// Applying the same files again is a no-op: no reload.
	cmd.calls = nil
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("unchanged apply ran %v", cmd.calls)
	}

	// Revert via snapshot restores the previous set (here: nothing owned).
	if err := n.Apply(context.Background(), Files{}); err != nil {
		t.Fatal(err)
	}
	snap, _ = n.Snapshot()
	if len(snap) != 0 {
		t.Errorf("files left after empty apply: %v", snap.Names())
	}
}

// networkd creates the device a .netdev describes but never removes it, so
// a tunnel or VLAN dropped from the configuration keeps running, with its
// addresses, until Ostiole deletes the link itself.
func TestApplyDeletesDevicesThatLostTheirNetdev(t *testing.T) {
	t.Parallel()
	var deleted []string
	n := &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: &fakeCmd{}, DelLink: func(name string) error {
		deleted = append(deleted, name)
		return nil
	}}

	files, err := n.Render(loadConfig(t, "testdata/full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("first apply deleted %v", deleted)
	}

	// Drop the WireGuard tunnel and the VLAN, keeping everything else.
	without := func(files Files, ifaces ...string) Files {
		out := Files{}
		for name, content := range files {
			keep := true
			for _, iface := range ifaces {
				if strings.HasPrefix(name, n.prefix()+iface+".") || strings.HasPrefix(name, n.prefix()+iface+"-") {
					keep = false
				}
			}
			if keep {
				out[name] = content
			}
		}
		return out
	}
	next := without(files, "wg0", "eth1.20")
	if err := n.Apply(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deleted, ","); got != "eth1.20,wg0" {
		t.Errorf("deleted %q, want eth1.20,wg0", got)
	}
	// The tunnel's key file goes with it.
	for name := range next {
		if strings.HasSuffix(name, ".key") {
			t.Errorf("key file %s outlived its tunnel", name)
		}
	}

	// A plain NIC has no .netdev, so dropping its unit must leave the link
	// alone: it is the kernel's, not ours.
	deleted = nil
	if err := n.Apply(context.Background(), without(next, "eth2")); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Errorf("deleted %v for a physical interface", deleted)
	}
}

func TestApplyRefusesForeignNames(t *testing.T) {
	t.Parallel()
	n := &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: &fakeCmd{}}
	for _, name := range []string{"20-wired.network", "../00-ostiole-x.network", "00-ostiole-../x"} {
		if err := n.Apply(context.Background(), Files{name: "x"}); err == nil {
			t.Errorf("Apply accepted %q", name)
		}
	}
}

func TestApplyReportsReloadFailure(t *testing.T) {
	t.Parallel()
	n := &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: &fakeCmd{err: errors.New("exit 1")}}
	err := n.Apply(context.Background(), Files{"00-ostiole-eth0.network": "x"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverFindsLoopback(t *testing.T) {
	t.Parallel()
	links, err := Discover()
	if err != nil {
		t.Skipf("netlink unavailable: %v", err)
	}
	for _, l := range links {
		if l.Kind == "loopback" && l.Name == "lo" {
			if !l.Up || l.MTU == 0 {
				t.Errorf("lo = %+v", l)
			}
			return
		}
	}
	t.Fatalf("no loopback in %+v", links)
}

func TestAutoBackendFollowsNetworkd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{networkd: "inactive"}
	a := &Auto{Networkd: &Networkd{LinkExists: allLinks, Dir: dir, Cmd: cmd}, Log: slog.New(slog.DiscardHandler)}
	files, err := a.Render(loadConfig(t, "testdata/minimal.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("render = %v, %v", files, err)
	}
	if a.Name() != "none" {
		t.Errorf("Name() = %q while inactive", a.Name())
	}
	if err := a.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if snap, _ := a.Networkd.Snapshot(); len(snap) != 0 {
		t.Error("units written while networkd inactive")
	}
	for _, c := range cmd.calls {
		if c[0] == "networkctl" {
			t.Errorf("networkctl called while inactive: %v", c)
		}
	}

	cmd.networkd = "active"
	if a.Name() != "systemd-networkd" {
		t.Errorf("Name() = %q while active", a.Name())
	}
	if err := a.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if snap, _ := a.Networkd.Snapshot(); len(snap) != len(files) {
		t.Errorf("units not written while active: %v", snap.Names())
	}
}

// The bond modes and hash policies Ostiole offers have to be the names the
// kernel itself uses, or networkd writes a value the driver rejects at
// link-up rather than at apply.
func TestBondSettingsUseKernelNames(t *testing.T) {
	t.Parallel()
	for _, mode := range model.BondModes {
		if got := netlink.StringToBondMode(string(mode)); got == netlink.BOND_MODE_UNKNOWN {
			t.Errorf("bond mode %q is not a mode the kernel knows", mode)
		}
	}
	for _, policy := range model.HashPolicies {
		if got := netlink.StringToBondXmitHashPolicy(policy); got == netlink.BOND_XMIT_HASH_POLICY_UNKNOWN {
			t.Errorf("transmit hash policy %q is not one the kernel knows", policy)
		}
	}
}

// A bridge or bond member must end up attached to its master, and never
// carry addressing of its own.
func TestRenderEnslavesMembers(t *testing.T) {
	t.Parallel()
	files, err := (&Networkd{}).Render(loadConfig(t, "testdata/aggregated.json"))
	if err != nil {
		t.Fatal(err)
	}
	for member, want := range map[string]string{
		"eth0": "Bond=bond0",
		"eth1": "Bond=bond0",
		"eth2": "Bridge=br0",
		"eth3": "Bridge=br0",
		"eth4": "Bond=bond1",
	} {
		unit := files["00-ostiole-"+member+".network"]
		if !strings.Contains(unit, want) {
			t.Errorf("%s is not attached to its master:\n%s", member, unit)
		}
		if strings.Contains(unit, "Address=") || strings.Contains(unit, "DHCP=") {
			t.Errorf("%s carries addressing of its own:\n%s", member, unit)
		}
		if !strings.Contains(unit, "LinkLocalAddressing=no") {
			t.Errorf("%s should have no link-local address:\n%s", member, unit)
		}
	}
	// The bond itself keeps the addressing and comes up without carrier,
	// so a switch that is not ready yet does not leave it unconfigured.
	bond := files["00-ostiole-bond0.network"]
	if !strings.Contains(bond, "ConfigureWithoutCarrier=yes") || !strings.Contains(bond, "DHCP=ipv4") {
		t.Errorf("bond0 unit:\n%s", bond)
	}
}

// Every plain key in a .network file belongs to the section above it, so
// a VLAN written after [DHCPv6] opens would be read as a DHCP setting and
// rejected. This caught exactly that.
func TestRenderKeepsKeysInTheirSection(t *testing.T) {
	t.Parallel()
	files, err := (&Networkd{}).Render(loadConfig(t, "testdata/delegated.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, unit := range files {
		if !strings.HasSuffix(name, ".network") {
			continue
		}
		section := ""
		for _, line := range strings.Split(unit, "\n") {
			switch {
			case strings.HasPrefix(line, "["):
				section = strings.Trim(line, "[]")
			case strings.HasPrefix(line, "VLAN=") && section != "Network":
				t.Errorf("%s: VLAN belongs in [Network], found it in [%s]", name, section)
			case strings.HasPrefix(line, "Address=") && section != "Network":
				t.Errorf("%s: Address belongs in [Network], found it in [%s]", name, section)
			}
		}
	}
}

// A WAN asks its ISP for a prefix and the inside interfaces each take a
// piece of it, without networkd also advertising it: dnsmasq does that.
func TestRenderPrefixDelegation(t *testing.T) {
	t.Parallel()
	files, err := (&Networkd{}).Render(loadConfig(t, "testdata/delegated.json"))
	if err != nil {
		t.Fatal(err)
	}
	wan := files["00-ostiole-wan0.network"]
	for _, want := range []string{"PrefixDelegationHint=::/56", "WithoutRA=solicit", "UseDelegatedPrefix=yes"} {
		if !strings.Contains(wan, want) {
			t.Errorf("wan0 unit is missing %q:\n%s", want, wan)
		}
	}
	for unit, subnet := range map[string]string{
		"00-ostiole-lan0.network":    "SubnetId=0x1",
		"00-ostiole-lan0.20.network": "SubnetId=0x2",
	} {
		got := files[unit]
		if !strings.Contains(got, "DHCPPrefixDelegation=yes") || !strings.Contains(got, subnet) {
			t.Errorf("%s:\n%s", unit, got)
		}
		if !strings.Contains(got, "Announce=no") {
			t.Errorf("%s should leave advertisements to dnsmasq:\n%s", unit, got)
		}
	}
}

// A dialled session's interface belongs to pppd; the Ethernet under it is
// brought up and left bare.
func TestRenderLeavesPPPoEToPppd(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/delegated.json")
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: "ppp0", Zone: "wan", Enabled: true,
		IPv4:  model.IPv4{Mode: model.AddrPPP},
		IPv6:  model.IPv6{Mode: model.AddrNone},
		PPPoE: &model.PPPoE{Parent: "eth9", Username: "someone", Password: "secret"},
	})
	files, err := (&Networkd{}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["00-ostiole-ppp0.network"]; ok {
		t.Error("networkd was given a unit for an interface pppd owns")
	}
	parent := files["00-ostiole-eth9.network"]
	if !strings.Contains(parent, "LinkLocalAddressing=no") || !strings.Contains(parent, "PPPoE session ppp0") {
		t.Errorf("the link under the session:\n%s", parent)
	}
	if strings.Contains(parent, "Address=") || strings.Contains(parent, "DHCP=") {
		t.Errorf("the link under the session carries addressing:\n%s", parent)
	}
}

// tailscaled creates its interface and addresses it from the tailnet, so
// networkd is given nothing for it at all.
func TestRenderLeavesTailscaleToTheDaemon(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.Zones = append(cfg.Zones, model.Zone{Name: "tailnet"})
	cfg.Interfaces = append(cfg.Interfaces, model.Interface{
		Name: model.TailscaleDevice, Zone: "tailnet", Enabled: true,
		IPv4:      model.IPv4{Mode: model.AddrNone},
		IPv6:      model.IPv6{Mode: model.AddrNone},
		Tailscale: &model.Tailscale{Port: 41641},
	})
	files, err := (&Networkd{}).Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if strings.Contains(name, model.TailscaleDevice) {
			t.Errorf("networkd was given %s for an interface tailscaled owns", name)
		}
	}
}

// hostapd creates a network's device, but everything after that is
// networkd's: a bridge port or an interface with an address.
func TestRenderWirelessNetworksAreInterfaces(t *testing.T) {
	t.Parallel()
	files, err := (&Networkd{}).Render(loadConfig(t, "testdata/wireless.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if strings.HasSuffix(name, ".netdev") && strings.Contains(name, "ap0") {
			t.Errorf("networkd was given %s for a device hostapd creates", name)
		}
	}
	if got := files["00-ostiole-ap0.network"]; !strings.Contains(got, "Bridge=br-lan") {
		t.Errorf("ap0 is not a port on br-lan:\n%s", got)
	}
	if got := files["00-ostiole-ap1.network"]; !strings.Contains(got, "Address=10.99.0.1/24") {
		t.Errorf("ap1 has no address of its own:\n%s", got)
	}
	radio := files["00-ostiole-wlp3s0.network"]
	if !strings.Contains(radio, "Name=wlp3s0") || strings.Contains(radio, "Bridge=") || strings.Contains(radio, "Address=") {
		t.Errorf("the radio is not an idle port:\n%s", radio)
	}
}

// The device that carries a shaped interface's incoming traffic is
// Ostiole's own doing, not an interface anybody configured. Leaving it in
// the list put it on the interfaces page as "not managed" and, worse, in
// the setup wizard's picker as a candidate LAN.
func TestDiscoverHidesTheShapingHelpers(t *testing.T) {
	t.Parallel()
	attrs := func(name string) netlink.LinkAttrs { return netlink.LinkAttrs{Name: name} }
	for _, tc := range []struct {
		link netlink.Link
		want bool
	}{
		{&netlink.Ifb{LinkAttrs: attrs("ifb-enp1s0")}, true},
		{&netlink.Ifb{LinkAttrs: attrs("ifb-enp0s3-aca3")}, true},
		// Somebody else's, reported as honestly as any other unmanaged link.
		{&netlink.Ifb{LinkAttrs: attrs("ifb0")}, false},
		// The name alone is not enough; the kind has to match too.
		{&netlink.Dummy{LinkAttrs: attrs("ifb-enp1s0")}, false},
		{&netlink.Device{LinkAttrs: attrs("enp1s0")}, false},
	} {
		if got := helper(tc.link); got != tc.want {
			t.Errorf("helper(%s %s) = %v, want %v",
				tc.link.Type(), tc.link.Attrs().Name, got, tc.want)
		}
	}
}

// And the real thing, where a kernel allows it: a helper device the
// shaper would create is not in what Discover reports.
func TestDiscoverHidesAHelperInTheKernel(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("creating a link needs root")
	}
	name := model.IFBName("ostdisc0")
	if err := netlink.LinkAdd(&netlink.Ifb{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		t.Skipf("cannot create an ifb here: %v", err)
	}
	t.Cleanup(func() {
		if l, err := netlink.LinkByName(name); err == nil {
			_ = netlink.LinkDel(l)
		}
	})
	links, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.Name == name {
			t.Fatalf("%s is still reported as an interface: %+v", name, l)
		}
	}
}

// The card's own interface is the radio, which the Wireless page shows;
// the networks hostapd makes on it come after it and are listed.
func TestDiscoverHidesTheRadioItself(t *testing.T) {
	t.Parallel()
	links := []Link{
		{Name: "eth0", Index: 2},
		{Name: "wlan0", Index: 3, Wireless: true, Phy: "phy0"},
		{Name: "ap0", Index: 6, Wireless: true, Phy: "phy0"},
		{Name: "ap1", Index: 7, Wireless: true, Phy: "phy0"},
		{Name: "wlan1", Index: 8, Wireless: true, Phy: "phy1"},
	}
	var got []string
	for _, l := range withoutRadios(links) {
		got = append(got, l.Name)
	}
	if want := "eth0 ap0 ap1"; strings.Join(got, " ") != want {
		t.Errorf("kept %q, want %q", got, want)
	}
}

// A .network for a device that does not exist yet is written and loaded
// but not reconfigured: hostapd creates ap0 after networkd has been told
// about it, and networkctl refuses a name it cannot resolve.
func TestApplyReconfiguresOnlyLinksThatExist(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{}
	n := &Networkd{Dir: t.TempDir(), Cmd: cmd, LinkExists: func(name string) bool { return name == "eth0" }}
	files := Files{
		"00-ostiole-eth0.network": "[Match]\nName=eth0\n",
		"00-ostiole-ap0.network":  "[Match]\nName=ap0\n",
	}
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 2 || cmd.calls[0][1] != "reload" {
		t.Fatalf("calls = %v", cmd.calls)
	}
	if got := strings.Join(cmd.calls[1][1:], " "); got != "reconfigure eth0" {
		t.Errorf("second call = %q, want only eth0 reconfigured", got)
	}

	// With no device at all there is a reload and nothing to reconfigure.
	cmd.calls = nil
	n.LinkExists = func(string) bool { return false }
	files["00-ostiole-eth0.network"] += "[Network]\nDHCP=yes\n"
	if err := n.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 1 || cmd.calls[0][1] != "reload" {
		t.Errorf("calls = %v, want a reload alone", cmd.calls)
	}
}

// A renew asks the server to extend the lease; a release reconfigures the
// link, which drops the lease and starts discovery over.
func TestRenewRunsNetworkctl(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{}
	n := &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: cmd}
	if err := n.Renew(context.Background(), "eth0", false); err != nil {
		t.Fatal(err)
	}
	if err := n.Renew(context.Background(), "eth0", true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"networkctl", "renew", "eth0"}, {"networkctl", "reconfigure", "eth0"}}
	if !reflect.DeepEqual(cmd.calls, want) {
		t.Errorf("calls = %v, want %v", cmd.calls, want)
	}

	failing := &fakeCmd{err: errors.New("exit 1")}
	n = &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: failing}
	err := n.Renew(context.Background(), "eth0", false)
	if err == nil || !strings.Contains(err.Error(), "networkctl renew eth0") {
		t.Errorf("err = %v", err)
	}
}

// Without networkd there is no lease to renew, and the answer says so
// rather than running networkctl against a stack that is not there.
func TestAutoRenewNeedsNetworkd(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{networkd: "inactive"}
	a := &Auto{Networkd: &Networkd{LinkExists: allLinks, Dir: t.TempDir(), Cmd: cmd}, Log: slog.New(slog.DiscardHandler)}
	if err := a.Renew(context.Background(), "eth0", false); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
	for _, c := range cmd.calls {
		if c[0] == "networkctl" {
			t.Errorf("networkctl called while inactive: %v", c)
		}
	}
	cmd.networkd = "active"
	if err := a.Renew(context.Background(), "eth0", true); err != nil {
		t.Fatal(err)
	}
	last := cmd.calls[len(cmd.calls)-1]
	if !reflect.DeepEqual(last, []string{"networkctl", "reconfigure", "eth0"}) {
		t.Errorf("last call = %v", last)
	}
}

// A WireGuard private key is readable by networkd's group and nobody
// else; every other unit is world-readable, as systemd expects. Nothing
// asserted the mode before, so a refactor could have made a key 0644
// with every test green.
func TestWriteFileKeepsKeysPrivate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := filepath.Join(dir, "wg0.key")
	unit := filepath.Join(dir, "00-ostiole-wg0.netdev")
	for path, content := range map[string]string{key: "secret", unit: "[NetDev]"} {
		if err := writeFile(path, content); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Stat(key); st.Mode().Perm() != 0o640 {
		t.Errorf("key mode = %o, want 640", st.Mode().Perm())
	}
	if st, _ := os.Stat(unit); st.Mode().Perm() != 0o644 {
		t.Errorf("unit mode = %o, want 644", st.Mode().Perm())
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".*.tmp")); len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}
