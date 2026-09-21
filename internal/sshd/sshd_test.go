package sshd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRun is every command this package is allowed to run under test.
// Nothing reaches the machine running the test: a real `sshd -t` here
// would be reading somebody's workstation.
type fakeRun struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (f *fakeRun) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, line)
	if f.fail[line] {
		return []byte("bad configuration"), errors.New("failed")
	}
	return []byte(f.out[line]), nil
}

func (f *fakeRun) ran(want string) bool {
	for _, c := range f.calls {
		if c == want {
			return true
		}
	}
	return false
}

// router is a filesystem with sshd set up the way a cloud image leaves
// it: the main file says no passwords, and the drop-in cloud-init wrote
// says yes and wins.
func router(t *testing.T) string {
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
	write("usr/sbin/sshd", "#!/bin/sh\n")
	write("etc/ssh/sshd_config", "# comment\nInclude /etc/ssh/sshd_config.d/*.conf\nPasswordAuthentication no\n")
	write("etc/ssh/sshd_config.d/50-cloud-init.conf", "PasswordAuthentication yes\n")
	write("etc/cloud/cloud.cfg.d/95_ds-vultr.cfg", "ssh_pwauth: 1\n")
	return root
}

func TestApplyWritesTheDropInThatWins(t *testing.T) {
	t.Parallel()
	root := router(t)
	run := &fakeRun{out: map[string]string{"systemctl is-active ssh.service": "active"}}
	s := System{Run: run, FS: root}

	if err := s.Apply(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, DropIn))
	if err != nil || !strings.Contains(string(raw), "PasswordAuthentication no") {
		t.Fatalf("drop-in = %q, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, CloudInitDropIn)); err != nil || !strings.Contains(string(raw), "ssh_pwauth: false") {
		t.Errorf("cloud-init pin = %q, %v", raw, err)
	}
	if !run.ran(filepath.Join(root, "usr/sbin/sshd") + " -t") {
		t.Errorf("the configuration was not checked: %v", run.calls)
	}
	if !run.ran("systemctl reload ssh.service") {
		t.Errorf("sshd was not reloaded: %v", run.calls)
	}

	// An apply that agrees with the router changes nothing and reloads
	// nothing, because every apply runs this.
	before := len(run.calls)
	if err := s.Apply(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != before {
		t.Errorf("a second apply ran %v", run.calls[before:])
	}

	// Allowing passwords again takes both files away.
	if err := s.Apply(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{DropIn, CloudInitDropIn} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s is still there", p)
		}
	}
}

// A configuration sshd rejects is put back rather than reloaded.
func TestApplyPutsBackWhatSSHDRefuses(t *testing.T) {
	t.Parallel()
	root := router(t)
	run := &fakeRun{fail: map[string]bool{filepath.Join(root, "usr/sbin/sshd") + " -t": true}}
	s := System{Run: run, FS: root}
	err := s.Apply(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "put back") {
		t.Fatalf("err = %v, want the rejection", err)
	}
	if _, err := os.Stat(filepath.Join(root, DropIn)); err == nil {
		t.Error("a rejected drop-in was left in place")
	}
}

// A main file without the Include gets one, at the top, or the drop-in
// would be decoration.
func TestApplyAddsTheIncludeWhenMissing(t *testing.T) {
	t.Parallel()
	root := router(t)
	main := filepath.Join(root, "etc/ssh/sshd_config")
	if err := os.WriteFile(main, []byte("PasswordAuthentication yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := System{Run: &fakeRun{}, FS: root}
	if err := s.Apply(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(main)
	if !strings.HasPrefix(string(raw), "# Added by ostiole") || !strings.Contains(string(raw), sshdInclude+"\nPasswordAuthentication yes") {
		t.Errorf("main file:\n%s", raw)
	}
}

// State reads the effective configuration, and answers "passwords" for a
// router it cannot read at all: that is the value whose apply changes
// nothing.
func TestStateReadsTheEffectiveConfiguration(t *testing.T) {
	t.Parallel()
	root := router(t)
	run := &fakeRun{out: map[string]string{
		filepath.Join(root, "usr/sbin/sshd") + " -T": "port 22\npasswordauthentication yes\npermitrootlogin yes\n",
	}}
	passwords, readable := System{Run: run, FS: root}.State(context.Background())
	if !passwords || !readable {
		t.Errorf("passwords = %v, readable = %v", passwords, readable)
	}
	run.out[filepath.Join(root, "usr/sbin/sshd")+" -T"] = "port 22\npasswordauthentication no\n"
	if passwords, _ := (System{Run: run, FS: root}).State(context.Background()); passwords {
		t.Error("passwords are reported as allowed")
	}

	passwords, readable = System{Run: run, FS: t.TempDir()}.State(context.Background())
	if !passwords || readable {
		t.Errorf("a router with no sshd: passwords = %v, readable = %v", passwords, readable)
	}
}

// A router with no sshd has nothing to apply and is not an error.
func TestApplyWithoutSSHDIsFine(t *testing.T) {
	t.Parallel()
	if err := (System{Run: &fakeRun{}, FS: t.TempDir()}).Apply(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}
