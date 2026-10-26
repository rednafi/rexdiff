package tui

// The base picker picks the base of the branch scope.

import (
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/vcs"
)

// openBasePicker lets the user pick the base of the branch scope. The
// current base has a tag. As in herdr-reviewr, the base of an open pull
// request and the default branch come first, with a tag. The picker does
// nothing on the PR tab, or if the base comes from the flag.
func (m *model) openBasePicker() {
	if m.baseFlag || m.tab == tabPR {
		return
	}
	ov := newOverlay(overlayBase, "base branch for the branch scope (or type any ref)")
	branches, err := m.repo.Branches(m.ctx)
	if err != nil {
		m.flash("branch list failed: " + err.Error())
		return
	}
	prBase := ""
	if pr := m.pr.pr; pr != nil && pr.State == vcs.PROpen {
		prBase = pr.Base
	}
	defaultBase := strings.TrimPrefix(m.repo.DefaultBase(m.ctx), "origin/")
	// groups has the PR base, then the default branch, then the others.
	// Each group keeps the newest first.
	var groups [3][]pickItem
	for _, b := range branches {
		var tags []string
		name, g := strings.TrimPrefix(b, "origin/"), 2
		if name == prBase {
			tags, g = append(tags, "pr base"), 0
		}
		if name == defaultBase {
			tags, g = append(tags, "default"), min(g, 1)
		}
		if b == m.base {
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
