package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

type updatePhase int

const (
	updateIdle updatePhase = iota
	updateRunning
	updateDone
)

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

func (m *model) startUpdate() tea.Cmd {
	if m.release == nil || m.updatePhase != updateIdle {
		return nil
	}
	m.updatePhase = updateRunning
	u, ctx, r := m.installer, m.ctx, *m.release
	return func() tea.Msg { return updatedMsg{err: u.Install(ctx, r)} }
}

func (m *model) onUpdated(msg updatedMsg) {
	if msg.err != nil {
		m.updatePhase = updateIdle
		m.flash("update failed: " + msg.err.Error())
		return
	}
	m.updatePhase = updateDone
}

// restartIfUpdated saves the resume state and reports whether rexdiff can
// quit to restart.
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
