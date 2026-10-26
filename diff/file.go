package diff

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// lineNumberColor is the ANSI palette colour of the line numbers. Colour 8
// is bright black.
const lineNumberColor = "8"

// RenderFile shows src, the content of the file at path, with line numbers
// and syntax colours. Each row is a [KindEqual] row, with the same line
// number on the two sides. The line numbers count as the [SideNew] side.
// RenderFile uses only the Width, Offset, Wrap, and Cache fields of o.
func RenderFile(path, src string, o Options) *Diff {
	// Chroma makes a lone CR a line break. RenderFile shows a lone CR as a
	// symbol, so that the lines are the same as [Lines] gives.
	raw := src
	src = strings.NewReplacer(
		"\r\n", "\n",
		"\r", "\u240d",
		"\t", strings.Repeat(" ", diffview.TabWidth),
	).Replace(src)
	hl := strings.Split(o.Cache.fileColors(path, src), "\n")
	hl = hl[:min(len(hl), countLines(src))] // chroma can add a final newline
	digits := len(strconv.Itoa(len(hl)))
	numWidth := digits + 2 // a space on each side of the number
	// The style renders once, around a format for the number.
	numFmt := lipgloss.NewStyle().Foreground(lipgloss.Color(lineNumberColor)).
		Render(" %*d ") + " "
	d := &Diff{newNum: numWidth, src: [2]string{raw, raw}, wrap: o.Wrap}
	d.codeAt = [2]int{numWidth + 1, numWidth + 1}
	if !o.Wrap {
		d.offset = o.Offset
	}
	code := o.Width - numWidth - 1 // the width after the number and a space
	blank := strings.Repeat(" ", numWidth+1)
	for i, l := range hl {
		for k, p := range fileLineParts(l, code, o) {
			num := blank
			if k == 0 {
				num = fmt.Sprintf(numFmt, digits, i+1)
			}
			line := num + p
			if o.Width > 0 { // the number or a wide character can be too wide
				line = ansi.Truncate(line, o.Width, "")
			}
			d.Lines = append(d.Lines, line)
			d.Rows = append(d.Rows, Row{
				Kind:      KindEqual,
				Old:       i + 1,
				New:       i + 1,
				Continued: k > 0,
			})
		}
	}
	return d
}

// fileLineParts returns the screen lines of the highlighted line l. If o has
// a width, each part fits in code cells. With [Options.Wrap], a long line
// gives more than one part. Without Wrap, the line skips [Options.Offset]
// cells and is cut.
func fileLineParts(l string, code int, o Options) []string {
	switch {
	case o.Width > 0 && code <= 0:
		return []string{""}
	case o.Width > 0 && o.Wrap:
		// Wrap can keep a leading space on a full line, so cut again.
		wrapped := ansi.Hardwrap(lipgloss.Wrap(l, code, ""), code, true)
		return strings.Split(wrapped, "\n")
	case o.Width > 0:
		return []string{ansi.Truncate(diffview.CutLeft(l, o.Offset), code, "…")}
	}
	return []string{diffview.CutLeft(l, o.Offset)}
}

// fileColors returns highlight(path, src), and keeps it for the next
// call. A nil c keeps nothing.
func (c *Cache) fileColors(path, src string) string {
	if c == nil {
		return highlight(path, src)
	}
	f := &c.file
	if f.path != path || f.src != src {
		f.path, f.src, f.out = path, src, highlight(path, src)
	}
	return f.out
}

// highlight colours src, the whole file at path, at once, so that tokens
// that span lines get the correct colour. If it cannot highlight src, it
// returns src with its control characters replaced.
func highlight(path, src string) string {
	it, err := diffview.Lexer(path, src).Tokenise(nil, src)
	if err != nil {
		return escapeKeepNewlines(src)
	}
	f := diffview.ANSIFormatter(lipgloss.NoColor{}, escapeKeepNewlines)
	var b strings.Builder
	if f.Format(&b, nil, it) != nil {
		return escapeKeepNewlines(src)
	}
	return b.String()
}

// escapeKeepNewlines replaces the control characters in a token, but keeps
// its line breaks. The caller splits the output into lines.
func escapeKeepNewlines(s string) string {
	parts := strings.Split(s, "\n")
	for i, p := range parts {
		parts[i] = diffview.Escape(p)
	}
	return strings.Join(parts, "\n")
}
