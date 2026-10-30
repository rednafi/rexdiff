package tui

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

// onClick starts an update on a click in the footer.
func (m *model) onClick(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.crossStep = 0
	if m.overlay != nil {
		return m.clickOverlay(mouse)
	}
	if m.mode == modeComment {
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
	if y := mouse.Y - bodyY; y >= m.shownBodyHeight() {
		return m.startUpdate()
	}
	// Time the next click from the end of this one, so that a slow copy
	// does not break a triple click.
	defer func() { m.click.at = time.Now() }()
	if m.inNavigator(mouse.X, mouse.Y) {
		y := mouse.Y - m.navigatorY()
		if y < 1 {
			return nil
		}
		r := m.navigatorStart(m.navigatorRows()) + y - 1
		double := m.countClick(mouse) >= 2
		if path := m.navigatorRowPath(r); double && path != "" {
			m.copyWithCount(path)
			return nil
		}
		m.clickNavigatorRow(r)
		return nil
	}
	return m.clickMain(mouse)
}

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
	p := m.curPane()
	x := mouse.X - m.mainX() - gutter
	if n := m.countClick(mouse); n >= 2 {
		if text := m.clickText(rowAt[y], x, n); text != "" {
			m.copyWithCount(text)
			return nil
		}
	}
	m.navigatorFocused = false
	link := ""
	if p.diff == nil { // rendered Markdown or the PR tab
		if url := linkAt(p.line(rowAt[y]), x); isWebURL(url) {
			link = url
		}
	}
	p.cur, p.selStart = p.lineStart(rowAt[y]), -1
	if m.cursorOnFold() {
		m.expandFold(p.rows[p.cur].Fold)
		return nil
	}
	side, onGutter := p.side(x)
	m.drag = dragState{
		active:   true,
		startRow: p.cur,
		gutter:   onGutter,
		prefer:   side,
		startY:   rowAt[y],
		startX:   x,
		endY:     rowAt[y],
		endX:     x,
		link:     link,
	}
	return nil
}

func linkAt(line string, x int) string {
	w := ansi.StringWidth(line)
	if x < 0 || x >= w {
		return ""
	}
	buf := uv.NewScreenBuffer(w, 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	return buf.CellAt(x, 0).Link.URL
}

func (m *model) inNavigator(x, y int) bool {
	lw, ly := m.navigatorWidth(), m.navigatorY()
	return lw > 0 && x >= m.navigatorX() && x < m.navigatorX()+lw && y >= ly && y < ly+m.navigatorRows()
}

func (m *model) onDrag(mouse tea.Mouse) {
	if !m.drag.active || mouse.Button != tea.MouseLeft {
		return
	}
	p := m.curPane()
	h := m.mainHeight()
	y := mouse.Y - m.mainY()
	switch {
	case y < 0:
		p.top = max(p.top-1, 0)
		p.cur = p.top
	case y >= h:
		p.top = min(p.top+1, p.last())
		p.cur = max(min(p.top+h-1, p.count()-1), 0)
	default:
		_, rowAt := m.mainLines(h, nil)
		for yy := min(y, len(rowAt)-1); yy >= 0; yy-- {
			if rowAt[yy] >= 0 {
				p.cur = rowAt[yy]
				break
			}
		}
	}
	m.drag.endY, m.drag.endX = p.cur, mouse.X-m.mainX()-gutter
	p.cur = p.lineStart(p.cur)
	m.drag.moved = p.cur != m.drag.startRow
	if m.drag.moved || m.drag.endX != m.drag.startX {
		m.click = clickState{} // a drag does not count as a click
	}
}

const clickInterval = 400 * time.Millisecond

const maxClicks = 3

type clickState struct {
	at   time.Time
	x, y int
	n    int
}

func (m *model) countClick(mouse tea.Mouse) int {
	c, now := m.click, time.Now()
	n := 1
	if c.n > 0 && c.x == mouse.X && c.y == mouse.Y && now.Sub(c.at) < clickInterval {
		n = min(c.n+1, maxClicks)
	}
	m.click = clickState{at: now, x: mouse.X, y: mouse.Y, n: n}
	return n
}

func (m *model) navigatorRowPath(r int) string {
	t := m.curNavigator()
	if t == nil || r < 0 || r >= len(t.rows) {
		return ""
	}
	if row := t.rows[r]; row.File >= 0 {
		return t.entries[row.File].Path
	}
	return t.rows[r].Dir
}

// clickText returns the word under x for a double click, and the source
// line for a triple click.
func (m *model) clickText(i, x, n int) string {
	line, char, ok := m.textAt(i, x)
	if !ok || n >= maxClicks {
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

// textAt returns the text as shown on screen for a pane without a diff.
func (m *model) textAt(i, x int) (line string, char int, ok bool) {
	p := m.curPane()
	if i < 0 || i >= p.count() {
		return "", 0, false
	}
	if p.diff == nil {
		line = strings.TrimRight(ansi.Strip(p.line(i)), " ")
		c := 0
		for _, r := range line {
			if c += ansi.StringWidth(string(r)); x < c {
				return line, char, true
			}
			char++
		}
		return line, char, true
	}
	side, n, char, ok := p.diff.Cell(i, x)
	if !ok {
		return "", 0, false
	}
	if lines := diff.SplitLines(m.contentFor(p.path, side)); n <= len(lines) {
		return lines[n-1], char, true
	}
	return "", 0, false
}

func (m *model) copyWithCount(text string) {
	if text != "" {
		m.copyText(text, "copied "+plural(utf8.RuneCountInString(text), "char"), nil)
	}
}

func (m *model) onMouseUp(mouse tea.Mouse) tea.Cmd {
	d := m.drag
	m.drag.active = false
	if d.active && d.link != "" && m.click.x == mouse.X && m.click.y == mouse.Y {
		return m.openURL(d.link, "link")
	}
	p := m.curPane()
	// A reload during the drag can leave the pane shorter than the
	// starting row.
	if !d.active || d.startRow >= len(p.rows) {
		return nil
	}
	if d.gutter {
		if d.moved {
			p.selStart = d.startRow
		}
		return m.openComment(d.prefer)
	}
	m.copyWithCount(m.dragText(d))
	return nil
}

// dragText includes the runes at both ends. A drag that ends on its start
// rune is a click and returns "".
func (m *model) dragText(d dragState) string {
	p := m.curPane()
	a, b := d.startY, d.endY // rendered Markdown copies the text as shown on screen
	if p.srcRows == nil {
		a, b = p.lineStart(a), p.lineStart(b)
	}
	_, ca, _ := m.textAt(d.startY, d.startX)
	_, cb, _ := m.textAt(d.endY, d.endX)
	if a > b || a == b && ca > cb {
		a, b, ca, cb = b, a, cb, ca
	}
	// A reload during the drag can leave the pane shorter than the drag.
	if a == b && ca == cb || b >= p.count() {
		return ""
	}
	var lines []string
	if p.srcRows != nil {
		for _, l := range p.lines[a : b+1] {
			lines = append(lines, strings.TrimRight(ansi.Strip(l), " "))
		}
	} else {
		lines = m.sourceLines(a, p.lineEnd(b), d.prefer)
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

// sourceLines uses the text shown on screen for a row with no source line.
func (m *model) sourceLines(lo, hi int, prefer diff.Side) []string {
	p := m.curPane()
	var out []string
	var src [2][]string
	for i := lo; i <= hi; i++ {
		if p.cont(i) {
			continue
		}
		target := m.targetAt(i, prefer)
		if target == nil {
			if i < p.count() {
				out = append(out, strings.TrimRight(ansi.Strip(p.line(i)), " "))
			}
			continue
		}
		if src[target.side] == nil {
			src[target.side] = diff.SplitLines(m.contentFor(p.path, target.side))
		}
		if lines := src[target.side]; target.line-1 < len(lines) {
			out = append(out, lines[target.line-1])
		}
	}
	return out
}

const wheelStep = 3

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
	if ov := m.overlay; ov != nil {
		_, _, y, _ := m.overlayBox()
		if pv := ov.search.preview; pv != nil && mouse.Y >= y+ov.search.previewY {
			pv.scroll(wheelStep * d)
			return
		}
		ov.move(d)
		return
	}
	if t := m.curNavigator(); t != nil && m.inNavigator(mouse.X, mouse.Y) {
		h := m.navigatorRows() - 1
		t.top = clampTop(t.start(h)+wheelStep*d, len(t.rows), max(h, 1))
		t.following = false
		return
	}
	p := m.curPane()
	h := m.mainHeight()
	p.top = clampTop(p.top+wheelStep*d, p.count(), h)
	p.following = false
}

func (m *model) clickOverlay(mouse tea.Mouse) tea.Cmd {
	box, x, y, itemsY := m.overlayBox()
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	inBox := mouse.X >= x && mouse.X < x+w && mouse.Y >= y && mouse.Y < y+h
	if !inBox {
		if m.overlay.kind != overlayQuit {
			m.closeOverlay()
		}
		return nil
	}
	idx := mouse.Y - y - itemsY
	onItem := idx >= m.overlay.itemsLo && idx < min(len(m.overlay.shown), m.overlay.itemsHi)
	if !onItem || !m.overlay.canHaveCursor(idx) {
		return nil
	}
	if idx == m.overlay.cur {
		return m.choose()
	}
	m.overlay.cur = idx
	return nil
}

func (m *model) clickHeader(a headerAction) tea.Cmd {
	switch {
	case a >= headerTabChanges && a <= headerTabPR:
		m.setTab(tab(a - headerTabChanges))
	case a >= headerScope0 && a < headerBase:
		s := scopes[a-headerScope0]
		if s == scopeCommits && m.newest == "" {
			return m.openCommitPicker()
		}
		m.setScope(s)
	case a == headerBase:
		return m.openBasePicker()
	case a == headerCommits:
		return m.openCommitPicker()
	case a == headerAgent:
		return m.startSend()
	}
	return nil
}
