// Package sysstat reads what the box is doing with its CPU, memory and
// disks. Everything here comes from /proc and statfs, needs no privileges,
// and costs a few file reads, so the dashboard can poll it.
package sysstat

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Stats is one reading of the machine's load.
type Stats struct {
	// CPUPercent is how busy the CPUs were since the previous reading,
	// 0-100 across all cores together. It is null until there are two
	// readings to compare.
	CPUPercent *float64 `json:"cpuPercent"`
	Cores      int      `json:"cores"`
	Load1      float64  `json:"load1"`
	Load5      float64  `json:"load5"`
	Load15     float64  `json:"load15"`
	// Memory in bytes. Available is what the kernel thinks a new process
	// could get, which is the number worth showing: free alone reads as
	// alarmingly low on a healthy box that is using its page cache.
	MemTotal     uint64 `json:"memTotal"`
	MemAvailable uint64 `json:"memAvailable"`
	SwapTotal    uint64 `json:"swapTotal"`
	SwapFree     uint64 `json:"swapFree"`
	// UptimeSeconds is how long the box has been up.
	UptimeSeconds int64        `json:"uptimeSeconds"`
	Filesystems   []Filesystem `json:"filesystems"`
}

// Filesystem is the space on one mounted path.
type Filesystem struct {
	Path string `json:"path"`
	// Total and Free are bytes. Free is what an unprivileged writer can
	// use, so it excludes the reserved blocks root keeps for itself.
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// minInterval is the shortest gap that gives a meaningful CPU figure. Two
// reads closer together than this measure noise, so the previous answer is
// repeated instead.
const minInterval = 200 * time.Millisecond

// Sampler turns the kernel's monotonic CPU counters into a percentage by
// remembering the previous reading. It is safe for concurrent use; several
// dashboards polling at once shorten the window but do not corrupt it.
type Sampler struct {
	// Root and ConfigDir are the filesystems reported. ConfigDir is
	// skipped when it sits on the same one as Root.
	Root      string
	ConfigDir string

	mu       sync.Mutex
	lastBusy uint64
	lastAll  uint64
	lastAt   time.Time
	percent  *float64
	now      func() time.Time
}

// New returns a sampler reporting / and the filesystem holding dir.
func New(dir string) *Sampler {
	return &Sampler{Root: "/", ConfigDir: dir, now: time.Now}
}

func (s *Sampler) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Read takes a reading. The CPU figure covers the time since the previous
// call, so the first one reports nothing.
func (s *Sampler) Read() (Stats, error) {
	var st Stats
	busy, all, err := cpuTimes()
	if err != nil {
		return st, err
	}
	s.mu.Lock()
	now := s.clock()
	if !s.lastAt.IsZero() && now.Sub(s.lastAt) >= minInterval {
		if dAll := all - s.lastAll; dAll > 0 && all >= s.lastAll && busy >= s.lastBusy {
			pct := float64(busy-s.lastBusy) / float64(dAll) * 100
			s.percent = &pct
		}
	}
	if s.lastAt.IsZero() || now.Sub(s.lastAt) >= minInterval {
		s.lastBusy, s.lastAll, s.lastAt = busy, all, now
	}
	st.CPUPercent = s.percent
	s.mu.Unlock()

	st.Cores = numCPU()
	st.Load1, st.Load5, st.Load15 = loadAvg()
	st.UptimeSeconds = uptime()
	mem, err := meminfo()
	if err != nil {
		return st, err
	}
	st.MemTotal, st.MemAvailable = mem["MemTotal"], mem["MemAvailable"]
	st.SwapTotal, st.SwapFree = mem["SwapTotal"], mem["SwapFree"]
	st.Filesystems = s.disks()
	return st, nil
}

// disks reports the root filesystem and, when it is a different one, the
// filesystem the configuration lives on: a box that keeps /etc separate
// runs out of room there first.
func (s *Sampler) disks() []Filesystem {
	var out []Filesystem
	seen := map[uint64]bool{}
	for _, path := range []string{s.Root, s.ConfigDir} {
		if path == "" {
			continue
		}
		var fs unix.Statfs_t
		if err := unix.Statfs(path, &fs); err != nil {
			continue
		}
		id := uint64(fs.Fsid.Val[0])<<32 | uint64(uint32(fs.Fsid.Val[1])) //nolint:gosec // an identity, not arithmetic
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Filesystem{
			Path:  path,
			Total: fs.Blocks * uint64(fs.Bsize), //nolint:gosec // Bsize is a block size, never negative
			Free:  fs.Bavail * uint64(fs.Bsize), //nolint:gosec // as above
		})
	}
	return out
}

// cpuTimes returns the busy and total jiffies across all CPUs. Idle and
// iowait are the not-busy part: a box waiting on a disk is not working.
func cpuTimes() (busy, all uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		for i, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse /proc/stat: %w", err)
			}
			all += v
			// Fields are user, nice, system, idle, iowait, …
			if i != 3 && i != 4 {
				busy += v
			}
		}
		return busy, all, nil
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, fmt.Errorf("no cpu line in /proc/stat")
}

func meminfo() (map[string]uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]uint64{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// Everything but a few counters is in kB; the ones read here are.
		if len(fields) > 1 && fields[1] == "kB" {
			v *= 1024
		}
		out[name] = v
	}
	return out, sc.Err()
}

func loadAvg() (one, five, fifteen float64) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	one, _ = strconv.ParseFloat(fields[0], 64)
	five, _ = strconv.ParseFloat(fields[1], 64)
	fifteen, _ = strconv.ParseFloat(fields[2], 64)
	return one, five, fifteen
}

func uptime() int64 {
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0
	}
	secs, _ := strconv.ParseFloat(fields[0], 64)
	return int64(secs)
}

// numCPU counts the CPUs the kernel accounts for, which is what the load
// average should be read against.
func numCPU() int {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	n := 0
	for line := range strings.Lines(string(raw)) {
		if strings.HasPrefix(line, "cpu") && !strings.HasPrefix(line, "cpu ") {
			n++
		}
	}
	return n
}
