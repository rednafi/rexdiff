package diff

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// Cell tells which source character the cell at column x of line row
// shows. Column 0 is the first column of the line. It returns the side,
// the line number from 1, and the index of the rune in [Lines] of that
// side. The index can be past the end of the line, for a cell after the
// code. ok is false if the cell is not on code, for example on a line
// number, a mark, or a hunk header.
func (d *Diff) Cell(row, x int) (s Side, line, index int, ok bool) {
	if row < 0 || row >= len(d.Rows) || d.Rows[row].Kind == KindHunk {
		return 0, 0, 0, false
	}
	r := d.Rows[row]
	s, onNumber := d.SideAt(x)
	if !d.split {
		s = SideNew
		if r.New == 0 {
			s = SideOld
		}
	}
	line = r.Line(s)
	if onNumber || line == 0 || x < d.codeAt[s] {
		return 0, 0, 0, false
	}
	text, ok := lineAt(d.src[s], line)
	if !ok {
		return 0, 0, 0, false
	}
	col := x - d.codeAt[s] + d.offset
	if d.wrap {
		start, width := d.segmentStart(row, s, text)
		if col >= width { // the blank cells after a wrapped part
			return s, line, utf8.RuneCountInString(text), true
		}
		col += start
	}
	return s, line, runeAt(text, col), true
}

// segmentStart returns the column in the source line text where the
// screen line row starts on side s, and the width of its code. A wrapped
// line can drop the spaces where it breaks. Thus it finds each screen line
// in the text, in order.
func (d *Diff) segmentStart(row int, s Side, text string) (start, width int) {
	first := row
	for first > 0 && d.Rows[first].Continued {
		first--
	}
	shown := expand(text)
	at := 0 // the byte in shown after the last screen line
	for i := first; i <= row; i++ {
		seg := d.segment(i, s)
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

// segment returns the code of side s on screen line i, without the
// spaces at the end.
func (d *Diff) segment(i int, s Side) string {
	plain := ansi.Strip(d.Lines[i])
	end := ansi.StringWidth(plain)
	if d.split && s == SideOld {
		end = d.newNumAt
	}
	return strings.TrimRight(ansi.Cut(plain, d.codeAt[s], end), " ")
}

// expand returns line as the screen shows it, with the tabs as spaces and
// the control characters as symbols.
func expand(line string) string {
	return diffview.Escape(strings.ReplaceAll(line, "\t", strings.Repeat(" ", diffview.TabWidth)))
}

// runeAt returns the index of the first rune of the grapheme cluster of
// line that covers column col. It returns the number of runes if col is
// past the end.
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

// lineAt returns line n of src, from 1, as [Lines] splits it.
func lineAt(src string, n int) (string, bool) {
	for l := range strings.Lines(src) {
		if n--; n == 0 {
			return trimEOL(l), true
		}
	}
	return "", false
}
