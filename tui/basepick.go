package tui

// The base picker lets the user choose the base branch for the branch scope.

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

// branchTimeout limits how long listing the branches can take.
const branchTimeout = 5 * time.Second

// branchesMsg carries the list of branches for the base picker.
type branchesMsg struct {
	branches []string
	err      error
}

// openBasePicker returns a command that lists the branches for the base
// picker. It returns nil on the PR tab, or when the base was set by a flag.
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

// onBranches opens the base picker with the branches in msg. The base of
// an open pull request comes first, then the default branch. These two and
// the current base are tagged.
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
	// groups holds the PR base, then the default branch, then the rest.
	// Each group keeps the order of msg.branches, newest first.
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

// baseRef returns the ref chosen in the base picker, given the selected
// item it. A valid typed ref, such as a tag, is preferred over the
// first match, but a match that the user moves the cursor to is preferred
// over the typed text. It returns ok false if the ref does not resolve.
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
