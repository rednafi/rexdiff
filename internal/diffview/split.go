// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"slices"
)

// A nil field means no line on that side.
type splitLine struct {
	old *diffLine
	new *diffLine
}

// hunkToSplit pairs a deleted line with the next unpaired added line,
// unless an equal line comes first.
func hunkToSplit(h *hunk) []splitLine {
	lines := slices.Clone(h.Lines)
	out := make([]splitLine, 0, len(lines))
	taken := make([]bool, len(lines))
	next := 0
	for i := range lines {
		ul := &lines[i]
		var l splitLine
		switch ul.Kind {
		case KindEqual:
			l.old, l.new = ul, ul
		case KindAdded:
			if taken[i] {
				continue
			}
			l.new = ul
		case KindDeleted:
			l.old = ul
			next = max(next, i+1)
			for next < len(lines) && lines[next].Kind != KindEqual &&
				(lines[next].Kind != KindAdded || taken[next]) {
				next++
			}
			if next < len(lines) && lines[next].Kind == KindAdded {
				l.new, taken[next] = &lines[next], true
				next++
			}
		}
		out = append(out, l)
	}
	return out
}
