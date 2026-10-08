package logfile

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"ostiole/internal/atomicfile"
)

// indexSuffix ends the name of a day's index, beside its file.
const indexSuffix = ".idx"

// member is one write of a day's file as its index has it: where it starts
// and how long it is, its format, the numbers and times of its first and
// last entries, and how many it holds. A page of an old day is read from
// the members it needs rather than from the whole file.
type member struct {
	At     int64     `json:"at"`
	Size   int64     `json:"size"`
	Format string    `json:"format"`
	First  uint64    `json:"first,omitempty"`
	Last   uint64    `json:"last,omitempty"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"`
	N      int       `json:"n"`
}

// indexPath is where a day file's index is kept.
func indexPath(data string) string { return strings.TrimSuffix(data, suffix) + indexSuffix }

// appendIndex adds a member to its file's index. The index can always be
// built again from the file, so it is not synced.
func appendIndex(data string, m member) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(indexPath(data), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readIndex reads a day file's index, or builds it again from the file
// when it is missing or does not account for every byte of it: a write cut
// short before its index line, a repair, a file older than indexes. built
// says it was built again, and is worth saving.
func readIndex(data string) (ms []member, built bool, err error) {
	info, err := os.Stat(data)
	if err != nil {
		return nil, false, err
	}
	if raw, err := os.ReadFile(indexPath(data)); err == nil {
		if ms, ok := parseIndex(raw, info.Size()); ok {
			return ms, false, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	ms, err = buildIndex(data)
	return ms, err == nil, err
}

// parseIndex reads an index, and says whether its members follow each
// other from the start of the file to its end.
func parseIndex(raw []byte, size int64) ([]member, bool) {
	var ms []member
	var end int64
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m member
		if json.Unmarshal(line, &m) != nil || m.At != end || m.Size <= 0 {
			return nil, false
		}
		ms = append(ms, m)
		end = m.At + m.Size
	}
	return ms, end == size
}

// buildIndex walks a day file's members once for what its index holds.
// A member cut short ends it, as it ends a read.
func buildIndex(data string) ([]member, error) {
	f, err := os.Open(data)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var ms []member
	var cur member
	var start int64
	each := func(comment string, r io.Reader) error {
		cur = member{At: start, Format: comment}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64<<10), maxLine)
		for sc.Scan() {
			if len(sc.Bytes()) == 0 {
				continue
			}
			seq, at := lineInfo(sc.Bytes())
			if cur.N == 0 {
				cur.First, cur.From = seq, at
			}
			cur.Last, cur.To = seq, at
			cur.N++
		}
		return sc.Err()
	}
	whole := func(end int64) {
		cur.Size = end - start
		ms = append(ms, cur)
		start = end
	}
	if _, _, err := members(f, each, whole); err != nil {
		return nil, err
	}
	return ms, nil
}

// saveIndex writes a day file's index whole.
func saveIndex(data string, ms []member) error {
	var buf bytes.Buffer
	for _, m := range ms {
		raw, err := json.Marshal(m)
		if err != nil {
			return err
		}
		buf.Write(append(raw, '\n'))
	}
	return atomicfile.Write(indexPath(data), buf.Bytes(), 0o600)
}

// lineInfo reads a line's number and when it was logged: its "logged"
// where it has one, since a WAF event's "time" is when its request opened,
// and otherwise its "time".
func lineInfo(line []byte) (uint64, time.Time) {
	var v struct {
		Seq    uint64    `json:"seq"`
		Logged time.Time `json:"logged"`
		Time   time.Time `json:"time"`
	}
	if json.Unmarshal(line, &v) != nil {
		return 0, time.Time{}
	}
	if !v.Logged.IsZero() {
		return v.Seq, v.Logged
	}
	return v.Seq, v.Time
}

// readMember reads one member's lines.
func readMember(path string, m member) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(io.NewSectionReader(f, m.At, m.Size))
	if err != nil {
		return nil, err
	}
	zr.Multistream(false)
	data, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(line) > 0 {
			out = append(out, line)
		}
	}
	return out, nil
}
