package sysstat

import (
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
	if first.Cores < 1 {
		t.Errorf("cores = %d", first.Cores)
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
