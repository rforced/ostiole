package services

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ostiole/internal/network"
)

func TestOpenVPNFixtureIsValid(t *testing.T) {
	t.Parallel()
	if err := loadConfig(t, "testdata/openvpn.json").Validate(); err != nil {
		t.Fatal(err)
	}
}

// explicit-exit-notify is for udp only: a tcp remote logs a NOTICE on every
// start, so one tcp remote leaves it out.
func TestOpenVPNExitNotifyOnlyOverUDP(t *testing.T) {
	t.Parallel()
	files, err := (&OpenVPN{Dir: "/etc/ostiole/openvpn"}).Render(loadConfig(t, "testdata/openvpn.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(files["ovpn0.conf"], "explicit-exit-notify") {
		t.Error("ovpn0 has a tcp remote and asks for explicit-exit-notify")
	}
	if !strings.Contains(files["ovpn1.conf"], "\nexplicit-exit-notify 1\n") {
		t.Error("ovpn1 is udp only and has no explicit-exit-notify")
	}
	if _, ok := files["ovpn2.conf"]; ok {
		t.Error("a disabled tunnel was rendered")
	}
	if _, ok := files["ovpn1.auth"]; ok {
		t.Error("a tunnel without a username has a credentials file")
	}
	// The router's own default route and resolvers are never the server's.
	for name, conf := range files {
		if !strings.HasSuffix(name, ".conf") {
			continue
		}
		for _, want := range []string{`pull-filter ignore "redirect-gateway"`, `pull-filter ignore "dhcp-option"`, `pull-filter ignore "dns "`} {
			if !strings.Contains(conf, want+"\n") {
				t.Errorf("%s lacks %s", name, want)
			}
		}
		for _, never := range []string{"persist-key", "script-security", "\nup ", "comp-", "cipher ", "mssfix"} {
			if strings.Contains(conf, never) {
				t.Errorf("%s has %q", name, never)
			}
		}
	}
}

func TestOpenVPNApply(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "openvpn")
	cmd := &fakeCmd{installed: true}
	o := &OpenVPN{Dir: dir, Cmd: cmd}
	files, err := o.Render(loadConfig(t, "testdata/openvpn.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Preflight(ctx, files); err != nil {
		t.Fatal(err)
	}
	if err := o.Apply(ctx, files); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ovpn0.conf", "ovpn0.auth", "ovpn1.conf"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600", name, info.Mode().Perm())
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the directory is %v (%v), want 0700", info.Mode().Perm(), err)
	}
	for _, unit := range []string{"ostiole-openvpn@ovpn0.service", "ostiole-openvpn@ovpn1.service"} {
		if !cmd.has("systemctl", "enable", "--now", "--no-block", unit) {
			t.Errorf("%s not enabled: %v", unit, cmd.calls)
		}
	}

	// The same files again: nothing restarts.
	cmd.calls = nil
	if err := o.Apply(ctx, files); err != nil {
		t.Fatal(err)
	}
	for _, c := range cmd.calls {
		if c[0] == "systemctl" && (c[1] == "restart" || c[1] == "enable") {
			t.Errorf("an unchanged apply ran %v", c)
		}
	}

	// A changed password restarts its tunnel.
	next := network.Files{"ovpn0.conf": files["ovpn0.conf"], "ovpn0.auth": "lab-user\nnew password\n", "ovpn1.conf": files["ovpn1.conf"]}
	cmd.calls = nil
	if err := o.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "restart", "--no-block", "ostiole-openvpn@ovpn0.service") {
		t.Errorf("a changed password did not restart: %v", cmd.calls)
	}

	// Dropping a tunnel stops it and takes its files.
	delete(next, "ovpn1.conf")
	cmd.calls = nil
	if err := o.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	if !cmd.has("systemctl", "disable", "--now", "ostiole-openvpn@ovpn1.service") {
		t.Errorf("a removed tunnel was not stopped: %v", cmd.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "ovpn1.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ovpn1.conf is still there: %v", err)
	}

	// No tunnels left: the last one goes too.
	if err := o.Apply(ctx, network.Files{}); err != nil {
		t.Fatal(err)
	}
	if left, _ := o.Snapshot(); len(left) != 0 {
		t.Errorf("left behind %v", left.Names())
	}
}

// A router with no tunnel must not have the directory created or
// systemctl run: an apply that has nothing to do with OpenVPN cannot be
// allowed to fail on it.
func TestOpenVPNApplyWithNothingTouchesNothing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "openvpn")
	cmd := &fakeCmd{}
	o := &OpenVPN{Dir: dir, Cmd: cmd}
	if err := o.Apply(context.Background(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if err := o.Preflight(context.Background(), network.Files{}); err != nil {
		t.Fatal(err)
	}
	if len(cmd.calls) != 0 {
		t.Errorf("ran %v", cmd.calls)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("created %s", dir)
	}
}

func TestOpenVPNPreflightWithoutTheUnit(t *testing.T) {
	t.Parallel()
	o := &OpenVPN{Dir: t.TempDir(), Cmd: &fakeCmd{}}
	err := o.Preflight(context.Background(), network.Files{"ovpn0.conf": "client\n"})
	if !errors.Is(err, errOpenVPNMissing) {
		t.Errorf("Preflight = %v, want %v", err, errOpenVPNMissing)
	}
	if err := o.Apply(context.Background(), network.Files{"ovpn0.conf": "client\n"}); !errors.Is(err, errOpenVPNMissing) {
		t.Errorf("Apply = %v, want %v", err, errOpenVPNMissing)
	}
}

func TestOpenVPNUnit(t *testing.T) {
	t.Parallel()
	unit := OpenVPNUnitContent("/usr/sbin/openvpn", "/etc/ostiole/openvpn")
	for _, want := range []string{
		"\nType=simple\n",
		"\nExecStart=/usr/sbin/openvpn --suppress-timestamps --nobind --config /etc/ostiole/openvpn/%i.conf\n",
		"\nRuntimeDirectory=ostiole-openvpn\n",
		"\nRuntimeDirectoryPreserve=yes\n",
		"\nCapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_SETPCAP\n",
		"\nDeviceAllow=/dev/net/tun rw\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", strings.TrimSpace(want), unit)
		}
	}
	if strings.Contains(unit, "NoNewPrivileges") {
		t.Errorf("NoNewPrivileges keeps a daemon in init_t under SELinux:\n%s", unit)
	}
}

func TestSetupOpenVPN(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	opts := SetupOptions{
		OpenVPN: true, OpenVPNBinary: "/usr/sbin/openvpn", ConfigDir: filepath.Join(root, "etc"),
		UnitDir: filepath.Join(root, "units"), Run: &fakeRunner{}, NoRestart: true,
	}
	if err := Setup(context.Background(), nil, opts, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "units", OpenVPNUnit)); err != nil {
		t.Errorf("no unit: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "etc", "openvpn")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("config directory: %v", err)
	}
}
