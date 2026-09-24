package nft

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/rforced/ostiole/internal/model"
)

// namespaced returns an Exec that runs nft inside a fresh unprivileged user
// and network namespace, or skips the test if that is not possible here
// (no nft, no unshare, or nested containers without userns).
func namespaced(t *testing.T) *Exec {
	t.Helper()
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	x := &Exec{Wrap: []string{"unshare", "-Urn"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := x.Version(ctx); err != nil {
		t.Skipf("cannot run nft in an unprivileged namespace: %v", err)
	}
	return x
}

func TestGoldenRulesetsLoadInKernel(t *testing.T) {
	x := namespaced(t)
	for _, in := range goldenCases(t) {
		name := strings.TrimSuffix(filepath.Base(in), ".json")
		t.Run(name, func(t *testing.T) {
			cfg := loadConfig(t, in)
			ruleset, err := Render(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := x.Check(ctx, ruleset); err != nil {
				t.Fatalf("nft -c rejected rendered ruleset:\n%v\n--- ruleset ---\n%s", err, ruleset)
			}
		})
	}
}

func TestApplyAndReadBackCounters(t *testing.T) {
	namespaced(t) // skip if namespaces are unavailable
	cfg := loadConfig(t, "testdata/full.json")
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Each unshare invocation is a fresh namespace, so apply and list must
	// happen in one shell.
	script := "nft -f - <<'EOF' && nft -j list table inet ostiole\n" + ruleset + "EOF\n"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apply+list failed: %v\n%s", err, out)
	}
	counters, err := ParseCounters(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"allow-lan", "web-from-servers", "block-smtp", "dns-to-self", "pf-web", "nat-lan"} {
		if _, ok := counters[id]; !ok {
			t.Errorf("counter for %q not found; have %v", id, keys(counters))
		}
	}
	if _, ok := counters["input/default-drop"]; !ok {
		t.Errorf("baseline counter input/default-drop missing")
	}
}

func TestListTableJSONNoTable(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := x.ListTableJSON(ctx)
	if !errors.Is(err, ErrNoTable) {
		t.Fatalf("err = %v, want ErrNoTable", err)
	}
}

func TestCheckReportsSyntaxErrors(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := x.Check(ctx, "table inet ostiole {\n chain input { type filter hook input priority filter; policy bogus; }\n}\n")
	var nerr *Error
	if !errors.As(err, &nerr) || nerr.Stderr == "" {
		t.Fatalf("expected *Error with stderr, got %v", err)
	}
}

func keys(c Counters) []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}
	return out
}

// The bootstrap ruleset is what a freshly installed router runs until its
// first apply, so it has to load on a real kernel like the rendered ones.
func TestBootstrapLoadsInKernel(t *testing.T) {
	x := namespaced(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, ports := range [][]uint16{{443, 22}, {8443}, nil} {
		ruleset := Bootstrap(ports)
		if err := x.Check(ctx, ruleset); err != nil {
			t.Fatalf("nft -c rejected the bootstrap ruleset for %v:\n%v\n--- ruleset ---\n%s", ports, err, ruleset)
		}
	}
	for _, in := range []string{"testdata/minimal.json", "testdata/full.json"} {
		ruleset := Fallback(loadConfig(t, in), []uint16{9443, 22})
		if err := x.Check(ctx, ruleset); err != nil {
			t.Fatalf("nft -c rejected the fallback ruleset for %s:\n%v\n--- ruleset ---\n%s", in, err, ruleset)
		}
	}
}

// runInNamespace re-runs the calling test inside a fresh unprivileged user
// and network namespace, where it can add links and send packets without
// being root on the host. It skips where the sandbox forbids that, which is
// what GitHub's runners do.
func runInNamespace(t *testing.T, env, name string) {
	t.Helper()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not installed")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	cmd := exec.Command("unshare", "-Urn", os.Args[0], "-test.run", "^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), env+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	if strings.Contains(string(out), "uid_map") || strings.Contains(string(out), "Operation not permitted") {
		t.Skipf("unprivileged namespaces are not allowed here: %s", strings.TrimSpace(string(out)))
	}
	t.Fatalf("inside namespace: %v\n%s", err, out)
}

// dummyLink brings up a dummy interface carrying addrs. What it sends is
// dropped by the driver, after the postrouting hook has seen it.
func dummyLink(t *testing.T, name string, addrs ...string) {
	t.Helper()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
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
}

// A golden records the order the renderer wrote, right or wrong, so this
// checks the order where it counts: a packet from a mapped host leaves
// through a kernel running the ruleset, and the counters say which rule
// translated it. A NAT statement ends the chain, so only one may count it.
func TestOneToOneBeatsOutboundNATInKernel(t *testing.T) {
	const env = "OSTIOLE_NFT_NETNS"
	if os.Getenv(env) == "" {
		runInNamespace(t, env, "TestOneToOneBeatsOutboundNATInKernel")
		return
	}

	cfg := loadConfig(t, "testdata/minimal.json")
	cfg.NAT.Outbound = model.OutboundNAT{Mode: model.OutboundHybrid, Rules: []model.OutboundRule{
		{ID: "nat-all", Enabled: true, Zone: "wan"},
	}}
	cfg.NAT.OneToOne = []model.OneToOneNAT{
		{ID: "one-mail", Enabled: true, Zone: "wan", External: "203.0.113.10", Internal: "192.168.1.25"},
	}
	ruleset, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The mapped host's address is local here, so this process can send
	// as it, straight out of the WAN.
	dummyLink(t, "eth0", "198.51.100.2/24", "192.168.1.25/32")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	x := &Exec{}
	if err := x.Apply(ctx, ruleset); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialUDP("udp4",
		&net.UDPAddr{IP: net.ParseIP("192.168.1.25")},
		&net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	raw, err := x.ListTableJSON(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counters, err := ParseCounters(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := counters["one-mail"].Packets; got != 1 {
		t.Errorf("the 1:1 rule counted %d packets, want 1", got)
	}
	for _, key := range []string{"nat-all", "nat_postrouting/auto-nat:wan"} {
		if got := counters[key].Packets; got != 0 {
			t.Errorf("%s counted %d packets: it translated the mapped host", key, got)
		}
	}
}
