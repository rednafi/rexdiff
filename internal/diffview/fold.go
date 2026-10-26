package diffview

import "github.com/aymanbagabas/go-udiff"

// foldMargin is the number of equal lines that show beside a change. A
// longer run of equal lines folds into one row, as in herdr-reviewr.
const foldMargin = 3

// Fold is a run of equal lines that the view hides. Before and After are
// the line numbers of its first line. Lines is the number of lines. Lines
// is 0 if the view hides no line.
type Fold struct {
	Before, After, Lines int
}

// foldHunk splits h into the hunks that show. folds[i] is the run of lines
// that hides above hunks[i]. If the last run hides, the last hunk has no
// lines. A run of equal lines hides if it has more than one line and no
// line of it is within foldMargin of a change. A run does not hide if
// expanded returns true for it.
func foldHunk(h *udiff.Hunk, expanded func(Fold) bool) (hunks []*udiff.Hunk, folds []Fold) {
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
	i := 0
	for i < n {
		if !keep[i] {
			j := i
			for j < n && !keep[j] {
				j++
			}
			f := Fold{Before: b, After: a, Lines: j - i}
			if f.Lines > 1 && (expanded == nil || !expanded(f)) {
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
