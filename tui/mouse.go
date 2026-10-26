package tui

// The mouse works as in herdr-reviewr. Click or drag on the line numbers to
// comment on a line or a range. Drag on the code to copy it. Double-click
// to copy a word, and triple-click to copy a line. Click a file or a tab to
// open it. Double-click a file to copy its path. Click a link to open it.

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// onClick handles a press of a mouse button.
func (m *model) onClick(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.armed = 0 // as a key does
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
	if y := mouse.Y - bodyY; y > m.bodyHeight() { // the footer, under the rule
		return m.startUpdate()
	}
	// The window of the next click starts after the work of this one, for
	// example a copy. A slow copy then does not break a triple click.
	defer func() { m.click.at = time.Now() }()
	if m.inList(mouse.X, mouse.Y) {
		if y := mouse.Y - m.listY(); y >= 1 {
			r := m.listStart(m.listRows()) + y - 1
			if m.countClick(mouse) >= 2 {
				if path := m.rowPath(r); path != "" {
					m.copyChars(path)
					return nil
				}
			}
			m.clickRow(r)
		}
		return nil
	}
	return m.clickMain(mouse)
}

// clickMain handles a press of the left button on the main pane. A click
// moves the cursor and starts a drag. A double or a triple click copies
// the text.
func (m *model) clickMain(mouse tea.Mouse) tea.Cmd {
	h := m.mainHeight()
	y := mouse.Y - m.mainY()
	if y < 0 || y >= h || mouse.X < m.mainX() || mouse.X >= m.mainX()+m.mainWidth() {
		return nil
	}
	_, rowAt := m.mainLines(h, nil)
	if y >= len(rowAt) || rowAt[y] < 0 {
		return nil
	}
	p := m.cur()
	x := mouse.X - m.mainX() - gutter
	if n := m.countClick(mouse); n >= 2 {
		if text := m.clickText(rowAt[y], x, n); text != "" {
			m.copyChars(text)
			return nil
		}
	}
	m.focusList = false
	if p.diff == nil { // a markdown view or the PR tab
		if url := linkAt(p.lines[rowAt[y]], x); isWebURL(url) {
			return m.browse(url, "link")
		}
	}
	p.cur, p.anchor = p.head(rowAt[y]), -1
	if m.atFold() {
		m.expandFold(p.rows[p.cur].Fold)
		return nil
	}
	side, onNumber := p.side(x)
	m.drag = dragState{
		active: true,
		start:  p.cur,
		gutter: onNumber,
		pref:   side,
		row:    rowAt[y],
		x:      x,
		toRow:  rowAt[y],
		toX:    x,
	}
	return nil
}

// linkAt returns the URL of the hyperlink at column x of line, or "".
// The markdown renderer puts a hyperlink on each link.
func linkAt(line string, x int) string {
	w := ansi.StringWidth(line)
	if x < 0 || x >= w {
		return ""
	}
	buf := uv.NewScreenBuffer(w, 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	return buf.CellAt(x, 0).Link.URL
}

// inList reports whether the screen cell at column x and line y is on
// the file list.
func (m *model) inList(x, y int) bool {
	lw, ly := m.listWidth(), m.listY()
	return lw > 0 && x >= m.listX() && x < m.listX()+lw && y >= ly && y < ly+m.listRows()
}

// onDrag moves the cursor with a drag. A drag past the top or the bottom
// scrolls.
func (m *model) onDrag(mouse tea.Mouse) {
	if !m.drag.active || mouse.Button != tea.MouseLeft {
		return
	}
	p := m.cur()
	h := m.mainHeight()
	y := mouse.Y - m.mainY()
	switch {
	case y < 0:
		p.top = max(p.top-1, 0)
		p.cur = p.top
	case y >= h:
		p.top = min(p.top+1, max(len(p.lines)-1, 0))
		p.cur = max(min(p.top+h-1, len(p.lines)-1), 0)
	default:
		_, rowAt := m.mainLines(h, nil)
		for yy := min(y, len(rowAt)-1); yy >= 0; yy-- {
			if rowAt[yy] >= 0 {
				p.cur = rowAt[yy]
				break
			}
		}
	}
	m.drag.toRow, m.drag.toX = p.cur, mouse.X-m.mainX()-gutter
	p.cur = p.head(p.cur) // the drag moves by source lines
	m.drag.moved = p.cur != m.drag.start
	if m.drag.moved || m.drag.toX != m.drag.x {
		m.click = clickChain{} // a drag is not a click
	}
}

// clickWindow is the longest time between the clicks of a double or a
// triple click, as in herdr-reviewr.
const clickWindow = 400 * time.Millisecond

// clickChain is the last press of the left button on text.
type clickChain struct {
	at   time.Time
	x, y int
	n    int // the number of presses in the chain, up to 3
}

// countClick adds a press to the chain and returns the count. A press on
// another cell, or after [clickWindow], starts a new chain. The time of
// the chain is the end of the last press.
func (m *model) countClick(mouse tea.Mouse) int {
	c, now := m.click, time.Now()
	n := 1
	if c.n > 0 && c.x == mouse.X && c.y == mouse.Y && now.Sub(c.at) < clickWindow {
		n = min(c.n+1, 3)
	}
	m.click = clickChain{at: now, x: mouse.X, y: mouse.Y, n: n}
	return n
}

// rowPath returns the path of row r of the file list, or "".
func (m *model) rowPath(r int) string {
	t := m.list()
	if t == nil || r < 0 || r >= len(t.rows) {
		return ""
	}
	if row := t.rows[r]; row.File >= 0 {
		return t.entries[row.File].Path
	}
	return t.rows[r].Dir
}

// clickText returns the text that a click of count n copies at column x
// of line i of the main pane. A double click gives the word under x, and
// a triple click gives the source line. It returns "" off a word or on an
// empty line.
func (m *model) clickText(i, x, n int) string {
	line, char, ok := m.textAt(i, x)
	if !ok || n >= 3 {
		return line
	}
	runes := []rune(line)
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	if char >= len(runes) || !isWord(runes[char]) {
		return ""
	}
	s, e := char, char+1
	for s > 0 && isWord(runes[s-1]) {
		s--
	}
	for e < len(runes) && isWord(runes[e]) {
		e++
	}
	return string(runes[s:e])
}

// textAt returns the source line that column x of line i of the main pane
// shows, and the index of the rune under x. A pane with no source shows
// its text on the screen.
func (m *model) textAt(i, x int) (line string, char int, ok bool) {
	p := m.cur()
	if i < 0 || i >= len(p.lines) {
		return "", 0, false
	}
	if p.diff == nil {
		line = strings.TrimRight(ansi.Strip(p.lines[i]), " ")
		c := 0
		for _, r := range line {
			if c += ansi.StringWidth(string(r)); x < c {
				return line, char, true
			}
			char++
		}
		return line, char, true
	}
	s, n, char, ok := p.diff.Cell(i, x)
	if !ok {
		return "", 0, false
	}
	if lines := diff.Lines(m.contentFor(p.path, s)); n <= len(lines) {
		return lines[n-1], char, true
	}
	return "", 0, false
}

// copyChars copies text and then shows the count of its characters, as
// herdr-reviewr does.
func (m *model) copyChars(text string) {
	if text != "" {
		m.copyText(text, "copied "+plural(utf8.RuneCountInString(text), "char"), nil)
	}
}

// onMouseUp ends a drag. A drag on the line numbers opens the comment
// box. A drag on the code copies the text from the press to the pointer.
func (m *model) onMouseUp() tea.Cmd {
	d := m.drag
	m.drag.active = false
	p := m.cur()
	// A reload during the drag can make the pane shorter than the start.
	if !d.active || d.start >= len(p.rows) {
		return nil
	}
	switch {
	case d.gutter: // a drag on the line numbers opens the comment box
		if d.moved {
			p.anchor = d.start
		}
		return m.startComment(d.pref)
	default:
		m.copyChars(m.dragText(d))
	}
	return nil
}

// dragText returns the text of drag d on the code, as herdr-reviewr
// copies it. The text starts at the rune under the press and ends at the
// rune under the pointer, both included. A drag that ends on its start
// rune is a click, and gives "".
func (m *model) dragText(d dragState) string {
	p := m.cur()
	a, b := p.head(d.row), p.head(d.toRow)
	if p.src != nil {
		a, b = d.row, d.toRow // rendered markdown gives the text on the screen
	}
	_, ca, _ := m.textAt(d.row, d.x)
	_, cb, _ := m.textAt(d.toRow, d.toX)
	if a > b || a == b && ca > cb {
		a, b, ca, cb = b, a, cb, ca
	}
	if a == b && ca == cb {
		return ""
	}
	var lines []string
	if p.src != nil {
		for _, l := range p.lines[a : b+1] {
			lines = append(lines, strings.TrimRight(ansi.Strip(l), " "))
		}
	} else {
		lines = m.sourceLines(a, p.end(b), d.pref)
	}
	if len(lines) == 0 {
		return ""
	}
	last := []rune(lines[len(lines)-1])
	lines[len(lines)-1] = string(last[:min(cb+1, len(last))])
	first := []rune(lines[0])
	lines[0] = string(first[min(ca, len(first)):])
	return strings.Join(lines, "\n")
}

// sourceLines returns the source lines of rows lo to hi of the current
// pane. It picks the side of each row as [model.target] does with pref. A
// row with no source line gives its text on the screen.
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

// onWheel scrolls the overlay, the file list, or the main pane. As in
// herdr-reviewr, the wheel moves the view of the file list, of the main
// pane, and of the preview of the search, and not the cursor. In the other
// parts of an overlay, it moves the cursor.
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
	if ov := m.overlay; m.mode == modeOverlay && ov != nil {
		_, _, y, _ := m.overlayBox()
		if pv := ov.search.preview; pv != nil && mouse.Y >= y+ov.search.previewY {
			pv.scroll(wheelStep * d)
			return
		}
		ov.move(d)
		return
	}
	if t := m.list(); t != nil && m.inList(mouse.X, mouse.Y) {
		h := m.listRows()
		top := m.listStart(h) + wheelStep*d
		t.top, t.free = min(max(top, 0), max(len(t.rows)-(h-1), 0)), true
		return
	}
	p := m.cur()
	h := m.mainHeight()
	p.top = min(max(p.top+wheelStep*d, 0), max(len(p.lines)-h, 0))
	// The wheel moves the view and not the cursor.
	p.free = true
}

// clickOverlay handles a click while an overlay is open. A click outside
// the box closes the overlay. The prompt before a quit does not close.
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
	if idx < m.overlay.rowsFrom || idx >= min(len(m.overlay.shown), m.overlay.rowsTo) ||
		!m.overlay.pickable(idx) {
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
