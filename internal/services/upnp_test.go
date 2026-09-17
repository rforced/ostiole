package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rforced/ostiole/internal/network"
	"github.com/rforced/ostiole/internal/nft"
)

// The miniupnpd configuration is golden-tested on the inputs that ask for
// the service; the rest must produce no files at all.
func TestRenderUPnPGolden(t *testing.T) {
	t.Parallel()
	inputs, _ := filepath.Glob("testdata/*.json")
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := loadConfig(t, in)
			u := &UPnP{Dir: UPnPDir, Leases: UPnPLeaseFile, UUID: "00000000-0000-5000-8000-000000000000"}
			files, err := u.Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			golden := strings.TrimSuffix(in, ".json") + ".upnp"
			if !nft.UPnPEnabled(cfg) {
				if len(files) != 0 {
					t.Fatalf("mapping service off but rendered %v", files.Names())
				}
				return
			}
			got := files.String()
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

// The chain names in the configuration are the ones the renderer emits.
// They are the whole reason miniupnpd can write into Ostiole's table, and
// a rename on one side alone would be silent: the daemon would answer
// every request and no packet would pass.
func TestUPnPConfNamesTheRenderedChains(t *testing.T) {
	t.Parallel()
	u := &UPnP{Dir: UPnPDir, Leases: UPnPLeaseFile}
	files, err := u.Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	conf := files[upnpConfName]
	for _, want := range []string{
		"upnp_table_name=ostiole",
		"upnp_nat_table_name=ostiole",
		"upnp_forward_chain=" + nft.UPnPForwardChain,
		"upnp_nat_chain=" + nft.UPnPPreroutingChain,
		"upnp_nat_postrouting_chain=" + nft.UPnPPostroutingChain,
	} {
		if !strings.Contains(conf, want+"\n") {
			t.Errorf("configuration is missing %q\n%s", want, conf)
		}
	}
	ruleset, err := nft.Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, chain := range []string{nft.UPnPForwardChain, nft.UPnPPreroutingChain, nft.UPnPPostroutingChain} {
		if !strings.Contains(ruleset, "chain "+chain+" {") {
			t.Errorf("ruleset does not define chain %q", chain)
		}
	}
}

// A router that does not run the service, and never has, must be left alone:
// every apply runs every backend, and the daemon's sandbox makes /etc
// read-only apart from the few paths it is given. This is the bug that
// made an unrelated apply fail on a router with no PPPoE.
func TestUPnPApplyDoesNothingWhenUnused(t *testing.T) {
	t.Parallel()
	cmd := &fakeCmd{}
	dir := filepath.Join(t.TempDir(), "absent")
	u := &UPnP{Dir: dir, Leases: UPnPLeaseFile, Cmd: cmd}
	if err := u.Apply(context.Background(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("ran %v on a router that does not use the service", cmd.calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("created %s on a router that does not use the service", dir)
	}
}

// Applying restarts the daemon even when its configuration has not
// changed: the apply that got here rebuilt `table inet ostiole`, so the
// rules miniupnpd had inserted are gone and it has to put them back.
func TestUPnPApplyRestartsOnEveryApply(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{installed: true}
	u := &UPnP{Dir: dir, Leases: UPnPLeaseFile, Cmd: cmd}
	files, err := u.Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := u.Apply(context.Background(), files); err != nil {
			t.Fatal(err)
		}
	}
	restarts := 0
	for _, c := range cmd.calls {
		if strings.Join(c, " ") == "systemctl restart "+UPnPUnit {
			restarts++
		}
	}
	if restarts != 2 {
		t.Errorf("restarted %d times, want one per apply", restarts)
	}
	raw, err := os.ReadFile(filepath.Join(dir, upnpConfName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ext_ifname=eth0") {
		t.Errorf("configuration not written:\n%s", raw)
	}
}

// Turning the service off stops the unit and takes the configuration away,
// so a daemon nobody starts again cannot be started by something else.
func TestUPnPApplyStopsWhenSwitchedOff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := &fakeCmd{installed: true}
	u := &UPnP{Dir: dir, Leases: UPnPLeaseFile, Cmd: cmd}
	files, err := u.Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if err := u.Apply(context.Background(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", UPnPUnit) {
		t.Errorf("did not stop the unit: %v", cmd.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, upnpConfName)); !os.IsNotExist(err) {
		t.Errorf("left the configuration behind")
	}
}

// Applying without miniupnpd installed has to fail loudly. The engine
// rolls the whole apply back on this error, which is the right outcome:
// the ruleset would carry chains nothing ever fills in.
func TestUPnPApplyNeedsTheDaemon(t *testing.T) {
	t.Parallel()
	u := &UPnP{Dir: t.TempDir(), Leases: UPnPLeaseFile, Cmd: &fakeCmd{}}
	files, err := u.Render(loadConfig(t, "testdata/upnp.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = u.Apply(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "services setup --with-upnp") {
		t.Errorf("error was %v, want the setup instruction", err)
	}
}

// The device id has to survive a restart, or every client decides it has
// met a new gateway and drops what it had.
func TestUPnPUUIDIsStable(t *testing.T) {
	t.Parallel()
	u := NewUPnP()
	if first, second := u.uuid(), u.uuid(); first != second {
		t.Errorf("uuid changed between reads: %s then %s", first, second)
	}
	if got := (&UPnP{UUID: "fixed"}).uuid(); got != "fixed" {
		t.Errorf("uuid() = %q, want the override", got)
	}
	if got := uuidV5(upnpNamespace, "abc"); len(got) != 36 || got[14] != '5' {
		t.Errorf("uuidV5 = %q, want a 36-character version 5 uuid", got)
	}
}

func TestParseMappings(t *testing.T) {
	t.Parallel()
	// The first line is the short form, the second carries a remote host,
	// the third a description with a colon in it, the fourth never expires.
	in := strings.NewReader(`UDP:19132:192.168.1.50:19132:1789000000:Minecraft
TCP:32400:203.0.113.9:192.168.1.20:32400:1789000123:Plex
TCP:8443:192.168.1.7:8443:1789000456:Camera: front door
TCP:9000:192.168.1.8:9000:0:Forever
nonsense
`)
	got, err := ParseMappings(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("parsed %d mappings, want 4: %+v", len(got), got)
	}
	// Sorted by external port, so the camera comes first.
	if got[0].ExternalPort != 8443 || got[0].Internal != "192.168.1.7" || got[0].Description != "Camera: front door" {
		t.Errorf("short form with a colon in the description parsed as %+v", got[0])
	}
	if got[1].ExternalPort != 9000 || got[1].Expires != nil {
		t.Errorf("a mapping that does not expire parsed as %+v", got[1])
	}
	if got[2].ExternalPort != 19132 || got[2].Protocol != "UDP" || got[2].InternalPort != 19132 {
		t.Errorf("short form parsed as %+v", got[2])
	}
	if got[3].ExternalPort != 32400 || got[3].Internal != "192.168.1.20" || got[3].Description != "Plex" {
		t.Errorf("remote-host form parsed as %+v", got[3])
	}
	if got[3].Expires == nil {
		t.Errorf("remote-host form lost its expiry: %+v", got[3])
	}
}

// A missing lease file is an empty list: miniupnpd writes it when the
// first mapping is made, so a router with none has no file at all.
func TestReadMappingsWithoutAFile(t *testing.T) {
	t.Parallel()
	got, err := (&UPnP{Leases: filepath.Join(t.TempDir(), "absent.leases")}).ReadMappings()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}

// The unit has to keep miniupnpd in the foreground, or systemd calls a
// forked daemon dead the moment it starts.
func TestUPnPUnitRunsInTheForeground(t *testing.T) {
	t.Parallel()
	unit := UPnPUnitContent("/usr/sbin/miniupnpd", "/etc/miniupnpd/ostiole.conf")
	if !strings.Contains(unit, "ExecStart=/usr/sbin/miniupnpd -d -f /etc/miniupnpd/ostiole.conf") {
		t.Errorf("unit does not run the daemon in the foreground:\n%s", unit)
	}
	if !strings.Contains(unit, "After=network.target ostiole-firewall.service") {
		t.Errorf("unit does not wait for the ruleset:\n%s", unit)
	}
}

// An iptables build answers every request and passes no packet, because
// its rules go somewhere Ostiole's chains never see. Refusing it at setup
// is the only place the difference is visible.
func TestNFTablesBuildCheck(t *testing.T) {
	t.Parallel()
	// Something that is not an ELF at all is taken on trust.
	plain := filepath.Join(t.TempDir(), "miniupnpd")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := nftablesBuild(plain); err != nil {
		t.Errorf("a non-ELF binary was refused: %v", err)
	}
	// A real dynamically linked binary that is plainly not miniupnpd links
	// no libnftnl, so it is refused.
	if _, err := os.Stat("/usr/bin/nft"); err == nil {
		if err := nftablesBuild("/usr/bin/ls"); err == nil {
			t.Errorf("a binary with no libnftnl was accepted")
		}
	}
}
