package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
)

// targetLine is the source line that a comment on a row attaches to.
type targetLine struct {
	side diff.Side
	line int
}

// target returns the line that a comment on row i of the current pane
// attaches to, or nil. If the row changes a line on side pref, target uses
// that side. Else target uses the new side. Thus a comment on an unchanged
// line is on the new side.
func (m *model) target(i int, pref diff.Side) *targetLine {
	p := m.cur()
	if p.path == "" || i < 0 || i >= len(p.rows) {
		return nil
	}
	r := p.rows[i]
	switch {
	case pref == diff.SideOld && r.Old > 0 && r.Kind != diff.KindEqual:
		return &targetLine{diff.SideOld, r.Old}
	case r.New > 0:
		return &targetLine{diff.SideNew, r.New}
	case r.Old > 0:
		return &targetLine{diff.SideOld, r.Old}
	}
	return nil
}

// selectedChange returns the selected file of the Changes tab, or nil.
func (m *model) selectedChange() *changedFile {
	if m.changeSel >= len(m.changeFiles) {
		return nil
	}
	return &m.changeFiles[m.changeSel]
}

// contentFor returns the text of side s of the file at path, as the pane
// shows it. Thus a comment quotes the lines on the screen, and not a newer
// version of the file. A file that the pane does not show gives "".
func (m *model) contentFor(path string, s diff.Side) string {
	if f := m.selectedChange(); m.tab == tabChanges && f != nil && f.Path == path {
		if s == diff.SideOld {
			return f.Old
		}
		return f.New
	}
	if m.tab == tabFiles && path == m.viewedPath {
		return m.viewed
	}
	return ""
}

// diffRev returns the revision of the diff in the Changes tab, for
// [diff.Anchor]. That is the picked commits, or "" for the working tree.
func (m *model) diffRev() string {
	if m.scope == scopeCommits {
		return m.from + ".." + m.to
	}
	return ""
}

// anchorOf returns the anchor of a comment made in tab t. As in
// herdr-reviewr, a comment on a file view is always on the working tree.
func (m *model) anchorOf(t tab) diff.Anchor {
	if t == tabChanges {
		return diff.Anchor{InDiff: true, Rev: m.diffRev()}
	}
	return diff.Anchor{}
}

// anchor returns the anchor of a comment made in the current tab.
func (m *model) anchor() diff.Anchor { return m.anchorOf(m.tab) }

// revCurrent reports whether the scope shows the diff that c is made on.
// Only then can the user edit c from the comment list.
func (m *model) revCurrent(c *diff.Comment) bool {
	return !c.Anchor.InDiff || c.Anchor.Rev == m.diffRev()
}

// stale reports whether the lines of c can have moved. A comment on a diff
// is stale if its file leaves the changes. A comment on a file view is
// stale if its file is gone.
func (m *model) stale(c *diff.Comment) bool {
	if c.Anchor.InDiff {
		return !slices.ContainsFunc(m.changeFiles, func(f changedFile) bool { return f.Path == c.Path })
	}
	_, err := os.Lstat(filepath.Join(m.repo.Root(), c.Path))
	return err != nil
}

// commentAtCursor returns the comment on the cursor line, or nil.
func (m *model) commentAtCursor() *diff.Comment {
	p := m.cur()
	t := m.target(p.cur, diff.SideNew)
	if t == nil {
		return nil
	}
	return m.comments.At(m.anchor(), p.path, t.side, t.line)
}

// selection returns the first and the last row of the visual selection.
// With no selection, it returns the rows of the cursor line.
func (p *pane) selection() (lo, hi int) {
	if p.anchor < 0 {
		return p.cur, p.end(p.cur)
	}
	return p.span(p.anchor, p.cur)
}

// span returns the first and the last row of the lines between rows a and
// b, in either order.
func (p *pane) span(a, b int) (lo, hi int) {
	return min(a, b), p.end(max(a, b))
}

// startComment opens the comment box for a new comment on the selection,
// or on the cursor line. A line can have more than one comment.
func (m *model) startComment(pref diff.Side) tea.Cmd {
	p := m.cur()
	t := m.target(p.cur, pref)
	if t == nil {
		return nil
	}
	lo, hi := p.selection()
	c := diff.NewComment(
		p.path,
		t.side,
		p.rows[lo:hi+1],
		m.contentFor(p.path, diff.SideOld),
		m.contentFor(p.path, diff.SideNew),
	)
	c.Anchor = m.anchor()
	m.draft = &draft{Comment: c, listRow: -1}
	m.sizeEditor()
	m.editor.SetValue("")
	m.mode = modeComment
	// Put the cursor on the last line of the range, so that the box opens
	// below the range.
	cursorToLine(p, c.Side, c.End)
	return m.editor.Focus()
}

// editComment opens the comment box with the text of c. After the edit,
// the comment list opens at listRow, if listRow is 0 or more.
func (m *model) editComment(c *diff.Comment, listRow int) tea.Cmd {
	m.draft = &draft{Comment: *c, editID: c.ID, listRow: listRow}
	m.sizeEditor()
	m.editor.SetValue(c.Text)
	m.mode = modeComment
	cursorToLine(m.cur(), c.Side, c.End)
	return m.editor.Focus()
}

// showsLine reports whether the current pane shows the last line of c,
// with the anchor of c.
func (m *model) showsLine(c *diff.Comment) bool {
	p := m.cur()
	return m.anchor() == c.Anchor && p.path == c.Path && p.rowOf(c.Side, c.End) >= 0
}

// updateComment handles a key while the comment box is open.
func (m *model) updateComment(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Save):
		// As in herdr-reviewr, blank text cancels. An edit keeps the
		// comment, its ID, and its place in the list.
		d := m.draft
		text := strings.TrimSpace(m.editor.Value())
		edited := m.comments.Find(d.editID)
		switch {
		case text == "":
		case edited != nil:
			edited.Text = text
			m.flash("comment updated")
		default:
			c := d.Comment
			c.Text = text
			m.comments.Add(c)
			m.flash("comment added")
		}
		m.closeComment()
		return nil
	case key.Matches(msg, m.keys.Cancel):
		m.closeComment()
		return nil
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	m.ensureVisible()
	return cmd
}

// closeComment closes the comment box and clears the selection. Then it
// does the reload that waits for the box. After an edit from the comment
// list, it opens the list again.
func (m *model) closeComment() {
	row := m.draft.listRow
	m.mode, m.draft = modeNormal, nil
	m.cur().anchor = -1
	m.editor.Blur()
	m.reloadDue()
	if row >= 0 && m.comments.Len() > 0 {
		m.openCommentList()
		m.overlay.cur = min(row, len(m.overlay.shown)-1)
	}
}

// nearNewLine returns the new line of row i. A row with no new line, such
// as a deleted line, gives the next new line below it. If there is none, it
// gives the last new line above it. If there is none, it gives 1.
func nearNewLine(rows []diff.Row, i int) int {
	for _, r := range rows[i:] {
		if r.New > 0 {
			return r.New
		}
	}
	for _, r := range slices.Backward(rows[:i]) {
		if r.New > 0 {
			return r.New
		}
	}
	return 1
}

// editOrOpen edits the comment at the cursor. If there is no comment, it
// opens the file in an editor at the cursor line. As in herdr-reviewr, it
// does nothing while a selection is active, so the selection stays.
func (m *model) editOrOpen() tea.Cmd {
	if m.cur().anchor >= 0 {
		return nil
	}
	if c := m.commentAtCursor(); c != nil {
		return m.editComment(c, -1)
	}
	p := m.cur()
	if p.path == "" {
		return nil
	}
	return m.openEditor(p.path, nearNewLine(p.rows, p.cur))
}
