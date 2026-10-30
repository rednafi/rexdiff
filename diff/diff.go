// Package diff renders the changes to a file as terminal lines. It also
// stores review comments on those lines.
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
	KindHunk Kind = iota // no source line
	KindEqual
	KindAdded
	KindDeleted
	KindChanged // a deleted and an added line side by side, in split view
)

// Side identifies one version of a file.
type Side int

// The two sides of a file.
const (
	SideOld Side = iota
	SideNew
)

// Row describes the source lines that one rendered line shows.
type Row struct {
	Kind Kind
	// Old and New are 1-based. 0 means no line on that side.
	Old, New int
	// Continued is true if the row wraps the line of the row above.
	Continued bool
	// Fold is set only on hunk headers.
	Fold Fold
}

// Line returns the row's line number on side.
func (r Row) Line(side Side) int {
	if side == SideOld {
		return r.Old
	}
	return r.New
}

// Fold is a run of hidden equal lines. Old and New number its first line.
type Fold struct {
	Old, New, Lines int
}

// Options controls how [Render] draws a diff.
type Options struct {
	Split bool
	// Width is in cells. Zero means no limit.
	Width int
	// ScrollX is in cells. Wrap ignores it.
	ScrollX int
	Wrap    bool
	// Expanded reports whether a fold is open. Nil keeps all folds hidden.
	Expanded func(Fold) bool
	// Signs uses + and - instead of a coloured bar.
	Signs bool
	Theme Theme
	// Cache is nil for no reuse.
	Cache *Cache
}

// Cache keeps render results between renders. The zero value is ready to
// use. A Cache is not safe for concurrent use.
type Cache struct {
	lines diffview.Cache
	rows  struct {
		from []diffview.Row
		to   []Row
	}
	file struct {
		path, src string
		raw       string
		h         *diffview.Highlighter
	}
}

// Diff is a rendered diff. Do not modify Rows, because later renders can
// share them. A Diff is not safe for concurrent use.
type Diff struct {
	Rows  []Row
	lines *diffview.Lines

	split    bool
	oldNum   int // width of the old line-number column
	newNum   int // width of the new line-number column
	newNumAt int

	texts  [2]string
	codeAt [2]int
	// codeWidth is 0 if the lines have no width limit.
	codeWidth [2]int
	scrollX   int // 0 when wrapping
	wrap      bool
}

// Render returns the diff from oldText to newText, highlighted for path.
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

// SideAt reports the side of 0-based column x and if x is on a line number.
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

// CountLines returns the added and deleted line counts. A change between
// CRLF and LF counts as a changed line.
func CountLines(oldText, newText string) (added, deleted int) {
	return diffview.CountLines(oldText, newText)
}

// SplitLines splits s into lines without their LF or CRLF endings. A final
// line ending gives no empty line.
func SplitLines(s string) []string {
	lines := slices.Collect(strings.Lines(s))
	for i, l := range lines {
		lines[i] = trimEOL(l)
	}
	return lines
}

func trimEOL(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

// convert caches its result in c, because the next render often has the
// same rows.
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

func countLines(s string) int {
	n := strings.Count(s, "\n")
	if s != "" && !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
