// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"slices"
)

// splitLine is one row of the split layout. A nil field means that side
// has no line.
type splitLine struct {
	old *diffLine
	new *diffLine
}

// hunkToSplit pairs the lines of h for the split layout. An equal line
// appears on both sides. A deleted line is paired with the next unpaired
// added line, unless an equal line comes first. Any other added line
// appears alone on the new side, in its original position.
func hunkToSplit(h *hunk) []splitLine {
	lines := slices.Clone(h.Lines)
	out := make([]splitLine, 0, len(lines))
	taken := make([]bool, len(lines)) // added lines already paired with a deleted line
	// next is the index where the search for an added line to pair starts.
	// Every line before it is either already paired or not an added line.
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
