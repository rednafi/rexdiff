package tui

// This file turns files into the lines of a pane: diffs, the file viewer,
// and markdown.

import (
	"errors"
	"strings"

	"charm.land/glamour/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// gutter is the width of the column for the cursor and the marks, at the
// left of the main pane.
const gutter = 2

// listWidth returns the width of the file list, or 0 if the list does not
// show.
func (m *model) listWidth() int {
	if m.listHidden || m.tab == tabPR {
		return 0
	}
	// The list never takes more than a third of a narrow pane.
	return min(m.listW, max(m.w/3, minListWidth))
}

// mainWidth is the width of the main pane, with its gutter.
func (m *model) mainWidth() int {
	w := m.w
	if lw := m.listWidth(); lw > 0 {
		w -= lw + sepWidth
	}
	return max(w, 24)
}

// codeWidth is the width of the main pane, without its gutter.
func (m *model) codeWidth() int { return m.mainWidth() - gutter }

// rerender renders the current pane again, for example after a change of
// the layout, the wrap, or the width. The cursor and the selection stay on
// their source lines. A cursor on a header stays above the first line
// below the header.
func (m *model) rerender() {
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

// diffOptions returns the options to render a diff with sideways scroll
// xoff.
func (m *model) diffOptions(xoff int) diff.Options {
	return diff.Options{
		Width:  m.codeWidth(),
		Offset: xoff,
		Wrap:   m.wrap,
		Split:  m.split,
		Signs:  m.cfg.DiffSigns,
		Theme: diff.Theme{
			Background: m.bg,
			Foreground: m.fg,
			Green:      m.green,
			Red:        m.red,
		},
	}
}

// isMarkdown reports whether path names a markdown file.
func isMarkdown(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".markdown")
}

// renderChanges shows the selected file in the Changes tab.
func (m *model) renderChanges() {
	p := &m.changes
	f := m.changedFile()
	if f == nil {
		p.path = ""
		p.set(nil, nil)
		return
	}
	p.path = f.Path
	switch {
	case f.Binary:
		p.setNote("binary file, not diffed")
	case f.TooBig:
		p.setNote("file over 2 MB or 50,000 lines, not diffed")
	case f.Old == f.New:
		// A rename without changes, or a change of the file mode.
		note := "no change to the content"
		if f.From != "" {
			note = "renamed from " + diff.Quote(f.From) + ", no change to the content"
		}
		p.setNote(note)
	case m.rendered && isMarkdown(f.Path) && f.Status != vcs.StatusDeleted:
		m.setMarkdown(p, f.New)
	default:
		o := m.diffOptions(p.xoff)
		o.Show = func(fd diff.Fold) bool { return m.showFold(f.Path, fd) }
		p.setDiff(diff.Render(f.Path, f.Old, f.New, o))
	}
}

// setNote shows a dim note on one row that shows no source line. The
// pane keeps its path.
func (p *pane) setNote(note string) {
	p.set([]string{dimStyle.Render("  " + note)}, []diff.Row{{}})
}

// renderViewer shows the selected file in the Files tab, even if the file
// did not change.
func (m *model) renderViewer() {
	m.viewer.path = "" // no file has the path "", so refreshViewer renders
	m.refreshViewer()
}

// refreshViewer shows the selected file in the Files tab if the file
// changed.
func (m *model) refreshViewer() {
	p := &m.viewer
	if m.allSel >= len(m.allFiles) {
		p.path = ""
		p.set(nil, nil)
		return
	}
	path := m.allFiles[m.allSel].Path
	content, err := m.repo.ReadFile(path)
	if content == m.viewed && p.path == path {
		return
	}
	m.viewed = content
	p.path = path
	switch {
	case errors.Is(err, vcs.ErrBinary):
		p.path = ""
		p.set([]string{dimStyle.Render("  binary file")}, nil)
	case errors.Is(err, vcs.ErrTooBig):
		p.path = ""
		p.set([]string{dimStyle.Render("  file over 2 MB, not shown")}, nil)
	case m.rendered && isMarkdown(path):
		m.setMarkdown(p, content)
	default:
		p.setDiff(diff.RenderFile(path, content, m.diffOptions(p.xoff)))
	}
}

// markdown returns a glamour renderer for the terminal theme, or nil. The
// renderer wraps at wrap columns.
func (m *model) markdown(wrap int) *glamour.TermRenderer {
	style := "dark"
	if m.bg != nil && !m.dark {
		style = "light"
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(wrap),
	)
	if err != nil {
		return nil
	}
	return r
}

// renderMarkdown renders src with r. If r is nil or fails, it returns src.
func renderMarkdown(r *glamour.TermRenderer, src string) []string {
	if r != nil {
		if out, err := r.Render(src); err == nil {
			src = out
		}
	}
	return strings.Split(strings.TrimRight(src, "\n"), "\n")
}

// setMarkdown shows src as rendered markdown. The rendered lines take no
// comments. Press m to see the source.
func (m *model) setMarkdown(p *pane, src string) {
	lines := renderMarkdown(m.markdown(m.codeWidth()-2), diff.Plain(src))
	p.path = ""
	p.set(lines, make([]diff.Row, len(lines)))
}
