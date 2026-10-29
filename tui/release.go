package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// When a newer release is available, the footer announces it. A key press
// or a click installs it in the background, and rexdiff then restarts in
// the same pane, keeping the comments and the user's position. Package
// update does the installing.

// updatePhase is the stage that a self-update has reached.
type updatePhase int

const (
	updateIdle    updatePhase = iota
	updateRunning             // the release is installing in the background
	updateDone                // rexdiff restarts once the user is ready
)

// fetchRelease returns a command that checks for the latest release. The
// command returns a releaseMsg if that release is newer, and nil otherwise,
// including on failure.
func (m *model) fetchRelease() tea.Cmd {
	u, ctx := m.installer, m.ctx
	return func() tea.Msg {
		r, newer, err := u.Latest(ctx)
		if err != nil || !newer {
			return nil
		}
		return releaseMsg{release: r}
	}
}

// releaseBadge returns the footer text about m.release.
func (m *model) releaseBadge() string {
	switch m.updatePhase {
	case updateRunning:
		return fmt.Sprintf("updating rexdiff to %s…", m.release.Tag)
	case updateDone:
		return fmt.Sprintf("rexdiff %s is installed. It starts when you are done", m.release.Tag)
	}
	return fmt.Sprintf("rexdiff %s is out: press %s or click here to update",
		m.release.Tag, firstKey(m.keys.Update))
}

// startUpdate starts installing the new release in the background. It
// does nothing if there is no release or an update has already started.
func (m *model) startUpdate() tea.Cmd {
	if m.release == nil || m.updatePhase != updateIdle {
		return nil
	}
	m.updatePhase = updateRunning
	u, ctx, r := m.installer, m.ctx, *m.release
	return func() tea.Msg { return updatedMsg{err: u.Install(ctx, r)} }
}

// onUpdated marks the update as done so that rexdiff can restart, or shows
// why the update failed. After a failure, the user can try again.
func (m *model) onUpdated(msg updatedMsg) {
	if msg.err != nil {
		m.updatePhase = updateIdle
		m.flash("update failed: " + msg.err.Error())
		return
	}
	m.updatePhase = updateDone
}

// restartIfUpdated saves the state to restore after the restart and
// stores its path in m.resumePath. It reports whether rexdiff can quit to
// restart. It returns false while the user is writing a comment or using
// an overlay, while a review is being sent, and while hasPending reports
// that the command returned for the last message is still to run.
func (m *model) restartIfUpdated(hasPending bool) bool {
	if m.mode != modeNormal || hasPending || m.sends > 0 {
		return false
	}
	path, err := m.saveResume()
	if err != nil {
		m.updatePhase, m.release = updateIdle, nil
		m.flash("updated, but the save of your comments failed: " + err.Error() +
			". Open rexdiff again for the new version")
		return false
	}
	m.resumePath = path
	return true
}
