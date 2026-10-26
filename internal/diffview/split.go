// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"slices"

	"github.com/aymanbagabas/go-udiff"
)

// splitLine is one row of the split layout. A nil side has no line.
type splitLine struct {
	before *udiff.Line
	after  *udiff.Line
}

// hunkToSplit pairs the lines of h for the split layout. An equal line is on
// both sides. A deleted line gets the next inserted line beside it, if no
// equal line comes first.
func hunkToSplit(h *udiff.Hunk) []splitLine {
	lines := slices.Clone(h.Lines)
	out := make([]splitLine, 0, len(lines))
	for len(lines) > 0 {
		ul := lines[0]
		lines = lines[1:]
		var sl splitLine
		switch ul.Kind {
		case udiff.Equal:
			sl.before, sl.after = &ul, &ul
		case udiff.Insert:
			sl.after = &ul
		case udiff.Delete:
			sl.before = &ul
			sl.after, lines = takeInsert(lines)
		}
		out = append(out, sl)
	}
	return out
}

// takeInsert removes the first inserted line before the next equal line from
// lines. It returns that line, or nil if there is none, and the other lines.
func takeInsert(lines []udiff.Line) (*udiff.Line, []udiff.Line) {
	for i, l := range lines {
		switch l.Kind {
		case udiff.Insert:
			return &l, slices.Delete(lines, i, i+1)
		case udiff.Equal:
			return nil, lines
		}
	}
	return nil, lines
}
