package memlimit

import (
	"runtime"
	"testing"

	"ostiole/internal/dnsblock"
	"ostiole/internal/model"
)

func TestDeriveFloorsAnEmptyConfiguration(t *testing.T) {
	if got := Derive(nil); got != Floor {
		t.Fatalf("Derive(nil) = %d, want %d", got, Floor)
	}
	if got := Derive(&model.Config{}); got != Floor {
		t.Fatalf("Derive(empty) = %d, want %d", got, Floor)
	}
}

func TestDeriveCountsTheLogsWithHeadroomAndTheIndex(t *testing.T) {
	cfg := &model.Config{}
	cfg.System.Management.FirewallLog.Entries = 2_000_000
	cfg.Blocking.Enabled = true
	want := int64(Baseline) + int64(dnsblock.DefaultMaxDomains)*IndexBytes +
		int64(float64(2_000_000*model.FirewallLogBytes)*headroom)
	if got := Derive(cfg); got != want {
		t.Fatalf("Derive = %d, want %d", got, want)
	}
	cfg.Blocking.MaxDomains = 5_000_000
	if got := Derive(cfg); got != want+int64(4_000_000)*IndexBytes {
		t.Fatalf("Derive with a raised ceiling = %d, want %d", got, want+int64(4_000_000)*IndexBytes)
	}
}

func TestApplySetsOnceAndRaisesAboveTheLiveHeap(t *testing.T) {
	var set []int64
	live := int64(0)
	l := &Limiter{
		Source: func() *model.Config { return nil },
		set:    func(n int64) int64 { set = append(set, n); return n },
		live:   func() int64 { return live },
	}
	if got := l.Apply(); got != Floor {
		t.Fatalf("Apply = %d, want %d", got, Floor)
	}
	l.Apply()
	if len(set) != 1 {
		t.Fatalf("an unchanged limit was set %d times", len(set))
	}
	live = Floor * 4 / 5
	want := int64(float64(live) * headroom)
	if got := l.Apply(); got != want {
		t.Fatalf("Apply near the live heap = %d, want %d", got, want)
	}
	if len(set) != 2 || set[1] != want {
		t.Fatalf("set = %v, want a second call with %d", set, want)
	}
}

func TestApplyLeavesTheEnvironmentsLimit(t *testing.T) {
	l := &Limiter{
		Source: func() *model.Config { return nil },
		set:    func(int64) int64 { t.Fatal("set the limit under GOMEMLIMIT"); return 0 },
		live:   func() int64 { return 0 },
		env:    true,
	}
	if got := l.Apply(); got != 0 {
		t.Fatalf("Apply = %d, want 0", got)
	}
}

func TestLiveReadsTheRuntime(t *testing.T) {
	runtime.GC()
	if Live() <= 0 {
		t.Fatal("Live gave nothing")
	}
}
