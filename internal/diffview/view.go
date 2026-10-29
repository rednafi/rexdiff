// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

// Package diffview draws the difference between two versions of a file for
// a terminal, with syntax highlighting.
package diffview

import (
	"image/color"
	"slices"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// View draws the difference between two versions of a file. Create one
// with [New], set its options, and call [View.RenderLines].
type View struct {
	path             string // selects the syntax highlighting
	oldText, newText string
	split            bool // show the old and new lines side by side
	width            int
	scrollX          int
	wrap             bool // wrap long lines instead of cutting them
	signs            bool // mark changed lines with + or - instead of a bar
	style            style
	expanded         func(Fold) bool // reports which folds are expanded; see [View.Expanded]
	cache            *Cache

	// prepare sets the fields below.
	hunks         []*hunk
	folds         []Fold // folds[i] is the fold above hunks[i]
	codeWidth     int
	fullCodeWidth int  // codeWidth plus the leading marks
	extraColOnNew bool // in split view, the new side is one cell wider
	oldNumDigits  int
	newNumDigits  int
}

// New returns a View of the change from oldText to newText, the old and
// new content of the file at path. The path selects the syntax
// highlighting. The View starts with the unified layout, no width limit
// and no tints.
func New(path, oldText, newText string) *View {
	return &View{
		path:    path,
		oldText: oldText,
		newText: newText,
		style:   terminalStyle(nil, nil, nil, nil, true),
		cache:   &Cache{},
	}
}

// Split selects the split layout if split is true, with the old lines on
// the left and the new lines on the right.
func (v *View) Split(split bool) *View {
	v.split = split
	return v
}

// Width sets the maximum width of the output, in cells. Zero means no limit.
func (v *View) Width(width int) *View {
	v.width = width
	return v
}

// ScrollX sets the number of cells to scroll the code to the right.
func (v *View) ScrollX(scrollX int) *View {
	v.scrollX = scrollX
	return v
}

// Wrap sets whether long lines continue on the following screen lines
// instead of being cut off. When wrap is true, [View.ScrollX] is ignored.
func (v *View) Wrap(wrap bool) *View {
	v.wrap = wrap
	return v
}

// Signs sets whether added lines are marked with + and deleted lines with
// -, instead of a bar.
func (v *View) Signs(signs bool) *View {
	v.signs = signs
	return v
}

// Expanded shows the hidden lines of each fold for which expanded returns
// true.
func (v *View) Expanded(expanded func(Fold) bool) *View {
	v.expanded = expanded
	return v
}

// Cache makes v store its highlighted lines and diff in c. c must not be
// nil.
func (v *View) Cache(c *Cache) *View {
	v.cache = c
	return v
}

// Theme sets the colours from the terminal's background, foreground, green
// and red. Text colours are ANSI palette indices. Added and deleted lines
// are tinted by mixing the background towards green and red. If green or
// red is nil, a fixed colour is used instead. If bg is nil, lines are not
// tinted. dark selects the tint strengths for a dark background.
func (v *View) Theme(bg, fg, green, red color.Color, dark bool) *View {
	v.style = terminalStyle(bg, fg, green, red, dark)
	return v
}

// renderAll renders every line of the prepared view and returns them with
// their rows.
func (v *View) renderAll() (string, []Row) {
	var b strings.Builder
	var rows []Row
	items := v.items()
	for k := range items {
		it := &items[k]
		for i, l := range v.itemLines(it) {
			b.WriteString(l)
			b.WriteByte('\n')
			r := it.row
			r.Continued = i > 0
			rows = append(rows, r)
		}
	}
	frame := lipgloss.NewStyle()
	if v.width > 0 {
		frame = frame.MaxWidth(v.width)
	}
	return frame.Render(strings.TrimSuffix(b.String(), "\n")), rows
}

// prepare computes the hunks, folds and column widths of the view.
func (v *View) prepare() {
	v.computeDiff()
	v.computeNumDigits()
	if v.width <= 0 {
		v.computeCodeWidth()
	} else {
		v.resizeCodeWidth()
	}
}

// Lines holds screen lines that are rendered when they are first
// requested. Each unit of the output, such as a line of a diff, takes one
// or more screen lines, which are rendered together. Lines is not safe for
// concurrent use.
type Lines struct {
	render func(k int) (lines []string, final bool) // renders unit k
	start  []int                                    // start[k] is the first screen line of unit k
	lines  []string
	done   []bool // done[i] is true if lines[i] is rendered for good
}

// NewLines returns n screen lines, where start[k] is the first screen line
// of unit k, and render renders the screen lines of unit k. If render
// returns fewer lines than the unit has, the rest are blank. If final is
// false, Lines renders the unit again the next time one of its lines is
// requested.
func NewLines(n int, start []int, render func(k int) (lines []string, final bool)) *Lines {
	return &Lines{render: render, start: start, lines: make([]string, n), done: make([]bool, n)}
}

// Line returns screen line i.
func (ls *Lines) Line(i int) string {
	if ls.done[i] {
		return ls.lines[i]
	}
	// The last unit that starts at or before i. A unit with no lines
	// starts at the same line as the next unit, so it is never chosen.
	k := sort.Search(len(ls.start), func(k int) bool { return ls.start[k] > i }) - 1
	first, end := ls.start[k], len(ls.lines)
	if k+1 < len(ls.start) {
		end = ls.start[k+1]
	}
	out, final := ls.render(k)
	for j := first; j < end; j++ {
		l := ""
		if j-first < len(out) {
			l = out[j-first]
		}
		ls.lines[j], ls.done[j] = l, final
	}
	return ls.lines[i]
}

// fit pads or cuts l to the width of v, as renderAll does. If v has no
// width, fit returns l unchanged. l contains no tabs, because
// replaceTabs has already replaced them.
func (v *View) fit(l string) string {
	if v.width <= 0 {
		return l
	}
	switch w := ansi.StringWidth(l); {
	case w < v.width:
		return l + strings.Repeat(" ", v.width-w)
	case w > v.width:
		return ansi.Truncate(l, v.width, "")
	}
	return l
}

// RenderLines returns the lines of the rendered diff, one row for each
// of them, and the positions of the line-number and code columns. Each
// line is rendered when it is first requested, unless the width leaves no
// room for the code: then all lines are rendered at once.
func (v *View) RenderLines() (*Lines, []Row, Geometry) {
	v.prepare()
	if v.width > 0 && v.codeWidth < 1 {
		// The lines can have different widths, so render them all to find
		// the widest.
		out, rows := v.renderAll()
		ls := &Lines{}
		if len(rows) > 0 {
			ls.lines = strings.Split(out, "\n")
			ls.done = make([]bool, len(ls.lines))
			for i := range ls.done {
				ls.done[i] = true
			}
		}
		return ls, rows, v.geometry()
	}
	f := &v.cache.folded
	key := itemsKey{split: v.split, wrap: v.wrap, codeWidth: v.codeWidth, extraColOnNew: v.extraColOnNew}
	if f.items == nil || f.key != key {
		f.key, f.items = key, v.items()
		f.start = make([]int, len(f.items))
		f.rows = make([]Row, 0, len(f.items))
		for k := range f.items {
			it := &f.items[k]
			f.start[k] = len(f.rows)
			for i := range v.itemHeight(it) {
				r := it.row
				r.Continued = i > 0
				f.rows = append(f.rows, r)
			}
		}
	}
	items := f.items
	ls := NewLines(len(f.rows), f.start, func(k int) ([]string, bool) {
		out := v.itemLines(&items[k])
		for i, l := range out {
			out[i] = v.fit(l)
		}
		return out, true
	})
	return ls, f.rows, v.geometry()
}

// computeDiff computes the hunks and folds of the diff. The [Cache]
// stores them for reuse by the next View with the same diff and hidden
// folds.
func (v *View) computeDiff() {
	whole, keep := v.cache.unified(v.oldText, v.newText)
	hidden := make([][]Fold, len(whole))
	for i, h := range whole {
		hidden[i] = hiddenFolds(h, keep[i], v.expanded)
	}
	f := &v.cache.folded
	if f.gen == v.cache.diff.gen && slices.EqualFunc(f.hunkFolds, hidden, slices.Equal) {
		v.hunks, v.folds = f.hunks, f.folds
		return
	}
	v.hunks, v.folds = nil, nil
	for i, h := range whole {
		hs, fs := foldHunk(h, keep[i], hidden[i])
		v.hunks = append(v.hunks, hs...)
		v.folds = append(v.folds, fs...)
	}
	*f = foldedDiff{gen: v.cache.diff.gen, hunkFolds: hidden, hunks: v.hunks, folds: v.folds}
}

// foldedDiff is the diff of a View after folding, and its items for one
// layout. A [Cache] stores the most recent one. The hunks, items and rows
// are not modified after they are built.
type foldedDiff struct {
	gen       int      // the generation of the Cache diff that the hunks come from
	hunkFolds [][]Fold // the hidden folds of each hunk in that diff
	hunks     []*hunk
	folds     []Fold

	// The items of [View.RenderLines], for the layout described by key.
	key   itemsKey
	items []item
	rows  []Row
	start []int // start[k] is the first row of items[k]
}

// itemsKey holds the values other than the hunks that the items and their
// heights depend on.
type itemsKey struct {
	split, wrap   bool
	codeWidth     int
	extraColOnNew bool
}

// unified returns the diff from oldText to newText as at most one hunk
// that holds every line, with CRs marked and tabs replaced, and the
// keptLines of each hunk. c stores the last result, which the caller must
// not modify.
func (c *Cache) unified(oldText, newText string) (hunks []*hunk, keep [][]bool) {
	u := &c.diff
	if u.gen > 0 && u.oldText == oldText && u.newText == newText {
		return u.hunks, u.keep
	}
	// One hunk holds every line. foldHunk hides long runs of equal lines
	// later.
	u.hunks = nil
	if h := lineDiff(oldText, newText); h != nil {
		u.hunks = []*hunk{h}
	}
	// A fold hides only equal lines, so these steps have the same result
	// before and after folding.
	markEndings(u.hunks)
	replaceTabs(u.hunks)
	u.oldText, u.newText = oldText, newText
	u.keep = make([][]bool, len(u.hunks))
	for i, h := range u.hunks {
		u.keep[i] = keptLines(h)
	}
	u.gen++
	return u.hunks, u.keep
}

// crMark marks a CR at the end of a line.
const crMark = "^M"

// noNewlineMark follows a changed line that has no line ending, the last
// line of its file.
const noNewlineMark = "  \\ No newline at end of file"

// markEndings removes the CR from each line ending. In each block of
// changes, it pairs deleted lines with added lines in order. If the two
// lines of a pair differ only by a CR, it adds crMark to the line that had
// the CR. It adds noNewlineMark to each changed line without a line
// ending.
func markEndings(hunks []*hunk) {
	for _, h := range hunks {
		ls := h.Lines
		for i := 0; i < len(ls); {
			deleted := i
			for i < len(ls) && ls[i].Kind == KindDeleted {
				i++
			}
			ins := i
			for i < len(ls) && ls[i].Kind == KindAdded {
				i++
			}
			for k := 0; deleted+k < ins && ins+k < i; k++ {
				markPair(&ls[deleted+k], &ls[ins+k])
			}
			if i == deleted { // an equal line
				i++
			}
		}
		for i := range ls {
			ls[i].Content, _ = cutCR(ls[i].Content, "")
			if ls[i].Kind != KindEqual && !strings.HasSuffix(ls[i].Content, "\n") {
				ls[i].Content += noNewlineMark
			}
		}
	}
}

// markPair adds crMark to the line that has a CR if oldLine and newLine
// differ only by the CR in the line ending.
func markPair(oldLine, newLine *diffLine) {
	o, oCR := cutCR(oldLine.Content, "")
	n, nCR := cutCR(newLine.Content, "")
	if oCR != nCR && o == n {
		oldLine.Content, _ = cutCR(oldLine.Content, crMark)
		newLine.Content, _ = cutCR(newLine.Content, crMark)
	}
}

// cutCR returns line with mark in place of the CR in its line ending, and
// reports whether line had that CR. The last line of a file can end in a
// CR without an LF.
func cutCR(line, mark string) (string, bool) {
	body, cr := strings.CutSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !cr {
		return line, false
	}
	return body + mark + line[len(body)+1:], true
}

// replaceTabs replaces tabs with spaces in the lines of the hunks. It runs
// after the diff is computed, so that replacing a tab with spaces still
// counts as a change.
func replaceTabs(hunks []*hunk) {
	for _, h := range hunks {
		for i := range h.Lines {
			h.Lines[i].Content = ExpandTabs(h.Lines[i].Content)
		}
	}
}

// computeNumDigits sets the number of digits in the largest old and new
// line numbers.
func (v *View) computeNumDigits() {
	v.oldNumDigits = 0
	v.newNumDigits = 0
	for _, h := range v.hunks {
		oldNum := len(strconv.Itoa(h.OldStart + len(h.Lines)))
		newNum := len(strconv.Itoa(h.NewStart + len(h.Lines)))
		v.oldNumDigits = max(v.oldNumDigits, oldNum)
		v.newNumDigits = max(v.newNumDigits, newNum)
	}
}

// computeCodeWidth sets the code width to the width of the widest code
// line or hunk header. The width is the same in both layouts.
func (v *View) computeCodeWidth() {
	v.codeWidth = 0
	for i, h := range v.hunks {
		v.codeWidth = max(v.codeWidth, ansi.StringWidth(v.headerText(i)))
		for _, l := range h.Lines {
			w := ansi.StringWidth(Escape(strings.TrimSuffix(l.Content, "\n"))) + 1
			v.codeWidth = max(v.codeWidth, w)
		}
	}
	v.fullCodeWidth = v.codeWidth + leadingMarksWidth
}

// resizeCodeWidth sets the code width so that the lines fit in v.width.
func (v *View) resizeCodeWidth() {
	fullNumWidth := v.oldNumDigits + v.newNumDigits
	fullNumWidth += lineNumPadding * 4 // padding on both sides of two line numbers

	if v.split {
		remainingWidth := v.width - fullNumWidth - leadingMarksWidth*2
		v.codeWidth = remainingWidth / 2
		v.extraColOnNew = remainingWidth%2 != 0
	} else {
		v.codeWidth = v.width - fullNumWidth - leadingMarksWidth
	}
	v.fullCodeWidth = v.codeWidth + leadingMarksWidth
}

// Geometry describes the line-number and code columns in the output of
// [View.RenderLines]. Use it to tell whether a column is on a line number or on
// code.
type Geometry struct {
	OldNum      int // the width of the old line-number column, in cells
	NewNum      int // the width of the new line-number column, in cells
	NewNumStart int // the 0-based column where the new line numbers start
	OldCode     int // the 0-based column where the old code starts
	NewCode     int // the 0-based column where the new code starts
}

// geometry returns the positions of the line-number and code columns.
func (v *View) geometry() Geometry {
	g := Geometry{
		OldNum: v.oldNumDigits + lineNumPadding*2,
		NewNum: v.newNumDigits + lineNumPadding*2,
	}
	g.NewNumStart = g.OldNum
	if v.split {
		g.NewNumStart = g.OldNum + v.fullCodeWidth
		g.OldCode = g.OldNum + leadingMarksWidth
		g.NewCode = g.NewNumStart + g.NewNum + leadingMarksWidth
		return g
	}
	g.OldCode = g.OldNum + g.NewNum + leadingMarksWidth
	g.NewCode = g.OldCode
	return g
}
