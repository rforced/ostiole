package shaping

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rforced/ostiole/internal/model"
)

// inNamespace runs a shell script inside a fresh unprivileged user and
// network namespace, or skips the test when that is not possible here: no
// tc (it is a package of its own on Red Hat family distributions), no
// unshare, or a kernel without the queue discipline.
func inNamespace(t *testing.T, script string) string {
	t.Helper()
	for _, bin := range []string{"tc", "ip", "unshare"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c",
		"ip link set lo up && tc qdisc replace dev lo root handle 571: cake bandwidth 1000000bit && ip link add ifb-probe type ifb")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("cannot shape inside an unprivileged namespace: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(ctx, "unshare", "-Urn", "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	return string(out)
}

func section(t *testing.T, out, name string) []byte {
	t.Helper()
	_, rest, ok := strings.Cut(out, "==="+name+"===\n")
	if !ok {
		t.Fatalf("no %s section in:\n%s", name, out)
	}
	if end := strings.Index(rest, "\n==="); end >= 0 {
		rest = rest[:end]
	}
	return []byte(strings.TrimSpace(rest))
}

// The rendered batch installs in a real kernel, and running it twice
// leaves the same thing behind rather than two of it. Idempotence is what
// makes the reconciler safe to run every five seconds.
func TestBatchInstallsAndIsIdempotent(t *testing.T) {
	files, err := Render(loadConfig(t, "testdata/pair.json"))
	if err != nil {
		t.Fatal(err)
	}
	batch := stripComments(BatchFor(files, "eth0") + BatchFor(files, "eth1"))
	script := strings.Join([]string{
		"set -e",
		"ip link add eth0 type dummy && ip link set eth0 up",
		"ip link add eth1 type dummy && ip link set eth1 up",
		"ip link add ifb-eth0 type ifb && ip link set ifb-eth0 up",
		"tc -batch - <<'BATCH'\n" + batch + "BATCH",
		"tc -batch - <<'BATCH'\n" + batch + "BATCH",
		"echo ===FILTERS===",
		"tc -j filter show dev eth0 parent ffff:",
		"echo ===QDISCS===",
		"tc -s -j qdisc show",
		"echo ===TEARDOWN===",
		"tc qdisc del dev eth0 handle ffff: ingress",
		"tc qdisc del dev eth0 root",
		"tc qdisc del dev eth1 root",
		"ip link del ifb-eth0",
		"tc -s -j qdisc show",
		"",
	}, "\n")
	out := inNamespace(t, script)

	// One filter, however many times the batch ran. tc prints an entry for
	// the preference as well as the filter itself; only the one carrying
	// actions is a filter.
	var filters []struct {
		Options struct {
			Actions []map[string]any `json:"actions"`
		} `json:"options"`
	}
	if err := json.Unmarshal(section(t, out, "FILTERS"), &filters); err != nil {
		t.Fatal(err)
	}
	installed := 0
	for _, f := range filters {
		if len(f.Options.Actions) > 0 {
			installed++
		}
	}
	if installed != 1 {
		t.Errorf("running the batch twice left %d filters, want 1:\n%s", installed, section(t, out, "FILTERS"))
	}

	stats, err := ParseQdiscs(section(t, out, "QDISCS"))
	if err != nil {
		t.Fatal(err)
	}
	for dev, want := range map[string]int64{
		"eth0":     20_000_000,  // the WAN's upload leaves the device
		"ifb-eth0": 200_000_000, // its download arrives through the helper
		"eth1":     50_000_000,  // the LAN's cap
	} {
		got, ok := stats[dev]
		if !ok {
			t.Errorf("no queue on %s; have %v", dev, stats)
			continue
		}
		if got.Bits != want {
			t.Errorf("%s bandwidth = %d bit/s, want %d", dev, got.Bits, want)
		}
		if len(got.Tins) != len(model.Tiers) {
			t.Errorf("%s has %d tiers, want %d", dev, len(got.Tins), len(model.Tiers))
		}
	}

	// Taking it down leaves nothing of ours behind.
	left, err := ParseQdiscs(section(t, out, "TEARDOWN"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("teardown left %v", left)
	}
}

// The marks nftables sets have to land in the tiers this package names.
// Everything else is arithmetic nobody can check by reading it.
func TestMarksLandInTheRightTiers(t *testing.T) {
	lines := []string{"set -e", "ip link set lo up",
		"tc qdisc replace dev lo root handle 571: cake bandwidth 10000000bit diffserv4 fwmark 0x07000000"}
	for _, tier := range model.Tiers {
		mark, _ := tier.Mark()
		// One ping per tier, marked the way the firewall marks a flow.
		lines = append(lines, fmt.Sprintf("ping -c 1 -W 1 -m %d 127.0.0.1 >/dev/null", mark))
	}
	// Every device, the way the live view asks: `tc ... show dev lo` leaves
	// the device out of its own JSON on iproute2 6.17, and the parser keys
	// the queues by device.
	lines = append(lines, "echo ===QDISCS===", "tc -s -j qdisc show", "")
	out := inNamespace(t, strings.Join(lines, "\n"))

	stats, err := ParseQdiscs(section(t, out, "QDISCS"))
	if err != nil {
		t.Fatal(err)
	}
	lo, ok := stats["lo"]
	if !ok {
		t.Fatalf("no queue on lo: %v", stats)
	}
	for i, tin := range lo.Tins {
		if tin.Tier != model.Tiers[i] {
			t.Fatalf("tin %d is named %q, want %q", i, tin.Tier, model.Tiers[i])
		}
		if tin.SentPackets == 0 {
			t.Errorf("nothing marked for %s reached its tier; the mark and the queue disagree", tin.Tier)
		}
	}
}
