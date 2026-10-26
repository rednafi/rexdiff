package tui

// The base picker picks the base of the branch scope.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/vcs"
)

// branchTimeout is the longest time of the branch list.
const branchTimeout = 5 * time.Second

// branchesMsg has the branch list of the base picker.
type branchesMsg struct {
	branches []string
	err      error
}

// openBasePicker lists the branches for the base picker. The list does
// nothing on the PR tab, or if the base comes from the flag.
func (m *model) openBasePicker() tea.Cmd {
	if m.baseFlag || m.tab == tabPR {
		return nil
	}
	ctx, cancel := context.WithCancelCause(m.ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, branchTimeout,
		fmt.Errorf("branch list: %v timeout: %w", branchTimeout, context.DeadlineExceeded))
	repo := m.repo
	return func() tea.Msg {
		defer cancelTimeout()
		defer cancel(fmt.Errorf("branch list: done: %w", context.Canceled))
		branches, err := repo.Branches(ctx)
		return branchesMsg{branches: branches, err: err}
	}
}

// onBranches opens the base picker with the branch list of msg. The
// current base has a tag. As in herdr-reviewr, the base of an open pull
// request and the default branch come first, with a tag.
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
	// groups has the PR base, then the default branch, then the others.
	// Each group keeps the newest first.
	var groups [3][]pickItem
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
		groups[g] = append(groups[g], pickItem{label: b, id: b, tag: strings.Join(tags, " ")})
	}
	ov.setItems(slices.Concat(groups[:]...))
	m.open(ov)
}

// baseRef returns the ref that the base picker picks, with the selected
// item it. A typed ref, for example a tag, wins over the first match. A
// match that the user moves to wins over the typed text. If the ref is
// not valid, ok is false.
func (m *model) baseRef(it *pickItem) (ref string, ok bool) {
	ref = strings.TrimSpace(m.overlay.input.Value())
	_, err := m.repo.Resolve(m.ctx, ref)
	if it != nil && (ref == "" || err != nil || m.overlay.cur > 0) {
		ref = it.id
		_, err = m.repo.Resolve(m.ctx, ref)
	}
	return ref, ref != "" && err == nil
}
