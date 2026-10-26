// Package diffview draws a diff in the terminal. The code comes from
// charmbracelet/crush, with changes for rexdiff. See ../VENDORED.md.
package diffview

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

const (
	leadingSymbolsSize = 2   // the width of a symbol, such as symInsert
	lineNumPadding     = 1   // the space on each side of a line number
	ellipsis           = "…" // shows where a line is cut
)

// TabWidth is the number of spaces that replace a tab.
const TabWidth = 4

// These symbols are at the start of the code of a line. A bar in the colour
// of the line marks each changed line. [View.Signs] shows + and - in place
// of the bar.
const (
	symNone   = "  " // an equal line, a missing line, or a continued line
	symBar    = "▌ "
	symInsert = "+ "
	symDelete = "- "
)

// file is a path and its content.
type file struct {
	path    string
	content string
}

// layout is the unified or the split layout.
type layout int

const (
	layoutUnified layout = iota + 1
	layoutSplit
)

// View draws the difference between two versions of a file.
type View struct {
	layout  layout
	before  file
	after   file
	width   int
	xOffset int
	style   Style
	wrap    bool // wrap long lines instead of cutting them
	signs   bool // mark a changed line with + or -, not with a bar

	isComputed bool
	err        error
	unified    udiff.UnifiedDiff
	folds      []Fold          // folds[i] hides above unified.Hunks[i]
	expanded   func(Fold) bool // the folds that show, see [View.Expand]

	splitHunks []splitHunk

	codeWidth       int
	fullCodeWidth   int  // with the leading symbols
	extraColOnAfter bool // the new side of the split layout is one cell wider
	beforeNumDigits int
	afterNumDigits  int
	heights         []int // the number of screen lines of each row, for rows

	cache *Cache
}

// Cache keeps highlighted lines, so that chroma highlights each line once.
// Views that share a Cache reuse its lines and its last lexer. The zero
// value is an empty Cache. A Cache is not safe for concurrent use.
type Cache struct {
	lines map[syntaxKey]string
	size  int // the bytes of the lines and their sources

	// lexer keeps the last lexer, because to select a lexer from the
	// content is slow.
	lexer struct {
		path, content string
		l             chroma.Lexer
	}
}

// maxCacheSize limits the bytes of the lines and the sources in a Cache.
// If a put goes above this size, put empties the Cache first.
const maxCacheSize = 32 << 20

// put keeps the highlighted line s for key k.
func (c *Cache) put(k syntaxKey, s string) {
	// k.source is a part of the file string. A copy lets the cache keep
	// the line without the whole file.
	k.source = strings.Clone(k.source)
	n := len(k.source) + len(s)
	if c.lines == nil || c.size+n > maxCacheSize {
		c.lines, c.size = make(map[syntaxKey]string), 0
	}
	c.lines[k] = s
	c.size += n
}

// New returns a View with the unified layout.
func New() *View {
	return &View{layout: layoutUnified, cache: &Cache{}}
}

// Cache makes v keep its highlighted lines in c. A nil c gives v a new
// Cache.
func (v *View) Cache(c *Cache) *View {
	if c == nil {
		c = &Cache{}
	}
	v.cache = c
	return v
}

// Split sets the split layout. The old lines are on the left and the new
// lines are on the right.
func (v *View) Split() *View {
	v.layout = layoutSplit
	return v
}

// Before sets the old version of the file.
func (v *View) Before(path, content string) *View {
	v.before = file{path: path, content: content}
	v.clearCaches()
	return v
}

// After sets the new version of the file.
func (v *View) After(path, content string) *View {
	v.after = file{path: path, content: content}
	v.clearCaches()
	return v
}

// clearCaches clears the diff. Call it when the content changes.
func (v *View) clearCaches() {
	v.isComputed = false
}

// Style sets the colours of the lines.
func (v *View) Style(style Style) *View {
	v.style = style
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

// Render returns the rendered diff, one row for each of its lines, and the
// columns of the line numbers.
func (v *View) Render() (string, []Row, Geometry) {
	out := v.text()
	return out, v.rows(), v.geometry()
}

// text returns the rendered diff. It also sets the fields that rows and
// geometry use.
func (v *View) text() string {
	v.heights = v.heights[:0]
	v.normalizeLineEndings()
	if err := v.computeDiff(); err != nil {
		return err.Error()
	}
	v.replaceTabs()
	v.convertDiffToSplit()
	v.adjustStyles()
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
	if v.layout == layoutSplit {
		return style.Render(strings.TrimSuffix(v.renderSplit(), "\n"))
	}
	return style.Render(strings.TrimSuffix(v.renderUnified(), "\n"))
}

// normalizeLineEndings changes CRLF line endings to LF.
func (v *View) normalizeLineEndings() {
	v.before.content = strings.ReplaceAll(v.before.content, "\r\n", "\n")
	v.after.content = strings.ReplaceAll(v.after.content, "\r\n", "\n")
}

// replaceTabs replaces the tabs in the lines of the diff with spaces. It runs
// after computeDiff, so that a change from a tab to spaces is a change.
func (v *View) replaceTabs() {
	spaces := strings.Repeat(" ", TabWidth)
	for _, h := range v.unified.Hunks {
		for i := range h.Lines {
			h.Lines[i].Content = strings.ReplaceAll(h.Lines[i].Content, "\t", spaces)
		}
	}
}

// computeDiff computes the diff of the two versions once. A later call
// returns the error of the first call.
func (v *View) computeDiff() error {
	if v.isComputed {
		return v.err
	}
	v.isComputed = true
	edits := udiff.Lines(v.before.content, v.after.content)
	// With a context as long as the file, the diff has one hunk with all
	// the lines. foldHunk then hides the long runs of equal lines.
	context := strings.Count(v.before.content, "\n") + 1
	v.unified, v.err = udiff.ToUnifiedDiff(
		v.before.path,
		v.after.path,
		v.before.content,
		edits,
		context,
	)
	hunks := v.unified.Hunks
	v.unified.Hunks, v.folds = nil, nil
	for _, h := range hunks {
		hs, fs := foldHunk(h, v.expanded)
		v.unified.Hunks = append(v.unified.Hunks, hs...)
		v.folds = append(v.folds, fs...)
	}
	return v.err
}

// convertDiffToSplit makes the split hunks, if the layout is split.
func (v *View) convertDiffToSplit() {
	if v.layout != layoutSplit {
		return
	}

	v.splitHunks = make([]splitHunk, len(v.unified.Hunks))
	for i, h := range v.unified.Hunks {
		v.splitHunks[i] = hunkToSplit(h)
	}
}

// adjustStyles adds padding and alignment to the line number styles.
func (v *View) adjustStyles() {
	setPadding := func(s lipgloss.Style) lipgloss.Style {
		return s.Padding(0, lineNumPadding).Align(lipgloss.Right)
	}
	v.style.missingLine.lineNumber = setPadding(v.style.missingLine.lineNumber)
	v.style.dividerLine.lineNumber = setPadding(v.style.dividerLine.lineNumber)
	v.style.equalLine.lineNumber = setPadding(v.style.equalLine.lineNumber)
	v.style.insertLine.lineNumber = setPadding(v.style.insertLine.lineNumber)
	v.style.deleteLine.lineNumber = setPadding(v.style.deleteLine.lineNumber)
}

// detectNumDigits finds the number of digits of the largest old and new line
// numbers.
func (v *View) detectNumDigits() {
	v.beforeNumDigits = 0
	v.afterNumDigits = 0

	for _, h := range v.unified.Hunks {
		before := len(strconv.Itoa(h.FromLine + len(h.Lines)))
		after := len(strconv.Itoa(h.ToLine + len(h.Lines)))
		v.beforeNumDigits = max(v.beforeNumDigits, before)
		v.afterNumDigits = max(v.afterNumDigits, after)
	}
}

// detectCodeWidth finds the width of the widest code line.
func (v *View) detectCodeWidth() {
	switch v.layout {
	case layoutUnified:
		v.detectUnifiedCodeWidth()
	case layoutSplit:
		v.detectSplitCodeWidth()
	}
	v.fullCodeWidth = v.codeWidth + leadingSymbolsSize
}

// codeWidthOf returns the width of the code of l, plus one cell.
func codeWidthOf(l *udiff.Line) int {
	return ansi.StringWidth(Escape(strings.TrimSuffix(l.Content, "\n"))) + 1
}

// detectUnifiedCodeWidth finds the width of the widest code line or hunk
// header in the unified layout.
func (v *View) detectUnifiedCodeWidth() {
	v.codeWidth = 0
	for i, h := range v.unified.Hunks {
		v.codeWidth = max(v.codeWidth, ansi.StringWidth(v.headerText(i)))
		for _, l := range h.Lines {
			v.codeWidth = max(v.codeWidth, codeWidthOf(&l))
		}
	}
}

// detectSplitCodeWidth finds the width of the widest code line or hunk
// header in the split layout.
func (v *View) detectSplitCodeWidth() {
	v.codeWidth = 0
	for i, h := range v.splitHunks {
		v.codeWidth = max(v.codeWidth, ansi.StringWidth(v.headerText(i)))
		for _, l := range h.lines {
			if l.before != nil {
				v.codeWidth = max(v.codeWidth, codeWidthOf(l.before))
			}
			if l.after != nil {
				v.codeWidth = max(v.codeWidth, codeWidthOf(l.after))
			}
		}
	}
}

// resizeCodeWidth sets the code width so that the lines fit in the width.
func (v *View) resizeCodeWidth() {
	fullNumWidth := v.beforeNumDigits + v.afterNumDigits
	fullNumWidth += lineNumPadding * 4 // two sides of two line numbers

	switch v.layout {
	case layoutUnified:
		v.codeWidth = v.width - fullNumWidth - leadingSymbolsSize
	case layoutSplit:
		remainingWidth := v.width - fullNumWidth - leadingSymbolsSize*2
		v.codeWidth = remainingWidth / 2
		v.extraColOnAfter = remainingWidth%2 != 0
	}

	v.fullCodeWidth = v.codeWidth + leadingSymbolsSize
}

// renderUnified renders the unified layout. Each line has the old and the
// new line numbers, then the code.
func (v *View) renderUnified() string {
	var b strings.Builder
	nums := func(ls lineStyle, before, after any) string {
		return v.lineNum(ls, before, v.beforeNumDigits) +
			v.lineNum(ls, after, v.afterNumDigits)
	}
	for i, h := range v.unified.Hunks {
		b.WriteString(v.unifiedHeader(i) + "\n")
		oldN, newN := h.FromLine, h.ToLine
		for _, l := range h.Lines {
			ls, sym := v.kindStyle(l.Kind)
			var num string
			switch l.Kind {
			case udiff.Equal:
				num = nums(ls, oldN, newN)
				oldN++
				newN++
			case udiff.Insert:
				num = nums(ls, " ", newN)
				newN++
			case udiff.Delete:
				num = nums(ls, oldN, " ")
				oldN++
			}
			blank := nums(ls, " ", " ")
			v.writeRow(&b, v.codeLines(ls, num, blank, sym, l.Content, v.codeWidth))
		}
	}
	return b.String()
}

// unifiedHeader renders the divider line of hunk i in the unified layout.
func (v *View) unifiedHeader(i int) string {
	div := v.style.dividerLine
	header := ansi.Truncate(v.headerText(i), v.fullCodeWidth, ellipsis)
	return div.lineNumber.Render(pad(ellipsis, v.beforeNumDigits)) +
		div.lineNumber.Render(pad(ellipsis, v.afterNumDigits)) +
		div.code.Width(v.fullCodeWidth).Render(header)
}

// renderSplit renders the split layout. The old lines are on the left and
// the new lines are on the right.
func (v *View) renderSplit() string {
	var b strings.Builder
	afterWidth := v.codeWidth + btoi(v.extraColOnAfter)
	for i, h := range v.splitHunks {
		b.WriteString(v.splitHeader(i) + "\n")
		oldN, newN := h.fromLine, h.toLine
		for _, l := range h.lines {
			left, leftFill := v.splitSide(l.before, oldN, v.beforeNumDigits, v.codeWidth)
			right, rightFill := v.splitSide(l.after, newN, v.afterNumDigits, afterWidth)
			if l.before != nil {
				oldN++
			}
			if l.after != nil {
				newN++
			}
			n := max(len(left), len(right))
			left, right = fillTo(left, n, leftFill), fillTo(right, n, rightFill)
			lines := make([]string, n)
			for k := range n {
				lines[k] = left[k] + right[k]
			}
			v.writeRow(&b, lines)
		}
	}
	return b.String()
}

// splitHeader renders the divider line of hunk i in the split layout. The
// header starts on the left side and can go on across the right side.
func (v *View) splitHeader(i int) string {
	div := v.style.dividerLine
	// The header covers the two code columns and the new line numbers.
	width := 2*v.fullCodeWidth + btoi(v.extraColOnAfter) + v.afterNumDigits + lineNumPadding*2
	return div.lineNumber.Render(pad(ellipsis, v.beforeNumDigits)) +
		div.code.Width(width).Render(ansi.Truncate(v.headerText(i), width, ellipsis))
}

// splitSide renders line l on one side of the split layout, one string for
// each screen line. l is nil if that side has no line. n is the line number
// of l. The fill function renders an empty screen line. Use fill when the
// other side has more screen lines.
func (v *View) splitSide(l *udiff.Line, n, digits, width int) (lines []string, fill func() string) {
	ls, sym, content := v.style.missingLine, symNone, ""
	if l != nil {
		ls, sym = v.kindStyle(l.Kind)
		content = l.Content
	}
	noNum := v.lineNum(ls, " ", digits)
	num := noNum
	if l != nil {
		num = v.lineNum(ls, n, digits)
	}
	fill = func() string { return v.codeLines(ls, noNum, "", symNone, "", width)[0] }
	return v.codeLines(ls, num, noNum, sym, content, width), fill
}

// fillTo adds the line that fill renders to lines, until lines has n lines.
func fillTo(lines []string, n int, fill func() string) []string {
	if len(lines) >= n {
		return lines
	}
	return append(lines, slices.Repeat([]string{fill()}, n-len(lines))...)
}

// kindStyle returns the style and the symbol of a line of kind k.
func (v *View) kindStyle(k udiff.OpKind) (lineStyle, string) {
	switch k {
	case udiff.Insert:
		return v.style.insertLine, v.mark(symInsert)
	case udiff.Delete:
		return v.style.deleteLine, v.mark(symDelete)
	}
	return v.style.equalLine, symNone
}

// mark returns sign if the View shows signs, else the bar.
func (v *View) mark(sign string) string {
	if v.signs {
		return sign
	}
	return symBar
}

// writeRow writes the screen lines of one row to b, and keeps their number
// for rows.
func (v *View) writeRow(b *strings.Builder, lines []string) {
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	v.heights = append(v.heights, len(lines))
}

// lineNum renders the line number n in a column of digits cells.
func (v *View) lineNum(ls lineStyle, n any, digits int) string {
	return ls.lineNumber.Render(pad(n, digits))
}

// codeLines renders one source line with nums, sym, and the code in width
// cells. It cuts a long line. With wrap, the line continues on more screen
// lines, which show blank in place of nums and no sym.
func (v *View) codeLines(ls lineStyle, nums, blank, sym, content string, width int) []string {
	code := v.highlightCode(strings.TrimSuffix(content, "\n"), ls.code.GetBackground())
	parts := []string{code}
	if v.wrap {
		// Wrap can keep a leading space on a full line, so cut again.
		w := max(width, 1)
		parts = strings.Split(ansi.Hardwrap(lipgloss.Wrap(code, w, ""), w, true), "\n")
		for i, p := range parts {
			// A wide character can be wider than w.
			parts[i] = ansi.Truncate(p, w, "")
		}
	} else {
		code = CutLeft(code, v.xOffset)
		parts[0] = ansi.Truncate(code, width, ellipsis)
		if v.xOffset > 0 && strings.TrimSpace(parts[0]) != "" {
			sym = strings.TrimSuffix(sym, " ") + ellipsis
		}
	}
	full := lipgloss.NewStyle().MaxWidth(width + leadingSymbolsSize)
	lines := make([]string, len(parts))
	for i, p := range parts {
		if i > 0 {
			nums, sym = blank, symNone
		}
		lines[i] = nums + full.Render(ls.symbol.Render(sym)+ls.code.Width(width).Render(p))
	}
	return lines
}

// hunkHeader returns the header line of hunk h, for example
// "  @@ -1,3 +1,4 @@ ".
func hunkHeader(h *udiff.Hunk) string {
	before, after := hunkLineCounts(h)
	return fmt.Sprintf("  @@ -%d,%d +%d,%d @@ ", h.FromLine, before, h.ToLine, after)
}

// hunkLineCounts returns the number of old and new lines in hunk h.
func hunkLineCounts(h *udiff.Hunk) (before, after int) {
	for _, l := range h.Lines {
		switch l.Kind {
		case udiff.Equal:
			before++
			after++
		case udiff.Insert:
			after++
		case udiff.Delete:
			before++
		}
	}
	return before, after
}

// highlightCode returns source with syntax colours on the background
// bgColor. The cache keeps the result. If chroma fails, highlightCode
// returns source as it is.
func (v *View) highlightCode(source string, bgColor color.Color) string {
	// Chroma makes a lone CR a line break. Thus show each CR as a symbol
	// first.
	source = strings.ReplaceAll(source, "\r", "\u240d")
	lexer := v.chromaLexer()
	key := newSyntaxKey(lexer.Config().Name, source, bgColor)
	if cached, ok := v.cache.lines[key]; ok {
		return cached
	}
	it, err := lexer.Tokenise(nil, source)
	if err != nil {
		return source
	}
	var b strings.Builder
	f := ANSIFormatter(bgColor, processChromaValue)
	if err := f.Format(&b, nil, it); err != nil {
		return source
	}
	out := b.String()
	v.cache.put(key, out)
	return out
}

// syntaxKey is the key of a [Cache]. It has the lexer, the source, and the
// background colour of the source. noColor is true for no background, because
// lipgloss.NoColor has the same RGBA value as black.
type syntaxKey struct {
	lexer, source string
	noColor       bool
	r, g, b, a    uint32
}

// newSyntaxKey returns the key of source for the lexer named lexer, on
// bgColor.
func newSyntaxKey(lexer, source string, bgColor color.Color) syntaxKey {
	k := syntaxKey{lexer: lexer, source: source}
	if _, none := bgColor.(lipgloss.NoColor); bgColor == nil || none {
		k.noColor = true
		return k
	}
	k.r, k.g, k.b, k.a = bgColor.RGBA()
	return k
}

// chromaLexer returns the lexer of the old file, and keeps it in the
// cache.
func (v *View) chromaLexer() chroma.Lexer {
	lx := &v.cache.lexer
	if lx.l == nil || lx.path != v.before.path || lx.content != v.before.content {
		lx.path, lx.content = v.before.path, v.before.content
		lx.l = Lexer(lx.path, lx.content)
	}
	return lx.l
}

// Lexer returns the coalesced lexer for the file at path. If no lexer
// matches the name, it selects one from the content. If no lexer matches
// the content, it returns the fallback lexer.
func Lexer(path, content string) chroma.Lexer {
	// matchLexer caches the lexer, and the lexer is already coalesced.
	if l := matchLexer(path); l != nil {
		return l
	}
	l := lexers.Analyse(content)
	if l == nil {
		l = lexers.Fallback
	}
	return chroma.Coalesce(l)
}

// processChromaValue escapes the text of a token for the terminal. A code
// line has no line break. Thus processChromaValue removes the trailing line
// breaks.
func processChromaValue(value string) string {
	return Escape(strings.TrimRight(value, "\n"))
}
