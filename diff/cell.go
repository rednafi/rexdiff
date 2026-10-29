package diff

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Cell reports which source character is shown in the cell at column x of
// screen line row, where column 0 is the first column. It returns the
// side, the 1-based line number, and the rune index within that line as
// split by [SplitLines]. For a cell after the end of the code, the index is the
// line's length in runes. ok is false if the cell is not on code, for
// example on a line number, a sign or a hunk header.
func (d *Diff) Cell(row, x int) (side Side, line, index int, ok bool) {
	if row < 0 || row >= len(d.Rows) || d.Rows[row].Kind == KindHunk {
		return 0, 0, 0, false
	}
	r := d.Rows[row]
	var onGutter bool
	side, onGutter = d.SideAt(x)
	if !d.split {
		side = SideNew
		if r.New == 0 {
			side = SideOld
		}
	}
	line = r.Line(side)
	if onGutter || line == 0 || x < d.codeAt[side] {
		return 0, 0, 0, false
	}
	text, found := lineAt(d.texts[side], line)
	if !found {
		return 0, 0, 0, false
	}
	col := x - d.codeAt[side] + d.scrollX
	if d.wrap {
		start, width := d.segmentStart(row, side, text)
		if col >= width { // a blank cell after the end of a wrapped part
			return side, line, utf8.RuneCountInString(text), true
		}
		col += start
	}
	return side, line, runeAt(text, col), true
}

// CodeWidth returns the number of columns that show code on side, or 0 if
// the lines have no width limit.
func (d *Diff) CodeWidth(side Side) int { return max(d.codeWidth[side], 0) }

// Locate finds where the byte at offset i of a source line is shown. row
// is the first screen line of the source line, and side the side that
// shows it. Locate returns the screen line that shows the byte, which is
// row unless the line wraps, and the column of the byte counted from the
// start of the code on that screen line, as if not scrolled.
func (d *Diff) Locate(row int, side Side, i int) (line, col int) {
	if row < 0 || row >= len(d.Rows) {
		return row, 0
	}
	text, found := lineAt(d.texts[side], d.Rows[row].Line(side))
	if !found {
		return row, 0
	}
	col = ansi.StringWidth(expand(text[:min(max(i, 0), len(text))]))
	if !d.wrap {
		return row, col
	}
	// Go through the screen lines of the wrapped line in order, as
	// segmentStart does, keeping the width of the code before each one.
	shown := expand(text)
	at, start := 0, 0 // the byte offset in shown and the column after the previous screen line
	for r := row; r < len(d.Rows) && (r == row || d.Rows[r].Continued); r++ {
		seg := d.segment(r, side)
		k := strings.Index(shown[at:], seg)
		if k < 0 {
			break
		}
		start += ansi.StringWidth(shown[at : at+k])
		width := ansi.StringWidth(seg)
		last := r+1 >= len(d.Rows) || !d.Rows[r+1].Continued
		if col < start+width || last {
			return r, col - start
		}
		at, start = at+k+len(seg), start+width
	}
	return row, col
}

// segmentStart returns the column in the source line text where screen
// line row starts on the given side, and the width of the code on that
// screen line. It searches text for each screen line of the wrapped line
// in order, because wrapping can drop spaces at the breaks.
func (d *Diff) segmentStart(row int, side Side, text string) (start, width int) {
	first := row
	for first > 0 && d.Rows[first].Continued {
		first--
	}
	shown := expand(text)
	at := 0 // byte offset in shown just after the previous screen line
	for i := first; i <= row; i++ {
		seg := d.segment(i, side)
		k := strings.Index(shown[at:], seg)
		if k < 0 {
			break
		}
		if i == row {
			return ansi.StringWidth(shown[:at+k]), ansi.StringWidth(seg)
		}
		at += k + len(seg)
	}
	return ansi.StringWidth(shown[:at]), 0
}

// segment returns the code of the given side on screen line i, without
// trailing spaces.
func (d *Diff) segment(i int, side Side) string {
	plain := ansi.Strip(d.Line(i))
	end := ansi.StringWidth(plain)
	if d.split && side == SideOld {
		end = d.newNumAt
	}
	return strings.TrimRight(ansi.Cut(plain, d.codeAt[side], end), " ")
}

// expand returns line as it appears on screen, with tabs replaced by
// spaces and control characters replaced by symbols.
func expand(line string) string {
	return diffview.Escape(diffview.ExpandTabs(line))
}

// runeAt returns the index of the first rune of the grapheme cluster in
// line that covers column col. If col is past the end, it returns the
// number of runes in line.
func runeAt(line string, col int) int {
	i, c := 0, 0
	for line != "" {
		g, _ := ansi.FirstGraphemeCluster(line, ansi.GraphemeWidth)
		w := diffview.TabWidth
		if g != "\t" {
			w = ansi.StringWidth(diffview.Escape(g))
		}
		if col < c+w {
			return i
		}
		c += w
		i += utf8.RuneCountInString(g)
		line = line[len(g):]
	}
	return i
}

// lineAt returns the 1-based line n of src, as split by [SplitLines]. It
// returns false if src has fewer than n lines.
func lineAt(src string, n int) (string, bool) {
	for l := range strings.Lines(src) {
		if n--; n == 0 {
			return trimEOL(l), true
		}
	}
	return "", false
}
