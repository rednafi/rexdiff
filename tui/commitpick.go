package tui

import (
	"fmt"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

const commitTimeout = 10 * time.Second

type commitsMsg struct {
	ov      *overlay
	commits []vcs.Commit
	since   string // merge base, or "" for the most recent commits
	err     error
}

// Listing can be slow in a large repository. So the picker shows a loading
// state, and closing it stops the listing.
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

// onCommits does nothing if the picker that asked for the commits has
// closed. This stops a slow list from opening the picker again.
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

func (ov *overlay) commitRange(it *overlayItem) (oldest, newest string) {
	if ov.rangeStart < 0 {
		return it.id, it.id
	}
	a, b := ov.rangeStart, ov.shown[ov.cur]
	return ov.items[max(a, b)].id, ov.items[min(a, b)].id
}
