package diff

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// lineNumColor is bright black in the ANSI palette.
const lineNumColor = "8"

// FileOptions are the [Options] that [RenderFile] uses.
type FileOptions struct {
	Width   int
	ScrollX int
	Wrap    bool
	Cache   *Cache
}

// RenderFile renders src as [KindEqual] rows with line numbers.
func RenderFile(path, src string, o FileOptions) *Diff {
	raw := src
	src, hl := o.Cache.fileColors(path, raw)
	n := countLines(src)
	digits := len(strconv.Itoa(n))
	numWidth := digits + 2
	f := &fileLines{
		hl:     hl,
		o:      o,
		digits: digits,
		numFmt: lipgloss.NewStyle().Foreground(lipgloss.Color(lineNumColor)).
			Render(" %*d ") + " ",
		codeWidth: o.Width - numWidth - 1,
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
	// Count wrapped parts from plain text, to avoid highlighting here.
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

type fileLines struct {
	hl            *diffview.Highlighter
	o             FileOptions
	digits        int
	codeWidth     int
	numFmt, blank string
	src           []string // as the highlighter got them
}

// render returns final false if the highlighter did not have the line
// ready. The lines then have no colours.
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
		if f.o.Width > 0 { // the number or a wide character can overflow
			line = ansi.Truncate(line, f.o.Width, "")
		}
		out[k] = line
	}
	return out, final
}

func (f *fileLines) source(i int) string {
	if i < len(f.src) {
		return f.src[i]
	}
	return ""
}

// plain matches how the highlighter shows an unhighlighted line.
func (f *fileLines) plain(i int) string { return diffview.Escape(f.source(i)) }

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

// fileColors stops the old highlighter when the file changes.
func (c *Cache) fileColors(path, raw string) (string, *diffview.Highlighter) {
	if c == nil {
		// Nothing stops this highlighter. Its goroutine ends at the end of
		// src.
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

// StopHighlighting stops the background highlighting of the last file. A
// [Diff] starts it again for the lines it reads.
func (c *Cache) StopHighlighting() {
	if c != nil && c.file.h != nil {
		c.file.h.Stop()
	}
}

// Highlighting reports whether the last file is still in background
// highlighting. While it is, draw the screen again to get more colours.
func (c *Cache) Highlighting() bool { return c != nil && c.file.h != nil && c.file.h.Busy() }

// fileChars replaces a lone CR with a symbol, because chroma treats it as
// a line break. The lines then match those of [SplitLines].
var fileChars = strings.NewReplacer("\r\n", "\n", "\r", diffview.CRSymbol, "\t", strings.Repeat(" ", diffview.TabWidth))

func shownFile(raw string) string { return fileChars.Replace(raw) }

func highlighter(path, src string) *diffview.Highlighter {
	return diffview.NewHighlighter(path, src, lipgloss.NoColor{}, escapeKeepNewlines, escapeKeepNewlines)
}

func escapeKeepNewlines(s string) string { return diffview.EachLine(s, diffview.Escape) }
