package logfile

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Count is how many lines a log's indexes give its members ending at or
// after since, bad lines and a straddling member's older lines included.
func Count(root, name string, since time.Time) (int, error) {
	files, err := dayFiles(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, f := range files {
		if !dayEnd(f.day).After(since) {
			continue
		}
		ms, err := savedIndex(f.path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", f.path, err))
			continue
		}
		for _, m := range ms {
			if !ended(m, since) {
				n += m.N
			}
		}
	}
	return n, errors.Join(errs...)
}

// streamDay is a day file's members still wanted, and where its index ends.
type streamDay struct {
	path string
	size int64
	end  int64
	ms   []member
}

// Stream hands fn a log's newest limit entries logged at or after since,
// oldest first, leaving unread the older members its indexes count out.
// fn can be handed up to a member's entries over limit: keep them in a ring.
func Stream[E any](root, name string, version int, limit int, since time.Time,
	parse func([]byte) (E, time.Time, error), fn func(E),
) (ReadStats, error) {
	var st ReadStats
	if limit <= 0 {
		return st, nil
	}
	files, err := dayFiles(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	want := format(name, version)
	count := func(m member) int {
		if m.Format != want {
			return 0
		}
		return m.N
	}
	var errs []error
	days := make([]streamDay, 0, len(files))
	total := 0
	for _, f := range files {
		if !dayEnd(f.day).After(since) {
			continue
		}
		ms, err := savedIndex(f.path)
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", f.path, err))
			continue
		}
		d := streamDay{path: f.path, size: f.size}
		for _, m := range ms {
			d.end = m.At + m.Size
			if !ended(m, since) {
				d.ms = append(d.ms, m)
				total += count(m)
			}
		}
		days = append(days, d)
	}
	skip := 0
over:
	for _, d := range days {
		for _, m := range d.ms {
			if total-count(m) < limit {
				break over
			}
			total -= count(m)
			skip++
		}
	}

	buf := make([]byte, 64<<10)
	br := bufio.NewReaderSize(nil, 64<<10)
	var zr gzip.Reader
	read := func(path string, ms []member) (unknown, broken bool, err error) {
		f, err := os.Open(path)
		if err != nil {
			return false, false, err
		}
		defer func() { _ = f.Close() }()
		for _, m := range ms {
			br.Reset(io.NewSectionReader(f, m.At, m.Size))
			if err := zr.Reset(br); err != nil {
				if errors.Is(err, io.EOF) || isBroken(err) {
					return unknown, true, nil
				}
				return unknown, false, err
			}
			zr.Multistream(false)
			if zr.Comment != want {
				unknown = true
				continue
			}
			sc := bufio.NewScanner(&zr)
			sc.Buffer(buf, maxLine)
			for sc.Scan() {
				if len(sc.Bytes()) == 0 {
					continue
				}
				e, at, err := parse(sc.Bytes())
				switch {
				case err != nil:
					st.Skipped++
				case !at.Before(since):
					fn(e)
				}
			}
			if err := sc.Err(); err != nil {
				if isBroken(err) {
					return unknown, true, nil
				}
				return unknown, false, err
			}
		}
		return unknown, false, nil
	}
	for _, d := range days {
		if skip > 0 && skip >= len(d.ms) {
			skip -= len(d.ms)
			continue
		}
		ms := d.ms[skip:]
		skip = 0
		var unknown, broken bool
		var err error
		if len(ms) > 0 {
			unknown, broken, err = read(d.path, ms)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", d.path, err))
		} else if d.size > d.end {
			// The index stops at a member cut short.
			broken = true
		}
		if unknown {
			st.Unknown = append(st.Unknown, filepath.Base(d.path))
		}
		if broken {
			st.Broken = append(st.Broken, filepath.Base(d.path))
		}
	}
	return st, errors.Join(errs...)
}

// savedIndex reads a day file's index, saving one it had to build.
func savedIndex(path string) ([]member, error) {
	ms, built, err := readIndex(path)
	if err == nil && built {
		_ = saveIndex(path, ms)
	}
	return ms, err
}

// ended reports whether a member ends before since, a timeless one never.
func ended(m member, since time.Time) bool {
	return !m.To.IsZero() && m.To.Before(since)
}
