package diffview

import (
	"fmt"

	"github.com/aymanbagabas/go-udiff"
)

// foldMargin is the number of equal lines that show beside a change. A
// longer run of equal lines folds into one row, as in herdr-reviewr.
const foldMargin = 3

// Fold is a run of equal lines that hides. Before and After are the line
// numbers of its first line. Lines is the number of lines, 0 if no line
// hides.
type Fold struct {
	Before, After, Lines int
}

// Show shows the lines of each fold for which show returns true.
func (v *View) Show(show func(Fold) bool) *View {
	v.show = show
	return v
}

// foldHunk splits h, the hunk of the whole file, into the hunks that show.
// folds[i] is the run of lines that hides above hunks[i]. If the last run
// of equal lines hides, the last hunk has no lines. A run hides if more
// than one of its lines is farther than foldMargin from a change, and show
// is nil or returns false for it.
func foldHunk(h *udiff.Hunk, show func(Fold) bool) (hunks []*udiff.Hunk, folds []Fold) {
	n := len(h.Lines)
	keep := make([]bool, n)
	for i, l := range h.Lines {
		if l.Kind != udiff.Equal {
			for j := max(i-foldMargin, 0); j <= min(i+foldMargin, n-1); j++ {
				keep[j] = true
			}
		}
	}
	var cur *udiff.Hunk
	var above Fold
	b, a := h.FromLine, h.ToLine
	for i := 0; i < n; {
		if !keep[i] {
			j := i
			for j < n && !keep[j] {
				j++
			}
			f := Fold{Before: b, After: a, Lines: j - i}
			if f.Lines > 1 && (show == nil || !show(f)) {
				above, cur = f, nil
				b, a, i = b+f.Lines, a+f.Lines, j
				continue
			}
			for k := i; k < j; k++ {
				keep[k] = true // the run shows as a whole
			}
		}
		if cur == nil {
			cur = &udiff.Hunk{FromLine: b, ToLine: a}
			hunks = append(hunks, cur)
			folds = append(folds, above)
			above = Fold{}
		}
		l := h.Lines[i]
		cur.Lines = append(cur.Lines, l)
		if l.Kind != udiff.Insert {
			b++
		}
		if l.Kind != udiff.Delete {
			a++
		}
		i++
	}
	if above.Lines > 0 {
		hunks = append(hunks, &udiff.Hunk{FromLine: b, ToLine: a})
		folds = append(folds, above)
	}
	return hunks, folds
}

// header returns the header line of hunk i: the fold above it, if lines
// hide, and the hunk header, if the hunk has lines. For example
// "  ⋯ 12 unmodified lines  @@ -15,7 +15,8 @@ ".
func (v *View) header(i int) string {
	h, s := v.unified.Hunks[i], ""
	if f := v.folds[i]; f.Lines > 0 {
		s = fmt.Sprintf("  ⋯ %d unmodified lines", f.Lines)
	}
	if len(h.Lines) == 0 {
		return s + " "
	}
	return s + hunkHeader(h)
}
