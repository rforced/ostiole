package sysstat

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadReportsTheMachine(t *testing.T) {
	t.Parallel()
	s := New(t.TempDir())

	first, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	// One reading is not enough to know how busy a CPU has been.
	if first.CPUPercent != nil {
		t.Errorf("first read reported CPU %v", *first.CPUPercent)
	}
	if first.Threads < 1 {
		t.Errorf("threads = %d", first.Threads)
	}
	if first.Cores < 1 || first.Cores > first.Threads {
		t.Errorf("cores = %d of %d threads", first.Cores, first.Threads)
	}
	if first.MemTotal == 0 || first.MemAvailable == 0 || first.MemAvailable > first.MemTotal {
		t.Errorf("memory = %d of %d", first.MemAvailable, first.MemTotal)
	}
	if first.UptimeSeconds <= 0 {
		t.Errorf("uptime = %d", first.UptimeSeconds)
	}
	if len(first.Filesystems) == 0 {
		t.Fatal("no filesystems reported")
	}
	for _, fs := range first.Filesystems {
		if fs.Total == 0 || fs.Free > fs.Total {
			t.Errorf("filesystem %s: %d free of %d", fs.Path, fs.Free, fs.Total)
		}
	}

	time.Sleep(minInterval + 50*time.Millisecond)
	second, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if second.CPUPercent == nil {
		t.Fatal("second read still has no CPU figure")
	}
	if *second.CPUPercent < 0 || *second.CPUPercent > 100 {
		t.Errorf("cpu = %v", *second.CPUPercent)
	}

	// A read that follows too closely repeats the last answer rather than
	// dividing by a window too short to mean anything.
	third, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if third.CPUPercent == nil || *third.CPUPercent != *second.CPUPercent {
		t.Errorf("a read inside the minimum window recomputed: %v", third.CPUPercent)
	}
}

func TestCPUTimesCountsIdleAsNotBusy(t *testing.T) {
	t.Parallel()
	busy, all, err := cpuTimes()
	if err != nil {
		t.Fatal(err)
	}
	if all == 0 {
		t.Fatal("no CPU time at all")
	}
	if busy >= all {
		t.Errorf("busy %d of %d: idle was counted as work", busy, all)
	}
}

// topology writes a sysfs tree for a machine of pkgs sockets, each with
// cores cores of threads threads, plus the smt/active the kernel derives
// from it.
func topology(t *testing.T, pkgs, cores, threads int) string {
	t.Helper()
	dir := t.TempDir()
	smt := "0"
	if threads > 1 {
		smt = "1"
	}
	if err := os.MkdirAll(filepath.Join(dir, "smt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "smt", "active"), []byte(smt+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cpu := 0
	for pkg := range pkgs {
		for core := range cores {
			for range threads {
				d := filepath.Join(dir, fmt.Sprintf("cpu%d", cpu), "topology")
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				write := func(name string, v int) {
					if err := os.WriteFile(filepath.Join(d, name), fmt.Appendf(nil, "%d\n", v), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				write("core_id", core)
				write("physical_package_id", pkg)
				cpu++
			}
		}
	}
	return dir
}

func TestCoresCountsTheHardwareNotTheThreads(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		pkgs, cores, threads int
		want                 int
	}{
		{"one socket with SMT", 1, 4, 2, 4},
		{"one socket without", 1, 4, 1, 4},
		{"two sockets with SMT", 2, 8, 2, 16},
		{"a single CPU", 1, 1, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Sampler{SysCPU: topology(t, tt.pkgs, tt.cores, tt.threads)}
			if got := s.cores(tt.pkgs * tt.cores * tt.threads); got != tt.want {
				t.Errorf("cores = %d, want %d", got, tt.want)
			}
		})
	}
}

// Some kernels give every CPU the same package and core, which counts as
// one core running all the threads. smt/active saying no core has more
// than one thread is the way out: the CPUs are the cores.
func TestCoresWithoutSMTTrustsTheCPUCount(t *testing.T) {
	t.Parallel()
	dir := topology(t, 1, 1, 8)
	if err := os.WriteFile(filepath.Join(dir, "smt", "active"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Sampler{SysCPU: dir}
	if got := s.cores(8); got != 8 {
		t.Errorf("cores = %d, want 8", got)
	}
}

// A kernel that publishes no topology at all leaves the card with the
// threads alone rather than a made-up number of cores.
func TestCoresUnknownWithoutATopology(t *testing.T) {
	t.Parallel()
	s := &Sampler{SysCPU: t.TempDir()}
	if got := s.cores(8); got != 0 {
		t.Errorf("cores = %d, want 0", got)
	}
}

func TestConntrackReadsTheTable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	nf := filepath.Join(dir, "net", "netfilter")
	if err := os.MkdirAll(nf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nf, "nf_conntrack_count"), []byte("2113\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nf, "nf_conntrack_max"), []byte("65536\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Sampler{ProcSys: dir}
	got := s.conntrack()
	if got == nil || got.Count != 2113 || got.Max != 65536 {
		t.Errorf("conntrack = %+v, want 2113 of 65536", got)
	}
}

// A kernel without the module has no files to read, and the dashboard
// should not show a meter of zero over zero.
func TestConntrackAbsentWithoutTheModule(t *testing.T) {
	t.Parallel()
	s := &Sampler{ProcSys: t.TempDir()}
	if got := s.conntrack(); got != nil {
		t.Errorf("conntrack = %+v, want nil", got)
	}
}
