// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

// Package diffview draws the difference between two versions of a file in
// the terminal, with syntax colours.
package diffview

import (
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

// View draws the difference between two versions of a file. Make one with
// [New], set its options, and call [View.Render].
type View struct {
	path          string // selects the lexer
	before, after string
	split         bool // the old and the new lines side by side
	width         int
	xOffset       int
	wrap          bool // wrap long lines instead of cutting them
	signs         bool // mark a changed line with + or -, not with a bar
	style         style
	expanded      func(Fold) bool // the folds that show, see [View.Expand]
	cache         *Cache

	// Render sets the fields below.
	hunks           []*udiff.Hunk
	folds           []Fold // folds[i] hides above hunks[i]
	rows            []Row
	codeWidth       int
	fullCodeWidth   int  // with the leading symbols
	extraColOnAfter bool // the new side of the split layout is one cell wider
	beforeNumDigits int
	afterNumDigits  int
}

// New returns a View of the change from before to after, the old and the
// new content of the file at path. The path selects the syntax colours.
// The View has the unified layout, no width limit, and no tints.
func New(path, before, after string) *View {
	return &View{
		path:   path,
		before: before,
		after:  after,
		style:  terminalStyle(nil, nil, nil, nil, true),
		cache:  &Cache{},
	}
}

// Split sets the split layout if split is true. The old lines are on the
// left and the new lines are on the right.
func (v *View) Split(split bool) *View {
	v.split = split
	return v
}

// Width sets the maximum width of the output, in cells. Zero means no limit.
func (v *View) Width(width int) *View {
	v.width = width
	return v
}

// XOffset sets the number of cells to scroll the code to the right.
func (v *View) XOffset(xOffset int) *View {
	v.xOffset = xOffset
	return v
}

// Wrap makes long lines continue on the next screen lines if wrap is
// true, instead of cutting them. Then the x offset does not apply.
func (v *View) Wrap(wrap bool) *View {
	v.wrap = wrap
	return v
}

// Signs marks an inserted line with + and a deleted line with - if signs
// is true, in place of a bar.
func (v *View) Signs(signs bool) *View {
	v.signs = signs
	return v
}

// Expand shows the lines of each fold for which expanded returns true.
func (v *View) Expand(expanded func(Fold) bool) *View {
	v.expanded = expanded
	return v
}

// Cache makes v keep its highlighted lines in c. The Cache c must not be
// nil.
func (v *View) Cache(c *Cache) *View {
	v.cache = c
	return v
}

// Theme sets the colours from the colours of the terminal. These are the
// background, the foreground, and the ANSI green and red. The text colours
// are ANSI palette indices, so the terminal selects the real colours.
// Theme mixes the tints of inserted and deleted lines from the background
// toward green and red. If green or red is nil, it uses a fixed green or
// red. If bg is nil, it adds no tint. Dark selects the tint amounts for a
// dark background.
func (v *View) Theme(bg, fg, green, red color.Color, dark bool) *View {
	v.style = terminalStyle(bg, fg, green, red, dark)
	return v
}

// Render returns the rendered diff, one row for each of its lines, and the
// columns of the line numbers.
func (v *View) Render() (string, []Row, Geometry) {
	v.rows = nil
	if err := v.computeDiff(); err != nil {
		return err.Error(), nil, v.geometry()
	}
	v.markEndings()
	v.replaceTabs()
	v.detectNumDigits()
	if v.width <= 0 {
		v.detectCodeWidth()
	} else {
		v.resizeCodeWidth()
	}

	style := lipgloss.NewStyle()
	if v.width > 0 {
		style = style.MaxWidth(v.width)
	}
	var out string
	if v.split {
		out = v.renderSplit()
	} else {
		out = v.renderUnified()
	}
	return style.Render(strings.TrimSuffix(out, "\n")), v.rows, v.geometry()
}

// computeDiff computes the hunks and the folds of the diff.
func (v *View) computeDiff() error {
	edits := udiff.Lines(v.before, v.after)
	// With a context as long as the file, the diff has one hunk with all
	// the lines. foldHunk then hides the long runs of equal lines.
	context := strings.Count(v.before, "\n") + 1
	unified, err := udiff.ToUnifiedDiff("", "", v.before, edits, context)
	if err != nil {
		return err
	}
	v.hunks, v.folds = nil, nil
	for _, h := range unified.Hunks {
		hs, fs := foldHunk(h, v.expanded)
		v.hunks = append(v.hunks, hs...)
		v.folds = append(v.folds, fs...)
	}
	return nil
}

// crMark shows a CR at the end of a line, as less and vim show it.
const crMark = "^M"

// markEndings removes the CR of each line ending, so that it does not
// show. In a block of changes, the deleted lines and the inserted lines
// pair up in their order. As in herdr-reviewr, a line of a pair that
// differs from the other line only in a CR gets crMark. Thus a change of
// only the line ending shows.
func (v *View) markEndings() {
	for _, h := range v.hunks {
		ls := h.Lines
		for i := 0; i < len(ls); {
			dels := i
			for i < len(ls) && ls[i].Kind == udiff.Delete {
				i++
			}
			ins := i
			for i < len(ls) && ls[i].Kind == udiff.Insert {
				i++
			}
			for k := 0; dels+k < ins && ins+k < i; k++ {
				markPair(&ls[dels+k], &ls[ins+k])
			}
			if i == dels { // an equal line
				i++
			}
		}
		for i := range ls {
			ls[i].Content, _ = cutCR(ls[i].Content, "")
		}
	}
}

// markPair adds crMark to old or to new if the two lines differ only in
// the CR of the line ending.
func markPair(old, new *udiff.Line) {
	o, oCR := cutCR(old.Content, "")
	n, nCR := cutCR(new.Content, "")
	if oCR != nCR && o == n {
		old.Content, _ = cutCR(old.Content, crMark)
		new.Content, _ = cutCR(new.Content, crMark)
	}
}

// cutCR returns line with mark in place of the CR of its line ending, and
// reports whether line has that CR. The last line of a file can have a CR
// and no LF.
func cutCR(line, mark string) (string, bool) {
	body, lf := strings.CutSuffix(line, "\n")
	body, cr := strings.CutSuffix(body, "\r")
	if cr {
		body += mark
	}
	if lf {
		body += "\n"
	}
	return body, cr
}

// replaceTabs replaces the tabs in the lines of the diff with spaces. It runs
// after computeDiff, so that a change from a tab to spaces is a change.
func (v *View) replaceTabs() {
	spaces := strings.Repeat(" ", TabWidth)
	for _, h := range v.hunks {
		for i := range h.Lines {
			h.Lines[i].Content = strings.ReplaceAll(h.Lines[i].Content, "\t", spaces)
		}
	}
}

// detectNumDigits finds the number of digits of the largest old and new line
// numbers.
func (v *View) detectNumDigits() {
	v.beforeNumDigits = 0
	v.afterNumDigits = 0
	for _, h := range v.hunks {
		before := len(strconv.Itoa(h.FromLine + len(h.Lines)))
		after := len(strconv.Itoa(h.ToLine + len(h.Lines)))
		v.beforeNumDigits = max(v.beforeNumDigits, before)
		v.afterNumDigits = max(v.afterNumDigits, after)
	}
}

// detectCodeWidth finds the width of the widest code line or hunk header.
// The split layout shows the same lines, so the width is the same for the
// two layouts.
func (v *View) detectCodeWidth() {
	v.codeWidth = 0
	for i, h := range v.hunks {
		v.codeWidth = max(v.codeWidth, ansi.StringWidth(v.headerText(i)))
		for _, l := range h.Lines {
			w := ansi.StringWidth(Escape(strings.TrimSuffix(l.Content, "\n"))) + 1
			v.codeWidth = max(v.codeWidth, w)
		}
	}
	v.fullCodeWidth = v.codeWidth + leadingSymbolsSize
}

// resizeCodeWidth sets the code width so that the lines fit in the width.
func (v *View) resizeCodeWidth() {
	fullNumWidth := v.beforeNumDigits + v.afterNumDigits
	fullNumWidth += lineNumPadding * 4 // two sides of two line numbers

	if v.split {
		remainingWidth := v.width - fullNumWidth - leadingSymbolsSize*2
		v.codeWidth = remainingWidth / 2
		v.extraColOnAfter = remainingWidth%2 != 0
	} else {
		v.codeWidth = v.width - fullNumWidth - leadingSymbolsSize
	}
	v.fullCodeWidth = v.codeWidth + leadingSymbolsSize
}

// Geometry gives the columns of the line numbers and of the code in the
// output of [View.Render]. Use it to tell a line number from the code.
type Geometry struct {
	BeforeNum     int // the width of the old line-number column, in cells
	AfterNum      int // the width of the new line-number column, in cells
	AfterNumStart int // the column where the new line numbers start, from 0
	BeforeCode    int // the column where the old code starts, from 0
	AfterCode     int // the column where the new code starts, from 0
}

// geometry returns the columns of the line numbers and of the code.
func (v *View) geometry() Geometry {
	g := Geometry{
		BeforeNum: v.beforeNumDigits + lineNumPadding*2,
		AfterNum:  v.afterNumDigits + lineNumPadding*2,
	}
	g.AfterNumStart = g.BeforeNum
	if v.split {
		g.AfterNumStart = g.BeforeNum + v.fullCodeWidth
		g.BeforeCode = g.BeforeNum + leadingSymbolsSize
		g.AfterCode = g.AfterNumStart + g.AfterNum + leadingSymbolsSize
		return g
	}
	g.BeforeCode = g.BeforeNum + g.AfterNum + leadingSymbolsSize
	g.AfterCode = g.BeforeCode
	return g
}
