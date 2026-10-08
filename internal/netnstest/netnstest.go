// Package netnstest runs a test inside a fresh unprivileged user and
// network namespace, where it has the network capabilities without being
// root and can change links and routes without touching the host.
package netnstest

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ostiole/internal/netlink"
	"ostiole/internal/testenv"
)

// env marks the copy of the test binary that runs inside the namespace.
const env = "OSTIOLE_NETNS"

// Enter re-runs the calling test inside a new namespace and returns false,
// because the copy inside has done the test. In that copy it returns true.
// It skips when unshare or a command the test needs is missing, and where
// the sandbox forbids namespaces, as Ubuntu's AppArmor does by default;
// testenv.Unavailable fails the test instead where every test must run.
func Enter(t *testing.T, needs ...string) bool {
	t.Helper()
	if os.Getenv(env) != "" {
		return true
	}
	for _, bin := range append([]string{"unshare"}, needs...) {
		if _, err := exec.LookPath(bin); err != nil {
			testenv.Unavailable(t, "%s not installed", bin)
		}
	}
	cmd := exec.CommandContext(t.Context(), "unshare", "-Urn", os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v") //nolint:gosec // this test binary, run again
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		testenv.Unavailable(t, "unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
	return false
}

// Ruleset loads an nft ruleset into the namespace. A test that calls it
// asks Enter for nft.
func Ruleset(t *testing.T, ruleset string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(ruleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft: %v\n%s", err, out)
	}
}

// Dummy brings up a dummy link carrying addrs. What it is sent is dropped
// by the driver, after the kernel's hooks have seen it.
func Dummy(t *testing.T, name string, addrs ...string) netlink.Link {
	t.Helper()
	if err := netlink.AddLink(name, "dummy"); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	link := Link(t, name)
	if err := netlink.SetLinkUp(link.Index); err != nil {
		t.Fatalf("bring %s up: %v", name, err)
	}
	for _, addr := range addrs {
		p, err := netip.ParsePrefix(addr)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.AddAddr(link.Index, p); err != nil {
			t.Fatalf("address %s on %s: %v", addr, name, err)
		}
	}
	return Link(t, name)
}

// Link looks a link up by name.
func Link(t *testing.T, name string) netlink.Link {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	return link
}

// NewNS makes a network namespace beside the one the test runs in, the
// far side of a cable, and returns a file that holds it open. Do runs code
// in it.
func NewNS(t *testing.T) *os.File {
	t.Helper()
	var ns *os.File
	var err error
	within(t, func() error { return unix.Unshare(unix.CLONE_NEWNET) }, func() {
		ns, err = os.Open("/proc/thread-self/ns/net")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ns.Close() })
	return ns
}

// Do runs fn in the namespace ns holds. A socket fn opens belongs to that
// namespace for good, so what fn sets up stays there.
func Do(t *testing.T, ns *os.File, fn func()) {
	t.Helper()
	within(t, func() error { return unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET) }, fn)
}

// within runs fn on this goroutine's thread after enter moves the thread
// to another namespace, and moves it back after.
func within(t *testing.T, enter func() error, fn func()) {
	t.Helper()
	runtime.LockOSThread()
	here, err := os.Open("/proc/thread-self/ns/net")
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer func() { _ = here.Close() }()
	if err := enter(); err != nil {
		runtime.UnlockOSThread()
		t.Fatalf("enter a namespace: %v", err)
	}
	defer func() {
		// A thread that cannot get back stays locked, and ends with its
		// goroutine rather than run anything else in the wrong namespace.
		if err := unix.Setns(int(here.Fd()), unix.CLONE_NEWNET); err != nil {
			panic(fmt.Sprintf("back to the test's namespace: %v", err))
		}
		runtime.UnlockOSThread()
	}()
	fn()
}
