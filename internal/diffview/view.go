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

// View draws the difference between two versions of a file.
type View struct {
	path             string
	oldText, newText string
	split            bool
	width            int
	scrollX          int
	wrap             bool
	signs            bool
	style            style
	expanded         func(Fold) bool
	cache            *Cache

	// prepare sets the fields below.
	hunks         []*hunk
	folds         []Fold // folds[i] is the fold above hunks[i]
	codeWidth     int
	fullCodeWidth int
	extraColOnNew bool // in split view, the new side is one cell wider
	oldNumDigits  int
	newNumDigits  int
}

// New returns a unified View with no width limit and no tints.
func New(path, oldText, newText string) *View {
	return &View{
		path:    path,
		oldText: oldText,
		newText: newText,
		style:   terminalStyle(nil, nil, nil, nil, true),
		cache:   &Cache{},
	}
}

// Split selects the side-by-side layout.
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

// Wrap continues long lines on the next screen lines. It makes
// [View.ScrollX] have no effect.
func (v *View) Wrap(wrap bool) *View {
	v.wrap = wrap
	return v
}

// Signs marks changed lines with + and - instead of a bar.
func (v *View) Signs(signs bool) *View {
	v.signs = signs
	return v
}

// Expanded shows the lines of each fold for which expanded returns true.
func (v *View) Expanded(expanded func(Fold) bool) *View {
	v.expanded = expanded
	return v
}

// Cache makes v use c, which must not be nil.
func (v *View) Cache(c *Cache) *View {
	v.cache = c
	return v
}

// Theme sets the colours from the terminal's colours. A nil green or red
// uses a fixed colour. A nil bg gives no tints.
func (v *View) Theme(bg, fg, green, red color.Color, dark bool) *View {
	v.style = terminalStyle(bg, fg, green, red, dark)
	return v
}

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

func (v *View) prepare() {
	v.computeDiff()
	v.computeNumDigits()
	if v.width <= 0 {
		v.computeCodeWidth()
	} else {
		v.resizeCodeWidth()
	}
}

// Lines renders screen lines lazily. It is not safe for concurrent use.
type Lines struct {
	render func(k int) (lines []string, final bool)
	start  []int
	lines  []string
	done   []bool // lines[i] is rendered for good
}

// NewLines returns n screen lines. start[k] is the first screen line of
// unit k. If render returns too few lines, the rest are blank. If final is
// false, the unit renders again on the next request.
func NewLines(n int, start []int, render func(k int) (lines []string, final bool)) *Lines {
	return &Lines{render: render, start: start, lines: make([]string, n), done: make([]bool, n)}
}

// Line returns screen line i.
func (ls *Lines) Line(i int) string {
	if ls.done[i] {
		return ls.lines[i]
	}
	// A unit with no lines starts at the same line as the next unit. So it
	// is never chosen.
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

// fit pads or cuts l like renderAll. l must contain no tabs.
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

// RenderLines returns the lazy lines, their rows and the column positions.
func (v *View) RenderLines() (*Lines, []Row, Geometry) {
	v.prepare()
	if v.width > 0 && v.codeWidth < 1 {
		// The lines can have different widths. Render all to find the
		// widest.
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

// Do not modify a foldedDiff after it is built.
type foldedDiff struct {
	gen       int // the Cache diff generation of the hunks
	hunkFolds [][]Fold
	hunks     []*hunk
	folds     []Fold

	key   itemsKey
	items []item
	rows  []Row
	start []int
}

// itemsKey holds every input, other than hunks, of items and their heights.
type itemsKey struct {
	split, wrap   bool
	codeWidth     int
	extraColOnNew bool
}

// Do not modify the result of unified, because c keeps it.
func (c *Cache) unified(oldText, newText string) (hunks []*hunk, keep [][]bool) {
	u := &c.diff
	if u.gen > 0 && u.oldText == oldText && u.newText == newText {
		return u.hunks, u.keep
	}
	u.hunks = nil
	if h := lineDiff(oldText, newText); h != nil {
		u.hunks = []*hunk{h}
	}
	// A fold hides only equal lines, so these steps give the same result
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

const crMark = "^M"

const noNewlineMark = "  \\ No newline at end of file"

// markEndings removes the CR from each line ending. If a deleted and added
// pair differs only by a CR, the line with the CR gets crMark.
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
			if i == deleted {
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

func markPair(oldLine, newLine *diffLine) {
	o, oCR := cutCR(oldLine.Content, "")
	n, nCR := cutCR(newLine.Content, "")
	if oCR != nCR && o == n {
		oldLine.Content, _ = cutCR(oldLine.Content, crMark)
		newLine.Content, _ = cutCR(newLine.Content, crMark)
	}
}

// The last line of a file can end in a CR without an LF.
func cutCR(line, mark string) (string, bool) {
	body, cr := strings.CutSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if !cr {
		return line, false
	}
	return body + mark + line[len(body)+1:], true
}

// replaceTabs runs after the diff, so a tab changed to spaces still
// counts as a change.
func replaceTabs(hunks []*hunk) {
	for _, h := range hunks {
		for i := range h.Lines {
			h.Lines[i].Content = ExpandTabs(h.Lines[i].Content)
		}
	}
}

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

func (v *View) resizeCodeWidth() {
	fullNumWidth := v.oldNumDigits + v.newNumDigits
	fullNumWidth += lineNumPadding * 4

	if v.split {
		remainingWidth := v.width - fullNumWidth - leadingMarksWidth*2
		v.codeWidth = remainingWidth / 2
		v.extraColOnNew = remainingWidth%2 != 0
	} else {
		v.codeWidth = v.width - fullNumWidth - leadingMarksWidth
	}
	v.fullCodeWidth = v.codeWidth + leadingMarksWidth
}

// Geometry describes the columns of [View.RenderLines].
type Geometry struct {
	OldNum      int // width in cells
	NewNum      int // width in cells
	NewNumStart int // 0-based column
	OldCode     int // 0-based column
	NewCode     int // 0-based column
}

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
