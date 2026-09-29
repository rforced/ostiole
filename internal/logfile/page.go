package logfile

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/rforced/ostiole/internal/logsearch"
)

// Page reads a log's files the way logsearch.Walk reads its memory: newest
// first from the entry before, or from the newest when before is zero,
// keeping those keep accepts, until limit are kept, the files run out, the
// budget does or ctx is done. Only entries logged at or after since are
// read, and only members in the log's own format. parse reads a line and
// says when its entry was logged; seq is the entry's number.
func Page[E any](ctx context.Context, w *Writer, name string, version int, before uint64, since time.Time,
	limit int, budget logsearch.Budget,
	parse func([]byte) (E, time.Time, error), seq func(*E) uint64, keep func(*E) bool,
) (logsearch.Page[E], error) {
	page := logsearch.Page[E]{Entries: make([]E, 0, min(limit, 256))}
	files, err := dayFiles(filepath.Join(w.Dir, name))
	if errors.Is(err, fs.ErrNotExist) || limit <= 0 {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	want := format(name, version)
	indexes := make([][]member, len(files))
	index := func(fi int) []member {
		if indexes[fi] == nil {
			ms, err := w.index(name, files[fi].path)
			if err != nil || ms == nil {
				ms = []member{}
			}
			indexes[fi] = ms
		}
		return indexes[fi]
	}
	wanted := func(m member) bool {
		return m.Format == want && (before == 0 || m.First < before) && !m.To.Before(since)
	}
	// older reports whether a member before member mi of file fi may hold
	// an entry to read, which is what says a page has one after it.
	older := func(fi, mi int) bool {
		for f := fi; f >= 0 && dayEnd(files[f].day).After(since); f-- {
			ms := index(f)
			from := len(ms) - 1
			if f == fi {
				from = mi - 1
			}
			for m := from; m >= 0; m-- {
				if wanted(ms[m]) {
					return true
				}
			}
		}
		return false
	}
	started := time.Now()
	for fi := len(files) - 1; fi >= 0 && dayEnd(files[fi].day).After(since); fi-- {
		ms := index(fi)
		for mi := len(ms) - 1; mi >= 0; mi-- {
			if !wanted(ms[mi]) {
				continue
			}
			if err := ctx.Err(); err != nil {
				return page, err
			}
			lines, err := readMember(files[fi].path, ms[mi])
			if err != nil {
				// Pruned, or cut back by a repair, since its index was read.
				continue
			}
			// A member's lines are in the order they were numbered, so
			// once one is before the entry asked for, every one before it
			// is too.
			for li := len(lines) - 1; li >= 0; li-- {
				e, at, err := parse(lines[li])
				if err != nil {
					continue
				}
				s := seq(&e)
				if (before != 0 && s >= before) || at.Before(since) {
					continue
				}
				page.Walked++
				page.Last, page.LastAt = s, at
				if keep(&e) {
					page.Entries = append(page.Entries, e)
					if len(page.Entries) >= limit {
						page.Next, page.More = s, li > 0 || older(fi, mi)
						return page, nil
					}
				}
				// The clock is read now and then: it costs more than a match.
				if page.Walked >= budget.Entries || (page.Walked%256 == 0 && time.Since(started) >= budget.Time) {
					page.Next, page.More = s, li > 0 || older(fi, mi)
					if page.More {
						page.SearchedTo = at
					}
					return page, nil
				}
			}
		}
	}
	return page, nil
}

// index reads a day file's index for a page. It holds the log's lock, so
// the writer's next member does not land while the index is built again,
// and saves what had to be built.
func (w *Writer) index(name, path string) ([]member, error) {
	if l := w.find(name); l != nil {
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	ms, built, err := readIndex(path)
	if err == nil && built {
		_ = saveIndex(path, ms)
	}
	return ms, err
}
