package tui

import (
	"cmp"
	"fmt"
	"iter"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark/util"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/vcs"
)

// markdownRenderer caches the last renderer, because creating one is
// slow. It returns nil on failure.
func (m *model) markdownRenderer(wrap int) *glamour.TermRenderer {
	style := "dark"
	if !m.dark() {
		style = "light"
	}
	renderer := &m.markdown
	if renderer.r != nil && renderer.style == style && renderer.wrap == wrap {
		return renderer.r
	}
	r, err := newMarkdownRenderer(style, wrap)
	if err != nil {
		return nil
	}
	*renderer = markdownCache{r: r, style: style, wrap: wrap}
	return r
}

// blockSourcesOf caches blockSources for the last src.
func (m *model) blockSourcesOf(src string) (spans [][2]int, srcs []string, work int) {
	c := &m.markdown.sources
	if !c.done || c.src != src {
		c.spans, c.srcs, c.work = blockSources(diff.SplitLines(src))
		c.src, c.done = src, true
	}
	return c.spans, c.srcs, c.work
}

// hasMarkdownText returns false if src contains "<", because HTML can
// span or hide blocks. The caller must then render all of src.
func hasMarkdownText(blocks [][]string, src string) bool {
	if strings.Contains(src, "<") && len(src) <= maxMarkdownBlockBytes {
		return false
	}
	for _, b := range blocks {
		if slices.ContainsFunc(b, func(l string) bool { return !blankLine(l) }) {
			return true
		}
	}
	return false
}

func blankLine(l string) bool {
	return strings.TrimSpace(ansi.Strip(l)) == ""
}

func newMarkdownRenderer(style string, wrap int) (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(wrap),
	)
}

type markdownCache struct {
	r     *glamour.TermRenderer
	style string
	wrap  int

	more    []*glamour.TermRenderer // set up like r, one per extra goroutine
	blocks  map[string][]string     // keyed by source
	pr      map[string][]string     // keyed by source
	nothing struct {
		src  string
		done bool
		is   bool
	}
	sources struct {
		src   string
		done  bool
		spans [][2]int
		srcs  []string
		work  int
	}
}

const minParallelBlocks = 32

// renderBlocks caches only the blocks of the previous call.
func (m *model) renderBlocks(r *glamour.TermRenderer, srcs []string) [][]string {
	mc := &m.markdown
	out := make([][]string, len(srcs))
	var todo []int
	for i, src := range srcs {
		if lines, ok := mc.blocks[src]; ok && mc.r == r && r != nil {
			out[i] = lines
		} else {
			todo = append(todo, i)
		}
	}
	workers := min(runtime.GOMAXPROCS(0), len(todo)/minParallelBlocks+1)
	if r == nil || mc.r != r {
		workers = 1
	}
	// Each goroutine needs its own renderer.
	rs := []*glamour.TermRenderer{r}
	for len(mc.more) < workers-1 {
		more, err := newMarkdownRenderer(mc.style, mc.wrap)
		if err != nil {
			break
		}
		mc.more = append(mc.more, more)
	}
	rs = append(rs, mc.more[:min(len(mc.more), workers-1)]...)
	var next atomic.Int64
	var wg sync.WaitGroup
	for _, wr := range rs {
		wg.Go(func() {
			for k := int(next.Add(1)) - 1; k < len(todo); k = int(next.Add(1)) - 1 {
				out[todo[k]] = renderBlock(wr, srcs[todo[k]], mc.wrap)
			}
		})
	}
	wg.Wait()
	if r != nil && mc.r == r {
		mc.blocks = make(map[string][]string, len(srcs))
		for i, src := range srcs {
			mc.blocks[src] = out[i]
		}
	}
	return out
}

// Limits on the Markdown that rexdiff renders. Glamour time and memory
// grow with markdownWork, and one slow render blocks the screen. A file
// past a file limit shows as its source diff. A block past a block limit
// shows as source. Glamour is very slow when the indent is wider than the
// pane, so maxMarkdownDepth limits nesting.
const (
	maxMarkdownBytes      = 512 << 10
	maxMarkdownWork       = 2 << 20
	maxMarkdownBlockBytes = 64 << 10
	maxMarkdownBlockWork  = 128 << 10
	maxMarkdownDepth      = 12
	maxLinkDefBytes       = 2 << 10
)

const (
	noteMarkdownTooBig = "markdown too large to render"
	noteBlockTooBig    = "  block too large to render · source below"
	noteBlockTooDeep   = "  block nested too deeply to render · source below"
)

// renderableMarkdown uses the blockSourcesOf cache, because the title
// asks on every draw.
func (m *model) renderableMarkdown(src string) bool {
	if len(src) > maxMarkdownBytes {
		return false
	}
	_, _, work := m.blockSourcesOf(src)
	return work <= maxMarkdownWork
}

// blockNote returns "" if the block can render. work is 0 if it cannot.
func blockNote(src string) (note string, work int) {
	if len(src) > maxMarkdownBlockBytes {
		return noteBlockTooBig, 0
	}
	work, depth := markdownWork(src)
	if depth > maxMarkdownDepth {
		return noteBlockTooDeep, 0
	}
	if work > maxMarkdownBlockWork {
		return noteBlockTooBig, 0
	}
	return "", work
}

func renderBlock(r *glamour.TermRenderer, src string, width int) []string {
	if note, _ := blockNote(src); note != "" {
		return sourceLines(note, src, width)
	}
	return renderMarkdown(r, src)
}

const sourceMargin = "  "

func sourceLines(note, src string, width int) []string {
	room := max(width-len(sourceMargin), 1)
	var out []string
	for l := range strings.SplitSeq(wrapWords(note, room), "\n") {
		out = append(out, sourceMargin+dimStyle.Render(l))
	}
	for l := range strings.SplitSeq(termtext.ExpandTabs(strings.TrimRight(src, "\n")), "\n") {
		for part := range strings.SplitSeq(ansi.Hardwrap(l, room, true), "\n") {
			out = append(out, sourceMargin+part)
		}
	}
	return out
}

// markdownWork estimates glamour's work and returns the deepest nesting.
// Glamour time grows with the square of the depth. The output repeats a
// link definition at each reference.
func markdownWork(src string) (work, depth int) {
	var ds depthScanner
	for l := range strings.Lines(src) {
		n := ds.next(l)
		depth = max(depth, n)
		d := max(n, 1)
		work += len(l) * d * d
	}
	defs := linkDefs(strings.Split(src, "\n"), len(src))
	if len(defs) == 0 {
		return work, depth
	}
	size := make(map[string]int, len(defs))
	for label, lines := range defs {
		for _, l := range lines {
			size[label] += len(l)
		}
	}
	for _, m := range linkLabel.FindAllStringSubmatch(src, -1) {
		work += size[util.ToLinkReference([]byte(m[1]))]
	}
	return work, depth
}

// depthScanner counts how deeply each line of a Markdown block can nest.
// The count is never less than the real depth, so it is safe as a limit.
// It does not track when a list item ends, so it can only count too many.
type depthScanner struct {
	items []bool // items[c]: a list item's text starts at column c
	// fence opened the current code block, or is "" outside one.
	fence    string
	fenceCol int
}

func (ds *depthScanner) next(l string) int {
	depth, col := 0, 0
	gapStart := 0
	gap := func() {
		for c := gapStart + 1; c <= col && c < len(ds.items); c++ {
			if ds.items[c] {
				depth++
			}
		}
		gapStart = col
	}
	marker := func(i, width int) int {
		gap()
		depth++
		col += width
		if i < len(l) && l[i] == ' ' {
			i++
			col++
		}
		gapStart = col
		return i
	}
	inCode := ds.fence != ""
	i := 0
	for i < len(l) {
		if inCode && col >= ds.fenceCol {
			// The rest of the line is code, unless it closes the block.
			if strings.HasPrefix(strings.TrimLeft(l[i:], " \t"), ds.fence) {
				ds.fence = ""
			}
			col = ds.fenceCol
			gap()
			return depth
		}
		c := l[i]
		if c == ' ' || c == '\t' {
			col++
			if c == '\t' {
				col += termtext.TabWidth - 1
			}
			i++
			continue
		}
		if c == '>' {
			i = marker(i+1, 1)
			continue
		}
		if n := listMarker(l[i:]); n > 0 {
			// A list marker left of the fence ends the code block.
			inCode, ds.fence = false, ""
			i = marker(i+n, n)
			ds.addItem(col)
			continue
		}
		break
	}
	gap()
	if inCode && strings.TrimSpace(l[i:]) != "" {
		ds.fence = ""
	}
	if ds.fence == "" {
		ds.openFence(l[i:], col)
	}
	return depth
}

func (ds *depthScanner) addItem(col int) {
	if col >= len(ds.items) {
		ds.items = append(ds.items, make([]bool, col+1-len(ds.items))...)
	}
	ds.items[col] = true
}

// openFence treats a fence indented four or more columns past the nearest
// list item text as code, not a fence.
func (ds *depthScanner) openFence(rest string, col int) {
	t := strings.TrimLeft(rest, " \t")
	indent := col + len(rest) - len(t) // tabs inside the fence line are rare, so count them as one
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return
	}
	if t[0] == '`' && strings.Contains(strings.TrimLeft(t, "`"), "`") {
		return // a backtick fence has no backtick in its info string
	}
	base := 0
	for c := min(indent, len(ds.items)-1); c > 0; c-- {
		if ds.items[c] {
			base = c
			break
		}
	}
	if indent-base >= 4 {
		return
	}
	n := len(t) - len(strings.TrimLeft(t, t[:1]))
	ds.fence, ds.fenceCol = t[:n], indent
}

func listMarker(s string) int {
	n := 0
	switch {
	case s == "":
		return 0
	case s[0] == '-' || s[0] == '*' || s[0] == '+':
		n = 1
	default:
		for n < len(s) && n < 9 && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n == 0 || n == len(s) || s[n] != '.' && s[n] != ')' {
			return 0
		}
		n++
	}
	if n < len(s) && strings.IndexByte(" \t\r\n", s[n]) < 0 {
		return 0
	}
	return n
}

func renderMarkdown(r *glamour.TermRenderer, src string) []string {
	if r != nil {
		if out, err := r.Render(src); err == nil {
			src = out
		}
	}
	return strings.Split(strings.TrimRight(src, "\n"), "\n")
}

func (m *model) markdownSource() (src string, ok bool) {
	switch m.tab {
	case tabChanges:
		f := m.curChangedFile()
		if f == nil || !isMarkdown(f.Path) || f.Status == vcs.StatusDeleted {
			return "", false
		}
		src = f.NewText
	case tabAllFiles:
		if e := m.curEntry(); e == nil || !isMarkdown(e.Path) {
			return "", false
		}
		src = m.allFiles.shownText
	}
	return src, src != ""
}

// rendersNothing counts a src too large to render as text. The view then
// still switches, and the title tells why it shows source.
func (m *model) rendersNothing(src string) bool {
	if !m.renderableMarkdown(src) {
		return false
	}
	r := m.markdownRenderer(m.codeWidth() - 2)
	n := &m.markdown.nothing
	if r != nil && m.markdown.r == r && n.done && n.src == src {
		return n.is
	}
	_, srcs, _ := m.blockSourcesOf(src)
	is := !hasMarkdownText(m.renderBlocks(r, srcs), src)
	if is && len(src) <= maxMarkdownBlockBytes {
		lines := renderBlock(r, termtext.Plain(src), m.markdown.wrap)
		is = !slices.ContainsFunc(lines, func(l string) bool { return !blankLine(l) })
	}
	if r != nil && m.markdown.r == r {
		n.src, n.done, n.is = src, true, is
	}
	return is
}

// setMarkdown takes nil rows if nothing changed. Each rendered line maps
// to the first source line of its block. The blank line after it maps to
// the last, so a comment covers the whole block.
func (m *model) setMarkdown(p *pane, oldText, newText string, rows []diff.Row) {
	lines, oldLines := diff.SplitLines(newText), diff.SplitLines(oldText)
	if rows == nil {
		for n := range lines {
			rows = append(rows, diff.Row{Kind: diff.KindEqual, New: n + 1})
		}
	}
	ix := newRowIndex(rows)
	r := m.markdownRenderer(m.codeWidth() - 2)
	var out []string
	var outRows []diff.Row
	removed := func(lo, hi int) {
		if n := ix.deletedLines(oldLines, lo, hi); n > 0 {
			// The marker is a deleted row, so ] and [ stop at it. See
			// removedMarker.
			row := diff.Row{Kind: diff.KindDeleted, New: max(lo, 1)}
			blank := row
			blank.Kind, blank.Continued = diff.KindEqual, true
			text := fmt.Sprintf(" − %s removed", plural(n, "line"))
			out = append(out, deletedStyle.Render("▎")+dimStyle.Render(text), "")
			outRows = append(outRows, row, blank)
		}
	}
	see := m.keys.Rendered.Help().Key
	spans, srcs, _ := m.blockSourcesOf(newText)
	blocks := m.renderBlocks(r, srcs)
	prevRows := 0
	for k, span := range spans {
		first, last := span[0], span[1]
		removed(prevRows, first)
		prevRows = last
		block := blocks[k]
		for len(block) > 0 && blankLine(block[0]) {
			block = block[1:]
		}
		for len(block) > 0 && blankLine(block[len(block)-1]) {
			block = block[:len(block)-1]
		}
		bar, n := ix.changeBar(first, last)
		switch {
		case len(block) > 0:
		case n == 1:
			block = []string{dimStyle.Render("  ⚠ 1 changed line doesn't render · " + see + " to see")}
		case n > 1:
			block = []string{dimStyle.Render(fmt.Sprintf(
				"  ⚠ %d changed lines don't render · %s to see", n, see))}
		default:
			continue
		}
		// The lines of a changed block are changed rows, so ] and [ stop
		// at the block.
		kind := diff.KindEqual
		if bar != "" {
			kind = diff.KindChanged
		}
		for i, l := range block {
			if bar != "" {
				l = bar + ansi.TruncateLeft(l, 1, "")
			}
			out = append(out, l)
			outRows = append(outRows, diff.Row{Kind: kind, New: first, Continued: i > 0})
		}
		out = append(out, "")
		outRows = append(outRows, diff.Row{Kind: diff.KindEqual, New: last, Continued: true})
	}
	removed(prevRows, len(lines)+1)
	p.set(out, outRows)
	p.srcRows = rows
}

// removedMarker maps to the line before the removed lines, which the
// block above also shows. Its comments thus show only below the block.
func (p *pane) removedMarker(i int) bool {
	return p.srcRows != nil && i >= 0 && i < len(p.rows) && p.rows[p.lineStart(i)].Kind == diff.KindDeleted
}

// blockSources returns 1-based line spans. Each source includes the link
// definitions it uses, so its reference links resolve. It stops when work
// exceeds maxMarkdownWork.
func blockSources(lines []string) (spans [][2]int, srcs []string, work int) {
	defs := linkDefs(lines, maxLinkDefBytes)
	for first, last := range markdownBlocks(lines) {
		src := strings.Join(lines[first-1:last], "\n")
		if note, _ := blockNote(src); note == "" {
			if used := usedDefs(src, defs, maxMarkdownBlockBytes-len(src)); len(used) > 0 {
				with := src + "\n\n" + strings.Join(used, "\n")
				if note, _ := blockNote(with); note == "" {
					src = with
				}
			}
		}
		src = termtext.Plain(src)
		_, w := blockNote(src)
		work += w
		spans = append(spans, [2]int{first, last})
		srcs = append(srcs, src)
		if work > maxMarkdownWork {
			break
		}
	}
	return spans, srcs, work
}

func linkDefs(lines []string, maxLen int) map[string][]string {
	defs := map[string][]string{}
	for _, l := range lines {
		if len(l) > maxLen {
			continue
		}
		if m := linkDef.FindStringSubmatch(l); m != nil {
			label := util.ToLinkReference([]byte(m[1]))
			defs[label] = append(defs[label], l)
		}
	}
	return defs
}

// usedDefs returns nil if the lines exceed limit bytes. Bracket text that
// is not a link only adds an unused definition.
func usedDefs(src string, defs map[string][]string, limit int) []string {
	if len(defs) == 0 {
		return nil
	}
	var out []string
	size := 0
	seen := map[string]bool{}
	for _, m := range linkLabel.FindAllStringSubmatch(src, -1) {
		label := util.ToLinkReference([]byte(m[1]))
		if seen[label] {
			continue
		}
		seen[label] = true
		for _, l := range defs[label] {
			if size += len(l) + 1; size > limit {
				return nil
			}
			out = append(out, l)
		}
	}
	return out
}

var (
	linkDef   = regexp.MustCompile(`^\s{0,3}\[([^\]]+)\]:`)
	linkLabel = regexp.MustCompile(`\[((?:[^\[\]\\]|\\.)+)\]`)
)

// rowIndex avoids a full scan, unless the keys ever decrease.
type rowIndex struct {
	rows     []diff.Row
	key      []int  // the new line of row i, or the last one before a deleted row
	at       []int  // the last new line before row i
	deleted  []bool // row i follows deleted rows
	monotone bool
}

func newRowIndex(rows []diff.Row) *rowIndex {
	ix := &rowIndex{
		rows: rows, key: make([]int, len(rows)), at: make([]int, len(rows)),
		deleted: make([]bool, len(rows)), monotone: true,
	}
	at, del, key := 0, false, 0
	for i, r := range rows {
		ix.at[i], ix.deleted[i] = at, del
		switch {
		case r.Continued:
		case r.Kind == diff.KindDeleted:
			key, del = at, true
		default:
			key, del, at = r.New, false, r.New
		}
		if i > 0 && key < ix.key[i-1] {
			ix.monotone = false
		}
		ix.key[i] = key
	}
	return ix
}

// first returns 0 if the keys are not monotone.
func (ix *rowIndex) first(line int) int {
	if !ix.monotone {
		return 0
	}
	i, _ := slices.BinarySearch(ix.key, line)
	return i
}

// deletedLines counts non-blank deleted lines after new lines lo to hi-1.
// Deleted lines just before an added or changed line are a change to it,
// not a removal.
func (ix *rowIndex) deletedLines(oldLines []string, lo, hi int) int {
	s := ix.first(lo)
	n, run, at := 0, 0, 0
	if s < len(ix.rows) {
		at = ix.at[s]
	}
	for _, r := range ix.rows[s:] {
		switch {
		case r.Continued:
		case r.Kind == diff.KindDeleted:
			if at >= lo && at < hi && r.Old <= len(oldLines) && strings.TrimSpace(oldLines[r.Old-1]) != "" {
				run++
			}
		default:
			if r.Kind == diff.KindEqual {
				n += run
			}
			run, at = 0, r.New
			if ix.monotone && at >= hi {
				return n
			}
		}
	}
	return n + run
}

// markdownBlocks yields 1-based line spans. A blank line outside a code
// fence ends a block, unless the next line is indented.
func markdownBlocks(lines []string) iter.Seq2[int, int] {
	return func(yield func(int, int) bool) {
		first, last, fence := 0, 0, ""
		for i, l := range lines {
			if fence == "" {
				if strings.TrimSpace(l) == "" {
					continue
				}
				if first > 0 && last < i && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
					if !yield(first, last) {
						return
					}
					first = 0
				}
			}
			switch t := strings.TrimLeft(l, " "); {
			case fence != "":
				if strings.HasPrefix(t, fence) {
					fence = ""
				}
			case strings.HasPrefix(t, "```"), strings.HasPrefix(t, "~~~"):
				fence = t[:3]
			}
			if first == 0 {
				first = i + 1
			}
			last = i + 1
		}
		if first > 0 {
			yield(first, last)
		}
	}
}

// changeBar returns "" if the rows do not change lines first to last. n
// counts the added or changed new lines, or else the deleted lines.
func (ix *rowIndex) changeBar(first, last int) (bar string, n int) {
	s := ix.first(first)
	added, changed, deleted := 0, false, false
	gone := 0
	at := 0
	if s < len(ix.rows) {
		at, deleted = ix.at[s], ix.deleted[s]
	}
	for _, r := range ix.rows[s:] {
		if r.Continued {
			continue
		}
		if r.Kind == diff.KindDeleted {
			deleted = true
			if at >= first && at < last {
				changed = true
				gone++
			}
			continue
		}
		if r.New >= first && r.New <= last && r.Kind != diff.KindEqual {
			added++
			changed = changed || deleted || r.Kind == diff.KindChanged
		}
		deleted, at = false, r.New
		if at >= last {
			break
		}
	}
	n = cmp.Or(added, gone)
	switch {
	case changed:
		return noteStyle.Render("▎"), n
	case added > 0:
		return addedStyle.Render("▎"), n
	}
	return "", 0
}
