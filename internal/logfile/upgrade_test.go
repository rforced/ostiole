package logfile

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// oldFile writes a day file as the first release of the files did: lines
// without numbers, one member per slice of words.
func oldFile(t *testing.T, dir, day string, comment string, batches ...[]string) {
	t.Helper()
	var file bytes.Buffer
	for _, words := range batches {
		zw := gzip.NewWriter(&file)
		zw.Comment = comment
		for _, w := range words {
			fmt.Fprintf(zw, `{"time":"%sT12:00:00Z","word":%q}`+"\n", day, w)
		}
		_ = zw.Close()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, day+suffix), file.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Files written before numbers were kept are written again numbered, in
// order from the oldest, member for member, and once only. A file with a
// member in another format is left alone.
func TestUpgradeNumbersTheOlderFormat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "test")
	oldFile(t, dir, "2026-09-26", "ostiole test 1", []string{"a", "b"}, []string{"c"})
	oldFile(t, dir, "2026-09-27", "ostiole test 1", []string{"d"})
	oldFile(t, dir, "2026-09-28", "ostiole test 9", []string{"odd"})
	n, err := Upgrade(root, "test", 1, 2, WithSeq)
	if err != nil || n != 2 {
		t.Fatalf("upgraded %d: %v", n, err)
	}
	got, st, err := readAll(root, "test", 2, 100, time.Time{})
	if err != nil || len(st.Unknown) != 1 {
		t.Fatalf("read %+v, %+v (%v)", got, st, err)
	}
	var numbered []string
	for _, e := range got {
		numbered = append(numbered, fmt.Sprintf("%d%s", e.Seq, e.Word))
	}
	if fmt.Sprint(numbered) != "[1a 2b 3c 4d]" {
		t.Errorf("numbered %v", numbered)
	}
	if ms, built, _ := readIndex(filepath.Join(dir, "2026-09-26"+suffix)); built || len(ms) != 2 ||
		ms[0].First != 1 || ms[1].Last != 3 || ms[1].Format != "ostiole test 2" {
		t.Errorf("index built %v: %+v", built, ms)
	}
	if NewestSeq(root, "test") != 4 {
		t.Errorf("newest %d", NewestSeq(root, "test"))
	}
	if n, err := Upgrade(root, "test", 1, 2, WithSeq); err != nil || n != 0 {
		t.Errorf("a second upgrade wrote %d (%v)", n, err)
	}
}

// An upgrade cut short carries on numbering after what it had written.
func TestUpgradeCarriesOnWhereItStopped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "test")
	oldFile(t, dir, "2026-09-26", "ostiole test 1", []string{"a", "b"})
	if _, err := Upgrade(root, "test", 1, 2, WithSeq); err != nil {
		t.Fatal(err)
	}
	oldFile(t, dir, "2026-09-27", "ostiole test 1", []string{"c"})
	if _, err := Upgrade(root, "test", 1, 2, WithSeq); err != nil {
		t.Fatal(err)
	}
	got, _, _ := readAll(root, "test", 2, 100, time.Time{})
	if len(got) != 3 || got[2].Seq != 3 || got[2].Word != "c" {
		t.Errorf("read %+v", got)
	}
}

func TestWithSeqPutsTheNumberFirst(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]string{
		`{"a":1}`:  `{"seq":7,"a":1}`,
		`{}`:       `{"seq":7}`,
		`not json`: `not json`,
	} {
		if got := string(WithSeq([]byte(line), 7)); got != want {
			t.Errorf("%s: %s, want %s", line, got, want)
		}
	}
}
