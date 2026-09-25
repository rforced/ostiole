// Package netnstest runs a test inside a fresh unprivileged user and
// network namespace, where it has the network capabilities without being
// root and can change links and routes without touching the host.
package netnstest

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

// env marks the copy of the test binary that runs inside the namespace.
const env = "OSTIOLE_NETNS"

// Enter re-runs the calling test inside a new namespace and returns false,
// because the copy inside has done the test. In that copy it returns true.
// It skips when unshare or a command the test needs is missing, or where
// the sandbox forbids namespaces, which is what GitHub's runners do.
func Enter(t *testing.T, needs ...string) bool {
	t.Helper()
	if os.Getenv(env) != "" {
		return true
	}
	for _, bin := range append([]string{"unshare"}, needs...) {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	cmd := exec.CommandContext(t.Context(), "unshare", "-Urn", os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v") //nolint:gosec // this test binary, run again
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return false
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
	return false
}

// Dummy brings up a dummy link carrying addrs. What it is sent is dropped
// by the driver, after the kernel's hooks have seen it.
func Dummy(t *testing.T, name string, addrs ...string) netlink.Link {
	t.Helper()
	link := &netlink.Dummy{Name: name}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("bring %s up: %v", name, err)
	}
	for _, addr := range addrs {
		a, err := netlink.ParseAddr(addr)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.AddrAdd(link, a); err != nil {
			t.Fatalf("address %s on %s: %v", addr, name, err)
		}
	}
	return link
}
