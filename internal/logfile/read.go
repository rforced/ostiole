package logfile

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ReadStats is what reading a log back found beside its entries.
type ReadStats struct {
	// Skipped counts lines that did not decode.
	Skipped int
	// Unknown names the files holding a member in a format this build
	// cannot read, which was passed over.
	Unknown []string
	// Broken names the files cut short, read up to the cut.
	Broken []string
}

// Read reads a log's files back, newest first, until it holds limit
// entries or reaches since, and returns them oldest first: what the log's
// memory would hold had the daemon not stopped. parse reads one line and
// says when its entry was logged. A file that cannot be read is passed
// over and named in the error, beside what the rest gave.
func Read[E any](root, name string, version int, limit int, since time.Time,
	parse func([]byte) (E, time.Time, error),
) ([]E, ReadStats, error) {
	var st ReadStats
	if limit <= 0 {
		return nil, st, nil
	}
	files, err := dayFiles(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, st, nil
	}
	if err != nil {
		return nil, st, err
	}
	want := format(name, version)
	var parts [][]E
	var errs []error
	have := 0
	for i := len(files) - 1; i >= 0 && have < limit; i-- {
		f := files[i]
		// Nothing in a day that ended before since is kept, nor in any
		// day before it.
		if !dayEnd(f.day).After(since) {
			break
		}
		w := window[E]{max: limit - have}
		unknown, broken, err := readFile(f.path, want, since, parse, &w, &st.Skipped)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", f.path, err))
		}
		if unknown {
			st.Unknown = append(st.Unknown, filepath.Base(f.path))
		}
		if broken {
			st.Broken = append(st.Broken, filepath.Base(f.path))
		}
		got := w.items()
		parts = append(parts, got)
		have += len(got)
	}
	out := make([]E, 0, have)
	for i := len(parts) - 1; i >= 0; i-- {
		out = append(out, parts[i]...)
	}
	return out, st, errors.Join(errs...)
}

// readFile reads one day's lines into w, a member at a time: a member's
// entries count only once it has checked out, since the writer cuts a
// broken one off before it appends. A member in another format is passed
// over.
func readFile[E any](path, want string, since time.Time, parse func([]byte) (E, time.Time, error),
	w *window[E], skipped *int,
) (unknown, broken bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, err
	}
	defer func() { _ = f.Close() }()
	var staged []E
	bad := 0
	each := func(comment string, data io.Reader) error {
		staged, bad = staged[:0], 0
		if comment != want {
			unknown = true
			return nil
		}
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 64<<10), maxLine)
		for sc.Scan() {
			if len(sc.Bytes()) == 0 {
				continue
			}
			e, at, err := parse(sc.Bytes())
			switch {
			case err != nil:
				bad++
			case !at.Before(since):
				staged = append(staged, e)
			}
		}
		return sc.Err()
	}
	whole := func(int64) {
		for _, e := range staged {
			w.push(e)
		}
		*skipped += bad
		staged = staged[:0]
	}
	_, broken, err = members(f, each, whole)
	return unknown, broken, err
}

// window keeps the last max entries pushed into it, or every one when max
// is negative.
type window[E any] struct {
	max  int
	buf  []E
	next int
}

func (w *window[E]) push(e E) {
	if w.max < 0 {
		// No bound: a day's entries, all kept.
		w.buf = append(w.buf, e)
		return
	}
	if w.max == 0 {
		return
	}
	if len(w.buf) < w.max {
		w.buf = append(w.buf, e)
		return
	}
	w.buf[w.next] = e
	w.next = (w.next + 1) % w.max
}

// items is what the window holds, oldest first.
func (w *window[E]) items() []E {
	if w.next == 0 {
		return w.buf
	}
	out := make([]E, 0, len(w.buf))
	out = append(out, w.buf[w.next:]...)
	return append(out, w.buf[:w.next]...)
}

// ReadEach reads a log's files back oldest first from the day since falls
// in, handing each entry logged at or after since to fn: for a log rebuilt
// from all its lines rather than refilled to a size. A file that cannot be
// read is passed over and named in the error.
func ReadEach[E any](root, name string, version int, since time.Time,
	parse func([]byte) (E, time.Time, error), fn func(E),
) (ReadStats, error) {
	var st ReadStats
	files, err := dayFiles(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	want := format(name, version)
	var errs []error
	for _, f := range files {
		if !dayEnd(f.day).After(since) {
			continue
		}
		// A member's entries go on only once it checks out, as Read's
		// do; a day at a time is what is held.
		w := &window[E]{max: -1}
		unknown, broken, err := readFile(f.path, want, since, parse, w, &st.Skipped)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", f.path, err))
		}
		for _, e := range w.buf {
			fn(e)
		}
		if unknown {
			st.Unknown = append(st.Unknown, filepath.Base(f.path))
		}
		if broken {
			st.Broken = append(st.Broken, filepath.Base(f.path))
		}
	}
	return st, errors.Join(errs...)
}

// Ascending keeps the entries read back whose numbers rise, in place: a
// log's numbers only grow, and one that does not, or has none, would put
// the ring out of order.
func Ascending[E any](entries []E, seq func(*E) uint64) []E {
	out := entries[:0]
	var last uint64
	for i := range entries {
		if s := seq(&entries[i]); s > last {
			out = append(out, entries[i])
			last = s
		}
	}
	return out
}
