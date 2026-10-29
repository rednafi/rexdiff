package tui

// Rendering of pane lines: diffs, files in the Files tab, and Markdown.

import (
	"cmp"
	"errors"
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
	"github.com/rednafi/rexdiff/internal/diffview"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/vcs"
)

// gutter is the width of the column at the left of the main pane that
// holds the cursor and other markers.
const gutter = 1

// Sizes of the file list. At the left or right, it takes navigatorSidePct
// of the screen width by default, kept between minNavigatorPct and
// maxNavigatorSidePct. Above or below, it takes navigatorStackPct of the
// body rows by default, kept between minNavigatorPct and
// maxNavigatorStackPct.
const (
	navigatorSidePct      = 22
	maxNavigatorSideWidth = 40 // caps the default width, unless minNavigatorPct requires more
	navigatorStackPct     = 25
	minNavigatorPct       = 15
	maxNavigatorSidePct   = 60
	maxNavigatorStackPct  = 50
)

// navigatorShown reports whether the file list is visible. The PR tab has
// no file list.
func (m *model) navigatorShown() bool { return !m.navigatorHidden && m.tab != tabPR }

// navigatorSideWidth returns the width in columns of the file list when it
// is at the left or right. The default is navigatorSidePct of the screen,
// at most maxNavigatorSideWidth, and always within the bounds of
// clampNavigatorWidth.
func (m *model) navigatorSideWidth() int {
	return m.clampNavigatorWidth(cmp.Or(m.navigatorWidthSet, min(m.w*navigatorSidePct/100, maxNavigatorSideWidth)))
}

// clampNavigatorWidth clamps w to the allowed width of the file list when
// it is at the left or right.
func (m *model) clampNavigatorWidth(w int) int {
	return min(max(w, m.w*minNavigatorPct/100), m.w*maxNavigatorSidePct/100)
}

// navigatorWidth returns the width of the file list, or 0 if it is not
// visible.
func (m *model) navigatorWidth() int {
	switch {
	case !m.navigatorShown():
		return 0
	case m.navigatorPosition.stacked():
		return m.w
	}
	return m.navigatorSideWidth()
}

// navigatorHeight returns the number of body rows, including its rule,
// that the file list takes above or below the main pane, for a body of h
// rows. It returns 0 if the file list is not above or below, or if the
// rule and title would leave no room for a file.
func (m *model) navigatorHeight(h int) int {
	if !m.navigatorShown() || !m.navigatorPosition.stacked() {
		return 0
	}
	if lh := shareOf(h, m.navigatorPct); lh > 2 {
		return lh
	}
	return 0
}

// minMainWidth is the smallest width of the main pane, including its
// gutter. The pane keeps this width even if the file list or a narrow
// terminal leaves less room.
const minMainWidth = 24

// mainWidth returns the width of the main pane, including its gutter.
func (m *model) mainWidth() int {
	w := m.w
	if m.navigatorShown() && !m.navigatorPosition.stacked() {
		w -= m.navigatorSideWidth() + sepWidth
	}
	return max(w, minMainWidth)
}

// mainHeight returns the number of rows in the main pane.
func (m *model) mainHeight() int { return m.mainHeightOf(m.bodyHeight()) }

// mainHeightOf returns the number of rows in the main pane, not counting
// its title, for a body of h rows.
func (m *model) mainHeightOf(h int) int { return max(h-m.navigatorHeight(h)-m.titleHeight(), 0) }

// titleHeight returns the number of lines in the title of the main pane.
// The PR tab has no title, because it shows no file.
func (m *model) titleHeight() int {
	if m.tab == tabPR {
		return 0
	}
	return 1
}

// mainTitle renders the title of the main pane: the full path of the file
// being shown and, in the Changes tab, its added and deleted line counts.
// In rendered view, it notes a Markdown file that is too large to render.
// A path that is too long is cut at the start.
func (m *model) mainTitle() string {
	var name, stat string
	switch m.tab {
	case tabChanges:
		f := m.curChangedFile()
		if f == nil {
			return ""
		}
		name = f.Path
		if f.OldPath != "" {
			name = f.OldPath + " → " + f.Path
		}
		stat = f.stat()
	case tabAllFiles:
		// The pane has no path for a file that it cannot show, such as a
		// binary file, but the title still names the file.
		if m.allFiles.cur < len(m.allFiles.entries) {
			name = m.allFiles.entries[m.allFiles.cur].Path
		}
	}
	if name == "" {
		return ""
	}
	if src, ok := m.markdownSource(); ok && m.rendered && !m.renderableMarkdown(src) {
		stat = strings.TrimSpace(stat + "  " + dimStyle.Render(noteMarkdownTooBig))
	}
	room := max(m.mainWidth()-ansi.StringWidth(stat)-3, 1)
	return " " + titleStyle.Render(cutStart(name, room)) + "  " + stat
}

// cutStart fits p into room columns. If p is too long, it cuts the start
// so that the file name stays visible.
func cutStart(p string, room int) string {
	p = termtext.Quote(p)
	if ansi.StringWidth(p) > room {
		p = "…" + lastColumns(p, room-1)
	}
	return p
}

// codeWidth returns the width of the main pane, not counting its gutter.
func (m *model) codeWidth() int { return m.mainWidth() - gutter }

// rerender renders the current pane again, for example after the layout,
// wrapping or width changes. The cursor and the selection stay on the same
// source lines. A cursor on a header row keeps the same distance above the
// first source line below it.
func (m *model) rerender() {
	m.sizeCommentInput()
	p := m.curPane()
	next := p.cur
	for next < len(p.rows) && m.targetAt(next, diff.SideNew) == nil {
		next++
	}
	up := next - p.cur
	cur, selStart := m.targetAt(next, diff.SideNew), m.targetAt(p.selStart, diff.SideNew)
	switch m.tab {
	case tabChanges:
		m.renderChanges()
	case tabAllFiles:
		m.renderAllFiles()
	case tabPR:
		m.renderPR()
	}
	if cur != nil {
		if i := p.rowOf(cur.side, cur.line); i >= 0 {
			p.cur = max(i-up, 0)
		}
	}
	if selStart != nil {
		if i := p.rowOf(selStart.side, selStart.line); i >= 0 {
			p.selStart = i
		}
	}
}

// sizeCommentInput sets the comment box width to fit the main pane.
func (m *model) sizeCommentInput() { m.commentInput.SetWidth(max(m.mainWidth()-8, 20)) }

// minSplitWidth is the narrowest code width that allows the split layout:
// two halves of 60 columns, each with its line numbers, and a separator
// column.
const minSplitWidth = 135

// splitAllowed reports whether the current file can be shown in the split
// layout. It must be a diff in the Changes tab with content on both sides
// and an actual change. Rendered Markdown cannot be split.
func (m *model) splitAllowed() bool {
	if m.tab != tabChanges {
		return false
	}
	f := m.curChangedFile()
	return f != nil && f.Err == nil && !m.renderedShown(f) &&
		f.OldText != "" && f.NewText != "" && f.OldText != f.NewText
}

// splitShown reports whether the current file is shown in the split
// layout. The user must have chosen split, and the pane must be wide
// enough. The choice is kept, so the split returns when the pane is wide
// enough again.
func (m *model) splitShown() bool {
	return m.split && m.codeWidth() >= minSplitWidth && m.splitAllowed()
}

// renderedShown reports whether the Changes tab shows f as rendered
// Markdown.
func (m *model) renderedShown(f *changedFile) bool {
	return m.rendered && isMarkdown(f.Path) && f.Status != vcs.StatusDeleted && m.renderableMarkdown(f.NewText)
}

// fileOptions returns the options for rendering a file scrolled scrollX
// columns to the right.
func (m *model) fileOptions(scrollX int) diff.FileOptions {
	return diff.FileOptions{Width: m.codeWidth(), ScrollX: scrollX, Wrap: m.wrap, Cache: &m.cache}
}

// diffOptions returns the options for rendering a diff scrolled scrollX
// columns to the right.
func (m *model) diffOptions(scrollX int) diff.Options {
	return diff.Options{
		Width:   m.codeWidth(),
		ScrollX: scrollX,
		Wrap:    m.wrap,
		Split:   m.splitShown(),
		Signs:   m.cfg.DiffSigns,
		Theme: diff.Theme{
			Background: m.bg,
			Foreground: m.fg,
			Green:      m.green,
			Red:        m.red,
		},
		Cache: &m.cache,
	}
}

// isMarkdown reports whether path names a Markdown file.
func isMarkdown(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".markdown")
}

// Notes shown instead of a file that rexdiff cannot display.
const (
	noteBinary = "binary file · no line comments"
	noteTooBig = "file too large to show"
)

// fileNote returns the note for a file that vcs did not read, or "" if
// err has no matching note. err comes from ReadFile or File.Err.
func fileNote(err error) string {
	switch {
	case errors.Is(err, vcs.ErrBinary):
		return noteBinary
	case errors.Is(err, vcs.ErrTooBig):
		return noteTooBig
	}
	return ""
}

// renderChanges renders the current file in the Changes tab.
func (m *model) renderChanges() {
	p := &m.changes.pane
	f := m.curChangedFile()
	if f == nil {
		p.path = ""
		p.set(nil, nil)
		return
	}
	p.path = f.Path
	switch {
	case f.Err != nil:
		p.setNote(fileNote(f.Err))
	case f.OldText == f.NewText:
		// The change is a rename without edits, or a file mode change.
		note := "no change to the content"
		if f.OldPath != "" {
			note = "renamed from " + termtext.Quote(f.OldPath) + ", no change to the content"
		}
		p.setNote(note)
	case m.renderedShown(f):
		all := func(diff.Fold) bool { return true }
		// Only the rows are needed, so the lines are not rendered.
		d := diff.Render(f.Path, f.OldText, f.NewText, diff.Options{Expanded: all, Cache: &m.cache})
		m.setMarkdown(p, f.OldText, f.NewText, d.Rows)
	default:
		if o := m.expandedFolds[f.Path]; o != nil && o.Old != f.oldHash {
			delete(m.expandedFolds, f.Path)
		}
		o := m.diffOptions(p.scrollX)
		a := m.anchorOf(tabChanges)
		comments := slices.DeleteFunc(m.comments.All(), func(c *diff.Comment) bool {
			return c.Anchor != a
		})
		o.Expanded = func(fold diff.Fold) bool { return m.foldExpanded(f.Path, comments, fold) }
		p.setDiff(diff.Render(f.Path, f.OldText, f.NewText, o))
	}
}

// setNote shows note dimmed on a single row with no source line. The pane
// keeps its path.
func (p *pane) setNote(note string) {
	p.set([]string{dimStyle.Render("  " + note)}, []diff.Row{{}})
}

// renderAllFiles renders the current file in the Files tab, even if the
// file has not changed. It reads the file again only if a different file
// is selected or the last read failed.
func (m *model) renderAllFiles() {
	af := &m.allFiles
	if af.cur < len(af.entries) && af.entries[af.cur].Path == af.shownPath && af.shownErr == nil {
		m.showFile(af.shownPath, af.shownText, nil)
		return
	}
	af.shownPath = "" // no file has the path "", so reloadShownFile always renders
	m.reloadShownFile()
}

// reloadShownFile reads the current file in the Files tab and renders it
// again if it has changed.
func (m *model) reloadShownFile() {
	p := &m.allFiles.pane
	if m.allFiles.cur >= len(m.allFiles.entries) {
		p.path, m.allFiles.shownPath = "", ""
		p.set(nil, nil)
		return
	}
	path := m.allFiles.entries[m.allFiles.cur].Path
	content, err := m.repo.ReadFile(path)
	if err == nil {
		m.readErr = ""
	}
	shown := err == nil && m.allFiles.shownErr == nil &&
		content == m.allFiles.shownText && m.allFiles.shownPath == path
	if shown {
		return
	}
	m.allFiles.shownText, m.allFiles.shownPath, m.allFiles.shownErr = content, path, err
	m.showFile(path, content, err)
}

// showFile shows content, the file at path, in the Files tab. If err, the
// error from reading the file, is not nil, it shows a note instead.
func (m *model) showFile(path, content string, err error) {
	p := &m.allFiles.pane
	p.path = path
	note := fileNote(err)
	rendered := m.rendered && isMarkdown(path) && m.renderableMarkdown(content)
	if note != "" || err != nil || rendered {
		// The previous file may still be highlighted in the background.
		m.cache.StopHighlighting()
	}
	switch {
	case note != "":
		p.path = ""
		p.set([]string{dimStyle.Render("  " + note)}, nil)
	case err != nil:
		// Without a watcher, every tick reads the file again, so log each
		// distinct error only once.
		if text := err.Error(); text != m.readErr {
			m.readErr = text
			m.log.Warn("read file failed", "path", path, "err", err)
		}
		p.path = ""
		p.set([]string{dimStyle.Render("  could not read this file")}, nil)
	case rendered:
		m.setMarkdown(p, "", content, nil)
	default:
		p.setDiff(diff.RenderFile(path, content, m.fileOptions(p.scrollX)))
	}
}

// markdownRenderer returns a glamour renderer for the terminal theme that
// wraps at wrap columns, or nil if it cannot create one. The model caches
// the last renderer, because creating one is slow.
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

// blockSourcesOf returns blockSources(diff.SplitLines(src)). The result for
// the most recent src is cached, because the title, rendersNothing and
// setMarkdown each ask for it.
func (m *model) blockSourcesOf(src string) (spans [][2]int, srcs []string, work int) {
	c := &m.markdown.sources
	if !c.done || c.src != src {
		c.spans, c.srcs, c.work = blockSources(diff.SplitLines(src))
		c.src, c.done = src, true
	}
	return c.spans, c.srcs, c.work
}

// hasMarkdownText reports whether any rendered block of src contains text,
// and so whether src as a whole does. It returns false if src contains
// "<", because HTML can span blocks or hide them, so the caller must
// render the whole of src. A src longer than maxMarkdownBlockBytes is too
// slow to render whole, so for it the blocks decide.
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

// blankLine reports whether the rendered line l shows no text.
func blankLine(l string) bool {
	return strings.TrimSpace(ansi.Strip(l)) == ""
}

// newMarkdownRenderer returns a glamour renderer with the given style that
// wraps at wrap columns.
func newMarkdownRenderer(style string, wrap int) (*glamour.TermRenderer, error) {
	return glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(wrap),
	)
}

// markdownCache holds a glamour renderer with its style and wrap width.
// It also caches the blocks rendered by the last renderBlocks call, the
// texts rendered by the last renderPR call, the last rendersNothing result
// and the last blockSourcesOf result, because rendering a large file is
// slow.
type markdownCache struct {
	r     *glamour.TermRenderer
	style string
	wrap  int

	more    []*glamour.TermRenderer // extra renderers set up like r, one per additional goroutine
	blocks  map[string][]string     // the rendered lines of each block, keyed by its source
	pr      map[string][]string     // the rendered lines of each PR text, keyed by its source
	nothing struct {
		src  string
		done bool // is holds the answer for src
		is   bool
	}
	sources struct {
		src   string
		done  bool // the fields below hold the answer for src
		spans [][2]int
		srcs  []string
		work  int
	}
}

// minParallelBlocks is the number of uncached blocks per extra goroutine
// in renderBlocks. With fewer blocks than this, rendering uses one core.
const minParallelBlocks = 32

// renderBlocks returns the rendered lines of each source in srcs, as
// renderMarkdown would return them with r, which is the renderer in
// m.markdown. Blocks rendered by the previous call come from the cache.
// Many blocks are rendered in parallel, each goroutine using its own
// renderer. All goroutines finish before renderBlocks returns.
func (m *model) renderBlocks(r *glamour.TermRenderer, srcs []string) [][]string {
	mc := &m.markdown
	out := make([][]string, len(srcs))
	var todo []int // indexes of the blocks that are not cached
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
	// Each goroutine needs its own renderer. The first one uses r.
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

// Limits on the Markdown that rexdiff renders, because glamour takes time
// and memory in proportion to the work that markdownWork counts, and a
// single slow render blocks the screen.
//
// A file longer than maxMarkdownBytes shows as its source diff, with a
// note in the title. So does a file whose blocks, with the link
// definitions that each block gets, add up to more than maxMarkdownWork.
//
// A block longer than maxMarkdownBlockBytes or with more work than
// maxMarkdownBlockWork, such as a huge table or a long list nested three
// deep, shows as source within the rendered file.
//
// A block whose block quotes and lists nest more than maxMarkdownDepth
// deep also shows as source, because glamour slows down sharply once the
// indentation is wider than the pane.
//
// A link definition longer than maxLinkDefBytes is not added to the
// blocks that refer to it, so its references show as text.
const (
	maxMarkdownBytes      = 512 << 10
	maxMarkdownWork       = 2 << 20
	maxMarkdownBlockBytes = 64 << 10
	maxMarkdownBlockWork  = 128 << 10
	maxMarkdownDepth      = 12
	maxLinkDefBytes       = 2 << 10
)

// Notes for Markdown that rexdiff does not render.
const (
	noteMarkdownTooBig = "markdown too large to render"
	noteBlockTooBig    = "  block too large to render · source below"
	noteBlockTooDeep   = "  block nested too deeply to render · source below"
)

// renderableMarkdown reports whether src is small enough to show
// rendered. The title asks on every draw, so the work comes from the
// cache of blockSourcesOf.
func (m *model) renderableMarkdown(src string) bool {
	if len(src) > maxMarkdownBytes {
		return false
	}
	_, _, work := m.blockSourcesOf(src)
	return work <= maxMarkdownWork
}

// blockNote returns the note to show instead of the rendered block
// source src, or "" if the block can be rendered. work is the work of
// rendering it, as markdownWork counts it, or 0 if it is not rendered.
func blockNote(src string) (note string, work int) {
	if len(src) > maxMarkdownBlockBytes {
		return noteBlockTooBig, 0
	}
	if markdownDepth(src) > maxMarkdownDepth {
		return noteBlockTooDeep, 0
	}
	if work = markdownWork(src); work > maxMarkdownBlockWork {
		return noteBlockTooBig, 0
	}
	return "", work
}

// renderBlock returns the lines of the block source src rendered with r,
// as renderMarkdown does. A block that blockNote rejects is not rendered:
// it shows as its note followed by its source, wrapped to width columns.
func renderBlock(r *glamour.TermRenderer, src string, width int) []string {
	if note, _ := blockNote(src); note != "" {
		return sourceLines(note, src, width)
	}
	return renderMarkdown(r, src)
}

// sourceMargin is the margin of rendered text, which sourceLines gives
// to each line of source.
const sourceMargin = "  "

// sourceLines returns note, dimmed, followed by the lines of the Markdown
// src with tabs expanded and the margin of rendered text. A line wider
// than width columns, counting the margin, is wrapped, and each part has
// the margin.
func sourceLines(note, src string, width int) []string {
	room := max(width-len(sourceMargin), 1)
	var out []string
	for l := range strings.SplitSeq(wrapWords(note, room), "\n") {
		out = append(out, sourceMargin+dimStyle.Render(l))
	}
	for l := range strings.SplitSeq(diffview.ExpandTabs(strings.TrimRight(src, "\n")), "\n") {
		for part := range strings.SplitSeq(ansi.Hardwrap(l, room, true), "\n") {
			out = append(out, sourceMargin+part)
		}
	}
	return out
}

// markdownWork returns an estimate of the work that glamour does to
// render the Markdown src. Each line counts its length times the square
// of its depth, as depthScanner counts it, because glamour takes that
// much longer on each item of a nested list. Each reference to a link
// definition in src adds the length of the definition, because the output
// repeats it there. Any text in brackets counts as a reference.
func markdownWork(src string) int {
	work := 0
	var ds depthScanner
	for l := range strings.Lines(src) {
		d := max(ds.next(l), 1)
		work += len(l) * d * d
	}
	defs := linkDefs(strings.Split(src, "\n"), len(src))
	if len(defs) == 0 {
		return work
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
	return work
}

// markdownDepth returns a depth that block quotes and lists in the
// Markdown src do not nest beyond: the largest depth of any of its lines,
// as depthScanner counts it.
func markdownDepth(src string) int {
	depth := 0
	var ds depthScanner
	for l := range strings.Lines(src) {
		depth = max(depth, ds.next(l))
	}
	return depth
}

// depthScanner counts how deeply each line of a Markdown block may nest,
// one line after another. The count is never less than the real depth,
// so that it is safe to use as a limit, but it stays close for ordinary
// Markdown.
//
// Each quote or list marker at the start of a line counts as one level,
// whatever its width, so "1. " and "- " count the same. Indentation
// counts as one level for each list item above whose text starts within
// it: a line indented to the text of an item belongs to that item. The
// scanner does not track when an item ends, so it counts every item seen
// so far in the block, which can only count too many.
//
// Inside a fenced code block, the indentation of the code does not
// count, only the indentation up to the column of the fence. A line with
// text left of that column, or with a list marker left of it, ends the
// code block, as it does in CommonMark, and counts as usual.
type depthScanner struct {
	// items[c] reports whether the text of a list item seen so far starts
	// at column c.
	items []bool
	// fence is the fence that opened the current code block, such as
	// "```", or "" outside a code block. fenceCol is its column.
	fence    string
	fenceCol int
}

// next returns the depth of the line l, which follows the lines given
// before it.
func (ds *depthScanner) next(l string) int {
	depth, col := 0, 0
	gapStart := 0 // the column where the current run of blanks starts
	// gap counts the list items whose text starts in the run of blanks
	// that ends at col, and starts a new run.
	gap := func() {
		for c := gapStart + 1; c <= col && c < len(ds.items); c++ {
			if ds.items[c] {
				depth++
			}
		}
		gapStart = col
	}
	// marker counts a marker that ends at l[i], skips the space after it,
	// and returns the index after that.
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
				col += diffview.TabWidth - 1
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
	// Text left of the fence, or a line of only blanks and markers, ends
	// the code block only if it has text.
	if inCode && strings.TrimSpace(l[i:]) != "" {
		ds.fence = ""
	}
	if ds.fence == "" {
		ds.openFence(l[i:], col)
	}
	return depth
}

// addItem records that the text of a list item starts at column col.
func (ds *depthScanner) addItem(col int) {
	if col >= len(ds.items) {
		ds.items = append(ds.items, make([]bool, col+1-len(ds.items))...)
	}
	ds.items[col] = true
}

// openFence starts a code block if rest, the text of a line that starts
// at column col, opens a fence. A fence indented four or more columns
// past the text of the nearest list item is code, not a fence.
func (ds *depthScanner) openFence(rest string, col int) {
	t := strings.TrimLeft(rest, " \t")
	indent := col + len(rest) - len(t) // tabs inside the fence line are rare, so count them as one
	if !strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "~~~") {
		return
	}
	if t[0] == '`' && strings.Contains(strings.TrimLeft(t, "`"), "`") {
		return // a backtick fence has no backtick in its info string
	}
	base := 0 // the column of the text of the nearest list item
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

// listMarker returns the width of the list marker at the start of s, or
// 0 if s does not start with one. A marker is "-", "*" or "+", or up to
// nine digits followed by "." or ")", and must be followed by a blank or
// the end of the line.
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

// renderMarkdown returns the lines of src rendered with r. If r is nil or
// rendering fails, it returns the lines of src unchanged.
func renderMarkdown(r *glamour.TermRenderer, src string) []string {
	if r != nil {
		if out, err := r.Render(src); err == nil {
			src = out
		}
	}
	return strings.Split(strings.TrimRight(src, "\n"), "\n")
}

// markdownSource returns the Markdown text of the current file. ok is
// false if the file is not Markdown, has been deleted, or is empty.
func (m *model) markdownSource() (src string, ok bool) {
	switch m.tab {
	case tabChanges:
		f := m.curChangedFile()
		if f == nil || !isMarkdown(f.Path) || f.Status == vcs.StatusDeleted {
			return "", false
		}
		src = f.NewText
	case tabAllFiles:
		if m.allFiles.cur >= len(m.allFiles.entries) || !isMarkdown(m.allFiles.entries[m.allFiles.cur].Path) {
			return "", false
		}
		src = m.allFiles.shownText
	}
	return src, src != ""
}

// rendersNothing reports whether the Markdown src renders to no visible
// text. The result for the most recent src is cached. A src too large to
// render counts as text, so that m still switches the view and the title
// says why the file shows as source.
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

// setMarkdown shows newText in p as rendered Markdown, block by block, with
// a change bar on each changed block. rows are the diff rows from oldText
// to newText, or nil if nothing changed. Each rendered line maps to the
// first source line of its block and the blank line after it to the last,
// so a comment on a block covers it all. A removed block, or a changed one
// that renders nothing, shows as a dim marker line.
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
	// removed adds a marker row for the lines deleted after new lines lo
	// up to, but not including, hi.
	removed := func(lo, hi int) {
		if n := ix.deletedLines(oldLines, lo, hi); n > 0 {
			// The marker counts as a deleted row, so that ] and [ stop at it.
			// It is a line of its own, so that the cursor can rest on it.
			// It maps to the line before the removed lines, but shows no
			// comments, as removedMarker explains.
			row := diff.Row{Kind: diff.KindDeleted, New: max(lo, 1)}
			blank := row
			blank.Kind, blank.Continued = diff.KindEqual, true
			text := fmt.Sprintf(" − %s removed", plural(n, "line"))
			out = append(out, deletedStyle.Render("▎")+dimStyle.Render(text), "")
			outRows = append(outRows, row, blank)
		}
	}
	see := m.keys.Rendered.Help().Key
	// Render all blocks up front, so that they can be rendered in
	// parallel.
	spans, srcs, _ := m.blockSourcesOf(newText)
	blocks := m.renderBlocks(r, srcs)
	prevRows := 0 // the last line of the previous block
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
		// The lines of a changed block count as changed rows, so that ]
		// and [ stop at the block.
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

// removedMarker reports whether row i of p belongs to the marker that
// rendered Markdown shows for removed lines. The marker maps to the line
// before the removed lines, which the block above also shows, so the
// comments on that line show only below the block, not twice.
func (p *pane) removedMarker(i int) bool {
	return p.srcRows != nil && i >= 0 && i < len(p.rows) && p.rows[p.lineStart(i)].Kind == diff.KindDeleted
}

// blockSources returns the first and last line numbers, counting from 1,
// of each block in the Markdown lines, and the source to render for each
// block. A block's source includes the link reference definitions in
// lines whose labels appear in the block, so that its reference links
// resolve, unless they would make the block too large to render. Only
// definitions of at most maxLinkDefBytes are included, and none in a
// block that blockNote rejects. work is the sum of the work of the blocks
// that are rendered, as markdownWork counts it. Once work exceeds
// maxMarkdownWork, blockSources stops and returns the blocks so far.
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

// linkDefs returns the lines of link reference definitions in lines that
// are at most maxLen bytes long, by label.
func linkDefs(lines []string, maxLen int) map[string][]string {
	defs := map[string][]string{} // the definition lines of each label
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

// usedDefs returns the lines in defs whose labels appear in brackets in
// the Markdown block src. It matches labels as CommonMark does, ignoring
// case and runs of white space. Text in brackets that is not a link only
// adds a definition that the block does not use. It returns nil if the
// lines would take more than limit bytes.
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

// linkDef matches the first line of a link reference definition and
// captures its label. linkLabel matches text in brackets that can be a
// link label, which cannot hold an unescaped bracket, and captures it.
var (
	linkDef   = regexp.MustCompile(`^\s{0,3}\[([^\]]+)\]:`)
	linkLabel = regexp.MustCompile(`\[((?:[^\[\]\\]|\\.)+)\]`)
)

// rowIndex finds the rows for a range of new lines without scanning every
// row. key[i] is the new line of row i, or the last new line before it for
// a deleted row. at[i] is the last new line before row i, and deleted[i]
// reports whether row i follows deleted rows. If the keys ever decrease,
// each lookup scans all the rows.
type rowIndex struct {
	rows     []diff.Row
	key      []int
	at       []int
	deleted  []bool
	monotone bool // the keys never decrease
}

// newRowIndex builds a rowIndex for rows.
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

// first returns the first row whose key is at least line, or 0 if the keys
// are not monotone.
func (ix *rowIndex) first(line int) int {
	if !ix.monotone {
		return 0
	}
	i, _ := slices.BinarySearch(ix.key, line)
	return i
}

// deletedLines returns the number of non-blank lines of oldLines that the
// rows delete right after a new line in the range lo to hi, not including
// hi. Deleted lines directly followed by an added or changed line count as
// a change to that line, not as a removal.
func (ix *rowIndex) deletedLines(oldLines []string, lo, hi int) int {
	// Rows before s have keys below lo, so neither they nor deletions
	// before them can add to the count.
	s := ix.first(lo)
	n, run, at := 0, 0, 0 // at is the last new line before the current row
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
				return n // all later rows have keys of hi or more
			}
		}
	}
	return n + run
}

// markdownBlocks yields the first and last line numbers, counting from 1,
// of each block in the Markdown lines. A blank line outside a code fence
// ends a block, unless the next line is indented, as in a list item.
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

// changeBar returns the change bar for source lines first to last, or ""
// if the rows do not change them. The bar is green if lines were only
// added there, and yellow if any line there was also changed or deleted.
// n is the number of changed lines: the added or changed new lines if
// there are any, and otherwise the deleted lines.
func (ix *rowIndex) changeBar(first, last int) (bar string, n int) {
	// Rows before s have keys below first, so they cannot affect the
	// result.
	s := ix.first(first)
	added, changed, deleted := 0, false, false
	gone := 0 // the number of deleted lines in the range
	at := 0   // the last new line before the current row
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
