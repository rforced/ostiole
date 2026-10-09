package logfile

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// fiveDays is a rig whose files hold six entries a day from the 23rd to the
// 27th, four hours apart from midnight, two to a member.
func fiveDays(t *testing.T) *rig {
	t.Helper()
	g := newRig(t, 1000)
	for d := 23; d <= 27; d++ {
		for h := range 6 {
			g.r.add(time.Date(2026, 9, d, 4*h, 0, 0, 0, time.UTC), fmt.Sprintf("%d-%d", d, h))
			if h%2 == 1 {
				g.w.writeDue(true)
			}
		}
	}
	return g
}

func streamWords(t *testing.T, root string, limit int, since time.Time) ([]string, ReadStats) {
	t.Helper()
	var got []string
	st, err := Stream(root, "test", 1, limit, since, parse, func(e entry) { got = append(got, e.Word) })
	if err != nil {
		t.Fatal(err)
	}
	return got, st
}

// readAll is what Stream hands over, cut to the newest limit entries.
func readAll(root, name string, version, limit int, since time.Time) ([]entry, ReadStats, error) {
	var got []entry
	st, err := Stream(root, name, version, limit, since, parse, func(e entry) { got = append(got, e) })
	if limit >= 0 && len(got) > limit {
		got = got[len(got)-limit:]
	}
	return got, st, err
}

// spoil breaks the gzip header of a day file's member i, leaving its index
// as it was.
func spoil(t *testing.T, path string, i int) {
	t.Helper()
	ms, _, err := readIndex(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[ms[i].At] = 0
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gzMember(comment, body string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Comment = comment
	_, _ = zw.Write([]byte(body))
	_ = zw.Close()
	return buf.Bytes()
}

// What Stream hands over ends in the newest limit entries logged at or
// after since, oldest first, and runs over the limit by less than a member.
func TestStreamEndsInTheNewestEntries(t *testing.T) {
	t.Parallel()
	g := fiveDays(t)
	sinces := []time.Time{
		{},
		time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
	}
	for _, since := range sinces {
		all := words(slices.DeleteFunc(slices.Clone(g.r.entries), func(e entry) bool { return e.Time.Before(since) }))
		for limit := 1; limit <= 32; limit++ {
			want := all[max(0, len(all)-limit):]
			got, st := streamWords(t, g.w.Dir, limit, since)
			if !slices.Equal(got[max(0, len(got)-limit):], want) || len(got) > limit+1 {
				t.Errorf("since %v, limit %d: %v, want it to end in %v", since, limit, got, want)
			}
			if st.Skipped != 0 || st.Unknown != nil || st.Broken != nil {
				t.Errorf("since %v, limit %d: stats %+v", since, limit, st)
			}
		}
	}
}

// Cut to the limit, what Stream hands over is the newest entries within
// the limit and the days, oldest first.
func TestStreamKeepsTheNewestWithinTheLimitAndDays(t *testing.T) {
	t.Parallel()
	g := newRig(t, 1000)
	for d := 20; d <= 27; d++ {
		for h := range 3 {
			g.r.add(time.Date(2026, 9, d, 8*h, 0, 0, 0, time.UTC), fmt.Sprintf("%d-%d", d, h))
		}
	}
	g.w.writeDue(true)

	got, st, err := readAll(g.w.Dir, "test", 1, 4, time.Time{})
	if err != nil || st.Skipped != 0 {
		t.Fatal(err, st)
	}
	if !slices.Equal(words(got), []string{"26-2", "27-0", "27-1", "27-2"}) {
		t.Errorf("four entries = %v", words(got))
	}
	since := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	got, _, _ = readAll(g.w.Dir, "test", 1, 1000, since)
	if !slices.Equal(words(got), []string{"25-2", "26-0", "26-1", "26-2", "27-0", "27-1", "27-2"}) {
		t.Errorf("since the 25th at noon = %v", words(got))
	}
	// Nothing read, nothing there.
	if got, _, err := readAll(g.w.Dir, "missing", 1, 10, time.Time{}); err != nil || len(got) != 0 {
		t.Errorf("missing log = %v, %v", got, err)
	}
}

// Days and members that end before since are not read, nor the entries
// before it in a member that straddles it.
func TestStreamStartsAtSince(t *testing.T) {
	t.Parallel()
	g := fiveDays(t)
	dir := filepath.Join(g.w.Dir, "test")
	spoil(t, filepath.Join(dir, "2026-09-24"+suffix), 0)
	spoil(t, filepath.Join(dir, "2026-09-26"+suffix), 0)
	got, st := streamWords(t, g.w.Dir, 100, time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC))
	want := []string{"26-3", "26-4", "26-5", "27-0", "27-1", "27-2", "27-3", "27-4", "27-5"}
	if !slices.Equal(got, want) || st.Broken != nil {
		t.Errorf("entries %v, broken %v", got, st.Broken)
	}
}

// The members older than the newest limit entries are passed over unread,
// so a spoilt one there goes unnoticed until the limit reaches it, and the
// files after it still stream.
func TestStreamLeavesOlderMembersUnread(t *testing.T) {
	t.Parallel()
	g := fiveDays(t)
	spoil(t, filepath.Join(g.w.Dir, "test", "2026-09-26"+suffix), 0)
	if got, st := streamWords(t, g.w.Dir, 3, time.Time{}); !slices.Equal(got, []string{"27-2", "27-3", "27-4", "27-5"}) ||
		st.Broken != nil {
		t.Errorf("limit 3: %v, broken %v", got, st.Broken)
	}
	if got, st := streamWords(t, g.w.Dir, 7, time.Time{}); !slices.Equal(got, []string{"26-4", "26-5", "27-0", "27-1", "27-2", "27-3", "27-4", "27-5"}) ||
		st.Broken != nil {
		t.Errorf("limit 7: %v, broken %v", got, st.Broken)
	}
	got, st := streamWords(t, g.w.Dir, 100, time.Time{})
	want := words(slices.DeleteFunc(slices.Clone(g.r.entries), func(e entry) bool { return e.Time.Day() == 26 }))
	if !slices.Equal(got, want) || len(got) != 24 || slices.Contains(got, "26-5") || !slices.Contains(got, "27-5") {
		t.Errorf("limit 100: %v, want %v", got, want)
	}
	if !slices.Equal(st.Broken, []string{"2026-09-26" + suffix}) {
		t.Errorf("broken %v", st.Broken)
	}
}

// A bad line is counted, a member in a format this build does not know is
// passed over and its file named, a file with a cut member is named and
// read up to it, and the next file is read; a file without an index gets
// one.
func TestStreamPassesOverWhatItCannotRead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var file []byte
	file = append(file, gzMember("ostiole test 1", `{"time":"2026-09-26T01:00:00Z","word":"a"}`+"\nnot json\n")...)
	file = append(file, gzMember("ostiole test 2", `{"time":"2026-09-26T02:00:00Z","word":"future"}`+"\n")...)
	file = append(file, gzMember("ostiole test 1", `{"time":"2026-09-26T03:00:00Z","word":"b"}`+"\n")...)
	cut := gzMember("ostiole test 1", `{"time":"2026-09-26T04:00:00Z","word":"cut"}`+"\n")
	file = append(file, cut[:len(cut)-3]...)
	if err := os.WriteFile(filepath.Join(dir, "2026-09-26"+suffix), file, 0o600); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(dir, "2026-09-27"+suffix)
	if err := os.WriteFile(next, gzMember("ostiole test 1", `{"time":"2026-09-27T01:00:00Z","word":"c"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, st := streamWords(t, root, 100, time.Time{})
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("entries = %v", got)
	}
	if st.Skipped != 1 || !slices.Equal(st.Unknown, []string{"2026-09-26" + suffix}) ||
		!slices.Equal(st.Broken, []string{"2026-09-26" + suffix}) {
		t.Errorf("stats = %+v", st)
	}
	if ms, built, err := readIndex(next); err != nil || built || len(ms) != 1 || ms[0].N != 1 {
		t.Errorf("index of the 27th built %v: %+v, %v", built, ms, err)
	}
}

// With no limit, as the daemon reads the traffic and gateway minutes,
// Stream hands every entry since a time over, oldest first, file by file.
func TestStreamWithoutALimitGoesOldestFirst(t *testing.T) {
	t.Parallel()
	g := newRig(t, 1000)
	for d := 24; d <= 27; d++ {
		for h := range 2 {
			g.r.add(time.Date(2026, 9, d, 12*h, 0, 0, 0, time.UTC), fmt.Sprintf("%d-%d", d, h))
		}
	}
	g.w.writeDue(true)
	var got []string
	st, err := Stream(g.w.Dir, "test", 1, math.MaxInt, time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC), parse,
		func(e entry) { got = append(got, e.Word) })
	if err != nil || st.Skipped != 0 {
		t.Fatal(err, st)
	}
	if !slices.Equal(got, []string{"25-1", "26-0", "26-1", "27-0", "27-1"}) {
		t.Errorf("entries = %v", got)
	}
	if _, err := Stream(g.w.Dir, "missing", 1, math.MaxInt, time.Time{}, parse, func(entry) { t.Error("read") }); err != nil {
		t.Error(err)
	}
}

// Count adds up the indexes of the members that end at or after since, a
// member straddling it whole, and builds and keeps an index that is
// missing.
func TestCountReadsTheIndexes(t *testing.T) {
	t.Parallel()
	g := fiveDays(t)
	path := filepath.Join(g.w.Dir, "test", "2026-09-27"+suffix)
	if err := os.Remove(indexPath(path)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		since time.Time
		want  int
	}{
		{time.Time{}, 30},
		{time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC), 18},
		{time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC), 16},
		{time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), 0},
	} {
		if n, err := Count(g.w.Dir, "test", c.since); err != nil || n != c.want {
			t.Errorf("since %v: %d, %v, want %d", c.since, n, err, c.want)
		}
	}
	if _, built, err := readIndex(path); err != nil || built {
		t.Errorf("index of the 27th built %v, %v", built, err)
	}
}

// A log with no files has nothing to count or hand over, and nor does a
// limit of none.
func TestStreamWithNothingToRead(t *testing.T) {
	t.Parallel()
	g := fiveDays(t)
	if n, err := Count(g.w.Dir, "missing", time.Time{}); n != 0 || err != nil {
		t.Errorf("count of a missing log = %d, %v", n, err)
	}
	for name, limit := range map[string]int{"missing": 10, "test": 0} {
		st, err := Stream(g.w.Dir, name, 1, limit, time.Time{}, parse, func(entry) { t.Errorf("%s: handed an entry", name) })
		if err != nil || st.Skipped != 0 || st.Unknown != nil || st.Broken != nil {
			t.Errorf("%s: %+v, %v", name, st, err)
		}
	}
}
