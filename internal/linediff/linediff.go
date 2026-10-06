// Package linediff lists the lines two texts do not share, for showing what
// rewriting a file would change in it.
package linediff

import (
	"slices"
	"strings"
)

// Kind says which text a line is from.
type Kind int

const (
	// Removed is a line of the old text that the new one does not have.
	Removed Kind = iota
	// Added is a line of the new text that the old one did not have.
	Added
	// same is a line both have; Diff leaves them out.
	same
)

// Line is one line of a difference.
type Line struct {
	Kind Kind
	Text string
}

// maxEdits bounds the alignment. Past it the two texts are given whole,
// the old one removed and the new one added: a difference that large reads
// as everything changed anyway, and the alignment's memory grows with the
// square of it.
const maxEdits = 1000

// Diff returns the lines removed from a and added in b, in the order they
// come, each run of removals ahead of the additions beside it. A final
// newline is a line of its own, so a text that only gained or lost one
// differs by an empty line.
func Diff(a, b string) []Line {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	// Most changes are in one place, so the shared start and end go before
	// anything is aligned.
	start := 0
	for start < len(x) && start < len(y) && x[start] == y[start] {
		start++
	}
	end := 0
	for end < len(x)-start && end < len(y)-start && x[len(x)-1-end] == y[len(y)-1-end] {
		end++
	}
	x, y = x[start:len(x)-end], y[start:len(y)-end]
	edits, ok := align(x, y)
	if !ok {
		edits = make([]Line, 0, len(x)+len(y))
		for _, l := range x {
			edits = append(edits, Line{Removed, l})
		}
		for _, l := range y {
			edits = append(edits, Line{Added, l})
		}
	}
	return grouped(edits)
}

// align walks the fewest removals and additions that turn x into y
// (Myers, "An O(ND) Difference Algorithm and Its Variations", 1986),
// keeping each round's furthest points to walk back along. The result
// holds the shared lines too. ok is false past maxEdits.
func align(x, y []string) (edits []Line, ok bool) {
	n, m := len(x), len(y)
	limit := min(n+m, maxEdits)
	off := limit + 1
	// v[off+k] is how far along x the furthest path on diagonal k got.
	v := make([]int, 2*limit+3)
	var trace [][]int
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var px int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				px = v[off+k+1]
			} else {
				px = v[off+k-1] + 1
			}
			py := px - k
			for px < n && py < m && x[px] == y[py] {
				px++
				py++
			}
			v[off+k] = px
			if px >= n && py >= m {
				return walkBack(x, y, trace, d), true
			}
		}
		row := make([]int, d+1)
		for k := -d; k <= d; k += 2 {
			row[(k+d)/2] = v[off+k]
		}
		trace = append(trace, row)
	}
	return nil, false
}

// walkBack follows the rounds from the end of both texts to their start.
func walkBack(x, y []string, trace [][]int, d int) []Line {
	px, py := len(x), len(y)
	var out []Line
	for ; d > 0; d-- {
		prev := trace[d-1]
		at := func(k int) int { return prev[(k+d-1)/2] }
		k := px - py
		down := k == -d || (k != d && at(k-1) < at(k+1))
		pk := k - 1
		if down {
			pk = k + 1
		}
		sx := at(pk)
		sy := sx - pk
		// The round's one edit, then the shared lines it ran along.
		ex, ey := sx+1, sy
		if down {
			ex, ey = sx, sy+1
		}
		for px > ex && py > ey {
			px--
			py--
			out = append(out, Line{same, x[px]})
		}
		if down {
			out = append(out, Line{Added, y[sy]})
		} else {
			out = append(out, Line{Removed, x[sx]})
		}
		px, py = sx, sy
	}
	for px > 0 && py > 0 {
		px--
		py--
		out = append(out, Line{same, x[px]})
	}
	slices.Reverse(out)
	return out
}

// grouped drops the shared lines and puts each run of edits between them
// in order: what goes, then what comes in its place.
func grouped(edits []Line) []Line {
	out := make([]Line, 0, len(edits))
	var added []Line
	flush := func() {
		out = append(out, added...)
		added = added[:0]
	}
	for _, e := range edits {
		switch e.Kind {
		case same:
			flush()
		case Removed:
			out = append(out, e)
		case Added:
			added = append(added, e)
		}
	}
	flush()
	return out
}
