package tui

// The mouse works as in herdr-reviewr. Click or drag on the line numbers to
// comment on a line or a range. Drag on the code to copy it. Click a file
// or a tab to open it.

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// onClick handles a press of a mouse button.
func (m *model) onClick(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	if m.mode == modeOverlay && m.overlay != nil {
		return m.clickOverlay(mouse)
	}
	if m.mode == modeComment {
		// A click does nothing while the comment box is open.
		return nil
	}
	if mouse.Y == 0 {
		_, spans := m.header()
		for _, s := range spans {
			if mouse.X >= s.x0 && mouse.X < s.x1 {
				return m.clickHeader(s.action)
			}
		}
		return nil
	}
	h := m.bodyHeight()
	y := mouse.Y - bodyY
	if y > h { // the footer, under the rule at y == h
		return m.install()
	}
	if y < 0 || y >= h {
		return nil
	}
	if m.inList(mouse.X) {
		if y >= 1 {
			m.focusList = true
			m.pickFile(m.listStart(h) + y - 1)
		}
		return nil
	}
	_, rowAt := m.mainLines(h)
	if y >= len(rowAt) || rowAt[y] < 0 {
		return nil
	}
	p := m.cur()
	m.focusList = false
	p.cur, p.anchor = p.head(rowAt[y]), -1
	if m.onFold() {
		m.expandFold(p.cur)
		return nil
	}
	side, onNumber := p.side(mouse.X - m.mainX() - gutter)
	m.drag = dragState{
		active: true,
		start:  p.cur,
		gutter: onNumber,
		pref:   side,
	}
	return nil
}

// inList tells if screen column x is on the file list.
func (m *model) inList(x int) bool {
	lw := m.listWidth()
	return lw > 0 && x >= m.listX() && x < m.listX()+lw
}

// onDrag moves the cursor with a drag. A drag past the top or the bottom
// scrolls.
func (m *model) onDrag(mouse tea.Mouse) {
	if !m.drag.active || mouse.Button != tea.MouseLeft {
		return
	}
	p := m.cur()
	h := m.bodyHeight()
	y := mouse.Y - bodyY
	switch {
	case y < 0:
		p.top = max(p.top-1, 0)
		p.cur = p.top
	case y >= h:
		p.top = min(p.top+1, max(len(p.lines)-1, 0))
		p.cur = max(min(p.top+h-1, len(p.lines)-1), 0)
	default:
		_, rowAt := m.mainLines(h)
		for yy := min(y, len(rowAt)-1); yy >= 0; yy-- {
			if rowAt[yy] >= 0 {
				p.cur = rowAt[yy]
				break
			}
		}
	}
	p.cur = p.head(p.cur) // the drag moves by source lines
	m.drag.moved = p.cur != m.drag.start
}

// onRelease ends a drag. A drag on the line numbers opens the comment
// box. A drag on the code copies the lines.
func (m *model) onRelease() tea.Cmd {
	d := m.drag
	m.drag.active = false
	p := m.cur()
	// A reload during the drag can make the pane shorter than the start.
	if !d.active || d.start >= len(p.rows) {
		return nil
	}
	switch {
	case d.gutter: // a line or a range on the line numbers: comment
		if d.moved {
			p.anchor = d.start
		}
		return m.startComment(d.pref)
	case d.moved: // a range on the code: copy it
		lo, hi := p.span(d.start, p.cur)
		out := m.sourceLines(lo, hi, d.pref)
		m.flash("copied " + plural(len(out), "line"))
		return tea.SetClipboard(strings.Join(out, "\n"))
	}
	return nil
}

// sourceLines returns the source lines of rows lo to hi of the current
// pane. It uses side pref if they change it. A row with no source line
// gives its text on the screen.
func (m *model) sourceLines(lo, hi int, pref diff.Side) []string {
	p := m.cur()
	var out []string
	var src [2][]string // the lines of each side, read once
	for i := lo; i <= hi; i++ {
		if p.cont(i) {
			continue
		}
		t := m.target(i, pref)
		if t == nil {
			if i < len(p.lines) {
				out = append(out, strings.TrimRight(ansi.Strip(p.lines[i]), " "))
			}
			continue
		}
		if src[t.side] == nil {
			src[t.side] = diff.Lines(m.contentFor(p.path, t.side))
		}
		if lines := src[t.side]; t.line-1 < len(lines) {
			out = append(out, lines[t.line-1])
		}
	}
	return out
}

// wheelStep is the number of rows that one step of the wheel scrolls.
const wheelStep = 3

// onWheel scrolls the overlay, the file list, or the main pane.
func (m *model) onWheel(mouse tea.Mouse) {
	var d int
	switch mouse.Button {
	case tea.MouseWheelDown:
		d = 1
	case tea.MouseWheelUp:
		d = -1
	default:
		return
	}
	if m.mode == modeOverlay && m.overlay != nil {
		m.overlay.cur = min(max(m.overlay.cur+d, 0), max(len(m.overlay.shown)-1, 0))
		return
	}
	if m.inList(mouse.X) {
		m.pickFile(m.listSel() + d)
		return
	}
	p := m.cur()
	h := m.bodyHeight()
	p.top = min(max(p.top+wheelStep*d, 0), max(len(p.lines)-h, 0))
	// The wheel moves the view and not the cursor.
	p.free = true
}

// clickOverlay handles a click while an overlay is open. A click outside
// the box closes it, but not the prompt before a quit.
func (m *model) clickOverlay(mouse tea.Mouse) tea.Cmd {
	box, x, y, listY := m.overlayBox()
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	inBox := mouse.X >= x && mouse.X < x+w && mouse.Y >= y && mouse.Y < y+h
	if !inBox {
		if m.overlay.kind != overlayQuit {
			m.closeOverlay()
		}
		return nil
	}
	idx := mouse.Y - y - listY
	if idx < 0 || idx >= len(m.overlay.shown) {
		return nil
	}
	if idx == m.overlay.cur { // a click on the selected row picks it
		return m.chooseOverlay()
	}
	m.overlay.cur = idx
	return nil
}

// clickHeader does action a of a click on the header.
func (m *model) clickHeader(a headerAction) tea.Cmd {
	switch {
	case a >= headerTabChanges && a <= headerTabPR:
		m.setTab(tab(a - headerTabChanges))
	case a >= headerScope0 && a < headerBase:
		s := scopes[a-headerScope0]
		if s == scopeCommits && m.to == "" {
			m.openCommitPicker()
		} else {
			m.setScope(s)
		}
	case a == headerBase:
		m.openBasePicker()
	case a == headerCommits:
		m.openCommitPicker()
	case a == headerAgent:
		return m.startSend()
	}
	return nil
}
