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

const (
	KindHunk   Kind = iota // a hunk header. It shows no source line.
	KindEqual              // a line that is the same on the two sides
	KindInsert             // a line that only the new side has
	KindDelete             // a line that only the old side has
	KindChange             // in the split layout, a deleted line beside an inserted line
)

// Side identifies one version of a file.
type Side int

const (
	SideOld Side = iota // the version before the change
	SideNew             // the version after the change
)

// Row tells which source lines one rendered line shows. Old and New are
// line numbers that start at 1. A value of 0 means that the row has no line
// on that side. Continued is true if the line continues the source lines of
// the line above, because [Options.Wrap] wrapped them.
//
// A long run of equal lines folds into the hunk header below it. For a
// hunk header, Fold is that run.
type Row struct {
	Kind      Kind
	Old, New  int
	Continued bool
	Fold      Fold
}

// Fold is a run of equal lines that hides. Old and New are the line
// numbers of its first line. Lines is the number of lines, 0 if no line
// hides.
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
	// Show tells if the lines of a fold show. If Show is nil, no fold
	// shows. RenderFile does not use it.
	Show func(Fold) bool
	// Signs marks a changed line with + or -. Without Signs, a bar in the
	// colour of the line marks it.
	Signs bool
	// Theme sets the colours.
	Theme Theme
}

// tabWidth is the number of spaces that replace a tab.
const tabWidth = 4

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
}

// Render compares the old and the new content of the file at path. The path
// selects the syntax highlighter.
func Render(path, old, new string, o Options) *Diff {
	dv := diffview.New().
		Before(path, old).
		After(path, new).
		Width(o.Width).
		TabWidth(tabWidth).
		XOffset(o.Offset)
	if o.Show != nil {
		dv = dv.Show(func(f diffview.Fold) bool { return o.Show(Fold{f.Before, f.After, f.Lines}) })
	}
	if o.Split {
		dv = dv.Split()
	}
	if o.Signs {
		dv = dv.Signs()
	}
	if o.Wrap {
		dv = dv.Wrap()
	}
	dv = o.Theme.apply(dv)
	out, rows, g := dv.Render()

	d := &Diff{split: o.Split}
	for _, r := range rows {
		d.Rows = append(d.Rows, Row{
			Kind:      kindOf(r.Kind),
			Old:       r.Before,
			New:       r.After,
			Continued: r.Continued,
			Fold:      Fold{r.Fold.Before, r.Fold.After, r.Fold.Lines},
		})
	}
	if len(d.Rows) > 0 {
		d.Lines = strings.Split(out, "\n")
	}
	d.oldNum, d.newNum, d.newNumAt = g.BeforeNum, g.AfterNum, g.AfterNumStart
	return d
}

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

// Count returns the number of lines that a change from old to new adds and
// deletes. It ignores the difference between CRLF and LF line endings.
func Count(old, new string) (added, deleted int) {
	old = strings.ReplaceAll(old, "\r\n", "\n")
	new = strings.ReplaceAll(new, "\r\n", "\n")
	u, err := udiff.ToUnifiedDiff("", "", old, udiff.Lines(old, new), 0)
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
// s is Lines(s)[n-1]. A comment counts lines in this way.
func Lines(s string) []string {
	var lines []string
	for l := range strings.Lines(s) {
		l = strings.TrimSuffix(l, "\n")
		lines = append(lines, strings.TrimSuffix(l, "\r"))
	}
	return lines
}
