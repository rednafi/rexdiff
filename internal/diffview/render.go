// Derived from charmbracelet/crush (FSL-1.1-MIT). See ../DERIVED.md.

package diffview

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	leadingMarksWidth = 2   // the width of a mark, such as markAdded
	lineNumPadding    = 1   // the spaces on each side of a line number
	ellipsis          = "…" // marks where a line is cut off
)

// The marks shown before the code on each line. A changed line has a bar
// in its colour, or + or - with [View.Signs].
const (
	markNone    = "  " // an equal line, a missing line, or a continued line
	markBar     = "▎ "
	markAdded   = "+ "
	markDeleted = "- "
)

// Kind is the kind of a rendered row.
type Kind int

// The kinds of row.
const (
	KindHunk    Kind = iota // a hunk header, with no source line
	KindEqual               // a line that is the same on both sides
	KindAdded               // a line that exists only on the new side
	KindDeleted             // a line that exists only on the old side
	KindChanged             // a deleted line next to an added line, in split view
)

// Side identifies one version of a file.
type Side int

// The two sides of a file.
const (
	SideOld Side = iota // the version before the change
	SideNew             // the version after the change
)

// Row describes the source lines that one rendered line shows. Old and New
// are 1-based line numbers, or 0 if the row has no line on that side.
// Continued is true if the row continues the line above it because of
// wrapping. For a hunk header, Fold is the run of hidden equal lines above
// the header.
type Row struct {
	Kind      Kind
	Old, New  int
	Continued bool
	Fold      Fold
}

// Line returns the row's line number on the given side.
func (r Row) Line(side Side) int {
	if side == SideOld {
		return r.Old
	}
	return r.New
}

// item is one row of the diff before it is rendered to screen lines: a
// hunk header, a line in the unified layout, or a pair of lines in the
// split layout.
type item struct {
	row            Row
	header         int       // the hunk index for a header, or -1
	line           *diffLine // the line, in the unified layout
	pair           splitLine // the pair of lines, in the split layout
	oldNum, newNum int       // the line numbers of the line or pair
}

// items returns the items of the layout in order: each hunk's header,
// followed by its lines.
func (v *View) items() []item {
	n := len(v.hunks) // one header per hunk, and at most one item per line
	for _, h := range v.hunks {
		n += len(h.Lines)
	}
	out := make([]item, 0, n)
	for i, h := range v.hunks {
		out = append(out, item{row: Row{Kind: KindHunk, Fold: v.folds[i]}, header: i})
		oldNum, newNum := h.OldStart, h.NewStart
		if v.split {
			for _, l := range hunkToSplit(h) {
				it := item{row: Row{Kind: splitKind(l)}, header: -1, pair: l, oldNum: oldNum, newNum: newNum}
				if l.old != nil {
					it.row.Old = oldNum
					oldNum++
				}
				if l.new != nil {
					it.row.New = newNum
					newNum++
				}
				out = append(out, it)
			}
			continue
		}
		for k := range h.Lines {
			l := &h.Lines[k]
			it := item{header: -1, line: l, oldNum: oldNum, newNum: newNum}
			switch l.Kind {
			case KindEqual:
				it.row = Row{Kind: KindEqual, Old: oldNum, New: newNum}
				oldNum++
				newNum++
			case KindAdded:
				it.row = Row{Kind: KindAdded, New: newNum}
				newNum++
			case KindDeleted:
				it.row = Row{Kind: KindDeleted, Old: oldNum}
				oldNum++
			}
			out = append(out, it)
		}
	}
	return out
}

// itemLines renders it as screen lines.
func (v *View) itemLines(it *item) []string {
	switch {
	case it.header >= 0 && v.split:
		return []string{v.splitHeader(it.header)}
	case it.header >= 0:
		return []string{v.unifiedHeader(it.header)}
	case v.split:
		return v.splitLines(it)
	}
	return v.unifiedLines(it)
}

// unifiedLines renders the line of it in the unified layout. Each screen
// line has the old and new line numbers, followed by the code.
func (v *View) unifiedLines(it *item) []string {
	nums := func(ls lineStyle, oldNum, newNum string) string {
		return lineNum(ls, oldNum, v.oldNumDigits) +
			lineNum(ls, newNum, v.newNumDigits)
	}
	l := it.line
	ls, mark := v.kindStyle(l.Kind)
	var num string
	switch l.Kind {
	case KindEqual:
		num = nums(ls, strconv.Itoa(it.oldNum), strconv.Itoa(it.newNum))
	case KindAdded:
		num = nums(ls, " ", strconv.Itoa(it.newNum))
	case KindDeleted:
		num = nums(ls, strconv.Itoa(it.oldNum), " ")
	}
	blank := nums(ls, " ", " ")
	return v.codeLines(ls, num, blank, mark, l.Content, v.codeWidth)
}

// splitLines renders the pair of lines of it in the split layout, with the
// old line on the left and the new line on the right.
func (v *View) splitLines(it *item) []string {
	l := it.pair
	left, leftFill := v.splitSide(l.old, it.oldNum, v.oldNumDigits, v.codeWidth)
	right, rightFill := v.splitSide(l.new, it.newNum, v.newNumDigits, v.newWidth())
	n := max(len(left), len(right))
	left, right = fillTo(left, n, leftFill), fillTo(right, n, rightFill)
	lines := make([]string, n)
	for k := range n {
		lines[k] = left[k] + right[k]
	}
	return lines
}

// newWidth returns the code width of the new side in the split layout.
func (v *View) newWidth() int { return v.codeWidth + btoi(v.extraColOnNew) }

// itemHeight returns the number of screen lines that itemLines renders
// for it. It does not render lines that fit in the width.
func (v *View) itemHeight(it *item) int {
	switch {
	case it.header >= 0 || !v.wrap:
		return 1
	case v.split:
		return max(v.partCount(it.pair.old, v.codeWidth), v.partCount(it.pair.new, v.newWidth()))
	}
	return v.partCount(it.line, v.codeWidth)
}

// partCount returns the number of screen lines that line l needs when
// wrapped to width cells. A nil l needs one line.
func (v *View) partCount(l *diffLine, width int) int {
	if l == nil {
		return 1
	}
	content := strings.TrimSuffix(l.Content, "\n")
	if Fits(content, width) {
		return 1
	}
	// codeLines breaks the line where WrapParts breaks its plain text, so
	// the count needs no highlighting.
	return len(WrapParts(plainCode(content), width))
}

// plainCode returns the code line content escaped and without colours, as
// highlightCode returns it when it cannot highlight.
func plainCode(content string) string { return Escape(strings.ReplaceAll(content, "\r", CRSymbol)) }

// Fits reports whether line s, after escaping, fits in width cells both
// with and without colours. It returns false if any grapheme cluster has
// more than one rune, because colour codes between the runes can change
// its width.
func Fits(s string, width int) bool {
	width = max(width, 1)
	if len(s) <= width && isPrintableASCII(s) {
		return true
	}
	s, w := Escape(s), 0
	for s != "" {
		c, cw := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if w += cw; w > width || utf8.RuneCountInString(c) > 1 {
			return false
		}
		s = s[len(c):]
	}
	return true
}

// isPrintableASCII reports whether s contains only printable ASCII
// characters. Each byte of such a string is one cell wide, and [Escape]
// leaves it unchanged.
func isPrintableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// unifiedHeader renders the divider line of hunk i in the unified layout.
func (v *View) unifiedHeader(i int) string {
	div := v.style.dividerLine
	header := ansi.Truncate(v.headerText(i), v.fullCodeWidth, ellipsis)
	return div.num.Render(pad(ellipsis, v.oldNumDigits)) +
		div.num.Render(pad(ellipsis, v.newNumDigits)) +
		div.code.Width(v.fullCodeWidth).Render(header)
}

// splitHeader renders the divider line of hunk i in the split layout. The
// header starts on the left side and can extend across the right side.
func (v *View) splitHeader(i int) string {
	div := v.style.dividerLine
	// The header spans both code columns and the new line numbers.
	width := 2*v.fullCodeWidth + btoi(v.extraColOnNew) + v.newNumDigits + lineNumPadding*2
	return div.num.Render(pad(ellipsis, v.oldNumDigits)) +
		div.code.Width(width).Render(ansi.Truncate(v.headerText(i), width, ellipsis))
}

// splitSide renders line l, with line number n, on one side of the split
// layout, returning one string per screen line. A nil l means the line is
// missing on this side. fill renders an empty screen line, used when the
// other side has more lines.
func (v *View) splitSide(l *diffLine, n, digits, width int) (lines []string, fill func() string) {
	ls, mark, content := v.style.missingLine, markNone, ""
	if l != nil {
		ls, mark = v.kindStyle(l.Kind)
		content = l.Content
	}
	noNum := lineNum(ls, " ", digits)
	num := noNum
	if l != nil {
		num = lineNum(ls, strconv.Itoa(n), digits)
	}
	fill = func() string { return v.codeLines(ls, noNum, "", markNone, "", width)[0] }
	return v.codeLines(ls, num, noNum, mark, content, width), fill
}

// splitKind returns the row kind of l in the split layout.
func splitKind(l splitLine) Kind {
	switch {
	case l.old != nil && l.new != nil && l.old.Kind == KindEqual:
		return KindEqual
	case l.old != nil && l.new != nil:
		return KindChanged
	case l.old != nil:
		return KindDeleted
	}
	return KindAdded
}

// fillTo appends lines rendered by fill until lines has n lines.
func fillTo(lines []string, n int, fill func() string) []string {
	if len(lines) >= n {
		return lines
	}
	return append(lines, slices.Repeat([]string{fill()}, n-len(lines))...)
}

// kindStyle returns the style and mark for a line of kind k.
func (v *View) kindStyle(k Kind) (lineStyle, string) {
	switch k {
	case KindAdded:
		return v.style.addedLine, v.mark(markAdded)
	case KindDeleted:
		return v.style.deletedLine, v.mark(markDeleted)
	}
	return v.style.equalLine, markNone
}

// mark returns sign if v shows signs, and the bar otherwise.
func (v *View) mark(sign string) string {
	if v.signs {
		return sign
	}
	return markBar
}

// lineNum renders the line number n in a column that is digits cells wide.
func lineNum(ls lineStyle, n string, digits int) string {
	return ls.num.Render(pad(n, digits))
}

// codeLines renders one source line as nums, then mark, then the code in
// width cells. Without wrapping, it cuts off a long line. With wrapping,
// the line continues on more screen lines, which show blank instead of
// nums and no mark.
func (v *View) codeLines(ls lineStyle, nums, blank, mark, content string, width int) []string {
	content = strings.TrimSuffix(content, "\n")
	code := v.highlightCode(content, ls.code.GetBackground())
	parts := []string{code}
	if v.wrap {
		if !Fits(content, width) {
			parts = WrapLine(plainCode(content), code, width)
		}
		w := max(width, 1)
		for i, p := range parts {
			// A single wide character can be wider than w.
			parts[i] = ansi.Truncate(p, w, "")
		}
	} else {
		parts[0] = CutLeft(code, v.scrollX)
		if v.width > 0 {
			parts[0] = ansi.Truncate(parts[0], width, ellipsis)
		}
		if v.scrollX > 0 && strings.TrimSpace(parts[0]) != "" {
			mark = strings.TrimSuffix(mark, " ") + ellipsis
		}
	}
	full := lipgloss.NewStyle().MaxWidth(width + leadingMarksWidth)
	lines := make([]string, len(parts))
	for i, p := range parts {
		if i > 0 {
			nums, mark = blank, markNone
		}
		if v.width <= 0 && !v.wrap {
			// With no width limit nothing is cut. Colours can make a
			// cluster of runes measure wider than its plain text, so the
			// line is padded, not cut, to width.
			pad := strings.Repeat(" ", max(width-ansi.StringWidth(p), 0))
			lines[i] = nums + ls.mark.Render(mark) + ls.code.Render(p+pad)
			continue
		}
		lines[i] = nums + full.Render(ls.mark.Render(mark)+ls.code.Width(width).Render(p))
	}
	return lines
}

// WrapLine wraps a line to width cells and returns the screen lines.
// plain is the text of the line, escaped, and code is the same text with
// colours. The lines break where [WrapParts] breaks plain, so their number
// does not depend on the colours, which can change the width of a
// grapheme cluster of several runes. If code does not hold the text of
// plain with only escape sequences added, the lines have no colours.
func WrapLine(plain, code string, width int) []string {
	parts := WrapParts(plain, width)
	if code != plain {
		if coloured, ok := colourParts(plain, code, parts); ok {
			return coloured
		}
	}
	return parts
}

// colourParts returns parts, the screen lines that wrapping took from
// plain in order, with the colours that code gives their text. Each line
// starts with the escape sequences in effect at its first character, and
// ends with a reset if any are in effect at its end. ok is false if code
// is not plain with escape sequences added.
func colourParts(plain, code string, parts []string) (out []string, ok bool) {
	pos, i := 0, 0 // the bytes of plain and code read so far
	var sgr string // the escape sequences since the last reset
	// escapes reads the escape sequences at code[i:], and writes them to b
	// if b is not nil.
	escapes := func(b *strings.Builder) {
		for i < len(code) && code[i] == ansi.ESC {
			seq, _, n, _ := ansi.DecodeSequence(code[i:], ansi.NormalState, nil)
			if seq == "\x1b[m" || seq == "\x1b[0m" {
				sgr = ""
			} else {
				sgr += seq
			}
			if b != nil {
				b.WriteString(seq)
			}
			i += n
		}
	}
	// text reads the next byte of plain from code, writing it to b if b
	// is not nil. It reports whether code has that byte there.
	text := func(b *strings.Builder) bool {
		escapes(b)
		if i >= len(code) || code[i] != plain[pos] {
			return false
		}
		if b != nil {
			b.WriteByte(code[i])
		}
		i, pos = i+1, pos+1
		return true
	}
	out = make([]string, len(parts))
	for k, part := range parts {
		start := strings.Index(plain[pos:], part)
		if start < 0 {
			return nil, false
		}
		for end := pos + start; pos < end; { // the text that wrapping dropped
			if !text(nil) {
				return nil, false
			}
		}
		escapes(nil)
		var b strings.Builder
		b.WriteString(sgr)
		for end := pos + len(part); pos < end; {
			if !text(&b) {
				return nil, false
			}
		}
		if sgr != "" {
			b.WriteString("\x1b[m")
		}
		out[k] = b.String()
	}
	return out, true
}

// WrapParts wraps the highlighted code to width cells and returns the
// screen lines.
func WrapParts(code string, width int) []string {
	// lipgloss.Wrap can leave a leading space on a full line, so hard-wrap
	// the result again.
	w := max(width, 1)
	return strings.Split(ansi.Hardwrap(lipgloss.Wrap(code, w, ""), w, true), "\n")
}

// headerText returns the header text of hunk i, for example
// "  ⋯ 12 unmodified lines  @@ -15,7 +15,8 @@ ". It includes the size of
// the fold above the hunk if the fold hides lines, and the hunk range if
// the hunk has lines. It ends with a space.
func (v *View) headerText(i int) string {
	h, s := v.hunks[i], ""
	if fold := v.folds[i]; fold.Lines > 0 {
		s = fmt.Sprintf("  ⋯ %d unmodified lines", fold.Lines)
	}
	if len(h.Lines) == 0 {
		return s + " "
	}
	return s + hunkHeader(h)
}

// hunkHeader returns the range header of hunk h, for example
// "  @@ -1,3 +1,4 @@ ".
func hunkHeader(h *hunk) string {
	oldCount, newCount := hunkLineCounts(h)
	return fmt.Sprintf("  @@ -%d,%d +%d,%d @@ ", h.OldStart, oldCount, h.NewStart, newCount)
}

// hunkLineCounts returns the number of old and new lines in hunk h.
func hunkLineCounts(h *hunk) (oldCount, newCount int) {
	for _, l := range h.Lines {
		oldCount, newCount = l.next(oldCount, newCount)
	}
	return oldCount, newCount
}
