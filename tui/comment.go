package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
)

type targetLine struct {
	side diff.Side
	line int
}

func (m *model) targetAt(i int, prefer diff.Side) *targetLine {
	p := m.curPane()
	if p.path == "" || i < 0 || i >= len(p.rows) {
		return nil
	}
	r := p.rows[i]
	switch {
	case prefer == diff.SideOld && r.Old > 0 && r.Kind != diff.KindEqual:
		return &targetLine{diff.SideOld, r.Old}
	case r.New > 0:
		return &targetLine{diff.SideNew, r.New}
	case r.Old > 0:
		return &targetLine{diff.SideOld, r.Old}
	}
	return nil
}

func (m *model) curChangedFile() *changedFile {
	if m.changes.cur >= len(m.changes.files) {
		return nil
	}
	return &m.changes.files[m.changes.cur]
}

func (m *model) contentFor(path string, side diff.Side) string {
	if f := m.curChangedFile(); m.tab == tabChanges && f != nil && f.Path == path {
		if side == diff.SideOld {
			return f.OldText
		}
		return f.NewText
	}
	if m.tab == tabAllFiles && path == m.allFiles.shownPath {
		return m.allFiles.shownText
	}
	return ""
}

func (m *model) diffRev() string {
	if m.scope == scopeCommits {
		return m.oldest + ".." + m.newest
	}
	return ""
}

func (m *model) anchorOf(t tab) diff.Anchor {
	if t == tabChanges {
		return diff.Anchor{InDiff: true, Rev: m.diffRev()}
	}
	return diff.Anchor{}
}

func (m *model) curAnchor() diff.Anchor { return m.anchorOf(m.tab) }

func (m *model) commentOnCurRev(c *diff.Comment) bool {
	return !c.Anchor.InDiff || c.Anchor.Rev == m.diffRev()
}

// commentStale reports whether the lines of c may no longer hold its code.
// Comments do not follow moved code. It returns false for a comment on a
// diff that is not shown.
func (m *model) commentStale(c *diff.Comment) bool {
	if c.Anchor.InDiff {
		i := slices.IndexFunc(m.changes.files, func(f changedFile) bool { return f.Path == c.Path })
		switch {
		case i < 0:
			return true
		case c.Anchor.Rev != m.diffRev() || m.changes.files[i].Err != nil:
			return false
		case c.Side == diff.SideOld:
			return c.Moved(m.changes.files[i].OldText)
		}
		return c.Moved(m.changes.files[i].NewText)
	}
	// Check the file as it is now, not as the Files tab last showed it,
	// so that the answer is right on every tab.
	full := filepath.Join(m.repo.Root(), c.Path)
	info, err := os.Stat(full)
	if err != nil {
		_, err := os.Lstat(full) // a broken symbolic link still exists
		return err != nil
	}
	f, ok := m.commentFiles[c.Path]
	if !ok || f.size != info.Size() || !f.modTime.Equal(info.ModTime()) {
		text, err := m.repo.ReadFile(c.Path)
		f = fileCopy{size: info.Size(), modTime: info.ModTime(), text: text, err: err}
		if m.commentFiles == nil {
			m.commentFiles = make(map[string]fileCopy)
		}
		m.commentFiles[c.Path] = f
	}
	return f.err == nil && c.Moved(f.text)
}

type fileCopy struct {
	size    int64
	modTime time.Time
	text    string
	err     error
}

// jumpToNearLine is for when the pane shows the file of c but not its
// line, for example because the file is now shorter.
func (m *model) jumpToNearLine(c *diff.Comment) {
	p := m.curPane()
	hasSide := func(r diff.Row) bool { return r.Line(c.Side) > 0 }
	if p.path != c.Path || p.rowOf(c.Side, c.Start) >= 0 || !slices.ContainsFunc(p.rows, hasSide) {
		return
	}
	p.cur = p.lineStart(lineRow(p.rows, c.Side, c.Start))
}

func (m *model) curComment() *diff.Comment {
	p := m.curPane()
	target := m.targetAt(p.cur, diff.SideNew)
	if target == nil {
		return nil
	}
	return m.commentOn(p.cur, target)
}

func (p *pane) selRange() (lo, hi int) {
	if p.selStart < 0 {
		return p.cur, p.lineEnd(p.cur)
	}
	return p.span(p.selStart, p.cur)
}

// selRows returns the source rows of the selection. In rendered Markdown,
// it includes the deleted lines between the selected blocks.
func (p *pane) selRows() []diff.Row {
	lo, hi := p.selRange()
	if p.srcRows == nil {
		return p.rows[lo : hi+1]
	}
	first, last := p.rows[p.lineStart(lo)].New, p.rows[hi].New
	i := slices.IndexFunc(p.srcRows, func(r diff.Row) bool { return r.New == first })
	j := slices.IndexFunc(p.srcRows, func(r diff.Row) bool { return r.New == last })
	return p.srcRows[i : j+1]
}

func (p *pane) span(a, b int) (lo, hi int) {
	return min(a, b), p.lineEnd(max(a, b))
}

func (m *model) openComment(prefer diff.Side) tea.Cmd {
	p := m.curPane()
	target := m.targetAt(p.cur, prefer)
	if target == nil {
		return nil
	}
	c := diff.NewComment(
		p.path,
		target.side,
		p.selRows(),
		m.contentFor(p.path, diff.SideOld),
		m.contentFor(p.path, diff.SideNew),
	)
	c.Anchor = m.curAnchor()
	m.draft = &draft{Comment: c, itemRow: -1}
	m.sizeCommentInput()
	m.commentInput.SetValue("")
	m.mode = modeComment
	// The box opens below the cursor, so move the cursor to the last
	// commented line.
	jumpToLine(p, c.Side, c.End)
	return m.commentInput.Focus()
}

// editComment opens the comment list again at itemRow when the box
// closes, if itemRow is 0 or more.
func (m *model) editComment(c *diff.Comment, itemRow int) tea.Cmd {
	m.draft = &draft{Comment: *c, editID: c.ID, itemRow: itemRow}
	m.sizeCommentInput()
	m.commentInput.SetValue(c.Text)
	m.mode = modeComment
	jumpToLine(m.curPane(), c.Side, c.End)
	return m.commentInput.Focus()
}

func (m *model) commentShown(c *diff.Comment) bool {
	p := m.curPane()
	return m.curAnchor() == c.Anchor && p.path == c.Path && p.rowOf(c.Side, c.End) >= 0
}

func (m *model) updateComment(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.SaveComment):
		d := m.draft
		text := strings.TrimSpace(m.commentInput.Value())
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
	m.commentInput, cmd = m.commentInput.Update(msg)
	m.scrollToCursor()
	return cmd
}

func (m *model) closeComment() {
	row := m.draft.itemRow
	m.mode, m.draft = modeNormal, nil
	m.curPane().selStart = -1
	m.commentInput.Blur()
	m.reloadIfDue()
	if row >= 0 && m.comments.Len() > 0 {
		m.openComments()
		m.overlay.cur = min(row, len(m.overlay.shown)-1)
	}
}

func nearestNewLine(rows []diff.Row, i int) int {
	if j := slices.IndexFunc(rows[i:], func(r diff.Row) bool { return r.New > 0 }); j >= 0 {
		return rows[i+j].New
	}
	for _, r := range slices.Backward(rows[:i]) {
		if r.New > 0 {
			return r.New
		}
	}
	return 1
}

func (m *model) onEdit() tea.Cmd {
	if m.curPane().selStart >= 0 {
		return nil
	}
	if c := m.curComment(); c != nil {
		return m.editComment(c, -1)
	}
	p := m.curPane()
	if p.path == "" {
		return nil
	}
	return m.launchEditor(p.path, nearestNewLine(p.rows, p.cur))
}
