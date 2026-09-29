// Package logfile keeps the logs Ostiole holds in memory in files as well,
// while System → General says so, and reads them back when the daemon
// starts. Each log has a directory under Dir holding one file per UTC day,
// 2026-09-27.jsonl.gz. A write appends one gzip member of JSON lines and
// syncs it, so zcat, zgrep and jq read the files, and a power cut loses at
// most the write it cut short. Each member's header comment names its
// format, "ostiole firewall 1", so a later format can still tell the old
// one apart.
package logfile

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is where the files go: the daemon unit's LogsDirectory.
const Dir = "/var/log/ostiole"

const (
	// suffix ends a day's file name.
	suffix = ".jsonl.gz"
	// dayLayout is how a file's UTC day is written in its name.
	dayLayout = "2006-01-02"
	// maxLine bounds a line the reader takes: a query answered from
	// sixty-four lists runs to a few kilobytes.
	maxLine = 1 << 20
)

// format is what each member's header says it holds.
func format(name string, version int) string {
	return fmt.Sprintf("ostiole %s %d", name, version)
}

// day is the UTC day t falls on, as a file names it.
func day(t time.Time) string { return t.UTC().Format(dayLayout) }

// dayFile is one day of a log.
type dayFile struct {
	day  string
	path string
	size int64
}

// dayFiles lists a log's day files, oldest first. Anything else in the
// directory is left alone.
func dayFiles(dir string) ([]dayFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []dayFile
	for _, e := range entries {
		d, ok := strings.CutSuffix(e.Name(), suffix)
		if !ok || !e.Type().IsRegular() {
			continue
		}
		if _, err := time.Parse(dayLayout, d); err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, dayFile{day: d, path: filepath.Join(dir, e.Name()), size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].day < out[j].day })
	return out, nil
}

// dayEnd is when a file's day is over.
func dayEnd(d string) time.Time {
	t, err := time.Parse(dayLayout, d)
	if err != nil {
		return time.Time{}
	}
	return t.AddDate(0, 0, 1)
}

// counter counts what a gzip reader takes. It is a byte reader, so neither
// gzip nor flate puts a buffer of their own in front of it and reads ahead:
// the count is exactly where a member ends.
type counter struct {
	r *bufio.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *counter) ReadByte() (byte, error) {
	b, err := c.r.ReadByte()
	if err == nil {
		c.n++
	}
	return b, err
}

// members reads r a member at a time. each is given the header's comment
// and the member's data, as much of which as it likes; whole, when set, is
// told where the member ends once it has checked out. It stops at the
// first member that is cut short or does not check out, and returns where
// the last whole one ended.
func members(r io.Reader, each func(comment string, data io.Reader) error, whole func(end int64)) (end int64, broken bool, err error) {
	c := &counter{r: bufio.NewReaderSize(r, 64<<10)}
	var zr gzip.Reader
	for {
		if err := zr.Reset(c); err != nil {
			if errors.Is(err, io.EOF) && c.n == end {
				return end, false, nil
			}
			return end, true, nil
		}
		zr.Multistream(false)
		if err := each(zr.Comment, &zr); err != nil {
			if isBroken(err) {
				return end, true, nil
			}
			return end, false, err
		}
		// Whatever each left unread, the checksum at the end still has to
		// be reached for the member to count.
		if _, err := io.Copy(io.Discard, &zr); err != nil {
			return end, true, nil
		}
		end = c.n
		if whole != nil {
			whole(end)
		}
	}
}

// isBroken reports whether an error is a member that is cut short or
// corrupt rather than a failure to read the file.
func isBroken(err error) bool {
	var corrupt flate.CorruptInputError
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, gzip.ErrChecksum) ||
		errors.Is(err, gzip.ErrHeader) || errors.As(err, &corrupt)
}

// repair cuts a file back to its last whole member. A write a power cut
// stopped halfway leaves half a member, and nothing appended after it could
// be read. It reports how many bytes went.
func repair(path string) (int64, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end, _, err := members(f, func(string, io.Reader) error { return nil }, nil)
	if err != nil {
		return 0, err
	}
	if end >= info.Size() {
		return 0, nil
	}
	if err := f.Truncate(end); err != nil {
		return 0, err
	}
	return info.Size() - end, f.Sync()
}

// compress makes one member of lines under the header comment.
func compress(zw *gzip.Writer, lines []byte, comment string) ([]byte, error) {
	var buf bytes.Buffer
	zw.Reset(&buf)
	zw.Comment = comment
	if _, err := zw.Write(lines); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// appendFile appends one member to path and syncs it, and the directory
// when the file is new, and says where the member starts. A write that
// fails part way is cut back off, so the next one does not land after half
// a member.
func appendFile(path string, data []byte) (int64, error) {
	_, err := os.Stat(path)
	created := errors.Is(err, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return 0, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return 0, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Truncate(info.Size())
		_ = f.Close()
		return 0, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if created {
		syncDir(filepath.Dir(path))
	}
	return info.Size(), nil
}

// syncDir makes a new file's name as durable as its contents.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// firstTime reads when the first entry of a file was logged, or the zero
// time.
func firstTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer func() { _ = f.Close() }()
	var at time.Time
	_, _, _ = members(f, func(_ string, data io.Reader) error {
		sc := bufio.NewScanner(data)
		sc.Buffer(make([]byte, 0, 4096), maxLine)
		if sc.Scan() {
			_, at = lineInfo(sc.Bytes())
		}
		return errStop
	}, nil)
	return at
}

// errStop ends a read of members early.
var errStop = errors.New("stop")
