package shaping

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/rforced/ostiole/internal/model"
)

// The fixture is what tc really printed on the test VM after a batch this
// package rendered: a queue on the device, the ingress hook, and a queue
// on the helper.
func loadQdiscs(t *testing.T) map[string]CakeStats {
	t.Helper()
	raw, err := os.ReadFile("testdata/stats/qdiscs.json")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := ParseQdiscs(raw)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestParseQdiscs(t *testing.T) {
	t.Parallel()
	stats := loadQdiscs(t)
	if got, want := len(stats), 2; got != want {
		t.Fatalf("parsed %d queues, want %d: %v", got, want, stats)
	}
	lo, ok := stats["lo"]
	if !ok {
		t.Fatal("no queue on lo")
	}
	// tc reports bytes per second and nobody talks about a line that way.
	if got, want := lo.Bits, int64(20_000_000); got != want {
		t.Errorf("bandwidth = %d bit/s, want %d", got, want)
	}
	if lo.Bytes != 392 || lo.Packets != 4 {
		t.Errorf("counters = %d bytes, %d packets", lo.Bytes, lo.Packets)
	}
	if lo.MemoryUsed != 960 || lo.MemoryLimit != 4194304 {
		t.Errorf("memory = %d of %d", lo.MemoryUsed, lo.MemoryLimit)
	}
	if got, want := stats["ifb-lo"].Bits, int64(100_000_000); got != want {
		t.Errorf("helper bandwidth = %d bit/s, want %d", got, want)
	}
}

// The four tins come back in the order the tiers are named, which is what
// lets the page label a row without the router explaining itself.
func TestTinsAreNamedInTierOrder(t *testing.T) {
	t.Parallel()
	lo := loadQdiscs(t)["lo"]
	if got, want := len(lo.Tins), len(model.Tiers); got != want {
		t.Fatalf("%d tins, want %d", got, want)
	}
	for i, tin := range lo.Tins {
		if tin.Tier != model.Tiers[i] {
			t.Errorf("tin %d is %q, want %q", i, tin.Tier, model.Tiers[i])
		}
	}
	// The pings that went through the fixture were unmarked, so they
	// landed in the tier an unclassified packet gets.
	normal := lo.Tins[1]
	if normal.Tier != model.TierNormal {
		t.Fatalf("tin 1 is %q", normal.Tier)
	}
	if normal.SentBytes != 392 || normal.SentPackets != 4 {
		t.Errorf("normal tin sent %d bytes in %d packets", normal.SentBytes, normal.SentPackets)
	}
	if normal.PeakDelayUs != 16 {
		t.Errorf("peak delay = %d us, want 16", normal.PeakDelayUs)
	}
	if got, want := normal.ThresholdBits, int64(20_000_000); got != want {
		t.Errorf("threshold = %d bit/s, want %d", got, want)
	}
	for _, tin := range lo.Tins {
		if tin.Drops != 0 {
			t.Errorf("%s tin reports drops on an idle queue", tin.Tier)
		}
	}
}

// Nothing but Ostiole's own queues is reported: another cake somebody
// installed, or the kernel's own default, answers to nobody here.
func TestParseIgnoresForeignQueues(t *testing.T) {
	t.Parallel()
	stats, err := ParseQdiscs([]byte(`[
		{"kind":"cake","handle":"8001:","dev":"eth9","root":true,"options":{"bandwidth":"unlimited"}},
		{"kind":"fq_codel","handle":"571:","dev":"eth8","root":true},
		{"kind":"cake","handle":"571:","dev":"eth7","parent":"571:1"},
		{"kind":"cake","handle":"571:","dev":"eth6","root":true,"options":{"bandwidth":"unlimited"}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stats["eth9"]; ok {
		t.Error("a queue with a handle the kernel chose was taken for ours")
	}
	if _, ok := stats["eth8"]; ok {
		t.Error("something that is not cake was taken for ours")
	}
	if _, ok := stats["eth7"]; ok {
		t.Error("a queue that is not on a root was taken for ours")
	}
	// A queue with no ceiling is still ours; it just has no figure.
	if got, ok := stats["eth6"]; !ok || got.Bits != 0 {
		t.Errorf("an unlimited queue parsed as %+v", got)
	}
}

// The status names each direction the way the operator does and says
// which of them the kernel actually has.
func TestStatusNamesTheDirections(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("testdata/stats/qdiscs.json")
	if err != nil {
		t.Fatal(err)
	}
	ev := &events{}
	kern := &fakeKernel{ev: ev, links: map[string]bool{"eth0": true}, qdiscs: map[string][]Qdisc{}}
	s := &Shaper{
		Bin: "sh", Run: &stubQdiscs{raw: raw}, Kernel: kern, Log: slog.New(slog.DiscardHandler),
	}
	cfg := loadConfig(t, "testdata/pair.json")
	// The fixture's queues are on lo and its helper, so the interfaces are
	// renamed to match rather than the fixture being edited.
	cfg.Interfaces[0].Name, cfg.Interfaces[1].Name = "lo", "eth1"
	kern.links["lo"] = true

	rep, err := s.Status(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Available || rep.Reason != "" {
		t.Errorf("tc is on the path, yet the report says %q", rep.Reason)
	}
	if len(rep.Interfaces) != 2 {
		t.Fatalf("reported %d interfaces", len(rep.Interfaces))
	}
	wan := rep.Interfaces[0]
	if !wan.Present || !wan.External {
		t.Errorf("wan = %+v", wan)
	}
	// On a WAN the upload leaves the device and the download arrives on
	// the helper; both are installed in the fixture.
	if !wan.Upload.Installed || wan.Upload.Rate != 20_000_000 {
		t.Errorf("upload = %+v", wan.Upload)
	}
	if !wan.Download.Installed || wan.Download.Rate != 200_000_000 {
		t.Errorf("download = %+v", wan.Download)
	}
	// The LAN is shaped but nothing is in the kernel for it.
	lan := rep.Interfaces[1]
	if lan.Download.Rate != 50_000_000 || lan.Download.Installed {
		t.Errorf("lan download = %+v", lan.Download)
	}
	if lan.Upload.Rate != 0 {
		t.Errorf("lan upload was given a figure nobody asked for: %+v", lan.Upload)
	}
}

// Without tc there is nothing to report and the reason says what to do.
func TestStatusWithoutTC(t *testing.T) {
	t.Parallel()
	s := &Shaper{Bin: "ostiole-no-such-binary", PackageManager: "apt-get", Run: &stubQdiscs{}}
	rep, err := s.Status(context.Background(), loadConfig(t, "testdata/pair.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Available {
		t.Error("a missing tc was reported as available")
	}
	if rep.Reason == "" {
		t.Error("no reason given")
	}
	for _, in := range rep.Interfaces {
		if in.Download.Installed || in.Upload.Installed {
			t.Errorf("%s reports an installed queue with no tc", in.Name)
		}
	}
}

// stubQdiscs answers with one captured sample and nothing else.
type stubQdiscs struct{ raw []byte }

func (s *stubQdiscs) Batch(context.Context, string) error { return nil }
func (s *stubQdiscs) QdiscsJSON(context.Context) ([]byte, error) {
	if s.raw == nil {
		return []byte("[]"), nil
	}
	return s.raw, nil
}
func (s *stubQdiscs) FiltersJSON(context.Context, string, string) ([]byte, error) {
	return []byte("[]"), nil
}
func (s *stubQdiscs) Version(context.Context) (string, error) { return "tc", nil }
