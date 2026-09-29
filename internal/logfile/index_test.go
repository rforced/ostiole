package logfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// written is a rig whose files hold one entry a minute from noon, word i
// numbered i+1, written in batches of per.
func written(t *testing.T, n, per int) *rig {
	t.Helper()
	g := newRig(t, 1000)
	start := g.clock.t
	for i := range n {
		g.r.add(start.Add(time.Duration(i)*time.Minute), fmt.Sprint(i))
		if (i+1)%per == 0 {
			g.w.writeDue(true)
		}
	}
	g.w.writeDue(true)
	return g
}

// Each member written has a line in its day's index saying where it is and
// what it holds.
func TestTheWriterIndexesEachMember(t *testing.T) {
	t.Parallel()
	g := written(t, 5, 3)
	path := filepath.Join(g.w.Dir, "test", "2026-09-27"+suffix)
	ms, built, err := readIndex(path)
	if err != nil || built {
		t.Fatalf("index built %v: %v", built, err)
	}
	info, _ := os.Stat(path)
	noon := g.clock.t
	want := []member{
		{At: 0, Format: "ostiole test 1", First: 1, Last: 3, From: noon, To: noon.Add(2 * time.Minute), N: 3},
		{Format: "ostiole test 1", First: 4, Last: 5, From: noon.Add(3 * time.Minute), To: noon.Add(4 * time.Minute), N: 2},
	}
	if len(ms) != 2 || ms[1].At != ms[0].Size || ms[1].At+ms[1].Size != info.Size() {
		t.Fatalf("index %+v for %d bytes", ms, info.Size())
	}
	for i := range want {
		want[i].At, want[i].Size = ms[i].At, ms[i].Size
		if ms[i] != want[i] {
			t.Errorf("member %d = %+v, want %+v", i, ms[i], want[i])
		}
	}
}

// An index that is missing, short or runs past the file is built again
// from the file, the same as it was.
func TestAnIndexThatDoesNotMatchIsBuiltAgain(t *testing.T) {
	t.Parallel()
	g := written(t, 5, 3)
	path := filepath.Join(g.w.Dir, "test", "2026-09-27"+suffix)
	good, _, _ := readIndex(path)
	raw, _ := os.ReadFile(indexPath(path))
	for name, damage := range map[string]func(){
		"missing": func() { _ = os.Remove(indexPath(path)) },
		"short":   func() { _ = os.WriteFile(indexPath(path), raw[:bytes.IndexByte(raw, '\n')+1], 0o600) },
		"long":    func() { _ = os.WriteFile(indexPath(path), append(slices.Clone(raw), raw...), 0o600) },
		"garbled": func() { _ = os.WriteFile(indexPath(path), []byte("{\n"), 0o600) },
	} {
		damage()
		ms, built, err := readIndex(path)
		if err != nil || !built || !slices.Equal(ms, good) {
			t.Errorf("%s: built %v, %+v (%v)", name, built, ms, err)
		}
	}
}
