package diffview

import "github.com/aymanbagabas/go-udiff"

// RowKind is the type of a rendered row.
type RowKind int

const (
	RowHunk   RowKind = iota // a hunk header
	RowEqual                 // a line that is the same in the two versions
	RowInsert                // an inserted line
	RowDelete                // a deleted line
	RowChange                // in the split layout, a deleted line beside an inserted line
)

// Row tells which source lines one rendered line shows. Before and After are
// line numbers from 1, or 0 if the row has no line on that side. Continued
// is true for the second and the next screen lines of a wrapped line.
// For a hunk header, Fold is the run of lines that hides above it.
type Row struct {
	Kind          RowKind
	Before, After int
	Continued     bool
	Fold          Fold
}

// rows returns one row for each line of the output of text. Call it after
// text.
func (v *View) rows() []Row {
	rows := v.lineRows()
	if !v.wrap {
		return rows
	}
	var out []Row
	h := v.heights
	for _, r := range rows {
		n := 1
		if r.Kind != RowHunk && len(h) > 0 {
			n, h = h[0], h[1:]
		}
		for k := range n {
			r.Continued = k > 0
			out = append(out, r)
		}
	}
	return out
}

// lineRows returns one row for each source line, and one for each hunk
// header.
func (v *View) lineRows() []Row {
	if v.layout == layoutSplit {
		return v.splitRows()
	}
	return v.unifiedRows()
}

// unifiedRows returns the rows of the unified layout.
func (v *View) unifiedRows() []Row {
	var rows []Row
	for i, h := range v.unified.Hunks {
		rows = append(rows, v.headerRow(i))
		b, a := h.FromLine, h.ToLine
		for _, l := range h.Lines {
			switch l.Kind {
			case udiff.Equal:
				rows = append(rows, Row{Kind: RowEqual, Before: b, After: a})
				b++
				a++
			case udiff.Insert:
				rows = append(rows, Row{Kind: RowInsert, After: a})
				a++
			case udiff.Delete:
				rows = append(rows, Row{Kind: RowDelete, Before: b})
				b++
			}
		}
	}
	return rows
}

// splitRows returns the rows of the split layout.
func (v *View) splitRows() []Row {
	var rows []Row
	for i, h := range v.splitHunks {
		rows = append(rows, v.headerRow(i))
		b, a := h.fromLine, h.toLine
		for _, l := range h.lines {
			r := Row{Kind: splitKind(l)}
			if l.before != nil {
				r.Before = b
				b++
			}
			if l.after != nil {
				r.After = a
				a++
			}
			rows = append(rows, r)
		}
	}
	return rows
}

// headerRow returns the row of the header of hunk i.
func (v *View) headerRow(i int) Row {
	return Row{Kind: RowHunk, Fold: v.folds[i]}
}

// splitKind returns the kind of the row of l in the split layout.
func splitKind(l *splitLine) RowKind {
	switch {
	case l.before != nil && l.after != nil && l.before.Kind == udiff.Equal:
		return RowEqual
	case l.before != nil && l.after != nil:
		return RowChange
	case l.before != nil:
		return RowDelete
	}
	return RowInsert
}

// Geometry gives the columns of the line numbers in the output of String.
// Use it to tell a line number from the code.
type Geometry struct {
	BeforeNum     int // the width of the old line-number column, in cells
	AfterNum      int // the width of the new line-number column, in cells
	AfterNumStart int // the column where the new line numbers start, from 0
}

// geometry returns the columns of the line numbers. Call it after text.
func (v *View) geometry() Geometry {
	g := Geometry{
		BeforeNum: v.beforeNumDigits + lineNumPadding*2,
		AfterNum:  v.afterNumDigits + lineNumPadding*2,
	}
	g.AfterNumStart = g.BeforeNum
	if v.layout == layoutSplit {
		g.AfterNumStart = g.BeforeNum + v.fullCodeWidth
	}
	return g
}
