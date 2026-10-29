package diff

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// lineNumColor is the ANSI palette colour of the line numbers (bright
// black).
const lineNumColor = "8"

// FileOptions controls how [RenderFile] draws a file. The fields do what
// the fields of [Options] with the same names do.
type FileOptions struct {
	Width   int    // the maximum width of a line, or 0 for no limit
	ScrollX int    // the cells to scroll the code to the right
	Wrap    bool   // continue a long line on the following lines
	Cache   *Cache // the results to reuse, or nil
}

// RenderFile renders src, the content of the file at path, with line
// numbers and syntax highlighting. Every row is a [KindEqual] row with the
// same line number on both sides, and the line numbers belong to
// [SideNew].
func RenderFile(path, src string, o FileOptions) *Diff {
	raw := src
	src, hl := o.Cache.fileColors(path, raw)
	n := countLines(src)
	digits := len(strconv.Itoa(n))
	numWidth := digits + 2 // a space on each side of the number
	f := &fileLines{
		hl:     hl,
		o:      o,
		digits: digits,
		// Render the style once, around a format verb for the number.
		numFmt: lipgloss.NewStyle().Foreground(lipgloss.Color(lineNumColor)).
			Render(" %*d ") + " ",
		codeWidth: o.Width - numWidth - 1, // the width left after the number and a space
		blank:     strings.Repeat(" ", numWidth+1),
		src:       strings.Split(src, "\n"),
	}
	d := &Diff{newNum: numWidth, texts: [2]string{raw, raw}, wrap: o.Wrap}
	d.codeAt = [2]int{numWidth + 1, numWidth + 1}
	if o.Width > 0 {
		d.codeWidth = [2]int{f.codeWidth, f.codeWidth}
	}
	if !o.Wrap {
		d.scrollX = o.ScrollX
	}
	// Only a wrapped line that does not fit uses more than one screen line.
	// The count comes from the plain text, so no line is highlighted here.
	wraps := o.Wrap && o.Width > 0 && f.codeWidth > 0
	start := make([]int, n)
	d.Rows = make([]Row, 0, n)
	for i := range n {
		parts := 1
		if wraps && !diffview.Fits(f.source(i), f.codeWidth) {
			parts = len(diffview.WrapParts(f.plain(i), f.codeWidth))
		}
		start[i] = len(d.Rows)
		for k := range parts {
			d.Rows = append(d.Rows, Row{Kind: KindEqual, Old: i + 1, New: i + 1, Continued: k > 0})
		}
	}
	d.lines = diffview.NewLines(len(d.Rows), start, f.render)
	return d
}

// fileLines renders the screen lines of [RenderFile].
type fileLines struct {
	hl            *diffview.Highlighter // the highlighted source lines
	o             FileOptions
	digits        int      // the number of digits in line numbers
	codeWidth     int      // the width left for code
	numFmt, blank string   // the line-number format, and blank space of the same width
	src           []string // the source lines, as the highlighter got them
}

// render returns the screen lines of the 0-based source line i. final is
// false if the highlighter did not have the line ready, so the lines have
// no colours for now.
func (f *fileLines) render(i int) (lines []string, final bool) {
	l, _, final := f.hl.Line(i)
	parts := fileLineParts(f.plain(i), l, f.codeWidth, f.o)
	out := make([]string, len(parts))
	for k, p := range parts {
		num := f.blank
		if k == 0 {
			num = fmt.Sprintf(f.numFmt, f.digits, i+1)
		}
		line := num + p
		if f.o.Width > 0 { // the number or a wide character can overflow the width
			line = ansi.Truncate(line, f.o.Width, "")
		}
		out[k] = line
	}
	return out, final
}

// source returns the 0-based source line i, or "" if there is none.
func (f *fileLines) source(i int) string {
	if i < len(f.src) {
		return f.src[i]
	}
	return ""
}

// plain returns the 0-based source line i escaped and without colours, as
// the highlighter shows a line that it does not highlight.
func (f *fileLines) plain(i int) string { return diffview.Escape(f.source(i)) }

// fileLineParts splits the highlighted line l, whose plain text is plain,
// into screen lines. If o sets a width, each part fits in codeWidth cells.
// With [FileOptions.Wrap], a long line becomes several parts, broken where
// the plain text breaks. Without Wrap, the first [FileOptions.ScrollX]
// cells are skipped and the rest is cut to fit.
func fileLineParts(plain, l string, codeWidth int, o FileOptions) []string {
	switch {
	case o.Width > 0 && codeWidth <= 0:
		return []string{""}
	case o.Width > 0 && o.Wrap && diffview.Fits(plain, codeWidth):
		return []string{l}
	case o.Width > 0 && o.Wrap:
		return diffview.WrapLine(plain, l, codeWidth)
	case o.Width > 0:
		return []string{ansi.Truncate(diffview.CutLeft(l, o.ScrollX), codeWidth, "…")}
	}
	return []string{diffview.CutLeft(l, o.ScrollX)}
}

// fileColors converts raw, the content of the file at path, for display by
// RenderFile, and returns the converted text and a highlighter for it. c
// stores both for the next call, and stops the old highlighter when the
// file changes. A nil c stores nothing.
func (c *Cache) fileColors(path, raw string) (string, *diffview.Highlighter) {
	if c == nil {
		// Nothing stops this highlighter; its goroutine ends when it
		// reaches the end of src.
		src := shownFile(raw)
		return src, highlighter(path, src)
	}
	f := &c.file
	src := f.src
	if f.h == nil || f.raw != raw {
		src = shownFile(raw)
	}
	if f.h == nil || f.path != path || f.src != src {
		if f.h != nil {
			f.h.Stop() // a Diff that uses it starts it again for the lines it reads
		}
		f.path, f.src, f.h = path, src, highlighter(path, src)
	}
	f.raw = raw
	return src, f.h
}

// StopHighlighting stops the background highlighting of the last file
// passed to [RenderFile] with c, for example when the file is no longer
// shown. A [Diff] of the file starts it again for the lines that it reads.
func (c *Cache) StopHighlighting() {
	if c != nil && c.file.h != nil {
		c.file.h.Stop()
	}
}

// Highlighting reports whether the last file passed to [RenderFile] with
// c is still being highlighted in the background. While it is, lines that
// were drawn without colours can get them, so the screen should be drawn
// again.
func (c *Cache) Highlighting() bool { return c != nil && c.file.h != nil && c.file.h.Busy() }

// fileChars replaces CRLF with LF and each tab with spaces. It replaces a
// lone CR with a symbol, because chroma treats it as a line break. The
// resulting lines match those of [SplitLines].
var fileChars = strings.NewReplacer("\r\n", "\n", "\r", diffview.CRSymbol, "\t", strings.Repeat(" ", diffview.TabWidth))

// shownFile returns raw converted for display by RenderFile.
func shownFile(raw string) string { return fileChars.Replace(raw) }

// highlighter returns a [diffview.Highlighter] for src, the whole content
// of the file at path. If src cannot be highlighted, the lines are those
// of src with control characters escaped.
func highlighter(path, src string) *diffview.Highlighter {
	return diffview.NewHighlighter(path, src, lipgloss.NoColor{}, escapeKeepNewlines, escapeKeepNewlines)
}

// escapeKeepNewlines escapes the control characters in s but keeps its
// line breaks.
func escapeKeepNewlines(s string) string { return diffview.EachLine(s, diffview.Escape) }
