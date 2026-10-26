package tui

// The comment list shows all the comments. The user can jump to a
// comment, edit it, delete it, or send or copy all the comments.

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// openCommentList lists the comments, to jump to one, edit one, delete
// one, or send or copy them all.
func (m *model) openCommentList() {
	if m.comments.Len() == 0 {
		m.flash("no comments yet")
		return
	}
	ov := newOverlay(overlayComments, "")
	m.fillCommentList(ov)
	m.open(ov)
}

// fillCommentList sets the comments as the items of ov, and the count in
// the title, as in herdr-reviewr.
func (m *model) fillCommentList(ov *overlay) {
	ov.title = fmt.Sprintf("Comments (%d)", m.comments.Len())
	var items []pickItem
	for _, c := range m.comments.All() {
		tag := ""
		if m.stale(c) {
			tag = "stale"
		}
		text, _, _ := strings.Cut(c.Text, "\n")
		items = append(items, pickItem{
			label:   c.Location(),
			detail:  text,
			tag:     tag,
			comment: c.ID,
		})
	}
	ov.setItems(items)
}

// updateCommentList handles a key in the comment list. The keys are those
// of herdr-reviewr. Enter also jumps to the comment.
func (m *model) updateCommentList(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, m.keys
	it := ov.selected()
	switch {
	case key.Matches(msg, k.Comments, k.Cancel):
		m.closeOverlay()
	case key.Matches(msg, overlayKeys.Choose):
		return m.chooseOverlay()
	case key.Matches(msg, k.Down):
		ov.move(1)
	case key.Matches(msg, k.Up):
		ov.move(-1)
	case key.Matches(msg, k.Send):
		return m.startSend()
	case key.Matches(msg, k.Copy):
		m.copyComments()
	case key.Matches(msg, k.Edit) && it != nil:
		// As in herdr-reviewr, a comment on another diff takes no edit.
		c := m.comments.Find(it.comment)
		if c == nil || !m.revCurrent(c) {
			return nil
		}
		row := ov.cur
		m.closeOverlay()
		m.jumpToComment(c)
		m.focusList = false
		if !m.showsLine(c) {
			m.flash("the line of the comment does not show")
			return nil
		}
		m.ensureVisible()
		return m.editComment(c, row)
	case key.Matches(msg, k.Delete) && it != nil:
		m.comments.Remove(it.comment)
		m.flash("comment deleted")
		if m.comments.Len() == 0 {
			m.closeOverlay()
			return nil
		}
		m.fillCommentList(ov)
	}
	return nil
}

// listHint returns the keys of the comment list, as the footer of
// herdr-reviewr shows them. The edit key shows only if the selected
// comment takes an edit.
func (m *model) listHint() string {
	k := m.keys
	edit := ""
	if it := m.overlay.selected(); it != nil {
		if c := m.comments.Find(it.comment); c != nil && m.revCurrent(c) {
			edit = fmt.Sprintf(" · %s edit", firstKey(k.Edit))
		}
	}
	return fmt.Sprintf("%s send %d · esc close · %s copy%s · %s delete · enter jump",
		firstKey(k.Send), m.comments.Len(), firstKey(k.Copy), edit,
		firstKey(k.Delete))
}
