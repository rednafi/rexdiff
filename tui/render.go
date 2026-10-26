package tui

// This file turns files into the lines of a pane. It renders diffs, the
// file viewer, and Markdown.

import (
	"cmp"
	"errors"
	"slices"
	"strings"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/vcs"
)

// gutter is the width of the column for the cursor and the marks, at the
// left of the main pane.
const gutter = 2

// The sizes of the file list, as in herdr-reviewr. At a side, the list
// takes listSidePct of the screen width by default, and stays between
// listMinPct and listMaxSidePct. Above or below, the list takes
// listStackPct of the body rows by default, and stays between listMinPct
// and listMaxStackPct.
const (
	listSidePct     = 32
	listStackPct    = 25
	listMinPct      = 15
	listMaxSidePct  = 60
	listMaxStackPct = 50
)

// listShown reports whether the file list shows. The PR tab has no file
// list.
func (m *model) listShown() bool { return !m.listHidden && m.tab != tabPR }

// sideWidth returns the width of the file list at a side, in columns.
// Without a width in columns, the list takes listSidePct of the screen.
func (m *model) sideWidth() int {
	return m.clampSide(cmp.Or(m.listW, m.w*listSidePct/100))
}

// clampSide returns w within the bounds of the width of the file list at
// a side.
func (m *model) clampSide(w int) int {
	return min(max(w, m.w*listMinPct/100), m.w*listMaxSidePct/100)
}

// listWidth returns the width of the file list, or 0 if the list does not
// show.
func (m *model) listWidth() int {
	switch {
	case !m.listShown():
		return 0
	case m.listPos.stacked():
		return m.w
	}
	return m.sideWidth()
}

// listHeight returns the number of body rows that the file list takes
// above or below the main pane, with its rule. The body has h rows. It
// returns 0 if the list does not show there, or if the rule leaves no
// row for the list.
func (m *model) listHeight(h int) int {
	if !m.listShown() || !m.listPos.stacked() {
		return 0
	}
	if lh := splitAxis(h, m.listPct); lh > 1 {
		return lh
	}
	return 0
}

// mainMinWidth is the width of the main pane, with its gutter, if the
// file list leaves less, or if the terminal is narrower.
const mainMinWidth = 24

// mainWidth returns the width of the main pane, with its gutter.
func (m *model) mainWidth() int {
	w := m.w
	if lw := m.listWidth(); lw > 0 && !m.listPos.stacked() {
		w -= lw + sepWidth
	}
	return max(w, mainMinWidth)
}

// mainHeight returns the number of rows of the main pane.
func (m *model) mainHeight() int { return m.mainHeightOf(m.bodyHeight()) }

// mainHeightOf returns the number of rows of the main pane in a body of h
// rows.
func (m *model) mainHeightOf(h int) int { return h - m.listHeight(h) }

// codeWidth returns the width of the main pane, without its gutter.
func (m *model) codeWidth() int { return m.mainWidth() - gutter }

// rerender renders the current pane again, for example after a change of
// the layout, the wrap, or the width. The cursor and the selection stay on
// their source lines. A cursor on a header stays above the first line
// below the header.
func (m *model) rerender() {
	m.sizeEditor()
	p := m.cur()
	next := p.cur
	for next < len(p.rows) && m.target(next, diff.SideNew) == nil {
		next++
	}
	up := next - p.cur
	cur, anchor := m.target(next, diff.SideNew), m.target(p.anchor, diff.SideNew)
	switch m.tab {
	case tabChanges:
		m.renderChanges()
	case tabFiles:
		m.renderViewer()
	case tabPR:
		m.renderPR()
	}
	if cur != nil {
		if i := p.rowOf(cur.side, cur.line); i >= 0 {
			p.cur = max(i-up, 0)
		}
	}
	if anchor != nil {
		if i := p.rowOf(anchor.side, anchor.line); i >= 0 {
			p.anchor = i
		}
	}
}

// sizeEditor fits the comment box to the width of the main pane.
func (m *model) sizeEditor() { m.editor.SetWidth(max(m.mainWidth()-8, 20)) }

// splitMinWidth is the narrowest code width that shows the split layout.
// It is the same as in herdr-reviewr: two halves of 60 columns, each with
// its line numbers, and a column between them.
const splitMinWidth = 135

// splitAllowed reports whether the current file can show in the split
// layout. The file must be a diff in the Changes tab, with lines on both
// sides and a change. Rendered markdown cannot split.
func (m *model) splitAllowed() bool {
	if m.tab != tabChanges {
		return false
	}
	f := m.selectedChange()
	return f != nil && f.Skip == nil && !m.isRendered(f) &&
		f.Old != "" && f.New != "" && f.Old != f.New
}

// splitShown reports whether the current file shows in the split layout.
// The user must choose split, and the pane must be wide enough. The choice
// stays, so a wider pane shows split again.
func (m *model) splitShown() bool {
	return m.split && m.codeWidth() >= splitMinWidth && m.splitAllowed()
}

// isRendered reports whether the Changes tab shows f as rendered
// markdown.
func (m *model) isRendered(f *changedFile) bool {
	return m.rendered && isMarkdown(f.Path) && f.Status != vcs.StatusDeleted
}

// diffOptions returns the options to render a diff with sideways scroll
// xoff.
func (m *model) diffOptions(xoff int) diff.Options {
	return diff.Options{
		Width:  m.codeWidth(),
		Offset: xoff,
		Wrap:   m.wrap,
		Split:  m.splitShown(),
		Signs:  m.cfg.DiffSigns,
		Theme: diff.Theme{
			Background: m.bg,
			Foreground: m.fg,
			Green:      m.green,
			Red:        m.red,
		},
		Cache: &m.cache,
	}
}

// isMarkdown reports whether path names a markdown file.
func isMarkdown(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".markdown")
}

// The notes in place of a file that rexdiff does not show. They are the
// same as in herdr-reviewr.
const (
	noteBinary = "binary file · no line comments"
	noteTooBig = "file too large to show"
)

// fileNote returns the note for a file that vcs does not read, or "" if
// err has no note. err is the error of ReadFile, or File.Skip.
func fileNote(err error) string {
	switch {
	case errors.Is(err, vcs.ErrBinary):
		return noteBinary
	case errors.Is(err, vcs.ErrTooBig):
		return noteTooBig
	}
	return ""
}

// renderChanges shows the selected file in the Changes tab.
func (m *model) renderChanges() {
	p := &m.changePane
	f := m.selectedChange()
	if f == nil {
		p.path = ""
		p.set(nil, nil)
		return
	}
	p.path = f.Path
	switch {
	case f.Skip != nil:
		p.setNote(fileNote(f.Skip))
	case f.Old == f.New:
		// The change is a rename with no edit, or a change of the file mode.
		note := "no change to the content"
		if f.From != "" {
			note = "renamed from " + termtext.Quote(f.From) + ", no change to the content"
		}
		p.setNote(note)
	case m.isRendered(f):
		m.setMarkdown(p, f.New)
	default:
		if o := m.folds[f.Path]; o != nil && o.Old != f.oldHash {
			delete(m.folds, f.Path)
		}
		o := m.diffOptions(p.xoff)
		a := m.anchorOf(tabChanges)
		comments := slices.DeleteFunc(m.comments.All(), func(c *diff.Comment) bool {
			return c.Anchor != a
		})
		o.Expanded = func(fd diff.Fold) bool { return m.expanded(f.Path, comments, fd) }
		p.setDiff(diff.Render(f.Path, f.Old, f.New, o))
	}
}

// setNote shows a dim note on one row that shows no source line. The
// pane keeps its path.
func (p *pane) setNote(note string) {
	p.set([]string{dimStyle.Render("  " + note)}, []diff.Row{{}})
}

// renderViewer shows the selected file in the Files tab, even if the file
// does not change.
func (m *model) renderViewer() {
	m.viewedPath = "" // no file has the path "", so refreshViewer renders
	m.refreshViewer()
}

// refreshViewer shows the selected file in the Files tab if the file
// changes.
func (m *model) refreshViewer() {
	p := &m.allPane
	if m.allSel >= len(m.allFiles) {
		p.path, m.viewedPath = "", ""
		p.set(nil, nil)
		return
	}
	path := m.allFiles[m.allSel].Path
	content, err := m.repo.ReadFile(path)
	if err == nil {
		m.readErr = ""
	}
	if err == nil && content == m.viewed && m.viewedPath == path {
		return
	}
	m.viewed, m.viewedPath = content, path
	p.path = path
	note := fileNote(err)
	switch {
	case note != "":
		p.path = ""
		p.set([]string{dimStyle.Render("  " + note)}, nil)
	case err != nil:
		// Without a watcher, each tick reads the file again. The log has
		// each error once.
		if text := err.Error(); text != m.readErr {
			m.readErr = text
			m.log.Warn("read file failed", "path", path, "err", err)
		}
		p.path = ""
		p.set([]string{dimStyle.Render("  could not read this file")}, nil)
	case m.rendered && isMarkdown(path):
		m.setMarkdown(p, content)
	default:
		p.setDiff(diff.RenderFile(path, content, m.diffOptions(p.xoff)))
	}
}

// markdown returns a glamour renderer for the terminal theme, or nil. The
// renderer wraps at wrap columns. The model keeps the last renderer,
// because a new one is slow to make.
func (m *model) markdown(wrap int) *glamour.TermRenderer {
	style := "dark"
	if !m.dark() {
		style = "light"
	}
	md := &m.md
	if md.r != nil && md.style == style && md.wrap == wrap {
		return md.r
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return nil
	}
	*md = mdRenderer{r: r, style: style, wrap: wrap}
	return r
}

// mdRenderer is a glamour renderer with its style and wrap width.
type mdRenderer struct {
	r     *glamour.TermRenderer
	style string
	wrap  int
}

// renderMarkdown returns the lines of src, rendered with r. If r is nil or
// fails, renderMarkdown returns the lines of src as they are.
func renderMarkdown(r *glamour.TermRenderer, src string) []string {
	if r != nil {
		if out, err := r.Render(src); err == nil {
			src = out
		}
	}
	return strings.Split(strings.TrimRight(src, "\n"), "\n")
}

// markdownSource returns the markdown text of the current file. ok is
// false if the file is not markdown, or if it has no text to render.
func (m *model) markdownSource() (src string, ok bool) {
	switch m.tab {
	case tabChanges:
		f := m.selectedChange()
		if f == nil || !isMarkdown(f.Path) || f.Status == vcs.StatusDeleted {
			return "", false
		}
		src = f.New
	case tabFiles:
		if m.allSel >= len(m.allFiles) || !isMarkdown(m.allFiles[m.allSel].Path) {
			return "", false
		}
		src = m.viewed
	}
	return src, src != ""
}

// rendersNothing reports whether the markdown src renders no text.
func (m *model) rendersNothing(src string) bool {
	for _, l := range renderMarkdown(m.markdown(m.codeWidth()-2), termtext.Plain(src)) {
		if strings.TrimSpace(ansi.Strip(l)) != "" {
			return false
		}
	}
	return true
}

// setMarkdown shows src as rendered markdown. The rendered lines take no
// comments. Press m to see the source.
func (m *model) setMarkdown(p *pane, src string) {
	lines := renderMarkdown(m.markdown(m.codeWidth()-2), termtext.Plain(src))
	p.path = ""
	p.set(lines, make([]diff.Row, len(lines)))
}
