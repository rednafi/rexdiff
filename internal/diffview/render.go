// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

const (
	leadingSymbolsSize = 2   // the width of a symbol, such as symInsert
	lineNumPadding     = 1   // the space on each side of a line number
	ellipsis           = "…" // shows where a line is cut
)

// These symbols are at the start of the code of a line. A bar in the colour
// of the line marks each changed line. [View.Signs] shows + and - in place
// of the bar.
const (
	symNone   = "  " // an equal line, a missing line, or a continued line
	symBar    = "▌ "
	symInsert = "+ "
	symDelete = "- "
)

// RowKind is the type of a rendered row.
type RowKind int

// The values of RowKind.
const (
	RowHunk   RowKind = iota // a hunk header
	RowEqual                 // a line that is the same in the two versions
	RowInsert                // an inserted line
	RowDelete                // a deleted line
	RowChange                // a deleted line beside an inserted line (split)
)

// Row tells which source lines one rendered line shows. Before and After are
// line numbers from 1, or 0 if the row has no line on that side. Continued
// is true for the second and the next screen lines of a wrapped line.
// For a hunk header, Fold is the run of lines that hides above the header.
type Row struct {
	Kind          RowKind
	Before, After int
	Continued     bool
	Fold          Fold
}

// renderUnified renders the unified layout. Each line has the old and the
// new line numbers, then the code.
func (v *View) renderUnified() string {
	var b strings.Builder
	nums := func(ls lineStyle, before, after any) string {
		return lineNum(ls, before, v.beforeNumDigits) +
			lineNum(ls, after, v.afterNumDigits)
	}
	for i, h := range v.hunks {
		v.writeHeader(&b, i, v.unifiedHeader(i))
		oldN, newN := h.FromLine, h.ToLine
		for _, l := range h.Lines {
			ls, sym := v.kindStyle(l.Kind)
			var r Row
			var num string
			switch l.Kind {
			case udiff.Equal:
				r = Row{Kind: RowEqual, Before: oldN, After: newN}
				num = nums(ls, oldN, newN)
				oldN++
				newN++
			case udiff.Insert:
				r = Row{Kind: RowInsert, After: newN}
				num = nums(ls, " ", newN)
				newN++
			case udiff.Delete:
				r = Row{Kind: RowDelete, Before: oldN}
				num = nums(ls, oldN, " ")
				oldN++
			}
			blank := nums(ls, " ", " ")
			v.writeRow(&b, r, v.codeLines(ls, num, blank, sym, l.Content, v.codeWidth))
		}
	}
	return b.String()
}

// unifiedHeader renders the divider line of hunk i in the unified layout.
func (v *View) unifiedHeader(i int) string {
	div := v.style.dividerLine
	header := ansi.Truncate(v.headerText(i), v.fullCodeWidth, ellipsis)
	return div.lineNumber.Render(pad(ellipsis, v.beforeNumDigits)) +
		div.lineNumber.Render(pad(ellipsis, v.afterNumDigits)) +
		div.code.Width(v.fullCodeWidth).Render(header)
}

// renderSplit renders the split layout. The old lines are on the left and
// the new lines are on the right.
func (v *View) renderSplit() string {
	var b strings.Builder
	afterWidth := v.codeWidth + btoi(v.extraColOnAfter)
	for i, h := range v.hunks {
		v.writeHeader(&b, i, v.splitHeader(i))
		oldN, newN := h.FromLine, h.ToLine
		for _, l := range hunkToSplit(h) {
			r := Row{Kind: splitKind(l)}
			left, leftFill := v.splitSide(l.before, oldN, v.beforeNumDigits, v.codeWidth)
			right, rightFill := v.splitSide(l.after, newN, v.afterNumDigits, afterWidth)
			if l.before != nil {
				r.Before = oldN
				oldN++
			}
			if l.after != nil {
				r.After = newN
				newN++
			}
			n := max(len(left), len(right))
			left, right = fillTo(left, n, leftFill), fillTo(right, n, rightFill)
			lines := make([]string, n)
			for k := range n {
				lines[k] = left[k] + right[k]
			}
			v.writeRow(&b, r, lines)
		}
	}
	return b.String()
}

// splitHeader renders the divider line of hunk i in the split layout. The
// header starts on the left side and can go on across the right side.
func (v *View) splitHeader(i int) string {
	div := v.style.dividerLine
	// The header covers the two code columns and the new line numbers.
	width := 2*v.fullCodeWidth + btoi(v.extraColOnAfter) + v.afterNumDigits + lineNumPadding*2
	return div.lineNumber.Render(pad(ellipsis, v.beforeNumDigits)) +
		div.code.Width(width).Render(ansi.Truncate(v.headerText(i), width, ellipsis))
}

// splitSide renders line l on one side of the split layout, one string for
// each screen line. l is nil if that side has no line. n is the line number
// of l. The fill function renders an empty screen line. Use fill when the
// other side has more screen lines.
func (v *View) splitSide(l *udiff.Line, n, digits, width int) (lines []string, fill func() string) {
	ls, sym, content := v.style.missingLine, symNone, ""
	if l != nil {
		ls, sym = v.kindStyle(l.Kind)
		content = l.Content
	}
	noNum := lineNum(ls, " ", digits)
	num := noNum
	if l != nil {
		num = lineNum(ls, n, digits)
	}
	fill = func() string { return v.codeLines(ls, noNum, "", symNone, "", width)[0] }
	return v.codeLines(ls, num, noNum, sym, content, width), fill
}

// splitKind returns the kind of the row of l in the split layout.
func splitKind(l splitLine) RowKind {
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

// fillTo adds the line that fill renders to lines, until lines has n lines.
func fillTo(lines []string, n int, fill func() string) []string {
	if len(lines) >= n {
		return lines
	}
	return append(lines, slices.Repeat([]string{fill()}, n-len(lines))...)
}

// kindStyle returns the style and the symbol of a line of kind k.
func (v *View) kindStyle(k udiff.OpKind) (lineStyle, string) {
	switch k {
	case udiff.Insert:
		return v.style.insertLine, v.mark(symInsert)
	case udiff.Delete:
		return v.style.deleteLine, v.mark(symDelete)
	}
	return v.style.equalLine, symNone
}

// mark returns sign if the View shows signs, else the bar.
func (v *View) mark(sign string) string {
	if v.signs {
		return sign
	}
	return symBar
}

// writeHeader writes header, the divider line of hunk i, to b, with its
// row.
func (v *View) writeHeader(b *strings.Builder, i int, header string) {
	v.writeRow(b, Row{Kind: RowHunk, Fold: v.folds[i]}, []string{header})
}

// writeRow writes the screen lines of one row to b. It adds r to the rows
// once for each screen line.
func (v *View) writeRow(b *strings.Builder, r Row, lines []string) {
	for k, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
		r.Continued = k > 0
		v.rows = append(v.rows, r)
	}
}

// lineNum renders the line number n in a column of digits cells.
func lineNum(ls lineStyle, n any, digits int) string {
	return ls.lineNumber.Render(pad(n, digits))
}

// codeLines renders one source line with nums, sym, and the code in width
// cells. It cuts a long line. With wrap, the line continues on more screen
// lines, which show blank in place of nums and no sym.
func (v *View) codeLines(ls lineStyle, nums, blank, sym, content string, width int) []string {
	code := v.highlightCode(strings.TrimSuffix(content, "\n"), ls.code.GetBackground())
	parts := []string{code}
	if v.wrap {
		// Wrap can keep a leading space on a full line, so cut again.
		w := max(width, 1)
		parts = strings.Split(ansi.Hardwrap(lipgloss.Wrap(code, w, ""), w, true), "\n")
		for i, p := range parts {
			// A wide character can be wider than w.
			parts[i] = ansi.Truncate(p, w, "")
		}
	} else {
		code = CutLeft(code, v.xOffset)
		parts[0] = ansi.Truncate(code, width, ellipsis)
		if v.xOffset > 0 && strings.TrimSpace(parts[0]) != "" {
			sym = strings.TrimSuffix(sym, " ") + ellipsis
		}
	}
	full := lipgloss.NewStyle().MaxWidth(width + leadingSymbolsSize)
	lines := make([]string, len(parts))
	for i, p := range parts {
		if i > 0 {
			nums, sym = blank, symNone
		}
		lines[i] = nums + full.Render(ls.symbol.Render(sym)+ls.code.Width(width).Render(p))
	}
	return lines
}

// headerText returns the text of the header of hunk i, for example
// "  ⋯ 12 unmodified lines  @@ -15,7 +15,8 @@ ". The text has the fold
// above the hunk if lines hide. It has the hunk header if the hunk has
// lines. The text ends with a space, as the hunk header does.
func (v *View) headerText(i int) string {
	h, s := v.hunks[i], ""
	if f := v.folds[i]; f.Lines > 0 {
		s = fmt.Sprintf("  ⋯ %d unmodified lines", f.Lines)
	}
	if len(h.Lines) == 0 {
		return s + " "
	}
	return s + hunkHeader(h)
}

// hunkHeader returns the header line of hunk h, for example
// "  @@ -1,3 +1,4 @@ ".
func hunkHeader(h *udiff.Hunk) string {
	before, after := hunkLineCounts(h)
	return fmt.Sprintf("  @@ -%d,%d +%d,%d @@ ", h.FromLine, before, h.ToLine, after)
}

// hunkLineCounts returns the number of old and new lines in hunk h.
func hunkLineCounts(h *udiff.Hunk) (before, after int) {
	for _, l := range h.Lines {
		switch l.Kind {
		case udiff.Equal:
			before++
			after++
		case udiff.Insert:
			after++
		case udiff.Delete:
			before++
		}
	}
	return before, after
}
