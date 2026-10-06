package linediff

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func show(lines []Line) string {
	var b strings.Builder
	for _, l := range lines {
		mark := map[Kind]string{Removed: "-", Added: "+", same: " "}[l.Kind]
		b.WriteString(mark + l.Text + "\n")
	}
	return b.String()
}

func TestDiff(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, a, b, want string
	}{
		{"equal", "a\nb\n", "a\nb\n", ""},
		{"both empty", "", "", ""},
		{"one line changed", "a\nb\nc\n", "a\nB\nc\n", "-b\n+B\n"},
		{"added at the end", "a\n", "a\nb\n", "+b\n"},
		{"removed at the start", "a\nb\n", "b\n", "-a\n"},
		{"from nothing", "", "a\nb\n", "+a\n+b\n"},
		{"final newline lost", "a\n", "a", "-\n"},
		{"two places", "a\nb\nc\nd\ne\n", "a\nB\nc\nd\nE\n", "-b\n+B\n-e\n+E\n"},
		// Removals come ahead of the additions beside them, whatever order
		// the alignment met them in.
		{"replaced run", "a\nb\nc\nd\n", "a\nx\ny\nd\n", "-b\n-c\n+x\n+y\n"},
	} {
		if got := show(Diff(c.a, c.b)); got != c.want {
			t.Errorf("%s: got\n%swant\n%s", c.name, got, c.want)
		}
	}
}

// lcs is the length of the longest common subsequence, the long way.
func lcs(x, y []string) int {
	prev, cur := make([]int, len(y)+1), make([]int, len(y)+1)
	for i := range x {
		for j := range y {
			if x[i] == y[j] {
				cur[j+1] = prev[j] + 1
			} else {
				cur[j+1] = max(cur[j], prev[j+1])
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(y)]
}

// The alignment turns one text into the other with as few edits as there
// can be: its shared and removed lines are the old text in order, its
// shared and added lines the new one, and what it shares is a longest
// common subsequence.
func TestAlignIsShortest(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	words := []string{"a", "b", "c", "d"}
	text := func() []string {
		out := make([]string, r.IntN(12))
		for i := range out {
			out[i] = words[r.IntN(len(words))]
		}
		return out
	}
	for i := range 5000 {
		x, y := text(), text()
		edits, ok := align(x, y)
		if !ok {
			t.Fatalf("case %d: gave up", i)
		}
		var old, cur []string
		shared := 0
		for _, e := range edits {
			switch e.Kind {
			case same:
				old, cur = append(old, e.Text), append(cur, e.Text)
				shared++
			case Removed:
				old = append(old, e.Text)
			case Added:
				cur = append(cur, e.Text)
			}
		}
		if !slices.Equal(old, x) || !slices.Equal(cur, y) {
			t.Fatalf("case %d: %q to %q gave\n%s", i, x, y, show(edits))
		}
		if want := lcs(x, y); shared != want {
			t.Fatalf("case %d: %q to %q shares %d lines, could share %d", i, x, y, shared, want)
		}
	}
}

// Past maxEdits the middles are given whole rather than aligned.
func TestDiffGivesUpOnTooMany(t *testing.T) {
	t.Parallel()
	var a, b []string
	for i := range maxEdits {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	got := Diff("same\n"+strings.Join(a, "\n")+"\nsame\n", "same\n"+strings.Join(b, "\n")+"\nsame\n")
	if len(got) != 2*maxEdits {
		t.Fatalf("%d lines", len(got))
	}
	if got[0] != (Line{Removed, "old 0"}) || got[maxEdits-1] != (Line{Removed, fmt.Sprintf("old %d", maxEdits-1)}) ||
		got[maxEdits] != (Line{Added, "new 0"}) {
		t.Errorf("order: %v %v %v", got[0], got[maxEdits-1], got[maxEdits])
	}
}

// A long file with one line changed far from either end costs one round,
// however long the file is.
func TestDiffFindsOneLineInALongFile(t *testing.T) {
	t.Parallel()
	var a []string
	for i := range 20000 {
		a = append(a, fmt.Sprintf("line %d", i))
	}
	b := slices.Clone(a)
	b[3] = "changed early"
	b[19990] = "changed late"
	got := show(Diff(strings.Join(a, "\n"), strings.Join(b, "\n")))
	if want := "-line 3\n+changed early\n-line 19990\n+changed late\n"; got != want {
		t.Errorf("got\n%s", got)
	}
}
