// Package diff renders the changes to a file as lines for a terminal, and
// stores review comments on those lines.
//
// [Render] and [RenderFile] return a [Diff]. Each of its lines has a [Row]
// that records which source lines it shows. The colours come from the
// terminal's palette.
package diff

import (
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Kind is the kind of a rendered row.
type Kind int

// The kinds of row.
const (
	KindHunk    Kind = iota // a hunk header, with no source line
	KindEqual               // a line that is the same on both sides
	KindAdded               // a line that exists only on the new side
	KindDeleted             // a line that exists only on the old side
	KindChanged             // a deleted line next to an added line, in split view
)

// Side identifies one version of a file.
type Side int

// The two sides of a file.
const (
	SideOld Side = iota // the version before the change
	SideNew             // the version after the change
)

// Row describes the source lines that one rendered line shows.
type Row struct {
	Kind Kind
	// Old and New are 1-based line numbers, or 0 if the row has no line on
	// that side.
	Old, New int
	// Continued is true if the row continues the line above it because of
	// [Options.Wrap].
	Continued bool
	// Fold is the run of hidden equal lines above a hunk header.
	Fold Fold
}

// Line returns the row's line number on side.
func (r Row) Line(side Side) int {
	if side == SideOld {
		return r.Old
	}
	return r.New
}

// Fold is a run of equal lines that the diff hides. Old and New are the
// line numbers of its first line, and Lines is the number of lines. Lines
// is 0 if no lines are hidden.
type Fold struct {
	Old, New, Lines int
}

// Options controls how [Render] draws a diff.
type Options struct {
	// Split shows the old lines on the left and the new lines on the right.
	// If it is false, the old and new lines share one column.
	Split bool
	// Width is the maximum width of a line, in cells. Zero means no limit.
	Width int
	// ScrollX is the number of cells to scroll the code to the right.
	ScrollX int
	// Wrap continues a line that is wider than Width on the following
	// lines, instead of cutting it off. ScrollX is ignored when Wrap is set.
	Wrap bool
	// Expanded reports whether the hidden lines of a fold are shown. If
	// Expanded is nil, all folds stay hidden.
	Expanded func(Fold) bool
	// Signs marks each changed line with + or -. Without Signs, a coloured
	// bar marks it.
	Signs bool
	// Theme sets the colours.
	Theme Theme
	// Cache stores results for reuse by later renders. If Cache is nil,
	// nothing is reused.
	Cache *Cache
}

// Cache stores highlighted lines, the computed diff and the rows between
// renders. The zero value is an empty Cache. A Cache is not safe for
// concurrent use.
type Cache struct {
	lines diffview.Cache // highlighted lines and the last diff, for Render
	rows  struct {       // the last rows that Render converted
		from []diffview.Row
		to   []Row
	}
	file struct { // the last file passed to RenderFile
		path, src string
		raw       string // the original content, before shownFile converted it to src
		h         *diffview.Highlighter
	}
}

// Diff is the rendered output. [Diff.Line] returns the screen line for
// Rows[i], and renders it the first time it is requested. There are no
// lines if the two versions are the same. Do not modify Rows, because
// later renders with the same Cache may share them. A Diff uses its Cache
// and is not safe for concurrent use.
type Diff struct {
	Rows  []Row
	lines *diffview.Lines

	split    bool
	oldNum   int // width of the old line-number column
	newNum   int // width of the new line-number column
	newNumAt int // column where the new line numbers start

	// The fields below are used by [Diff.Cell].
	texts  [2]string // the content of each side
	codeAt [2]int    // column where the code of each side starts
	// codeWidth is the width of the code of each side, or 0 if the lines
	// have no width limit.
	codeWidth [2]int
	scrollX   int // the horizontal scroll, or 0 when wrapping
	wrap      bool
}

// Render returns the diff between oldText and newText, the old and new
// content of the file at path. The path selects the syntax highlighting.
func Render(path, oldText, newText string, o Options) *Diff {
	dv := diffview.New(path, oldText, newText).
		Width(o.Width).
		ScrollX(o.ScrollX).
		Split(o.Split).
		Signs(o.Signs).
		Wrap(o.Wrap).
		Theme(o.Theme.Background, o.Theme.Foreground, o.Theme.Green,
			o.Theme.Red, o.Theme.dark())
	if o.Cache != nil {
		dv = dv.Cache(&o.Cache.lines)
	}
	if o.Expanded != nil {
		dv = dv.Expanded(func(f diffview.Fold) bool { return o.Expanded(Fold(f)) })
	}
	ls, rows, g := dv.RenderLines()
	d := &Diff{lines: ls, split: o.Split, texts: [2]string{oldText, newText}, wrap: o.Wrap}
	if !o.Wrap {
		d.scrollX = o.ScrollX
	}
	d.Rows = o.Cache.convert(rows)
	d.oldNum, d.newNum, d.newNumAt = g.OldNum, g.NewNum, g.NewNumStart
	d.codeAt = [2]int{g.OldCode, g.NewCode}
	if o.Width > 0 {
		d.codeWidth = [2]int{o.Width - g.OldCode, o.Width - g.NewCode}
		if o.Split {
			d.codeWidth[SideOld] = g.NewNumStart - g.OldCode
		}
	}
	return d
}

// Line returns screen line i, which belongs to Rows[i].
func (d *Diff) Line(i int) string { return d.lines.Line(i) }

// SideAt reports which side column x of a screen line belongs to, where
// column 0 is the first column. onGutter is true if x is in a line-number
// column.
func (d *Diff) SideAt(x int) (side Side, onGutter bool) {
	if d.split {
		switch {
		case x < d.oldNum:
			return SideOld, true
		case x >= d.newNumAt && x < d.newNumAt+d.newNum:
			return SideNew, true
		case x >= d.newNumAt:
			return SideNew, false
		}
		return SideOld, false
	}
	if x < d.oldNum {
		return SideOld, true
	}
	return SideNew, x < d.oldNum+d.newNum
}

// CountLines returns the number of lines added and deleted by the change
// from oldText to newText. A line whose ending changes between CRLF and LF
// counts as changed.
func CountLines(oldText, newText string) (added, deleted int) {
	return diffview.CountLines(oldText, newText)
}

// SplitLines splits s into lines and removes the LF or CRLF line endings.
// Line n of s is SplitLines(s)[n-1]. [Comment] line numbers use the same
// counting. Unlike [strings.Split], it returns no empty line after a final
// line ending.
func SplitLines(s string) []string {
	lines := slices.Collect(strings.Lines(s))
	for i, l := range lines {
		lines[i] = trimEOL(l)
	}
	return lines
}

// trimEOL returns line without its LF or CRLF line ending.
func trimEOL(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

// convert returns rows as the rows of a [Diff], or nil if there are none.
// c keeps the result, because the next render often has the same rows. A
// nil c keeps nothing.
func (c *Cache) convert(rows []diffview.Row) []Row {
	if len(rows) == 0 {
		return nil
	}
	if c != nil && len(c.rows.from) == len(rows) && &c.rows.from[0] == &rows[0] {
		return c.rows.to
	}
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = Row{Kind: Kind(r.Kind), Old: r.Old, New: r.New, Continued: r.Continued, Fold: Fold(r.Fold)}
	}
	if c != nil {
		c.rows.from, c.rows.to = rows, out
	}
	return out
}

// countLines returns the number of lines in s, counted as [SplitLines] counts
// them. A final line without a newline counts as a line.
func countLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
