package journalfeed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Record is one entry of a unit's journal.
type Record struct {
	Cursor  string
	Time    time.Time
	Message string
	// Stream is the output the entry came from. journald cuts a line
	// longer than its LineMax into several entries, and Cut marks each
	// piece but the last.
	Stream string
	Cut    bool
}

// Journal reads a unit's journal. The daemon's runs journalctl; a test's
// hands back what it was given.
type Journal interface {
	// Back hands fn the entries from the newest back to since, until fn
	// returns false or there are no more.
	Back(ctx context.Context, since time.Time, fn func(Record) bool) error
	// Follow hands fn the entries after cursor, or from since when there is
	// no cursor, and then each one as it is written, until ctx is done or
	// the read fails.
	Follow(ctx context.Context, cursor string, since time.Time, fn func(Record)) error
	// Holds reports whether the journal still has the entry at cursor.
	// Vacuuming takes the oldest files, and a cursor into one of them
	// would be read from wherever the journal starts now.
	Holds(ctx context.Context, cursor string) (bool, error)
}

// Journalctl reads one unit's journal with journalctl. Unit may be a
// pattern, which reads every instance of a template.
type Journalctl struct {
	Unit string
	// Bin is the journalctl to run; empty finds it on PATH.
	Bin string
}

// maxLine is the longest entry read, and the longest line put back
// together from pieces. A WAF event is a few kilobytes; anything past this
// is skipped rather than held.
const maxLine = 4 << 20

// args is what every read passes. --all, because without it journalctl
// reports a field over a few kilobytes as null. The message is asked for
// with what says whether journald cut it; the cursor and the time come
// with every entry.
func (j Journalctl) args(more ...string) []string {
	return append([]string{
		"--unit=" + j.Unit, "--output=json", "--output-fields=MESSAGE,_STREAM_ID,_LINE_BREAK",
		"--all", "--no-pager",
	}, more...)
}

// Back implements Journal.
func (j Journalctl) Back(ctx context.Context, since time.Time, fn func(Record) bool) error {
	return j.run(ctx, j.args("--reverse", "--since="+at(since)), fn)
}

// Follow implements Journal.
func (j Journalctl) Follow(ctx context.Context, cursor string, since time.Time, fn func(Record)) error {
	from := "--since=" + at(since)
	if cursor != "" {
		from = "--after-cursor=" + cursor
	}
	err := j.run(ctx, j.args("--follow", from), func(r Record) bool {
		fn(r)
		return true
	})
	if err == nil && ctx.Err() == nil {
		return errors.New("journalctl stopped following")
	}
	return err
}

// Holds implements Journal. The entry at a cursor that is gone is the next
// one the journal still has, which carries a cursor of its own.
func (j Journalctl) Holds(ctx context.Context, cursor string) (bool, error) {
	held := false
	err := j.run(ctx, j.args("--cursor="+cursor, "--lines=1"), func(r Record) bool {
		held = r.Cursor == cursor
		return false
	})
	return held, err
}

// at is a time as journalctl reads it: whole seconds since 1970, which no
// zone can misread. Rounding down reads a moment more, never less.
func at(t time.Time) string { return "@" + strconv.FormatInt(t.Unix(), 10) }

// run starts journalctl and hands fn each entry it prints, stopping it when
// fn returns false.
func (j Journalctl) run(ctx context.Context, args []string, fn func(Record) bool) error {
	bin := j.Bin
	if bin == "" {
		bin = "journalctl"
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stopped := false
	rd := bufio.NewReaderSize(out, 64<<10)
	for {
		line, err := readLine(rd)
		if err != nil {
			break
		}
		r, ok := parseRecord(line)
		if !ok {
			continue
		}
		if !fn(r) {
			stopped = true
			cancel()
			break
		}
	}
	// Whatever was not read goes, so a stopped journalctl is not left
	// blocked on a full pipe.
	_, _ = io.Copy(io.Discard, out)
	err = cmd.Wait()
	if stopped || ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("journalctl: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// readLine reads one line, skipping any longer than maxLine.
func readLine(rd *bufio.Reader) ([]byte, error) {
	var buf []byte
	skip := false
	for {
		part, err := rd.ReadSlice('\n')
		if !skip {
			if len(buf)+len(part) > maxLine {
				skip, buf = true, nil
			} else {
				buf = append(buf, part...)
			}
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return nil, err
		case skip:
			skip = false
			continue
		}
		return buf, nil
	}
}

// parseRecord reads one line of journalctl's JSON. The message is a string,
// or an array of bytes when it is not valid UTF-8.
func parseRecord(line []byte) (Record, bool) {
	var e struct {
		Cursor    string          `json:"__CURSOR"`
		Realtime  string          `json:"__REALTIME_TIMESTAMP"`
		Message   json.RawMessage `json:"MESSAGE"`
		Stream    string          `json:"_STREAM_ID"`
		LineBreak string          `json:"_LINE_BREAK"`
	}
	if err := json.Unmarshal(line, &e); err != nil || e.Cursor == "" {
		return Record{}, false
	}
	// The other breaks, nul, eof and pid-change, end a line too.
	r := Record{Cursor: e.Cursor, Stream: e.Stream, Cut: e.LineBreak == "line-max"}
	if us, err := strconv.ParseInt(e.Realtime, 10, 64); err == nil {
		r.Time = time.UnixMicro(us).UTC()
	}
	var s string
	if err := json.Unmarshal(e.Message, &s); err == nil {
		r.Message = s
	} else {
		var b []byte
		if err := json.Unmarshal(e.Message, &b); err == nil {
			r.Message = string(bytes.ToValidUTF8(b, []byte("?")))
		}
	}
	return r, true
}

// limited keeps the first n bytes written to it and drops the rest, so a
// chatty journalctl cannot grow the error without end.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
