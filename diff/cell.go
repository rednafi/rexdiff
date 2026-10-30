package diff

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Cell reports the source character at 0-based column x of screen line
// row. line is 1-based and index counts runes. After the end of the code,
// index is the line length. ok is false if the cell is not on code.
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
		if col >= width {
			return side, line, utf8.RuneCountInString(text), true
		}
		col += start
	}
	return side, line, runeAt(text, col), true
}

// CodeWidth returns the code width on side. 0 means no width limit.
func (d *Diff) CodeWidth(side Side) int { return max(d.codeWidth[side], 0) }

// Locate returns the screen line and code column of byte i of the source
// line that starts at screen line row. The column ignores scroll.
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
	shown := expand(text)
	at, start := 0, 0
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

// segmentStart searches for each wrapped part in order, because wrapping
// can drop spaces at the breaks.
func (d *Diff) segmentStart(row int, side Side, text string) (start, width int) {
	first := row
	for first > 0 && d.Rows[first].Continued {
		first--
	}
	shown := expand(text)
	at := 0
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

func (d *Diff) segment(i int, side Side) string {
	plain := ansi.Strip(d.Line(i))
	end := ansi.StringWidth(plain)
	if d.split && side == SideOld {
		end = d.newNumAt
	}
	return strings.TrimRight(ansi.Cut(plain, d.codeAt[side], end), " ")
}

func expand(line string) string {
	return diffview.Escape(diffview.ExpandTabs(line))
}

// runeAt returns the rune count if col is past the end.

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

func lineAt(src string, n int) (string, bool) {
	for l := range strings.Lines(src) {
		if n--; n == 0 {
			return trimEOL(l), true
		}
	}
	return "", false
}
