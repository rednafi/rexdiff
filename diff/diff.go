// Package diff renders the changes to a file as terminal lines, and keeps
// review comments on the lines.
//
// [Render] and [RenderFile] return a [Diff]. Each line in [Diff.Lines] has a
// [Row] that tells which source lines it shows. Use the rows to put a cursor
// or a [Comment] on a source line. The colours come from the terminal
// palette.
package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Kind is the type of a rendered row.
type Kind int

// These are the kinds of a row.
const (
	KindHunk   Kind = iota // a hunk header, with no source line
	KindEqual              // a line that is the same on the two sides
	KindInsert             // a line that only the new side has
	KindDelete             // a line that only the old side has
	KindChange             // a deleted line beside an inserted line (split)
)

// Side identifies one version of a file.
type Side int

// These are the two sides of a file.
const (
	SideOld Side = iota // the version before the change
	SideNew             // the version after the change
)

// Row tells which source lines one rendered line shows. Old and New are
// line numbers that start at 1. A value of 0 means that the row has no line
// on that side. Continued is true if the line continues the source lines of
// the line above, because [Options.Wrap] wraps them.
//
// A long run of equal lines folds into the hunk header below it. For a
// hunk header, Fold is that run.
type Row struct {
	Kind      Kind
	Old, New  int
	Continued bool
	Fold      Fold
}

// Fold is a run of equal lines that the diff hides. Old and New are the
// line numbers of its first line. Lines is the number of lines. Lines is 0
// if the diff hides no line.
type Fold struct {
	Old, New, Lines int
}

// Line returns the line number of the row on side s.
func (r Row) Line(s Side) int {
	if s == SideOld {
		return r.Old
	}
	return r.New
}

// Options control how [Render] and [RenderFile] draw their output.
type Options struct {
	// Split shows the old lines on the left and the new lines on the right.
	// If it is false, the old and the new lines are in one column.
	Split bool
	// Width is the maximum width of a line, in cells. Zero means no limit.
	Width int
	// Offset is the number of cells to scroll the code to the right.
	Offset int
	// Wrap continues a line that is wider than Width on the next lines,
	// instead of cutting it. Then Offset does not apply.
	Wrap bool
	// Expanded reports whether the lines of a fold show. If Expanded is
	// nil, no fold shows. RenderFile does not use it.
	Expanded func(Fold) bool
	// Signs marks a changed line with + or -. Without Signs, a bar in the
	// colour of the line marks it.
	Signs bool
	// Theme sets the colours.
	Theme Theme
	// Cache makes a new render of the same lines fast. If Cache is nil, the
	// render keeps nothing.
	Cache *Cache
}

// Cache keeps the syntax colours of the lines between renders, for example
// for a sideways scroll. The zero value is an empty Cache. A Cache is not
// safe for concurrent use.
type Cache struct {
	lines diffview.Cache                  // the lines of Render
	file  struct{ path, src, out string } // the last file of RenderFile
}

// Diff is the rendered output. Lines[i] is the screen line for Rows[i].
// Lines is empty when there is nothing to show, for example when the two
// versions are the same.
type Diff struct {
	Lines []string
	Rows  []Row

	split    bool
	oldNum   int // width of the old line-number column
	newNum   int // width of the new line-number column
	newNumAt int // column where the new line numbers start

	// The fields below are for [Diff.Cell].
	src    [2]string // the content of each side
	codeAt [2]int    // column where the code of each side starts
	offset int       // the sideways scroll, or 0 with wrap
	wrap   bool
}

// Render compares before, the old content of the file at path, with after,
// its new content. The path selects the syntax highlighter.
func Render(path, before, after string, o Options) *Diff {
	dv := diffview.New(path, before, after).
		Width(o.Width).
		XOffset(o.Offset).
		Split(o.Split).
		Signs(o.Signs).
		Wrap(o.Wrap).
		Theme(o.Theme.Background, o.Theme.Foreground, o.Theme.Green,
			o.Theme.Red, o.Theme.dark())
	if o.Cache != nil {
		dv = dv.Cache(&o.Cache.lines)
	}
	if o.Expanded != nil {
		dv = dv.Expand(func(f diffview.Fold) bool { return o.Expanded(foldOf(f)) })
	}
	out, rows, g := dv.Render()

	d := &Diff{split: o.Split, src: [2]string{before, after}, wrap: o.Wrap}
	if !o.Wrap {
		d.offset = o.Offset
	}
	for _, r := range rows {
		d.Rows = append(d.Rows, Row{
			Kind:      kindOf(r.Kind),
			Old:       r.Before,
			New:       r.After,
			Continued: r.Continued,
			Fold:      foldOf(r.Fold),
		})
	}
	if len(d.Rows) > 0 {
		d.Lines = strings.Split(out, "\n")
	}
	d.oldNum, d.newNum, d.newNumAt = g.BeforeNum, g.AfterNum, g.AfterNumStart
	d.codeAt = [2]int{g.BeforeCode, g.AfterCode}
	return d
}

// foldOf returns the Fold of the diffview fold f.
func foldOf(f diffview.Fold) Fold {
	return Fold{Old: f.Before, New: f.After, Lines: f.Lines}
}

// kindOf returns the Kind of the diffview row kind k.
func kindOf(k diffview.RowKind) Kind {
	switch k {
	case diffview.RowHunk:
		return KindHunk
	case diffview.RowInsert:
		return KindInsert
	case diffview.RowDelete:
		return KindDelete
	case diffview.RowChange:
		return KindChange
	}
	return KindEqual // diffview.RowEqual
}

// SideAt tells which side column x of a line belongs to. Column 0 is the
// first column of the line. If onNumber is true, x is on a line number.
func (d *Diff) SideAt(x int) (s Side, onNumber bool) {
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

// Count returns the number of lines that a change from before to after adds
// and deletes. It ignores the difference between CRLF and LF line endings.
func Count(before, after string) (added, deleted int) {
	before = strings.ReplaceAll(before, "\r\n", "\n")
	after = strings.ReplaceAll(after, "\r\n", "\n")
	u, err := udiff.ToUnifiedDiff("", "", before, udiff.Lines(before, after), 0)
	if err != nil {
		return 0, 0
	}
	for _, h := range u.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case udiff.Insert:
				added++
			case udiff.Delete:
				deleted++
			}
		}
	}
	return added, deleted
}

// Lines splits s into lines, without the LF or CRLF line endings. Line n of
// s is Lines(s)[n-1]. The lines of a [Comment] count in this way.
func Lines(s string) []string {
	var lines []string
	for l := range strings.Lines(s) {
		lines = append(lines, trimEOL(l))
	}
	return lines
}

// trimEOL returns line without its LF or CRLF line ending.
func trimEOL(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

// countLines returns the number of lines of s, as [Lines] counts them. A
// last line without a newline counts as a line.
func countLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
