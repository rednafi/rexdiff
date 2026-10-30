package tui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

const branchTimeout = 5 * time.Second

type branchesMsg struct {
	branches []string
	err      error
}

func (m *model) openBasePicker() tea.Cmd {
	if m.baseFromFlag || m.tab == tabPR {
		return nil
	}
	ctx, cancel := xctx.WithTimeout(m.ctx, branchTimeout, "branch list")
	repo := m.repo
	return func() tea.Msg {
		defer cancel()
		branches, err := repo.Branches(ctx)
		return branchesMsg{branches: branches, err: err}
	}
}

func (m *model) onBranches(msg branchesMsg) {
	if msg.err != nil {
		m.flash("branch list failed: " + msg.err.Error())
		return
	}
	if m.overlay != nil || m.tab == tabPR {
		return
	}
	ov := newOverlay(overlayBase, "base branch for the branch scope (or type any ref)")
	prBase := ""
	if pr := m.pr.pr; pr != nil && pr.State == vcs.PROpen {
		prBase = pr.Base
	}
	defaultBase := strings.TrimPrefix(m.defaultBase, "origin/")
	current := strings.TrimPrefix(m.base, "origin/")
	// Each group keeps the order of msg.branches, which is newest first.
	var groups [3][]overlayItem
	for _, b := range msg.branches {
		var tags []string
		name, g := strings.TrimPrefix(b, "origin/"), 2
		if name == prBase {
			tags, g = append(tags, "pr base"), 0
		}
		if name == defaultBase {
			tags, g = append(tags, "default"), min(g, 1)
		}
		if name == current {
			tags = append(tags, "current")
		}
		groups[g] = append(groups[g], overlayItem{label: b, id: b, tag: strings.Join(tags, " ")})
	}
	ov.setItems(slices.Concat(groups[:]...))
	m.openOverlay(ov)
}

// baseRef prefers a valid typed ref, such as a tag, over the first match.
// A match that the user moves the cursor to wins over the typed text.
func (m *model) baseRef(it *overlayItem) (ref string, ok bool) {
	ctx, done := m.gitContext("base resolve")
	defer done()
	ref = strings.TrimSpace(m.overlay.input.Value())
	_, err := m.repo.ResolveRev(ctx, ref)
	if it != nil && (ref == "" || err != nil || m.overlay.cur > 0) {
		ref = it.id
		_, err = m.repo.ResolveRev(ctx, ref)
	}
	return ref, ref != "" && err == nil
}
