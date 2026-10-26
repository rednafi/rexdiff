package tui

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// The colours are ANSI palette indices, so they follow the terminal theme.
var (
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	addStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	delStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	noteStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	staleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Strikethrough(true)
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	activeStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	findStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
)

// truncate cuts s to w columns, with an ellipsis.
func truncate(s string, w int) string { return ansi.Truncate(s, max(w, 0), "…") }

// selStyle returns the style of a selected row: a tint of the terminal
// background.
func (m *model) selStyle() lipgloss.Style {
	if m.bg == nil {
		return lipgloss.NewStyle().Reverse(true)
	}
	tint := lipgloss.Lighten(m.bg, 0.15)
	if !m.dark {
		tint = lipgloss.Darken(m.bg, 0.15)
	}
	return lipgloss.NewStyle().Bold(true).Background(tint)
}

// headerAction is what a click on a part of the header does.
type headerAction int

const (
	headerNone headerAction = iota
	headerTabChanges
	headerTabFiles
	headerTabPR
	headerScope0  // the first scope. Add the index of the scope.
	headerBase    = headerScope0 + headerAction(len(scopes))
	headerCommits = headerBase + 1
	headerAgent   = headerBase + 2
)

// headerSpan is a part of the header that takes clicks.
type headerSpan struct {
	x0, x1 int
	action headerAction
}

// headerLine builds the top line, and keeps the parts that take clicks.
type headerLine struct {
	b     strings.Builder
	spans []headerSpan
	x     int // the width so far, in columns
}

// add adds s. A click on s does action.
func (l *headerLine) add(s string, action headerAction) {
	w := lipgloss.Width(s)
	if action != headerNone {
		l.spans = append(l.spans, headerSpan{l.x, l.x + w, action})
	}
	l.b.WriteString(s)
	l.x += w
}

// header renders the top line, and returns the parts that take clicks.
func (m *model) header() (string, []headerSpan) {
	var l headerLine
	for i, t := range []string{"Changes", "Files", "PR"} {
		label := fmt.Sprintf("%d %s", i+1, t)
		if tab(i) == m.tab {
			label = activeStyle.Render(label)
		} else {
			label = dimStyle.Render(label)
		}
		l.add(" "+label+" ", headerTabChanges+headerAction(i))
	}
	sep := dimStyle.Render(" │ ")
	switch m.tab {
	case tabChanges:
		l.add(sep, headerNone)
		m.headerScopes(&l)
	case tabPR:
		if pr := m.pr.pr; pr != nil && pr.URL != "" {
			l.add(sep, headerNone)
			l.add(dimStyle.Render(string(pr.Forge)+" "), headerNone)
		}
	}
	// The right end shows where a send goes.
	if right := m.agentLabel(); right != "" {
		if pad := m.w - l.x - lipgloss.Width(right) - 1; pad > 0 {
			l.add(strings.Repeat(" ", pad), headerNone)
			l.add(right, headerAgent)
		}
	}
	return truncate(l.b.String(), m.w), l.spans
}

// headerScopes adds the scopes to the header, and what the current scope
// compares with.
func (m *model) headerScopes(l *headerLine) {
	for i, s := range scopes {
		style := dimStyle
		if s == m.scope {
			style = cursorStyle
		}
		l.add(style.Render(s.label()), headerScope0+headerAction(i))
		l.add("  ", headerNone)
	}
	switch m.scope {
	case ScopeBranch:
		l.add(dimStyle.Render("vs ")+m.base+dimStyle.Render(" ▾"), headerBase)
	case scopeCommits:
		l.add(m.commitsLabel(), headerCommits)
	case ScopeLastTurn:
		l.add(m.turnLabel(), headerNone)
	}
}

// commitsLabel names the commits of the commits scope in the header.
func (m *model) commitsLabel() string {
	if m.to == "" {
		return "pick ▾"
	}
	label := shortSHA(m.to)
	if m.from != m.to {
		label = shortSHA(m.from) + ".." + shortSHA(m.to)
	}
	return label + dimStyle.Render(" ▾")
}

// shortSHA returns the first 7 characters of a commit hash.
func shortSHA(s string) string { return s[:min(len(s), 7)] }

// turnLabel returns the text about the last turn of an agent, for the
// header.
func (m *model) turnLabel() string {
	if m.turn == nil {
		return dimStyle.Render("waiting for a turn")
	}
	ago := time.Since(m.turn.at).Round(time.Second)
	app := diff.Quote(m.turn.app) // the app name comes from the agent
	if m.turn.running {
		return noteStyle.Render("● ") + app + dimStyle.Render(" working · "+ago.String())
	}
	return app + dimStyle.Render(" · "+ago.String()+" ago")
}

// agentLabel tells how many agents can get the next send.
func (m *model) agentLabel() string {
	if m.rex == nil {
		return dimStyle.Render("not in Rex")
	}
	n := 0
	for block, a := range m.agents {
		if a.state != "" && block != m.rex.Block {
			n++
		}
	}
	if n == 0 {
		return dimStyle.Render("no agent")
	}
	return dimStyle.Render("send → ") + plural(n, "agent")
}

// statusMark returns the mark of a changed file in the file list.
func statusMark(s vcs.Status) string {
	switch s {
	case vcs.StatusAdded:
		return addStyle.Render("A")
	case vcs.StatusDeleted:
		return delStyle.Render("D")
	case vcs.StatusModified:
		return noteStyle.Render("M")
	case vcs.StatusRenamed:
		return noteStyle.Render("R")
	}
	return " "
}

// listLines renders h lines of the file list: a title and the files.
func (m *model) listLines(h int) []string {
	w := m.listWidth()
	var title string
	var n int
	var row func(i int) string // the loop makes only the rows on the screen
	switch m.tab {
	case tabChanges:
		title, n = fmt.Sprintf("Changes (%d)", len(m.files)), len(m.files)
		row = func(i int) string { return m.changedRow(m.files[i], w) }
	case tabFiles:
		title, n = fmt.Sprintf("Files (%d)", len(m.allFiles)), len(m.allFiles)
		row = func(i int) string { return m.entryRow(m.allFiles[i], w) }
	}
	out := []string{headerStyle.Render(title)}
	sel := m.listSel()
	for i := m.listStart(h); i < n && len(out) < h; i++ {
		r := row(i)
		if i == sel {
			r = m.selectedRow(r, w)
		}
		out = append(out, r)
	}
	return out
}

// changedRow renders changed file f in the file list, w columns wide.
func (m *model) changedRow(f changed, w int) string {
	adds := addStyle.Render(fmt.Sprintf("+%d", f.adds))
	stat := adds + " " + delStyle.Render(fmt.Sprintf("-%d", f.dels))
	if c := m.comments.Count(f.Path); c > 0 {
		stat = cursorStyle.Render(fmt.Sprintf("●%d ", c)) + stat
	}
	name := f.Path
	if f.From != "" {
		name = f.From + " → " + f.Path
	}
	return fileRow(statusMark(f.Status), name, stat, w)
}

// entryRow renders file f of the Files tab in the file list, w columns
// wide.
func (m *model) entryRow(f vcs.Entry, w int) string {
	if f.Ignored {
		return dimStyle.Render(fileRow(" ", f.Path, "", w))
	}
	stat := ""
	if c := m.comments.Count(f.Path); c > 0 {
		stat = cursorStyle.Render(fmt.Sprintf("●%d", c))
	}
	return fileRow(" ", f.Path, stat, w)
}

// selectedRow highlights row r of the selected file, w columns wide.
// When the list has the focus, a mark takes the place of the first space.
func (m *model) selectedRow(r string, w int) string {
	style := m.selStyle().Width(w)
	if !m.focusList {
		return style.Render(r)
	}
	return style.Render(cursorStyle.Render("▌") + ansi.TruncateLeft(r, 1, ""))
}

// listStart returns the index of the first file that the list shows.
func (m *model) listStart(h int) int { return max(0, m.listSel()-(h-2)) }

// fileRow returns " M name/of/file.go  +3 -1". If the path is too long, it
// removes the directory first, then the start of the name.
func fileRow(mark, p, stat string, w int) string {
	p = diff.Quote(p)
	room := w - lipgloss.Width(stat) - 4
	if n := ansi.StringWidth(p); n > room {
		if base := path.Base(p); ansi.StringWidth(base)+2 <= room {
			p = "…/" + base
		} else {
			p = "…" + ansi.TruncateLeft(p, n-room+1, "")
		}
	}
	pad := strings.Repeat(" ", max(room-ansi.StringWidth(p), 0)) // by width, not runes
	return " " + mark + " " + p + pad + " " + stat
}

// mainLines renders the visible part of the main pane. rowAt[y] is the row
// on screen line y, or -1 for a note, a comment, or the comment box.
func (m *model) mainLines(h int) (lines []string, rowAt []int) {
	p := m.cur()
	if note := m.emptyNote(); note != "" {
		return []string{"", "  " + dimStyle.Render(note)}, []int{-1, -1}
	}
	lo, hi := m.highlighted()
	add := func(l string, row int) {
		lines, rowAt = append(lines, l), append(rowAt, row)
	}
	// addBelow adds lines that show no source line, while there is room.
	addBelow := func(ls []string) {
		for _, l := range ls {
			if len(lines) >= h {
				return
			}
			add(l, -1)
		}
	}
	for i := p.top; i < len(p.lines) && len(lines) < h; i++ {
		// A wrapped line shows its marks on its first row, and its
		// comments below its last row.
		t := m.target(i, diff.SideNew)
		add(m.rowMark(i, t, lo, hi)+p.lines[i], i)
		if t != nil && p.end(i) == i {
			addBelow(m.commentsBelow(t))
		}
		if m.mode == modeComment && i == p.end(p.cur) {
			addBelow(m.commentBox())
		}
	}
	return lines, rowAt
}

// emptyNote returns the text that shows instead of an empty pane, or "".
func (m *model) emptyNote() string {
	p := m.cur()
	switch {
	case len(p.lines) > 0:
		return ""
	case p.note != "":
		return p.note
	case m.tab == tabPR && m.pr.loading:
		return "loading the pull request…"
	}
	return ""
}

// highlighted returns the first and the last row of the drag or the
// selection. With neither, it returns -1, -1.
func (m *model) highlighted() (lo, hi int) {
	p := m.cur()
	switch {
	case m.drag.active && m.drag.moved:
		return p.span(m.drag.start, p.cur)
	case p.anchor >= 0:
		return p.selection()
	}
	return -1, -1
}

// rowMark returns the gutter mark of row i. t is the line that row i
// shows, or nil. The mark shows the cursor, the rows lo to hi, a find hit,
// or a comment.
func (m *model) rowMark(i int, t *targetLine, lo, hi int) string {
	p := m.cur()
	_, hit := slices.BinarySearch(m.find.hits, i) // the hits are in order
	switch {
	case i == p.cur && m.tab != tabPR:
		return cursorStyle.Render("▶ ")
	case lo >= 0 && i >= lo && i <= hi:
		return cursorStyle.Render("┃ ")
	case hit:
		return findStyle.Render("≈ ")
	case t == nil || p.cont(i):
		return commentMark(nil)
	}
	return commentMark(m.comments.At(p.path, t.side, t.line))
}

// commentsBelow returns the lines of the comments whose range ends on
// line t. The comment that the box edits does not show.
func (m *model) commentsBelow(t *targetLine) []string {
	var out []string
	for _, c := range m.comments.EndingAt(m.cur().path, t.side, t.line) {
		if m.mode == modeComment && c.ID == m.draft.editID {
			continue
		}
		out = append(out, m.commentLines(c)...)
	}
	return out
}

// commentMark returns the gutter mark of a line with comment c, which can
// be nil.
func commentMark(c *diff.Comment) string {
	switch {
	case c == nil:
		return "  "
	case c.Stale:
		return dimStyle.Render("● ")
	}
	return cursorStyle.Render("● ")
}

// commentBox returns the lines of the comment box, indented.
func (m *model) commentBox() []string {
	var box []string
	for l := range strings.SplitSeq(m.editor.View(), "\n") {
		box = append(box, "    "+l)
	}
	return box
}

// commentLines returns the lines that show comment c below its range.
func (m *model) commentLines(c *diff.Comment) []string {
	var out []string
	for i, l := range strings.Split(c.Text, "\n") {
		prefix, st := "    💬 ", noteStyle
		if i > 0 {
			prefix = "       "
		}
		if c.Stale {
			st = staleStyle
			if i == 0 {
				l = "(stale) " + l
			}
		}
		out = append(out, truncate(prefix+st.Render(l), m.mainWidth()))
	}
	return out
}

// ensureVisible scrolls the main pane so that the cursor and the comment
// box below it show. The box shows below the last row of its line. When
// the box is open, a line taller than the screen shows only its end. When
// the box is closed, the line shows only its first rows.
func (m *model) ensureVisible() {
	p := m.cur()
	h := m.bodyHeight()
	if p.free {
		p.top = min(p.top, max(len(p.lines)-h, 0))
		return
	}
	p.cur = p.head(p.cur) // the cursor is on the first row of a wrapped line
	last, box := p.cur, 0
	if m.mode == modeComment {
		last, box = p.end(p.cur), lipgloss.Height(m.editor.View())
	}
	// Each row takes one line or more. So the last row cannot show if it is
	// h rows or more below the top.
	p.top = max(p.top, last-h+1)
	p.top = min(last, min(p.top, max(len(p.lines)-1, 0)))
	for p.top < last {
		_, rowAt := m.mainLines(h)
		idx := slices.Index(rowAt, p.end(p.cur))
		if idx >= 0 && idx+1+box <= h {
			return
		}
		p.top++
	}
}

// footer renders the notice, the status, and the help of the keys. The
// notice wraps, so that the user can read all of it.
func (m *model) footer() string {
	if m.notice != "" {
		return delStyle.Render(wrapWords(m.notice, m.w)) + "\n" + m.footerLine()
	}
	return m.footerLine()
}

// wrapWords breaks s into lines of width w or less, at spaces only.
// ansi.Wrap and lipgloss let a line end past w at a hyphen. Then the edge
// of the screen cuts off a "--". A word wider than w is cut, and a
// character wider than w gets a line of its own.
func wrapWords(s string, w int) string {
	w = max(w, 1)
	var lines []string
	line := ""
	for word := range strings.FieldsSeq(s) {
		switch {
		case line == "":
			line = word
		case ansi.StringWidth(line)+1+ansi.StringWidth(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
		for ansi.StringWidth(line) > w {
			head, rest := ansi.Truncate(line, w, ""), ansi.TruncateLeft(line, w, "")
			if head == "" { // the first character is wider than w
				_, n := utf8.DecodeRuneInString(line)
				head, rest = line[:n], line[n:]
			}
			lines = append(lines, head)
			line = rest
		}
	}
	return strings.Join(append(lines, line), "\n")
}

// footerLine renders the status and the help of the keys.
func (m *model) footerLine() string {
	m.help.SetWidth(m.w)
	var badge string
	switch {
	case m.status != "":
		badge = cursorStyle.Render(m.status)
	case m.release != nil:
		badge = cursorStyle.Render(m.releaseBadge())
	case m.find.active():
		f := m.find
		badge = findStyle.Render(fmt.Sprintf("≈ %q %d/%d", f.query, f.i+1, len(f.hits)))
	case m.mode == modeComment && m.draft != nil:
		badge = cursorStyle.Render("✎ " + m.draft.Location())
	case m.comments.Len() > 0:
		badge = cursorStyle.Render("● " + plural(m.comments.Len(), "unsent comment"))
	}
	hk := helpKeys{
		k:       m.keys,
		mode:    m.mode,
		tab:     m.tab,
		split:   m.split,
		visual:  m.cur().anchor >= 0,
		finding: m.find.active(),
		onFold:  m.onFold(),
	}
	if m.help.ShowAll {
		h := m.help.View(hk)
		if badge != "" {
			h = badge + "\n" + h
		}
		return h
	}
	if badge != "" {
		m.help.SetWidth(max(m.w-lipgloss.Width(badge)-4, 10))
		return truncate(badge+"    "+m.help.View(hk), m.w)
	}
	return m.help.View(hk)
}

// bodyHeight is the number of lines between the header and the footer.
func (m *model) bodyHeight() int { return m.heightAbove(m.footer()) }

// heightAbove returns the height of the body above footer.
func (m *model) heightAbove(footer string) int {
	return max(m.h-lipgloss.Height(footer)-2, 3)
}

// View renders the screen.
func (m *model) View() tea.View {
	footer := m.footer()
	h := m.heightAbove(footer)
	hdr, _ := m.header()

	ml, _ := m.mainLines(h)
	main := box(ml, m.mainWidth(), h)
	body := main
	if lw := m.listWidth(); lw > 0 {
		list := box(m.listLines(h), lw, h)
		sep := dimStyle.Render(strings.TrimSuffix(strings.Repeat(" │ \n", h), "\n"))
		if m.listLeft {
			body = lipgloss.JoinHorizontal(lipgloss.Top, list, sep, main)
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, main, sep, list)
		}
	}
	rule := dimStyle.Render(strings.Repeat("─", m.w))
	screen := hdr + "\n" + body + "\n" + rule + "\n" + footer

	if m.mode == modeOverlay && m.overlay != nil {
		box, x, y, _ := m.overlayBox()
		c := lipgloss.NewCanvas(m.w, m.h)
		c.Compose(lipgloss.NewLayer(screen))
		c.Compose(lipgloss.NewLayer(box).X(x).Y(y).Z(1))
		screen = c.Render()
	}

	// On a small screen, the parts have their minimum sizes. Cut the
	// screen to the size of the terminal.
	screen = lipgloss.NewStyle().MaxWidth(max(m.w, 1)).MaxHeight(max(m.h, 1)).Render(screen)
	v := tea.NewView(screen)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.ProgramStatus = m.programStatus()
	return v
}

// box joins lines into a block of exactly w columns and h lines.
func box(lines []string, w, h int) string {
	st := lipgloss.NewStyle().Width(w).MaxWidth(w).Height(h).MaxHeight(h)
	return st.Render(strings.Join(lines, "\n"))
}

// programStatus is the status that rexdiff reports to Rex with OSC 7501.
// Rex shows it in its tabs and its sidebar.
func (m *model) programStatus() *tea.ProgramStatus {
	msg := "reviewing " + plural(len(m.files), "file")
	if n := m.comments.Len(); n > 0 {
		msg = plural(n, "unsent comment")
	}
	return &tea.ProgramStatus{
		State:   tea.ProgramStateIdle,
		App:     appName,
		Title:   appName,
		Message: msg,
	}
}

// mainX is the screen column where the main pane starts.
func (m *model) mainX() int {
	if lw := m.listWidth(); lw > 0 && m.listLeft {
		return lw + sepWidth
	}
	return 0
}

// listX is the screen column where the file list starts.
func (m *model) listX() int {
	if !m.listLeft {
		return m.mainWidth() + sepWidth
	}
	return 0
}

// sepWidth is the width of the line " │ " between the panes.
const sepWidth = 3

// bodyY is the screen line where the body starts, below the header.
const bodyY = 1
