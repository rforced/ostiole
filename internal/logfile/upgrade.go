package logfile

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/rforced/ostiole/internal/atomicfile"
)

// NewestSeq is the highest number an entry in a log's files carries, zero
// when they hold none. A log's numbers go on from it after a restart, so a
// number in its files is never given twice, even to what came back from
// a day older than the log keeps.
func NewestSeq(root, name string) uint64 {
	files, err := dayFiles(filepath.Join(root, name))
	if err != nil {
		return 0
	}
	var newest uint64
	for _, f := range files {
		ms, built, err := readIndex(f.path)
		if err != nil {
			continue
		}
		if built {
			_ = saveIndex(f.path, ms)
		}
		for _, m := range ms {
			newest = max(newest, m.Last)
		}
	}
	return newest
}

// Upgrade writes a log's files in format from again in format to, a file
// at a time, oldest first, giving each line the next number through
// number. The numbers go on from the highest already in format to, so an
// upgrade cut short carries on where it stopped. A file holding a member
// in any other format is left as it is: its lines cannot be numbered
// below the ones written after them. It is for a caller that knows nothing
// else is writing the log, and reports how many files it wrote again.
func Upgrade(root, name string, from, to int, number func(line []byte, seq uint64) []byte) (int, error) {
	dir := filepath.Join(root, name)
	files, err := dayFiles(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	atomicfile.Sweep(dir)
	old, cur := format(name, from), format(name, to)
	next := uint64(1)
	done := 0
	for _, f := range files {
		ms, _, err := readIndex(f.path)
		if err != nil {
			return done, err
		}
		all := len(ms) > 0
		for _, m := range ms {
			all = all && m.Format == old
			if m.Format == cur {
				next = max(next, m.Last+1)
			}
		}
		if !all {
			continue
		}
		if err := rewrite(f.path, ms, cur, number, &next); err != nil {
			return done, fmt.Errorf("upgrade %s: %w", f.path, err)
		}
		done++
	}
	return done, nil
}

// rewrite writes a day file again with every line numbered, member for
// member, and its index beside it.
func rewrite(path string, ms []member, format string, number func([]byte, uint64) []byte, next *uint64) error {
	zw, err := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	index := make([]member, 0, len(ms))
	for _, m := range ms {
		lines, err := readMember(path, m)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			continue
		}
		nm := member{At: int64(out.Len()), Format: format}
		var buf []byte
		for _, line := range lines {
			line = number(line, *next)
			_, at := lineInfo(line)
			if nm.N == 0 {
				nm.First, nm.From = *next, at
			}
			nm.Last, nm.To = *next, at
			nm.N++
			*next++
			buf = append(append(buf, line...), '\n')
		}
		data, err := compress(zw, buf, format)
		if err != nil {
			return err
		}
		out.Write(data)
		nm.Size = int64(len(data))
		index = append(index, nm)
	}
	if err := atomicfile.Write(path, out.Bytes(), 0o600); err != nil {
		return err
	}
	return saveIndex(path, index)
}

// WithSeq gives a line of JSON its number, as its first field: how a line
// written before numbers were kept is numbered.
func WithSeq(line []byte, seq uint64) []byte {
	if len(line) < 2 || line[0] != '{' {
		return line
	}
	out := append(make([]byte, 0, len(line)+24), `{"seq":`...)
	out = strconv.AppendUint(out, seq, 10)
	if line[1] != '}' {
		out = append(out, ',')
	}
	return append(out, line[1:]...)
}
