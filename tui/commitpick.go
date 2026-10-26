package tui

// The commit picker picks a commit or a range of commits for the commits
// scope.

import (
	"fmt"
	"slices"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/vcs"
)

// openCommitPicker lets the user pick a commit or a range of commits for
// the commits scope, as in herdr-reviewr. It lists the first-parent
// commits after the fork point of the base, the newest first. On the base
// itself, or with no fork point, it lists the last commits. The current
// pick has the cursor and the anchor.
func (m *model) openCommitPicker() {
	if m.tab == tabPR {
		return
	}
	since, err := m.repo.MergeBaseCommit(m.ctx, m.base)
	if head, herr := m.repo.Resolve(m.ctx, "HEAD"); err != nil || herr != nil || since == head {
		since = ""
	}
	commits, err := m.repo.Commits(m.ctx, since)
	if err != nil {
		m.flash("commit list failed: " + err.Error())
		return
	}
	title := fmt.Sprintf("commits · last %d", vcs.LastCommits)
	if since != "" {
		title = fmt.Sprintf("commits · %d over %s", len(commits), m.base)
	}
	ov := newOverlay(overlayCommits, title)
	var items []pickItem
	for _, c := range commits {
		items = append(items, pickItem{
			label:  c.Short + "  " + c.Subject,
			detail: c.Author + ", " + c.When,
			id:     c.SHA,
		})
	}
	ov.setItems(items)
	isSHA := func(sha string) func(pickItem) bool {
		return func(it pickItem) bool { return it.id == sha }
	}
	newest := slices.IndexFunc(items, isSHA(m.to))
	oldest := slices.IndexFunc(items, isSHA(m.from))
	if m.to != "" && newest >= 0 && oldest >= 0 {
		ov.cur = newest
		if oldest > newest {
			ov.mark = oldest
		}
	}
	m.open(ov)
}

// updateCommitPicker handles a key in the commit picker, as herdr-reviewr
// does. The key of Select sets or clears the anchor of a range. Esc clears
// the anchor, else it closes the picker.
func (m *model) updateCommitPicker(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, m.keys
	switch {
	case key.Matches(msg, overlayKeys.Close) && ov.mark >= 0:
		ov.mark = -1
	case key.Matches(msg, overlayKeys.Close):
		m.closeOverlay()
	case key.Matches(msg, overlayKeys.Choose):
		return m.chooseOverlay()
	case key.Matches(msg, k.Down):
		ov.move(1)
	case key.Matches(msg, k.Up):
		ov.move(-1)
	case key.Matches(msg, k.PageDown):
		ov.move(pickerPage)
	case key.Matches(msg, k.PageUp):
		ov.move(-pickerPage)
	case key.Matches(msg, k.HalfDown):
		ov.move(pickerHalf)
	case key.Matches(msg, k.HalfUp):
		ov.move(-pickerHalf)
	case key.Matches(msg, k.Select) && ov.cur < len(ov.shown):
		if ov.mark == ov.shown[ov.cur] {
			ov.mark = -1
		} else {
			ov.mark = ov.shown[ov.cur]
		}
	}
	return nil
}

// commitHint returns the keys of the commit picker, as the footer of
// herdr-reviewr shows them.
func (m *model) commitHint() string {
	ov, k := m.overlay, m.keys
	if len(ov.shown) == 0 {
		return "esc cancel"
	}
	open, esc := "open", "cancel"
	if ov.mark >= 0 {
		esc = "clear"
		if n := max(ov.mark, ov.cur) - min(ov.mark, ov.cur) + 1; n > 1 {
			open = fmt.Sprintf("open %d", n)
		}
	}
	return fmt.Sprintf("enter %s · esc %s · %s %s move · %s select",
		open, esc, firstKey(k.Down), firstKey(k.Up), firstKey(k.Select))
}

// commitRange returns the oldest and the newest commit that the commit
// picker picks with item it. That is one commit, or the range from the
// mark to it. The list shows the newest commit first.
func (o *overlay) commitRange(it *pickItem) (oldest, newest string) {
	if o.mark < 0 {
		return it.id, it.id
	}
	a, b := o.mark, o.shown[o.cur]
	return o.items[max(a, b)].id, o.items[min(a, b)].id
}
