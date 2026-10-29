package tui

// The comment list shows every comment. From it the user can jump to a
// comment, edit or delete it, or send or copy all the comments.

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// openComments opens the comment list, or shows a message if there are no
// comments.
func (m *model) openComments() {
	if m.comments.Len() == 0 {
		m.flash("no comments yet")
		return
	}
	ov := newOverlay(overlayComments, "")
	m.setCommentItems(ov)
	m.openOverlay(ov)
}

// setCommentItems fills ov with one item per comment and puts the number
// of comments in its title.
func (m *model) setCommentItems(ov *overlay) {
	ov.title = fmt.Sprintf("Comments (%d)", m.comments.Len())
	var items []overlayItem
	for _, c := range m.comments.All() {
		tag := ""
		if m.commentStale(c) {
			tag = "stale"
		}
		text, _, _ := strings.Cut(c.Text, "\n")
		items = append(items, overlayItem{
			label:   c.Location(),
			detail:  text,
			tag:     tag,
			comment: c.ID,
		})
	}
	ov.setItems(items)
}

// updateComments handles a key press in the comment list. Enter jumps to
// the selected comment. The page and half-page keys move the cursor as in
// the commit picker.
func (m *model) updateComments(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, m.keys
	it := ov.curItem()
	switch {
	case key.Matches(msg, k.Comments, k.Cancel):
		m.closeOverlay()
	case key.Matches(msg, overlayKeys.Choose):
		return m.choose()
	case key.Matches(msg, k.Down):
		ov.move(1)
	case key.Matches(msg, k.Up):
		ov.move(-1)
	case key.Matches(msg, k.PageDown):
		ov.move(itemsPage)
	case key.Matches(msg, k.PageUp):
		ov.move(-itemsPage)
	case key.Matches(msg, k.HalfDown):
		ov.move(itemsHalf)
	case key.Matches(msg, k.HalfUp):
		ov.move(-itemsHalf)
	case key.Matches(msg, k.Send):
		return m.startSend()
	case key.Matches(msg, k.Copy):
		m.copyComments()
	case key.Matches(msg, k.Edit) && it != nil:
		// A comment made on a different diff cannot be edited here.
		c := m.comments.Find(it.comment)
		if c == nil || !m.commentOnCurRev(c) {
			return nil
		}
		row := ov.cur
		m.closeOverlay()
		m.jumpToComment(c)
		m.navigatorFocused = false
		if !m.commentShown(c) {
			m.flash("the line of the comment does not show")
			return nil
		}
		m.scrollToCursor()
		return m.editComment(c, row)
	case key.Matches(msg, k.Delete) && it != nil:
		m.comments.Delete(it.comment)
		m.flash("comment deleted")
		if m.comments.Len() == 0 {
			m.closeOverlay()
			return nil
		}
		m.setCommentItems(ov)
	}
	return nil
}

// commentsHint returns the key hints for the comment list. The edit key
// is listed only if the user can edit the selected comment.
func (m *model) commentsHint() string {
	k := m.keys
	edit := ""
	if it := m.overlay.curItem(); it != nil {
		if c := m.comments.Find(it.comment); c != nil && m.commentOnCurRev(c) {
			edit = fmt.Sprintf(" · %s edit", firstKey(k.Edit))
		}
	}
	return fmt.Sprintf("%s send %d · esc close · %s copy%s · %s delete · enter jump",
		firstKey(k.Send), m.comments.Len(), firstKey(k.Copy), edit,
		firstKey(k.Delete))
}
