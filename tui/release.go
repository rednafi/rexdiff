package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// The footer tells the user about a newer release. A key or a click
// starts the update to it in the background. Then rexdiff starts again in
// the same pane, with the comments and the place of the user. The update
// package installs the release. This file shows the state of the update.

// updatePhase is the step of the update of rexdiff.
type updatePhase int

const (
	phaseIdle     updatePhase = iota
	phaseUpdating             // the release installs in the background
	phaseUpdated              // rexdiff restarts when the user is ready
)

// checkRelease returns a command that asks for the latest release. Its
// message is a releaseMsg if the release is newer. A failure shows nothing,
// because rexdiff works the same without the check.
func (m *model) checkRelease() tea.Cmd {
	u, ctx := m.updater, m.ctx
	return func() tea.Msg {
		r, newer, err := u.Latest(ctx)
		if err != nil || !newer {
			return nil
		}
		return releaseMsg{release: r}
	}
}

// releaseBadge returns the text of the footer about m.release.
func (m *model) releaseBadge() string {
	switch m.phase {
	case phaseUpdating:
		return fmt.Sprintf("updating rexdiff to %s…", m.release.Tag)
	case phaseUpdated:
		return fmt.Sprintf("rexdiff %s is installed. It starts when you are done", m.release.Tag)
	}
	return fmt.Sprintf("rexdiff %s is out: press %s or click here to update",
		m.release.Tag, firstKey(m.keys.Update))
}

// startUpdate starts the update to the release in the background.
func (m *model) startUpdate() tea.Cmd {
	if m.release == nil || m.phase != phaseIdle {
		return nil
	}
	m.phase = phaseUpdating
	u, ctx, r := m.updater, m.ctx, *m.release
	return func() tea.Msg { return updatedMsg{err: u.Install(ctx, r)} }
}

// onUpdated prepares the restart, or tells why the update fails. After
// a failure, the user can try again.
func (m *model) onUpdated(msg updatedMsg) {
	if msg.err != nil {
		m.phase = phaseIdle
		m.flash("update failed: " + msg.err.Error())
		return
	}
	m.phase = phaseUpdated
}

// restartIfReady saves the resume state for the restart, and keeps its
// file in m.resumePath. It reports whether rexdiff can quit for the
// restart. It waits while the user writes a comment or uses an overlay.
// It also waits while a send runs, and while pending (the command of the
// last message) runs. Thus no work is lost.
func (m *model) restartIfReady(pending tea.Cmd) bool {
	if m.mode != modeNormal || pending != nil || m.sends > 0 {
		return false
	}
	path, err := m.saveResume()
	if err != nil {
		m.phase, m.release = phaseIdle, nil
		m.flash("updated, but the save of your comments failed: " + err.Error() +
			". Open rexdiff again for the new version")
		return false
	}
	m.resumePath = path
	return true
}
