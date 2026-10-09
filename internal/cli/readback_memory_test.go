package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ostiole/internal/dnslog"
	"ostiole/internal/fwlog"
	"ostiole/internal/logfile"
	"ostiole/internal/logring"
	"ostiole/internal/model"
	"ostiole/internal/requestlog"
	"ostiole/internal/wafevent"
	"ostiole/internal/waflog"
)

// The logs read back at once, as serve.go reads them, under a limit set as
// memlimit.Derive sets it, peak near what they hold, and an entry holds no
// more than the model's figure. OSTIOLE_STARTMEM=1 takes the counts from
// SM_QUERIES, SM_FIREWALL, SM_EVENTS and SM_REQUESTS and reads each log alone too.
func TestReadBackPeaksNearTheLiveSize(t *testing.T) {
	logs := madeUpLogs()
	cfg := &model.Config{}
	cfg.System.Logging.Level = model.LogInfo
	cfg.System.Logging.Files.Enabled = true
	dir := t.TempDir()
	started := time.Now()
	errs := make([]error, len(logs))
	var wg sync.WaitGroup
	for i, l := range logs {
		l.set(cfg, l.count)
		wg.Go(func() { errs[i] = writeMadeUp(dir, l) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote the made-up logs in %s", time.Since(started).Round(time.Millisecond))

	first := measureReadBack(cfg, dir, logs, 0)
	rows := make([]memRow, 0, len(logs)+1)
	for i, l := range logs {
		rows = append(rows, memRow{name: l.name, entries: first.entries[i], live: first.live[i], peak: -1})
	}
	rows = append(rows, first.row("all at once"))
	t.Log(memTable("first pass, no memory limit: what each log holds once all are read", rows))

	all := measureReadBack(cfg, dir, logs, first.total)
	rows = rows[:0]
	for i, l := range logs {
		if !memMeasuring() {
			rows = append(rows, memRow{name: l.name, entries: all.entries[i], live: all.live[i], peak: -1})
			continue
		}
		r := measureReadBack(cfg, dir, logs[i:i+1], first.live[i])
		rows = append(rows, r.row(l.name+" alone"))
		r.checkPeak(t, l.name+" alone")
	}
	rows = append(rows, all.row("all at once"))
	t.Log(memTable(fmt.Sprintf("second pass, limit %.1f MB all at once: own memory + 1.5 x live", mb(all.limit)), rows))

	for i, l := range logs {
		if all.entries[i] != l.count {
			t.Errorf("%s: kept %d entries of the %d written", l.name, all.entries[i], l.count)
			continue
		}
		if per := all.live[i] / int64(l.count); per > l.figure {
			t.Errorf("%s: an entry holds %d bytes live, over the %d of %s; raise it", l.name, per, l.figure, l.figureName)
		}
	}
	all.checkPeak(t, "all at once")
}

// memPeakFactor bounds the heap's rise during a read-back, over what it keeps.
const memPeakFactor = 1.8

// memLog is one log the test writes and reads back.
type memLog struct {
	name       string
	version    int
	count      int
	figure     int64
	figureName string
	set        func(*model.Config, int)
	line       func(i int, at time.Time) ([]byte, error)
	read       func(*model.Config, *logfile.Writer, *slog.Logger) (any, int)
}

func madeUpLogs() []memLog {
	return []memLog{
		{
			name: dnslog.FileName, version: dnslog.FileVersion, count: memCount("SM_QUERIES", 100_000),
			figure: model.QueryLogBytes, figureName: "model.QueryLogBytes",
			set: func(c *model.Config, n int) {
				c.Services.DNS.QueryLog = model.QueryLog{Enabled: true, Entries: n}
			},
			line: madeUpAnswer,
			read: func(cfg *model.Config, files *logfile.Writer, log *slog.Logger) (any, int) {
				q := dnslog.New()
				q.Slog = log
				readQueryLog(cfg, q, files, false, log)
				n, _ := q.Held()
				return q, n
			},
		},
		{
			name: fwlog.FileName, version: fwlog.FileVersion, count: memCount("SM_FIREWALL", 100_000),
			figure: model.FirewallLogBytes, figureName: "model.FirewallLogBytes",
			set: func(c *model.Config, n int) {
				c.System.Management.FirewallLog = model.FirewallLog{Entries: n}
			},
			line: madeUpPacket,
			read: func(cfg *model.Config, files *logfile.Writer, log *slog.Logger) (any, int) {
				ring := fwlog.NewRing(model.FirewallLog{}.Size())
				readFirewallLog(cfg, ring, files, false, log)
				n, _ := ring.Held()
				return ring, n
			},
		},
		{
			name: waflog.FileName, version: waflog.FileVersion, count: memCount("SM_EVENTS", 20_000),
			figure: model.ProxyEventBytes, figureName: "model.ProxyEventBytes",
			set: func(c *model.Config, n int) {
				c.Services.Proxy.Events = model.ProxyEvents{Entries: n}
			},
			line: madeUpEvent,
			read: func(cfg *model.Config, files *logfile.Writer, log *slog.Logger) (any, int) {
				events := waflog.New()
				readWAFEvents(cfg, events, files, false, log)
				n, _ := events.Held()
				return events, n
			},
		},
		{
			name: requestlog.FileName, version: requestlog.FileVersion, count: memCount("SM_REQUESTS", 50_000),
			figure: model.RequestBytes, figureName: "model.RequestBytes",
			set: func(c *model.Config, n int) {
				c.Services.Proxy.Requests = model.LogKeep{Entries: n}
			},
			line: madeUpRequest,
			read: func(cfg *model.Config, files *logfile.Writer, log *slog.Logger) (any, int) {
				requests := requestlog.New()
				readRing(cfg, requests, requestlog.Files(requests), requestlog.Settings, files, false, log)
				n, _ := requests.Held()
				return requests, n
			},
		},
	}
}

func memMeasuring() bool { return os.Getenv("OSTIOLE_STARTMEM") != "" }

func memCount(name string, def int) int {
	if !memMeasuring() {
		return def
	}
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return def
}

// writeMadeUp writes a log's last six days through the daemon's writer.
func writeMadeUp(dir string, l memLog) error {
	step := 6 * 24 * time.Hour / time.Duration(l.count)
	first := time.Now().UTC().Add(-time.Minute - time.Duration(l.count)*step)
	var newest uint64
	var lineErr error
	cfg := &model.Config{}
	cfg.System.Logging.Files.Enabled = true
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: slog.New(slog.DiscardHandler),
		Statfs: func(string) (uint64, uint64, error) { return 1, 2, nil }}
	files.Add(logfile.Log{
		Name: l.name, Version: l.version,
		On:     func(*model.Config) bool { return true },
		Days:   func(*model.Config) int { return 0 },
		Newest: func() uint64 { return newest },
		Size:   func() int { return l.count },
		Lines: func(after uint64, limit int, emit func(uint64, time.Time, []byte)) bool {
			end := min(int(after)+limit, l.count)
			for i := int(after); i < end && lineErr == nil; i++ {
				at := first.Add(time.Duration(i) * step)
				var raw []byte
				if raw, lineErr = l.line(i, at); lineErr == nil {
					emit(uint64(i+1), at, raw)
				}
			}
			return end < l.count && lineErr == nil
		},
	}, logfile.ReadStats{})
	newest = uint64(l.count)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	files.Run(ctx)
	if lineErr != nil {
		return lineErr
	}
	got, err := logfile.Count(dir, l.name, time.Time{})
	if err == nil && got != l.count {
		err = fmt.Errorf("%s: the files hold %d lines of %d", l.name, got, l.count)
	}
	return err
}

var (
	madeUpNames = [][2]string{
		{"host-", ".example.test"}, {"ads", ".example.test"}, {"cdn-", ".images.example.test"},
		{"api", ".example.test"}, {"tracker-", ".ads.example.test"}, {"update-", ".downloads.example.test"},
	}
	madeUpAgents = []string{
		"made-up-client/1.0", "made-up-browser/2.3 (Made-up OS 10; x86_64) made-up-engine/5.0",
		"made-up-tv/4.1 (Made-up TV; lab)",
	}
)

type madeUpRow struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Client string    `json:"client"`
	Name   string    `json:"name"`
	Type   string    `json:"type"`
	Status string    `json:"status"`
	Reason string    `json:"reason,omitempty"`
	Lists  []string  `json:"lists,omitempty"`
	Answer string    `json:"answer,omitempty"`
}

func madeUpAnswer(i int, at time.Time) ([]byte, error) {
	n := madeUpNames[i%len(madeUpNames)]
	name := n[0] + n[1]
	if strings.HasSuffix(n[0], "-") {
		name = n[0] + strconv.Itoa(i%4099) + n[1]
	}
	r := madeUpRow{Seq: uint64(i + 1), Time: at, Client: "192.0.2." + strconv.Itoa(2+i%250), Name: name, Type: "A"}
	if i%4 == 1 {
		r.Client = "2001:db8::" + strconv.FormatInt(int64(2+i%250), 16)
	}
	switch {
	case i%5 == 0:
		r.Status, r.Reason, r.Lists = "blocked", "list", []string{fmt.Sprintf("made-up-list-%d", 1+i%4)}
		if i%15 == 0 {
			r.Lists = append(r.Lists, fmt.Sprintf("made-up-list-%d", 1+(i+1)%4))
		}
	case i%23 == 0:
		r.Status = "nxdomain"
	case i%3 == 1:
		r.Type, r.Status, r.Answer = "AAAA", "ok", "2001:db8:5::"+strconv.FormatInt(int64(i%4000), 16)
	case i%7 == 3:
		r.Type, r.Status = "HTTPS", "ok"
	default:
		r.Status, r.Answer = "ok", "198.51.100."+strconv.Itoa(1+i%250)
	}
	return json.Marshal(&r)
}

func madeUpPacket(i int, at time.Time) ([]byte, error) {
	e := fwlog.Entry{
		Seq: uint64(i + 1), Time: at, Prefix: "ostiole:r:lab-ssh:drop:", RuleID: "lab-ssh", Zone: "wan",
		Kind: "rule", Action: "drop", InIface: "wan0", OutIface: "lan0", Family: "ipv4", Proto: "tcp",
		Src: "198.51.100." + strconv.Itoa(1+i%250), Dst: "192.0.2." + strconv.Itoa(1+i%250),
		SrcPort: uint16(1024 + i%60000), DstPort: 22, TCPFlags: "SYN", Length: 60,
	}
	if i%3 == 1 {
		e.Prefix, e.RuleID, e.Zone, e.Action, e.DstPort, e.TCPFlags = "ostiole:r:lab-web:accept:", "lab-web", "lab", "accept", 443, "ACK,PSH"
	}
	if i%4 == 2 {
		e.Family, e.Length = "ipv6", 80
		e.Src, e.Dst = "2001:db8:4::"+strconv.FormatInt(int64(i%65536), 16), "2001:db8:1::"+strconv.FormatInt(int64(2+i%250), 16)
	}
	return json.Marshal(&e)
}

func madeUpEvent(i int, at time.Time) ([]byte, error) {
	e := waflog.Entry{Seq: uint64(i + 1), Logged: at, Event: wafevent.Event{
		Time: at.Add(-2 * time.Second), ID: strconv.FormatInt(int64(i)*104729+1e12, 36), Site: "made-up-site",
		Client: "198.51.100." + strconv.Itoa(1+i%250), Method: "GET",
		URI: "/made-up/path?id=" + strconv.Itoa(i%777) + "&q=1%27%20or%201%3d1", Status: 403,
		Verdict: wafevent.VerdictBlocked, Engine: "On",
		Rules: []wafevent.Hit{
			{ID: 942100, Message: "SQL Injection Attack Detected via libinjection",
				Data: "Matched Data: s&1 found within ARGS:q: 1' or 1=1", Severity: "critical"},
			{ID: 949110, Message: "Inbound Anomaly Score Exceeded (Total Score: 5)", Severity: "critical"},
		},
	}}
	if i%2 == 0 {
		e.Rules = append(e.Rules, wafevent.Hit{ID: 980170,
			Message: "Anomaly Scores: (Inbound Scores: blocking=5, detection=5, per_pl=5-0-0-0, threshold=5)", Severity: "notice"})
	}
	e.Event = e.Redacted()
	return json.Marshal(&e)
}

func madeUpRequest(i int, at time.Time) ([]byte, error) {
	r := requestlog.Request{
		Stamp: logring.Stamp{Seq: uint64(i + 1), Time: at}, Site: "made-up-site",
		Client: "192.0.2." + strconv.Itoa(2+i%250), Method: "GET", Host: "media.example.test",
		Path:  "/made-up/path/" + strconv.FormatInt(int64(i*7919%100000), 16) + "/item.jpg",
		Proto: "HTTP/2.0", Status: 200, Bytes: int64(1000 + i%50000), Duration: 0.012,
		Agent: madeUpAgents[i%len(madeUpAgents)], By: requestlog.BySite,
	}
	if i%9 == 0 {
		r.Method, r.Path, r.Status = "POST", "/made-up/path/progress", 204
	}
	return json.Marshal(&r)
}

// memRun is what a read-back kept, and how far the heap in use rose.
type memRun struct {
	entries []int
	live    []int64
	total   int64
	peak    int64
	limit   int64
	took    time.Duration
}

// measureReadBack reads the logs at once, limited to own memory + 1.5 x live when live is known.
func measureReadBack(cfg *model.Config, dir string, logs []memLog, live int64) memRun {
	debug.FreeOSMemory()
	r := memRun{entries: make([]int, len(logs)), live: make([]int64, len(logs))}
	if live > 0 {
		r.limit = runtimeMemory() + live*3/2
		defer debug.SetMemoryLimit(debug.SetMemoryLimit(r.limit))
	}
	kept := make([]any, len(logs))
	before := liveHeap()
	start := heapInUse()
	peak := peakHeap(func() {
		at := time.Now()
		readEvery(cfg, dir, logs, kept, r.entries)
		r.took = time.Since(at)
	})
	r.peak = peak - start
	prev := liveHeap()
	r.total = prev - before
	for i := range kept {
		kept[i] = nil
		now := liveHeap()
		r.live[i], prev = prev-now, now
	}
	return r
}

func readEvery(cfg *model.Config, dir string, logs []memLog, kept []any, entries []int) {
	log := slog.New(slog.DiscardHandler)
	files := &logfile.Writer{Dir: dir, Source: func() *model.Config { return cfg }, Log: log}
	var wg sync.WaitGroup
	for i, l := range logs {
		wg.Go(func() { kept[i], entries[i] = l.read(cfg, files, log) })
	}
	wg.Wait()
}

// peakHeap samples the heap in use every millisecond while fn runs.
func peakHeap(fn func()) int64 {
	peak := heapInUse()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				peak = max(peak, heapInUse())
			}
		}
	}()
	fn()
	close(stop)
	<-done
	return max(peak, heapInUse())
}

func readMetrics(names ...string) []uint64 {
	s := make([]metrics.Sample, len(names))
	for i, name := range names {
		s[i].Name = name
	}
	metrics.Read(s)
	out := make([]uint64, len(s))
	for i := range s {
		out[i] = s[i].Value.Uint64()
	}
	return out
}

func heapInUse() int64 {
	m := readMetrics("/memory/classes/heap/objects:bytes", "/memory/classes/heap/unused:bytes")
	return int64(m[0] + m[1])
}

func liveHeap() int64 {
	runtime.GC()
	return int64(readMetrics("/gc/heap/live:bytes")[0])
}

// runtimeMemory is the runtime's memory as a memory limit counts it.
func runtimeMemory() int64 {
	m := readMetrics("/memory/classes/total:bytes", "/memory/classes/heap/released:bytes")
	return int64(m[0] - m[1])
}

func (r memRun) row(name string) memRow {
	n := 0
	for _, e := range r.entries {
		n += e
	}
	return memRow{name: name, entries: n, live: r.total, peak: r.peak, took: r.took}
}

func (r memRun) checkPeak(t *testing.T, name string) {
	t.Helper()
	if float64(r.peak) > memPeakFactor*float64(r.total) {
		t.Errorf("%s: the heap rose %.1f MB reading back what holds %.1f MB, %.2f times, over %.1f",
			name, mb(r.peak), mb(r.total), float64(r.peak)/float64(r.total), memPeakFactor)
	}
}

type memRow struct {
	name    string
	entries int
	live    int64
	peak    int64
	took    time.Duration
}

func memTable(title string, rows []memRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%-16s %9s %9s %7s %9s %9s %8s\n", title, "log", "entries", "live MB", "B/entry", "peak MB", "peak/live", "took")
	for _, r := range rows {
		peak, ratio, took := "-", "-", "-"
		if r.peak >= 0 {
			peak, ratio = fmt.Sprintf("%.1f", mb(r.peak)), fmt.Sprintf("%.2f", float64(r.peak)/float64(r.live))
			took = r.took.Round(time.Millisecond).String()
		}
		fmt.Fprintf(&b, "%-16s %9d %9.1f %7d %9s %9s %8s\n", r.name, r.entries, mb(r.live), r.live/int64(max(r.entries, 1)), peak, ratio, took)
	}
	return b.String()
}

func mb(n int64) float64 { return float64(n) / 1e6 }
