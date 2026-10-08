package logfile

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ostiole/internal/model"
)

// entry is a log line as a test writes it.
type entry struct {
	Seq  uint64    `json:"seq,omitempty"`
	Time time.Time `json:"time"`
	Word string    `json:"word"`
}

// ring is a log in memory: the newest size entries, numbered from 1.
type ring struct {
	mu      sync.Mutex
	entries []entry
	seq     uint64
	size    int
}

func (r *ring) add(at time.Time, word string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	r.entries = append(r.entries, entry{Seq: r.seq, Time: at, Word: word})
	if len(r.entries) > r.size {
		r.entries = r.entries[len(r.entries)-r.size:]
	}
}

func (r *ring) After(seq uint64, buf []entry) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := 0
	for i < len(r.entries) && r.entries[i].Seq <= seq {
		i++
	}
	n := copy(buf, r.entries[i:])
	return n, i+n < len(r.entries)
}

func (r *ring) Newest() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

func (r *ring) clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

func (r *ring) desc(name string, on *bool) Log {
	return Log{
		Name: name, Version: 1,
		On:     func(*model.Config) bool { return on == nil || *on },
		Days:   func(*model.Config) int { return 7 },
		Newest: r.Newest,
		Size:   func() int { return r.size },
		Lines: Lines(r.After, func(e *entry) uint64 { return e.Seq }, func(e *entry) time.Time { return e.Time },
			func() func([]byte, *entry) []byte {
				return func(buf []byte, e *entry) []byte {
					raw, _ := json.Marshal(e)
					return append(buf, raw...)
				}
			}),
	}
}

func parse(line []byte) (entry, time.Time, error) {
	var e entry
	err := json.Unmarshal(line, &e)
	return e, e.Time, err
}

// clock is a test's time, moved by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// rig is a writer over one ring in a temporary directory, with the files
// on and the disk half empty.
type rig struct {
	w     *Writer
	r     *ring
	clock *clock
	cfg   *model.Config
	free  uint64
}

func newRig(t *testing.T, size int) *rig {
	t.Helper()
	g := &rig{r: &ring{size: size}, clock: &clock{t: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}, free: 50}
	g.cfg = &model.Config{}
	g.cfg.System.Logging.Files = model.LogFiles{Enabled: true}
	g.w = &Writer{
		Dir:    t.TempDir(),
		Source: func() *model.Config { return g.cfg },
		Log:    slog.New(slog.DiscardHandler),
		Now:    g.clock.now,
		Statfs: func(string) (uint64, uint64, error) { return g.free, 100, nil },
	}
	g.w.Add(g.r.desc("test", nil), ReadStats{})
	g.w.follow()
	return g
}

// members lists what each member of a file holds.
func memberLines(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out [][]string
	var comments []string
	_, broken, err := members(f, func(comment string, data io.Reader) error {
		raw, err := io.ReadAll(data)
		if err != nil {
			return err
		}
		comments = append(comments, comment)
		var words []string
		for line := range strings.Lines(string(raw)) {
			var e entry
			if json.Unmarshal([]byte(line), &e) == nil {
				words = append(words, e.Word)
			}
		}
		out = append(out, words)
		return nil
	}, nil)
	if err != nil || broken {
		t.Fatalf("%s: broken %v, %v", path, broken, err)
	}
	for _, c := range comments {
		if c != "ostiole test 1" {
			t.Errorf("comment = %q", c)
		}
	}
	return out
}

// Nothing goes out until the oldest waiting entry has waited the
// interval, and then it goes as one member per write.
func TestWritesABatchPerMemberOnceTheIntervalIsUp(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.r.add(g.clock.t, "a")
	g.w.writeDue(false)
	path := filepath.Join(g.w.Dir, "test", "2026-09-27.jsonl.gz")
	if _, err := os.Stat(path); err == nil {
		t.Fatal("written before the interval")
	}
	g.clock.t = g.clock.t.Add(4 * time.Minute)
	g.r.add(g.clock.t, "b")
	g.w.writeDue(false)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("written before the oldest entry waited five minutes")
	}
	g.clock.t = g.clock.t.Add(time.Minute)
	g.w.writeDue(false)
	g.r.add(g.clock.t, "c")
	g.w.writeDue(true)
	if got := memberLines(t, path); !slices.EqualFunc(got, [][]string{{"a", "b"}, {"c"}}, slices.Equal) {
		t.Errorf("members = %v", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if dir, _ := os.Stat(filepath.Dir(path)); dir.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", dir.Mode())
	}
	st := g.w.Status()
	if l := st.Logs[0]; l.Files != 1 || l.Bytes != info.Size() || l.Unwritten != 0 || l.Written == nil ||
		l.Oldest == nil || !l.Oldest.Equal(time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("status = %+v", l)
	}
}

// A busy log does not wait for the interval once half what it holds is
// unwritten, so nothing turns over before it is written.
func TestWritesSoonerWhenHalfTheLogIsWaiting(t *testing.T) {
	t.Parallel()
	g := newRig(t, 10)
	for i := range 4 {
		g.r.add(g.clock.t, fmt.Sprint(i))
	}
	g.w.writeDue(false)
	if st := g.w.Status(); st.Logs[0].Files != 0 {
		t.Fatal("written with four of ten waiting")
	}
	g.r.add(g.clock.t, "4")
	g.w.writeDue(false)
	if st := g.w.Status(); st.Logs[0].Files != 1 || st.Logs[0].Unwritten != 0 {
		t.Fatalf("status = %+v", st.Logs[0])
	}
}

// Each entry goes in the file of its UTC day, whatever the router's zone.
func TestSplitsByUTCDay(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	east := time.FixedZone("UTC+10", 10*3600)
	g.r.add(time.Date(2026, 9, 27, 23, 59, 0, 0, time.UTC), "late")
	g.r.add(time.Date(2026, 9, 28, 9, 0, 0, 0, east), "early")
	g.r.add(time.Date(2026, 9, 28, 0, 1, 0, 0, time.UTC), "next")
	g.w.writeDue(true)
	dir := filepath.Join(g.w.Dir, "test")
	if got := memberLines(t, filepath.Join(dir, "2026-09-27.jsonl.gz")); !slices.Equal(got[0], []string{"late", "early"}) {
		t.Errorf("27th = %v", got)
	}
	if got := memberLines(t, filepath.Join(dir, "2026-09-28.jsonl.gz")); !slices.Equal(got[0], []string{"next"}) {
		t.Errorf("28th = %v", got)
	}
}

// A write a power cut stopped halfway is cut off before the next one, so
// everything after it can be read.
func TestRepairsACutMemberBeforeAppending(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.r.add(g.clock.t, "whole")
	g.w.writeDue(true)
	path := filepath.Join(g.w.Dir, "test", "2026-09-27.jsonl.gz")
	good, _ := os.Stat(path)
	var zbuf bytes.Buffer
	zw := gzip.NewWriter(&zbuf)
	zw.Comment = "ostiole test 1"
	_, _ = zw.Write([]byte(`{"time":"2026-09-27T12:00:00Z","word":"cut"}` + "\n"))
	_ = zw.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.Write(zbuf.Bytes()[:zbuf.Len()-5])
	_ = f.Close()

	// A new process has not written this file yet.
	h := &Writer{Dir: g.w.Dir, Source: g.w.Source, Log: g.w.Log, Now: g.w.Now, Statfs: g.w.Statfs}
	r := &ring{size: 100}
	h.Add(r.desc("test", nil), ReadStats{})
	h.follow()
	r.add(g.clock.t, "after")
	h.writeDue(true)
	if got := memberLines(t, path); !slices.EqualFunc(got, [][]string{{"whole"}, {"after"}}, slices.Equal) {
		t.Errorf("members = %v", got)
	}
	if info, _ := os.Stat(path); info.Size() <= good.Size() {
		t.Errorf("size %d after, %d before", info.Size(), good.Size())
	}
}

// Each log keeps the shorter of the files' days and its own, and the cap
// takes the oldest day of any log first. Today's file stays.
func TestPrunesByDaysThenByTheCap(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.cfg.System.Logging.Files.RetentionDays = 30
	g.w.follow()
	dir := filepath.Join(g.w.Dir, "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sized := func(d string, size int64) {
		t.Helper()
		// Sparse: the size is what counts against the cap.
		f, err := os.OpenFile(filepath.Join(dir, d+suffix), os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			err = f.Truncate(size)
			_ = f.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	days := []string{"2026-09-17", "2026-09-19", "2026-09-20", "2026-09-25", "2026-09-27"}
	for _, d := range days {
		sized(d, 1024)
	}
	// The log keeps seven days, so its files do too, for all the files'
	// thirty: the 20th is over at midnight, after the 20th at noon.
	g.w.prune(g.clock.t)
	files, _ := dayFiles(dir)
	if got := names(files); !slices.Equal(got, days[2:]) {
		t.Errorf("after the days: %v", got)
	}
	if g.w.Status().Capped {
		t.Error("capped at 3 KB")
	}
	// Over the cap the oldest day goes, until it is under.
	for _, d := range days[2:] {
		sized(d, 400<<20)
	}
	g.w.prune(g.clock.t)
	files, _ = dayFiles(dir)
	if got := names(files); !slices.Equal(got, days[3:]) {
		t.Errorf("after the cap: %v", got)
	}
	if !g.w.Status().Capped {
		t.Error("the cap decided and the status does not say so")
	}
}

// A log that caps its days keeps no more than that, whatever the files
// keep.
func TestALogCapsItsDays(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.cfg.System.Logging.Files.RetentionDays = 365
	g.w.follow()
	g.w.mu.Lock()
	g.w.logs[0].MaxDays = 2
	g.w.mu.Unlock()
	dir := filepath.Join(g.w.Dir, "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-09-24", "2026-09-26", "2026-09-27"} {
		if err := os.WriteFile(filepath.Join(dir, d+suffix), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g.w.prune(g.clock.t)
	files, _ := dayFiles(dir)
	if got := names(files); !slices.Equal(got, []string{"2026-09-26", "2026-09-27"}) {
		t.Errorf("kept %v", got)
	}
}

// Files set to fewer days than a log keeps win, and a log with no days of
// its own keeps the files' days.
func TestTheShorterDaysWin(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		own  int
		want []string
	}{
		"fewer in the files": {own: 7, want: []string{"2026-09-25", "2026-09-27"}},
		"none of its own":    {own: 0, want: []string{"2026-09-25", "2026-09-27"}},
		"fewer in the log":   {own: 1, want: []string{"2026-09-27"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			g := newRig(t, 100)
			g.cfg.System.Logging.Files.RetentionDays = 3
			g.w.follow()
			g.w.mu.Lock()
			g.w.logs[0].Days = func(*model.Config) int { return tc.own }
			g.w.mu.Unlock()
			dir := filepath.Join(g.w.Dir, "test")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, d := range []string{"2026-09-20", "2026-09-23", "2026-09-25", "2026-09-27"} {
				if err := os.WriteFile(filepath.Join(dir, d+suffix), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			g.w.prune(g.clock.t)
			files, _ := dayFiles(dir)
			if got := names(files); !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeptTakesTheFewestDays(t *testing.T) {
	t.Parallel()
	day := 24 * time.Hour
	own := func(n int) func(*model.Config) int { return func(*model.Config) int { return n } }
	cfg := &model.Config{}
	cfg.System.Logging.Files.RetentionDays = 30
	for _, tc := range []struct {
		log  Log
		cfg  *model.Config
		want time.Duration
	}{
		{Log{Days: own(7)}, cfg, 7 * day},
		{Log{Days: own(90)}, cfg, 30 * day},
		{Log{Days: own(0)}, cfg, 30 * day},
		{Log{Days: own(0), MaxDays: 20}, cfg, 20 * day},
		{Log{Days: own(90), MaxDays: 60}, &model.Config{}, time.Duration(model.DefaultLogFileDays) * day},
		{Log{Days: own(7)}, nil, time.Duration(model.DefaultLogFileDays) * day},
	} {
		if got := tc.log.Kept(tc.cfg); got != tc.want {
			t.Errorf("own %d, max %d: kept %v, want %v", tc.log.Days(cfg), tc.log.MaxDays, got, tc.want)
		}
	}
}

func names(files []dayFile) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.day)
	}
	return out
}

// A log whose file today alone is over the cap stops until tomorrow, and
// says so; the entries of today are not written.
func TestStopsForTheDayWhenTodayIsOverTheCap(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	dir := filepath.Join(g.w.Dir, "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	today := filepath.Join(dir, "2026-09-27"+suffix)
	f, err := os.Create(today)
	if err == nil {
		err = f.Truncate(1<<30 + 1)
		_ = f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	g.w.prune(g.clock.t)
	if !g.w.Status().Logs[0].OverCap {
		t.Fatal("not over the cap")
	}
	g.r.add(g.clock.t, "skipped")
	g.w.writeDue(true)
	if info, _ := os.Stat(today); info.Size() != 1<<30+1 {
		t.Error("written over the cap")
	}
	if st := g.w.Status().Logs[0]; st.Unwritten != 0 {
		t.Errorf("status = %+v", st)
	}
	// Tomorrow the old day goes and writing starts again.
	g.clock.t = g.clock.t.Add(24 * time.Hour)
	g.r.add(g.clock.t, "tomorrow")
	g.w.writeDue(true)
	if _, err := os.Stat(today); err == nil {
		t.Error("yesterday's file kept over the cap")
	}
	if got := memberLines(t, filepath.Join(dir, "2026-09-28"+suffix)); !slices.Equal(got[0], []string{"tomorrow"}) {
		t.Errorf("tomorrow = %v", got)
	}
}

// Under 5% free the entries wait in memory, and the status says why.
func TestWaitsForRoom(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.free = 4
	g.r.add(g.clock.t, "waits")
	g.w.writeDue(true)
	st := g.w.Status()
	if !st.Paused || st.Logs[0].Files != 0 || st.Logs[0].Unwritten != 1 {
		t.Fatalf("status = %+v", st)
	}
	g.free = 6
	g.w.writeDue(true)
	if st := g.w.Status(); st.Paused || st.Logs[0].Files != 1 {
		t.Errorf("status = %+v", st)
	}
}

// What the log let go of before it was written is counted, not guessed at.
func TestCountsWhatWasLostBeforeItWasWritten(t *testing.T) {
	t.Parallel()
	g := newRig(t, 4)
	g.r.add(g.clock.t, "0")
	g.w.writeDue(true)
	for i := range 6 {
		g.r.add(g.clock.t, fmt.Sprint(i+1))
	}
	g.w.writeDue(true)
	if st := g.w.Status().Logs[0]; st.Lost != 2 {
		t.Errorf("lost = %d, want 2", st.Lost)
	}
}

// Switching the files off deletes them, and on again writes what memory
// holds, none of it counted lost.
func TestSwitchingOffDeletesAndOnWritesWhatMemoryHolds(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.r.add(g.clock.t, "a")
	g.w.writeDue(true)
	// A log of a newer build's is in the directory too.
	if err := os.MkdirAll(filepath.Join(g.w.Dir, "later"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.cfg.System.Logging.Files.Enabled = false
	g.w.follow()
	if entries, _ := os.ReadDir(g.w.Dir); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
	g.r.add(g.clock.t, "b")
	g.w.writeDue(true)
	if entries, _ := os.ReadDir(g.w.Dir); len(entries) != 0 {
		t.Fatal("written while off")
	}
	g.cfg.System.Logging.Files.Enabled = true
	g.w.follow()
	g.w.writeDue(true)
	got := memberLines(t, filepath.Join(g.w.Dir, "test", "2026-09-27"+suffix))
	if !slices.Equal(got[0], []string{"a", "b"}) {
		t.Errorf("members = %v", got)
	}
	if st := g.w.Status().Logs[0]; st.Lost != 0 {
		t.Errorf("lost = %d", st.Lost)
	}
}

// A log switched off loses its files; the others keep theirs.
func TestALogSwitchedOffLosesItsFiles(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	on := true
	other := &ring{size: 100}
	g.w.Add(other.desc("other", &on), ReadStats{})
	g.r.add(g.clock.t, "a")
	other.add(g.clock.t, "b")
	g.w.writeDue(true)
	on = false
	g.w.follow()
	if _, err := os.Stat(filepath.Join(g.w.Dir, "other")); err == nil {
		t.Error("the log switched off kept its files")
	}
	if _, err := os.Stat(filepath.Join(g.w.Dir, "test")); err != nil {
		t.Error("the other log lost its files")
	}
}

// Clear empties the memory and the files together, and what arrives
// after is written.
func TestClearDeletesTheFilesWithTheMemory(t *testing.T) {
	t.Parallel()
	g := newRig(t, 100)
	g.r.add(g.clock.t, "a")
	g.w.writeDue(true)
	g.r.add(g.clock.t, "unwritten")
	if err := g.w.Clear("test", g.r.clear); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(g.w.Dir, "test")); err == nil {
		t.Fatal("files kept")
	}
	g.r.add(g.clock.t, "b")
	g.w.writeDue(true)
	got := memberLines(t, filepath.Join(g.w.Dir, "test", "2026-09-27"+suffix))
	if !slices.EqualFunc(got, [][]string{{"b"}}, slices.Equal) {
		t.Errorf("members = %v", got)
	}
	if st := g.w.Status().Logs[0]; st.Lost != 0 {
		t.Errorf("lost = %d", st.Lost)
	}
}

// A daemon on a unit from before the files cannot write the directory,
// and says what to run.
func TestSaysToRepairAReadOnlyDirectory(t *testing.T) {
	t.Parallel()
	err := sealed("/var/log/ostiole", &os.PathError{Op: "mkdir", Path: "/var/log/ostiole/x", Err: syscall.EROFS})
	if !errors.Is(err, syscall.EROFS) || !strings.Contains(err.Error(), "run `ostiole repair`") {
		t.Errorf("err = %v", err)
	}
	if err := sealed("x", syscall.EACCES); err.Error() != syscall.EACCES.Error() {
		t.Errorf("err = %v", err)
	}
}

// Reading back goes newest first until the entries or the days run out,
// and hands them back oldest first.
func TestReadsBackNewestFirstWithinEntriesAndDays(t *testing.T) {
	t.Parallel()
	g := newRig(t, 1000)
	for d := 20; d <= 27; d++ {
		for h := range 3 {
			g.r.add(time.Date(2026, 9, d, 8*h, 0, 0, 0, time.UTC), fmt.Sprintf("%d-%d", d, h))
		}
	}
	g.w.writeDue(true)

	words := func(es []entry) []string {
		var out []string
		for _, e := range es {
			out = append(out, e.Word)
		}
		return out
	}
	got, st, err := Read(g.w.Dir, "test", 1, 4, time.Time{}, parse)
	if err != nil || st.Skipped != 0 {
		t.Fatal(err, st)
	}
	if !slices.Equal(words(got), []string{"26-2", "27-0", "27-1", "27-2"}) {
		t.Errorf("four entries = %v", words(got))
	}
	since := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	got, _, _ = Read(g.w.Dir, "test", 1, 1000, since, parse)
	if !slices.Equal(words(got), []string{"25-2", "26-0", "26-1", "26-2", "27-0", "27-1", "27-2"}) {
		t.Errorf("since the 25th at noon = %v", words(got))
	}
	// Nothing read, nothing there.
	if got, _, err := Read(g.w.Dir, "missing", 1, 10, time.Time{}, parse); err != nil || len(got) != 0 {
		t.Errorf("missing log = %v, %v", got, err)
	}
}

// A bad line is skipped and counted; a member in a format this build does
// not know is passed over and its file named; a cut member is not read.
func TestReadPassesOverWhatItCannotRead(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	member := func(comment, body string) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Comment = comment
		_, _ = zw.Write([]byte(body))
		_ = zw.Close()
		return buf.Bytes()
	}
	var file []byte
	file = append(file, member("ostiole test 1", `{"time":"2026-09-27T01:00:00Z","word":"a"}`+"\nnot json\n")...)
	file = append(file, member("ostiole test 2", `{"time":"2026-09-27T02:00:00Z","word":"future"}`+"\n")...)
	file = append(file, member("ostiole test 1", `{"time":"2026-09-27T03:00:00Z","word":"b"}`+"\n")...)
	cut := member("ostiole test 1", `{"time":"2026-09-27T04:00:00Z","word":"cut"}`+"\n")
	file = append(file, cut[:len(cut)-3]...)
	if err := os.WriteFile(filepath.Join(dir, "2026-09-27"+suffix), file, 0o600); err != nil {
		t.Fatal(err)
	}
	got, st, err := Read(filepath.Dir(dir), "test", 1, 100, time.Time{}, parse)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Word != "a" || got[1].Word != "b" {
		t.Errorf("entries = %+v", got)
	}
	if st.Skipped != 1 || !slices.Equal(st.Unknown, []string{"2026-09-27" + suffix}) ||
		!slices.Equal(st.Broken, []string{"2026-09-27" + suffix}) {
		t.Errorf("stats = %+v", st)
	}
}

// ReadEach hands every entry since a time over, oldest first, file by
// file, with no bound on how many.
func TestReadEachGoesOldestFirst(t *testing.T) {
	t.Parallel()
	g := newRig(t, 1000)
	for d := 24; d <= 27; d++ {
		for h := range 2 {
			g.r.add(time.Date(2026, 9, d, 12*h, 0, 0, 0, time.UTC), fmt.Sprintf("%d-%d", d, h))
		}
	}
	g.w.writeDue(true)
	var got []string
	st, err := ReadEach(g.w.Dir, "test", 1, time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC), parse,
		func(e entry) { got = append(got, e.Word) })
	if err != nil || st.Skipped != 0 {
		t.Fatal(err, st)
	}
	if !slices.Equal(got, []string{"25-1", "26-0", "26-1", "27-0", "27-1"}) {
		t.Errorf("entries = %v", got)
	}
	if _, err := ReadEach(g.w.Dir, "missing", 1, time.Time{}, parse, func(entry) { t.Error("read") }); err != nil {
		t.Error(err)
	}
}
