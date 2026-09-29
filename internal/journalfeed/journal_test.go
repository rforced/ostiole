package journalfeed

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeJournalctl writes a journalctl that records its arguments, prints
// lines and then, when hang is set, waits to be stopped as a follower does.
// The tests that run one are not parallel: a script written while another
// test forks can be held open by the child, and then fails to run with
// "text file busy".
func fakeJournalctl(t *testing.T, lines []string, hang bool, exit int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "lines")
	if err := os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	argsFile = filepath.Join(dir, "args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\ncat %q\n", argsFile, out)
	if hang {
		script += "exec sleep 60\n"
	}
	if exit != 0 {
		script += fmt.Sprintf("echo 'Failed to seek to cursor' >&2\nexit %d\n", exit)
	}
	bin = filepath.Join(dir, "journalctl")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func line(cursor, message string) string {
	return fmt.Sprintf(`{"__CURSOR":%q,"__REALTIME_TIMESTAMP":"1790480447622867","MESSAGE":%q}`, cursor, message)
}

func argsOf(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(raw))
}

func TestBackReadsNewestFirstAndStopsWhenAsked(t *testing.T) {
	bin, args := fakeJournalctl(t, []string{line("c3", "three"), line("c2", "two"), line("c1", "one")}, true, 0)
	j := Journalctl{Unit: "ostiole-proxy.service", Bin: bin}
	since := time.Unix(1790000000, 500)
	var got []string
	started := time.Now()
	err := j.Back(context.Background(), since, func(r Record) bool {
		got = append(got, r.Cursor)
		return len(got) < 2
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[c3 c2]" {
		t.Errorf("read %v", got)
	}
	// It was stopped, not waited out.
	if time.Since(started) > 10*time.Second {
		t.Error("the read was not stopped")
	}
	want := []string{
		"--unit=ostiole-proxy.service", "--output=json", "--output-fields=MESSAGE,_STREAM_ID,_LINE_BREAK",
		"--all", "--no-pager", "--reverse", "--since=@1790000000",
	}
	if a := argsOf(t, args); !slices.Equal(a, want) {
		t.Errorf("args = %v, want %v", a, want)
	}
}

func TestFollowStartsAfterTheCursorOrAtTheTime(t *testing.T) {
	for name, tc := range map[string]struct {
		cursor string
		want   string
	}{
		"cursor": {"s=1;i=2", "--after-cursor=s=1;i=2"},
		"time":   {"", "--since=@1790000000"},
	} {
		t.Run(name, func(t *testing.T) {
			bin, args := fakeJournalctl(t, []string{line("c1", "one")}, false, 0)
			j := Journalctl{Unit: "ostiole-proxy.service", Bin: bin}
			var got []string
			err := j.Follow(context.Background(), tc.cursor, time.Unix(1790000000, 0), func(r Record) {
				got = append(got, r.Message)
			})
			// A follower that exits on its own has failed.
			if err == nil {
				t.Error("a journalctl that exited was taken for a follower still going")
			}
			if fmt.Sprint(got) != "[one]" {
				t.Errorf("read %v", got)
			}
			a := argsOf(t, args)
			if !slices.Contains(a, "--follow") || !slices.Contains(a, tc.want) {
				t.Errorf("args = %v, want --follow and %s", a, tc.want)
			}
		})
	}
}

func TestFollowEndsQuietlyWhenStopped(t *testing.T) {
	bin, _ := fakeJournalctl(t, []string{line("c1", "one")}, true, 0)
	j := Journalctl{Unit: "ostiole-proxy.service", Bin: bin}
	ctx, cancel := context.WithCancel(context.Background())
	err := j.Follow(ctx, "c0", time.Time{}, func(Record) { cancel() })
	if err != nil {
		t.Errorf("stopping the follower reported %v", err)
	}
}

func TestHoldsComparesTheCursorItLandsOn(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		want  bool
	}{
		"there":           {[]string{line("s=1;i=5", "x")}, true},
		"vacuumed":        {[]string{line("s=1;i=9", "x")}, false},
		"nothing after":   {nil, false},
		"another journal": {[]string{line("s=2;i=5", "x")}, false},
	} {
		t.Run(name, func(t *testing.T) {
			bin, args := fakeJournalctl(t, tc.lines, false, 0)
			j := Journalctl{Unit: "ostiole-proxy.service", Bin: bin}
			held, err := j.Holds(context.Background(), "s=1;i=5")
			if err != nil || held != tc.want {
				t.Errorf("holds = %v, %v; want %v", held, err, tc.want)
			}
			a := argsOf(t, args)
			if !slices.Contains(a, "--cursor=s=1;i=5") || !slices.Contains(a, "--lines=1") {
				t.Errorf("args = %v", a)
			}
		})
	}
}

func TestAFailingJournalctlSaysWhy(t *testing.T) {
	bin, _ := fakeJournalctl(t, nil, false, 1)
	j := Journalctl{Unit: "ostiole-proxy.service", Bin: bin}
	_, err := j.Holds(context.Background(), "bad")
	if err == nil || !strings.Contains(err.Error(), "Failed to seek to cursor") {
		t.Errorf("err = %v", err)
	}
}
func TestParseRecordReadsBothMessageForms(t *testing.T) {
	t.Parallel()
	r, ok := parseRecord([]byte(`{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1790480447622867","MESSAGE":"hello"}`))
	if !ok || r.Cursor != "s=1;i=2" || r.Message != "hello" || r.Time.UnixMicro() != 1790480447622867 {
		t.Errorf("string message: %+v %v", r, ok)
	}
	// journalctl prints a message that is not UTF-8 as its bytes.
	r, ok = parseRecord([]byte(`{"__CURSOR":"c","__REALTIME_TIMESTAMP":"1","MESSAGE":[104,105,255]}`))
	if !ok || r.Message != "hi?" {
		t.Errorf("byte message: %+v %v", r, ok)
	}
	if _, ok := parseRecord([]byte(`{"MESSAGE":"no cursor"}`)); ok {
		t.Error("an entry with no cursor was read")
	}
	// Only a line journald cut for its length goes on in the next entry.
	for lineBreak, cut := range map[string]bool{"line-max": true, "eof": false, "nul": false, "pid-change": false} {
		raw := fmt.Sprintf(`{"__CURSOR":"c","MESSAGE":"x","_STREAM_ID":"s1","_LINE_BREAK":%q}`, lineBreak)
		if r, _ := parseRecord([]byte(raw)); r.Cut != cut || r.Stream != "s1" {
			t.Errorf("%s: %+v", lineBreak, r)
		}
	}
}

func TestReadLineSkipsALineTooLongToHold(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxLine+10)
	// A small buffer, so every line is read in parts.
	rd := bufio.NewReaderSize(strings.NewReader("one\n"+long+"\ntwo\n"), 16)
	for _, want := range []string{"one\n", "two\n"} {
		got, err := readLine(rd)
		if err != nil || string(got) != want {
			t.Fatalf("read %q (%v), want %q", got, err, want)
		}
	}
}
