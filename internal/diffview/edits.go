package diffview

import (
	"strings"

	zdiff "znkr.io/diff"
	"znkr.io/diff/textdiff"
)

type hunk struct {
	OldStart, NewStart int
	Lines              []diffLine
}

type diffLine struct {
	Kind    Kind
	Content string // keeps its line ending
}

func (l diffLine) next(oldNum, newNum int) (int, int) {
	if l.Kind != KindAdded {
		oldNum++
	}
	if l.Kind != KindDeleted {
		newNum++
	}
	return oldNum, newNum
}

// The diff runs during a draw, and the default diff is slow on large
// rewrites. The fast diff can mark a few extra lines as changed.
const fastDiffLines = 10_000

// edits diffs from newText to oldText, so a Delete is a line of newText.
// The reverse order makes textdiff pick the same diff as git on ties.
func edits(oldText, newText string) []textdiff.Edit[string] {
	opts := []textdiff.Option{textdiff.IndentHeuristic()}
	if strings.Count(oldText, "\n")+strings.Count(newText, "\n") > fastDiffLines {
		opts = append(opts, zdiff.Fast())
	}
	return textdiff.Edits(newText, oldText, opts...)
}

// lineDiff returns nil if the texts are equal. A change to a line ending
// counts as a change.
func lineDiff(oldText, newText string) *hunk {
	h := &hunk{OldStart: 1, NewStart: 1}
	var added []diffLine
	changed := false
	for _, e := range edits(oldText, newText) {
		switch e.Op {
		case zdiff.Delete:
			added = append(added, diffLine{Kind: KindAdded, Content: e.Line})
			changed = true
		case zdiff.Insert:
			h.Lines = append(h.Lines, diffLine{Kind: KindDeleted, Content: e.Line})
			changed = true
		case zdiff.Match:
			h.Lines = append(append(h.Lines, added...), diffLine{Kind: KindEqual, Content: e.Line})
			added = added[:0]
		}
	}
	h.Lines = append(h.Lines, added...)
	if !changed {
		return nil
	}
	return h
}

// CountLines returns the added and deleted line counts.
func CountLines(oldText, newText string) (added, deleted int) {
	for _, e := range edits(oldText, newText) {
		switch e.Op {
		case zdiff.Delete:
			added++
		case zdiff.Insert:
			deleted++
		}
	}
	return added, deleted
}
