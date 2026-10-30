package diffview

const foldMargin = 3

// Fold is a run of hidden equal lines. Lines is 0 if no lines are hidden.
type Fold struct {
	Old, New, Lines int
}

func keptLines(h *hunk) []bool {
	n := len(h.Lines)
	keep := make([]bool, n)
	for i, l := range h.Lines {
		if l.Kind != KindEqual {
			for j := max(i-foldMargin, 0); j <= min(i+foldMargin, n-1); j++ {
				keep[j] = true
			}
		}
	}
	return keep
}

// hiddenFolds never hides a run of one line.
func hiddenFolds(h *hunk, keep []bool, expanded func(Fold) bool) []Fold {
	var hidden []Fold
	oldNum, newNum := h.OldStart, h.NewStart
	for i := 0; i < len(h.Lines); {
		if keep[i] {
			oldNum, newNum = h.Lines[i].next(oldNum, newNum)
			i++
			continue
		}
		j := i
		for j < len(h.Lines) && !keep[j] {
			j++
		}
		fold := Fold{Old: oldNum, New: newNum, Lines: j - i}
		if fold.Lines > 1 && (expanded == nil || !expanded(fold)) {
			hidden = append(hidden, fold)
		}
		oldNum, newNum, i = oldNum+fold.Lines, newNum+fold.Lines, j
	}
	return hidden
}

// foldHunk returns folds[i] as the fold above hunks[i]. If the last run is
// hidden, the last hunk has no lines.
func foldHunk(h *hunk, keep []bool, hidden []Fold) (hunks []*hunk, folds []Fold) {
	n := len(h.Lines)
	var cur *hunk
	var above Fold
	oldNum, newNum := h.OldStart, h.NewStart
	i := 0
	shown := 0
	for i < n {
		if !keep[i] && i >= shown {
			j := i
			for j < n && !keep[j] {
				j++
			}
			fold := Fold{Old: oldNum, New: newNum, Lines: j - i}
			if len(hidden) > 0 && hidden[0] == fold {
				hidden = hidden[1:]
				above, cur = fold, nil
				oldNum, newNum, i = oldNum+fold.Lines, newNum+fold.Lines, j
				continue
			}
			shown = j
		}
		if cur == nil {
			cur = &hunk{OldStart: oldNum, NewStart: newNum}
			hunks = append(hunks, cur)
			folds = append(folds, above)
			above = Fold{}
		}
		l := h.Lines[i]
		cur.Lines = append(cur.Lines, l)
		oldNum, newNum = l.next(oldNum, newNum)
		i++
	}
	if above.Lines > 0 {
		hunks = append(hunks, &hunk{OldStart: oldNum, NewStart: newNum})
		folds = append(folds, above)
	}
	return hunks, folds
}
