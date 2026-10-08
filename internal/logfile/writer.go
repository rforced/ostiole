package logfile

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ostiole/internal/model"
)

const (
	// chunk is how many entries are copied out of a log per hold of its
	// lock, as a search does.
	chunk = 4096
	// batch bounds one member, so a full log switched on is written a
	// slice at a time rather than gathered whole in memory.
	batch = 65536
	// follow is how often the configuration is read again.
	follow = 5 * time.Second
	// pruneEvery is how often old days go when nothing is written.
	pruneEvery = time.Hour
)

// Log is one log in memory the writer keeps in files.
type Log struct {
	// Name is the log's directory under Dir, and its key on the pages.
	Name string
	// Version is the format of its lines, named in each member's header.
	Version int
	// On says whether the log is on in a configuration, and Days how many
	// days it keeps there, zero for a log with no days of its own.
	On   func(*model.Config) bool
	Days func(*model.Config) int
	// MaxDays caps the days its files keep, however many the files'
	// setting says; zero is no cap.
	MaxDays int
	// Newest is the number of the newest entry the log took, zero before
	// the first. Numbers only grow.
	Newest func() uint64
	// Size is the most entries the log holds in memory.
	Size func() int
	// Lines hands emit the entries after seq, oldest first, at most limit
	// of them, each with its number, when it was logged and its line, and
	// says whether newer ones remain.
	Lines func(after uint64, limit int, emit func(seq uint64, at time.Time, line []byte)) (more bool)
}

// Kept is how long the log's files keep an entry in cfg: the files' days,
// or the log's own where those are fewer, and never more than MaxDays.
func (l Log) Kept(cfg *model.Config) time.Duration {
	var files model.LogFiles
	if cfg != nil {
		files = cfg.System.Logging.Files
	}
	days := files.Days()
	if cfg != nil {
		if own := l.Days(cfg); own > 0 {
			days = min(days, own)
		}
	}
	if l.MaxDays > 0 {
		days = min(days, l.MaxDays)
	}
	return time.Duration(days) * 24 * time.Hour
}

// Lines builds Log.Lines for a log of E. after copies the entries after
// seq into buf, oldest first, as a ring does under its lock; they are
// encoded outside it by the encoder made for that read.
func Lines[E any](after func(seq uint64, buf []E) (int, bool), seq func(*E) uint64, at func(*E) time.Time,
	encoder func() func(buf []byte, e *E) []byte,
) func(uint64, int, func(uint64, time.Time, []byte)) bool {
	return func(from uint64, limit int, emit func(uint64, time.Time, []byte)) bool {
		encode := encoder()
		buf := make([]E, min(limit, chunk))
		var line []byte
		more := false
		for taken := 0; taken < limit; {
			var n int
			n, more = after(from, buf[:min(len(buf), limit-taken)])
			for i := range n {
				e := &buf[i]
				from = seq(e)
				line = encode(line[:0], e)
				emit(from, at(e), line)
			}
			taken += n
			if n == 0 || !more {
				break
			}
		}
		return more
	}
}

// Writer keeps the registered logs in files while the configuration says
// so: each written once its oldest unwritten entry has waited the interval,
// or sooner once half what its memory holds is unwritten, and everything
// when the daemon stops.
type Writer struct {
	// Dir holds a directory per log.
	Dir string
	// Source is the configuration the router is running.
	Source func() *model.Config
	Log    *slog.Logger
	// Statfs reads a filesystem's free and total bytes; nil asks the
	// kernel.
	Statfs func(path string) (free, total uint64, err error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu     sync.Mutex
	logs   []*log
	cfg    *model.Config
	files  model.LogFiles
	free   uint64
	total  uint64
	low    bool
	capped bool
	pruned time.Time

	// zw is only used by the goroutine that writes.
	zw *gzip.Writer
}

// log is a registered log and how writing it goes. mu is held while it is
// written, pruned or cleared; the status fields are the writer's, under its
// lock, so a status read never waits on a write.
type log struct {
	Log
	mu       sync.Mutex
	on       bool
	fresh    bool
	pending  time.Time
	repaired map[string]bool
	written  atomic.Uint64

	lastWrite time.Time
	newest    time.Time
	lost      uint64
	skipped   int
	unknown   []string
	overCap   string
	err       error
	oldest    struct {
		path string
		at   time.Time
	}
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Writer) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

// Add registers a log. What it holds now counts as written: it came back
// from its files, or the files were off. read is what reading it back
// found.
func (w *Writer) Add(l Log, read ReadStats) {
	x := &log{Log: l, repaired: map[string]bool{}, skipped: read.Skipped, unknown: read.Unknown}
	x.written.Store(l.Newest())
	cfg := w.Source()
	x.on = cfg != nil && cfg.System.Logging.Files.Enabled && l.On(cfg)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.logs = append(w.logs, x)
}

// Run writes until ctx is done, then writes everything left and returns.
func (w *Writer) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var last time.Time
	for {
		if now := w.now(); now.Sub(last) >= follow {
			w.follow()
			last = now
		}
		w.writeDue(false)
		select {
		case <-ctx.Done():
			w.writeDue(true)
			return
		case <-tick.C:
		}
	}
}

// follow reads the configuration again. A log that is off, or every log
// when the files are, loses its files: what they hold goes as its memory
// does, and an older binary rolled back to may have left some.
func (w *Writer) follow() {
	cfg := w.Source()
	var files model.LogFiles
	if cfg != nil {
		files = cfg.System.Logging.Files
	}
	w.mu.Lock()
	w.cfg, w.files = cfg, files
	logs := slices.Clone(w.logs)
	w.mu.Unlock()
	if !files.Enabled {
		w.removeAll()
	}
	for _, l := range logs {
		want := files.Enabled && cfg != nil && l.On(cfg)
		l.mu.Lock()
		switch {
		case want && !l.on:
			// Everything the log holds goes in, as if it had always
			// been written; what it let go of while off was never lost.
			l.on, l.fresh, l.pending = true, true, time.Time{}
			l.written.Store(0)
		case !want && l.on:
			l.on = false
			w.remove(l)
		case !want:
			if _, err := os.Stat(filepath.Join(w.Dir, l.Name)); err == nil {
				w.remove(l)
			}
		}
		l.mu.Unlock()
	}
}

// removeAll deletes every log's directory, those of logs this build does
// not know included: Dir is Ostiole's alone.
func (w *Writer) removeAll() {
	entries, err := os.ReadDir(w.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			if err := os.RemoveAll(filepath.Join(w.Dir, e.Name())); err != nil {
				w.log().Warn("could not delete a log's files", "dir", e.Name(), "err", err)
			}
		}
	}
}

// remove deletes a log's directory and forgets how writing it went. The
// caller holds l.mu.
func (w *Writer) remove(l *log) {
	if err := os.RemoveAll(filepath.Join(w.Dir, l.Name)); err != nil {
		w.log().Warn("could not delete a log's files", "log", l.Name, "err", err)
	}
	l.repaired = map[string]bool{}
	w.mu.Lock()
	l.lastWrite, l.newest, l.lost, l.skipped, l.unknown, l.overCap, l.err = time.Time{}, time.Time{}, 0, 0, nil, "", nil
	w.mu.Unlock()
}

// Clear deletes a log's files along with what empty empties, its memory,
// so an entry cleared before it was written is not written after.
func (w *Writer) Clear(name string, empty func()) error {
	l := w.find(name)
	if l == nil {
		empty()
		return os.RemoveAll(filepath.Join(w.Dir, name))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	seq := l.Newest()
	empty()
	err := os.RemoveAll(filepath.Join(w.Dir, name))
	l.repaired = map[string]bool{}
	l.written.Store(seq)
	l.fresh, l.pending = true, time.Time{}
	w.mu.Lock()
	l.lastWrite, l.newest, l.lost, l.skipped, l.unknown, l.overCap, l.err = time.Time{}, time.Time{}, 0, 0, nil, "", nil
	w.mu.Unlock()
	return err
}

func (w *Writer) find(name string) *log {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, l := range w.logs {
		if l.Name == name {
			return l
		}
	}
	return nil
}

// writeDue writes each log that is due, every one when all is set, and
// prunes after a write or once an hour.
func (w *Writer) writeDue(all bool) {
	w.mu.Lock()
	logs, files := slices.Clone(w.logs), w.files
	pruned := w.pruned
	w.mu.Unlock()
	if !files.Enabled {
		return
	}
	now := w.now()
	wrote := false
	for _, l := range logs {
		if w.write(l, files, now, all) {
			wrote = true
		}
	}
	if wrote || now.Sub(pruned) >= pruneEvery {
		w.prune(now)
	}
}

// write writes one log if it is due, and reports whether it wrote.
func (w *Writer) write(l *log, files model.LogFiles, now time.Time, all bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.on {
		return false
	}
	newest := l.Newest()
	if newest <= l.written.Load() {
		l.pending = time.Time{}
		return false
	}
	if l.pending.IsZero() {
		l.pending = now
	}
	half := uint64(max(l.Size()/2, 1))
	if !all && now.Sub(l.pending) < files.Interval() && newest-l.written.Load() < half {
		return false
	}
	if !w.roomy() {
		return false
	}
	w.mu.Lock()
	overCap := l.overCap == day(now)
	if !overCap {
		l.overCap = ""
	}
	w.mu.Unlock()
	wrote := false
	for {
		b := w.collect(l)
		if b.count == 0 && b.last == 0 {
			// The log let go of them before they were written.
			w.lose(l, newest)
			break
		}
		var err error
		if !overCap {
			err = w.flush(l, b, now)
		} else {
			// Today's file alone is over the cap: what arrives today
			// is not written, and tomorrow starts again.
			l.written.Store(b.last)
		}
		if err != nil {
			w.mu.Lock()
			l.err = err
			w.mu.Unlock()
			w.log().Warn("could not write a log to its files", "log", l.Name, "err", err)
			break
		}
		wrote = wrote || !overCap
		if !b.more {
			break
		}
	}
	if l.written.Load() >= l.Newest() {
		l.pending = time.Time{}
	}
	return wrote
}

// lose counts what the log let go of before it was written, and moves
// past it. The caller holds l.mu.
func (w *Writer) lose(l *log, newest uint64) {
	written := l.written.Load()
	if newest <= written {
		return
	}
	if !l.fresh {
		w.mu.Lock()
		l.lost += newest - written
		w.mu.Unlock()
	}
	l.fresh = false
	l.written.Store(newest)
}

// lines is one write: the lines of each UTC day and what its index says of
// them, and the last entry taken.
type lines struct {
	days   map[string][]byte
	index  map[string]*member
	count  int
	last   uint64
	newest time.Time
	more   bool
}

// collect takes the next batch after what was written. A gap before its
// first entry is what the log let go of unwritten. The caller holds l.mu.
func (w *Writer) collect(l *log) *lines {
	b := &lines{days: map[string][]byte{}, index: map[string]*member{}}
	expect := l.written.Load() + 1
	var lost uint64
	b.more = l.Lines(l.written.Load(), batch, func(seq uint64, at time.Time, line []byte) {
		if seq > expect && !l.fresh {
			lost += seq - expect
		}
		l.fresh = false
		expect, b.last = seq+1, seq
		if len(line) == 0 {
			return
		}
		d := day(at)
		m := b.index[d]
		if m == nil {
			m = &member{First: seq, From: at}
			b.index[d] = m
		}
		m.Last, m.To = seq, at
		m.N++
		b.days[d] = append(append(b.days[d], line...), '\n')
		b.count++
		if at.After(b.newest) {
			b.newest = at
		}
	})
	if lost > 0 {
		w.mu.Lock()
		l.lost += lost
		w.mu.Unlock()
	}
	return b
}

// flush appends a batch, a member for each day, oldest day first. When a
// day fails, the days before it stay written. The caller holds l.mu.
func (w *Writer) flush(l *log, b *lines, now time.Time) error {
	dir := filepath.Join(w.Dir, l.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return sealed(w.Dir, err)
	}
	if w.zw == nil {
		w.zw, _ = gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	}
	days := make([]string, 0, len(b.days))
	for d := range b.days {
		days = append(days, d)
	}
	sort.Strings(days)
	comment := format(l.Name, l.Version)
	for _, d := range days {
		path := filepath.Join(dir, d+suffix)
		m := b.index[d]
		if err := w.repair(l, path); err != nil {
			l.written.Store(max(l.written.Load(), m.First-1))
			return sealed(w.Dir, err)
		}
		data, err := compress(w.zw, b.days[d], comment)
		var at int64
		if err == nil {
			at, err = appendFile(path, data)
		}
		if err != nil {
			l.written.Store(max(l.written.Load(), m.First-1))
			return sealed(w.Dir, err)
		}
		// An index line that does not make it is caught by its reader,
		// which builds the index again.
		m.At, m.Size, m.Format = at, int64(len(data)), comment
		_ = appendIndex(path, *m)
	}
	l.written.Store(b.last)
	w.mu.Lock()
	l.lastWrite, l.err = now, nil
	if b.newest.After(l.newest) {
		l.newest = b.newest
	}
	w.mu.Unlock()
	return nil
}

// repair cuts a file this process has not written yet back to its last
// whole member, once. The caller holds l.mu.
func (w *Writer) repair(l *log, path string) error {
	if l.repaired[path] {
		return nil
	}
	cut, err := repair(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if cut > 0 {
		w.log().Warn("cut a log file back to its last whole write", "file", path, "bytes", cut)
		_ = os.Remove(indexPath(path))
	}
	l.repaired[path] = true
	return nil
}

// sealed says what to run when the daemon's unit does not open the
// directory yet, as on a router whose units an update has not rewritten.
func sealed(dir string, err error) error {
	if !errors.Is(err, syscall.EROFS) {
		return err
	}
	return fmt.Errorf("%w (this daemon's unit does not open %s yet: run `ostiole repair`)", err, dir)
}

// roomy reports whether the filesystem has room to write: writing waits
// while less than 5% of it is free, and the entries wait in memory.
func (w *Writer) roomy() bool {
	statfs := w.Statfs
	if statfs == nil {
		statfs = kernelStatfs
	}
	free, total, err := statfs(w.Dir)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		// Not knowing is no reason to stop: the write says what is wrong.
		w.low = false
		return true
	}
	w.free, w.total = free, total
	w.low = total > 0 && free*20 < total
	return !w.low
}

func kernelStatfs(path string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	// The directory is missing until the first write; its filesystem is
	// its parent's.
	for {
		err = syscall.Statfs(path, &st)
		if err == nil || !errors.Is(err, syscall.ENOENT) || filepath.Dir(path) == path {
			break
		}
		path = filepath.Dir(path)
	}
	if err != nil {
		return 0, 0, err
	}
	return st.Bavail * uint64(st.Bsize), st.Blocks * uint64(st.Bsize), nil //nolint:gosec // block size is positive
}

// prune deletes the days each log no longer keeps, the shorter of the
// files' days and the log's own, then the oldest day of any log while
// the files are over the cap. Today's files are never deleted: a log
// whose file alone keeps the rest over the cap stops writing until
// tomorrow.
func (w *Writer) prune(now time.Time) {
	w.mu.Lock()
	logs, files, cfg := slices.Clone(w.logs), w.files, w.cfg
	w.pruned = now
	w.mu.Unlock()
	type held struct {
		l *log
		dayFile
	}
	var kept []held
	var total int64
	for _, l := range logs {
		l.mu.Lock()
		if !l.on {
			l.mu.Unlock()
			continue
		}
		cutoff := now.Add(-l.Kept(cfg))
		list, _ := dayFiles(filepath.Join(w.Dir, l.Name))
		for _, f := range list {
			if !dayEnd(f.day).After(cutoff) {
				w.drop(l, f.path)
				continue
			}
			kept = append(kept, held{l, f})
			total += f.size
		}
		l.mu.Unlock()
	}
	capped := total > files.MaxUse()
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].day < kept[j].day })
	today := day(now)
	for len(kept) > 0 && total > files.MaxUse() && kept[0].day != today {
		f := kept[0]
		kept = kept[1:]
		f.l.mu.Lock()
		w.drop(f.l, f.path)
		f.l.mu.Unlock()
		total -= f.size
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capped = capped
	if total > files.MaxUse() {
		for _, f := range kept {
			f.l.overCap = today
		}
	}
}

// drop deletes one day file and its index. The caller holds l.mu.
func (w *Writer) drop(l *log, path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.log().Warn("could not delete an old log file", "file", path, "err", err)
		return
	}
	_ = os.Remove(indexPath(path))
	delete(l.repaired, path)
}

// Status is what the files hold and how writing them goes.
type Status struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
	// Free and Total are the filesystem's, in bytes.
	Free  uint64 `json:"free,omitempty"`
	Total uint64 `json:"total,omitempty"`
	// Paused says writing waits for room: the filesystem has less than 5%
	// free.
	Paused bool `json:"paused,omitempty"`
	// Capped says the cap rather than the days decides how far back the
	// files go.
	Capped bool        `json:"capped,omitempty"`
	Logs   []LogStatus `json:"logs"`
}

// LogStatus is one log's files.
type LogStatus struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	// Oldest and Newest are the first entry the files hold and the last
	// one written; Written is when that was.
	Oldest  *time.Time `json:"oldest,omitempty"`
	Newest  *time.Time `json:"newest,omitempty"`
	Written *time.Time `json:"written,omitempty"`
	// Unwritten waits in memory. Lost went from memory before it was
	// written, and Skipped are lines the start could not read back.
	Unwritten uint64 `json:"unwritten"`
	Lost      uint64 `json:"lost,omitempty"`
	Skipped   int    `json:"skipped,omitempty"`
	// Unknown names files in a format this build cannot read.
	Unknown []string `json:"unknown,omitempty"`
	// OverCap says today's file keeps the files over the cap, so the log
	// is not written again until tomorrow.
	OverCap bool   `json:"overCap,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Status reads what the files hold now.
func (w *Writer) Status() Status {
	w.mu.Lock()
	logs := slices.Clone(w.logs)
	st := Status{Enabled: w.files.Enabled, Dir: w.Dir, Free: w.free, Total: w.total, Paused: w.low, Capped: w.capped,
		Logs: []LogStatus{}}
	w.mu.Unlock()
	today := day(w.now())
	for _, l := range logs {
		ls := LogStatus{Name: l.Name}
		list, _ := dayFiles(filepath.Join(w.Dir, l.Name))
		for _, f := range list {
			ls.Bytes += f.size
			ls.Files++
		}
		oldest := ""
		if len(list) > 0 {
			oldest = list[0].path
		}
		if newest := l.Newest(); newest > l.written.Load() {
			ls.Unwritten = newest - l.written.Load()
		}
		w.mu.Lock()
		if l.oldest.path != oldest {
			l.oldest.path, l.oldest.at = oldest, time.Time{}
		}
		cached := l.oldest.at
		w.mu.Unlock()
		if oldest != "" && cached.IsZero() {
			cached = firstTime(oldest)
		}
		w.mu.Lock()
		if l.oldest.path == oldest {
			l.oldest.at = cached
		}
		ls.Oldest = timeOrNil(cached)
		ls.Newest = timeOrNil(l.newest)
		ls.Written = timeOrNil(l.lastWrite)
		ls.Lost, ls.Skipped, ls.Unknown = l.lost, l.skipped, slices.Clone(l.unknown)
		ls.OverCap = l.overCap == today
		if l.err != nil {
			ls.Error = l.err.Error()
		}
		w.mu.Unlock()
		st.Logs = append(st.Logs, ls)
	}
	return st
}

func timeOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
