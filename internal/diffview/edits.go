package diffview

import (
	"strings"

	zdiff "znkr.io/diff"
	"znkr.io/diff/textdiff"
)

// hunk is a run of lines of a diff. OldStart and NewStart are the old
// and new line numbers of its first line.
type hunk struct {
	OldStart, NewStart int
	Lines              []diffLine
}

// diffLine is one line of a hunk. Content keeps its line ending.
type diffLine struct {
	Kind    Kind // KindEqual, KindAdded, or KindDeleted
	Content string
}

// next returns the old and new line numbers of the line after l, where
// oldNum and newNum are those of l.
func (l diffLine) next(oldNum, newNum int) (int, int) {
	if l.Kind != KindAdded {
		oldNum++
	}
	if l.Kind != KindDeleted {
		newNum++
	}
	return oldNum, newNum
}

// fastDiffLines is the number of lines, old and new together, above which
// edits uses the fast diff. The diff runs while the screen is drawn, and
// the default diff becomes slow on large rewrites. The fast diff can mark
// a few extra lines as changed.
const fastDiffLines = 10_000

// edits returns the line edits between oldText and newText, from newText
// to oldText: a Delete is a line of newText, and an Insert a line of
// oldText. Reversing makes textdiff pick the same diff as git when two
// diffs are equally short. Above fastDiffLines lines, edits uses the fast
// diff, so that no input makes it slow.
func edits(oldText, newText string) []textdiff.Edit[string] {
	opts := []textdiff.Option{textdiff.IndentHeuristic()}
	if strings.Count(oldText, "\n")+strings.Count(newText, "\n") > fastDiffLines {
		opts = append(opts, zdiff.Fast())
	}
	return textdiff.Edits(newText, oldText, opts...)
}

// lineDiff returns a hunk that holds every line of oldText and newText, or
// nil if the texts are equal. In each run of changes, the deleted lines
// come before the added lines. A line whose ending changes counts as
// changed.
func lineDiff(oldText, newText string) *hunk {
	h := &hunk{OldStart: 1, NewStart: 1}
	var added []diffLine // the added lines of the current run
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

// CountLines returns the number of lines added and deleted by the change
// from oldText to newText.
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
