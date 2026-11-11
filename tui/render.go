package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/vcs"
)

const gutter = 1

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
		// The pane has no path for a binary file, but the title names it.
		if e := m.curEntry(); e != nil {
			name = e.Path
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

func cutStart(p string, room int) string {
	p = termtext.Quote(p)
	if ansi.StringWidth(p) > room {
		p = "…" + lastColumns(p, room-1)
	}
	return p
}

func (m *model) codeWidth() int { return m.mainWidth() - gutter }

// rerender keeps the cursor and the selection on the same source lines. A
// cursor on a header row keeps its distance above the next source line.
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

func (m *model) sizeCommentInput() { m.commentInput.SetWidth(max(m.mainWidth()-8, 20)) }

// minSplitWidth allows two halves of 60 columns with line numbers, and a
// separator column.
const minSplitWidth = 135

func (m *model) splitAllowed() bool {
	if m.tab != tabChanges {
		return false
	}
	f := m.curChangedFile()
	return f != nil && f.Err == nil && !m.renderedShown(f) &&
		f.OldText != "" && f.NewText != "" && f.OldText != f.NewText
}

// splitShown keeps the split choice, so the split comes back when the
// pane is wide again.
func (m *model) splitShown() bool {
	return m.split && m.codeWidth() >= minSplitWidth && m.splitAllowed()
}

func (m *model) renderedShown(f *changedFile) bool {
	return m.rendered && isMarkdown(f.Path) && f.Status != vcs.StatusDeleted && m.renderableMarkdown(f.NewText)
}

func (m *model) fileOptions(scrollX int) diff.FileOptions {
	return diff.FileOptions{Width: m.codeWidth(), ScrollX: scrollX, Wrap: m.wrap, Cache: &m.cache}
}

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

func isMarkdown(path string) bool {
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".markdown")
}

const (
	noteBinary = "binary file · no line comments"
	noteTooBig = "file too large to show"
)

func fileNote(err error) string {
	switch {
	case errors.Is(err, vcs.ErrBinary):
		return noteBinary
	case errors.Is(err, vcs.ErrTooBig):
		return noteTooBig
	}
	return ""
}

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
		// A rename without edits, or a file mode change.
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

func (p *pane) setNote(note string) {
	p.set([]string{dimStyle.Render("  " + note)}, []diff.Row{{}})
}

// renderAllFiles reads the file again only if the selection changed or
// the last read failed.
func (m *model) renderAllFiles() {
	af := &m.allFiles
	if e := m.curEntry(); e != nil && e.Path == af.shownPath && af.shownErr == nil {
		m.showFile(af.shownPath, af.shownText, nil)
		return
	}
	af.shownPath = "" // no file has the path "", so reloadShownFile always renders
	m.reloadShownFile()
}

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
		// Each change of the files reads the file again. Log each error
		// only once.
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
