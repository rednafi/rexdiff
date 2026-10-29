package tui

// The commit picker lets the user choose a commit, or a range of commits,
// for the commits scope.

import (
	"fmt"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

// commitTimeout limits how long listing the commits can take.
const commitTimeout = 10 * time.Second

// commitsMsg carries the commits for the commit picker ov. since is the
// merge base that they come after, or "" for the most recent commits.
type commitsMsg struct {
	ov      *overlay
	commits []vcs.Commit
	since   string
	err     error
}

// openCommitPicker opens the commit picker and returns a command that
// lists its commits: the first-parent commits since the merge base with
// the base branch, newest first, up to vcs.MaxCommits. If HEAD is the
// merge base, or there is none, it lists the most recent commits instead.
// Listing can take a while in a large repository, so the picker shows that
// it is loading until the commits arrive, and closing it stops the
// listing. It does nothing on the PR tab.
func (m *model) openCommitPicker() tea.Cmd {
	if m.tab == tabPR {
		return nil
	}
	ctx, cancel := xctx.WithTimeout(m.ctx, commitTimeout, "commit list")
	ov := newOverlay(overlayCommits, "commits")
	ov.loading, ov.stop = true, cancel
	m.openOverlay(ov)
	repo, base := m.repo, m.base
	return func() tea.Msg {
		defer cancel()
		since, err := repo.MergeBaseCommit(ctx, base)
		if head, headErr := repo.ResolveRev(ctx, "HEAD"); err != nil || headErr != nil || since == head {
			since = ""
		}
		commits, err := repo.Commits(ctx, since)
		return commitsMsg{ov: ov, commits: commits, since: since, err: err}
	}
}

// onCommits fills the commit picker with the commits in msg. The cursor
// and range anchor start on the current choice. If the picker that asked
// for the commits has closed, onCommits does nothing, so that a slow list
// does not open the picker again after the user has moved on.
func (m *model) onCommits(msg commitsMsg) {
	ov := msg.ov
	if m.overlay != ov || m.tab == tabPR {
		return
	}
	ov.loading, ov.stop = false, nil
	if msg.err != nil {
		m.closeOverlay()
		m.flash("commit list failed: " + msg.err.Error())
		return
	}
	commits := msg.commits
	title := fmt.Sprintf("commits · last %d", vcs.LastCommits)
	switch {
	case msg.since != "" && len(commits) >= vcs.MaxCommits:
		title = fmt.Sprintf("commits · newest %d over %s", len(commits), m.base)
	case msg.since != "":
		title = fmt.Sprintf("commits · %d over %s", len(commits), m.base)
	}
	ov.title = title
	var items []overlayItem
	for _, c := range commits {
		items = append(items, overlayItem{
			label:  c.Short + "  " + c.Subject,
			detail: c.Author + ", " + c.When,
			id:     c.SHA,
		})
	}
	ov.setItems(items)
	isSHA := func(sha string) func(overlayItem) bool {
		return func(it overlayItem) bool { return it.id == sha }
	}
	newest := slices.IndexFunc(items, isSHA(m.newest))
	oldest := slices.IndexFunc(items, isSHA(m.oldest))
	if m.newest != "" && newest >= 0 && oldest >= 0 {
		ov.cur = newest
		if oldest > newest {
			ov.rangeStart = oldest
		}
	}
}

// updateCommitPicker handles a key press in the commit picker. The Select
// key sets or clears the start of a range. Esc clears the range start if
// there is one, and otherwise closes the picker.
func (m *model) updateCommitPicker(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, m.keys
	switch {
	case key.Matches(msg, overlayKeys.Close) && ov.rangeStart >= 0:
		ov.rangeStart = -1
	case key.Matches(msg, overlayKeys.Close):
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
	case key.Matches(msg, k.Select) && ov.cur < len(ov.shown):
		if ov.rangeStart == ov.shown[ov.cur] {
			ov.rangeStart = -1
		} else {
			ov.rangeStart = ov.shown[ov.cur]
		}
	}
	return nil
}

// commitHint returns the key hints for the commit picker.
func (m *model) commitHint() string {
	ov, k := m.overlay, m.keys
	if ov.loading || len(ov.shown) == 0 {
		return "esc cancel"
	}
	open, esc := "open", "cancel"
	if ov.rangeStart >= 0 {
		esc = "clear"
		if n := max(ov.rangeStart, ov.cur) - min(ov.rangeStart, ov.cur) + 1; n > 1 {
			open = fmt.Sprintf("open %d", n)
		}
	}
	return fmt.Sprintf("enter %s · esc %s · %s %s move · %s select",
		open, esc, firstKey(k.Down), firstKey(k.Up), firstKey(k.Select))
}

// commitRange returns the oldest and newest commits chosen in the commit
// picker, given the selected item it. Without a range start, both are the
// commit of it. Otherwise they are the ends of the range from the start
// to the cursor.
func (ov *overlay) commitRange(it *overlayItem) (oldest, newest string) {
	if ov.rangeStart < 0 {
		return it.id, it.id
	}
	a, b := ov.rangeStart, ov.shown[ov.cur]
	return ov.items[max(a, b)].id, ov.items[min(a, b)].id
}
