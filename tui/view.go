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
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/internal/tree"
	"github.com/rednafi/rexdiff/vcs"
)

// The colours are ANSI palette indices, so they follow the terminal theme.
var (
	// dimColor is the colour of the dim text and of the borders.
	dimColor    = lipgloss.Color("8")
	dimStyle    = lipgloss.NewStyle().Foreground(dimColor)
	boldStyle   = lipgloss.NewStyle().Bold(true)
	addStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	delStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	noteStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	headerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	activeStyle = lipgloss.NewStyle().Bold(true).Underline(true)
	findStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
)

// truncate cuts s to w columns, with an ellipsis.
func truncate(s string, w int) string { return ansi.Truncate(s, max(w, 0), "…") }

// dark reports whether the terminal background is dark. A background that
// is not known counts as dark.
func (m *model) dark() bool {
	return m.bg == nil || tea.BackgroundColorMsg{Color: m.bg}.IsDark()
}

// selStyle returns the style of a selected row. The style is a tint of the
// terminal background.
func (m *model) selStyle() lipgloss.Style {
	if m.bg == nil {
		return lipgloss.NewStyle().Reverse(true)
	}
	tint := lipgloss.Lighten(m.bg, 0.15)
	if !m.dark() {
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
		if label := m.turnLabel(); label != "" {
			l.add(label, headerNone)
		}
	}
}

// commitsLabel returns the header text for the commits of the commits scope.
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

// turnLabel returns the text about the last turn, for the header. As in
// herdr-reviewr, a saved turn from an earlier run has no label. With no
// turn, the Changes tab tells why, so the header shows nothing.
func (m *model) turnLabel() string {
	if m.turn == nil || m.turn.at.IsZero() {
		return ""
	}
	ago := time.Since(m.turn.at).Round(time.Second)
	app := termtext.Quote(m.turn.app) // the app name comes from the agent
	if m.turn.running {
		return noteStyle.Render("● ") + app + dimStyle.Render(" working · "+ago.String())
	}
	return app + dimStyle.Render(" · "+ago.String()+" ago")
}

// agentLabel returns the header text that tells how many agents can get
// the next send.
func (m *model) agentLabel() string {
	if m.rex == nil {
		return dimStyle.Render("not in Rex")
	}
	n := 0
	for block, a := range m.agents {
		if a.state != "" && a.app != appName && block != m.rex.Block() {
			n++
		}
	}
	if n == 0 {
		return dimStyle.Render("no agent")
	}
	return dimStyle.Render("send → ") + plural(n, "agent")
}

// statusMark returns the mark of a changed file in the file list.
func statusMark(s vcs.FileStatus) string {
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

// listLines renders h lines of the file list. The lines are a title and the
// rows of the tree.
func (m *model) listLines(h int) []string {
	t := m.list()
	if t == nil {
		return nil
	}
	w := m.listWidth()
	title := fmt.Sprintf("Changes (%d)", len(m.changeFiles))
	if m.tab == tabFiles {
		n := 0
		for _, e := range m.allFiles {
			if !strings.HasSuffix(e.Path, "/") {
				n++
			}
		}
		title = fmt.Sprintf("Files (%d)", n)
	}
	out := []string{headerStyle.Render(title)}
	for i := m.listStart(h); i < len(t.rows) && len(out) < h; i++ {
		r := m.treeRow(t.rows[i], w)
		if i == t.cur {
			r = m.selectedRow(r, w)
		}
		out = append(out, r)
	}
	return out
}

// treeRow renders row r of the file list, w columns wide.
func (m *model) treeRow(r tree.Row, w int) string {
	indent := strings.Repeat("  ", r.Depth)
	switch {
	case r.File < 0:
		return m.dirRow(r, indent, w)
	case m.tab == tabChanges:
		return m.changedRow(m.changeFiles[r.File], indent, r.Name, w)
	}
	return m.entryRow(m.allFiles[r.File], indent, r.Name, w)
}

// dirRow renders directory r of the file list, w columns wide. As in
// herdr-reviewr, a collapsed directory of the Files tab has a dot if it
// holds a change.
func (m *model) dirRow(r tree.Row, indent string, w int) string {
	arrow, mark := "▸", ""
	if r.Expanded {
		arrow = "▾"
	}
	if m.tab == tabFiles && !r.Expanded && r.Changed {
		mark = noteStyle.Render("•")
	}
	room := w - ansi.StringWidth(indent) - lipgloss.Width(mark) - 4
	name := boldStyle.Render(fitName(r.Name+"/", room))
	if r.Ignored {
		name = dimStyle.Render(fitName(r.Name+"/", room))
	}
	return " " + indent + dimStyle.Render(arrow) + " " + name + " " + mark
}

// changedRow renders changed file f in the file list, w columns wide. The
// row shows the name in the tree, after indent.
func (m *model) changedRow(f changedFile, indent, name string, w int) string {
	adds := addStyle.Render(fmt.Sprintf("+%d", f.adds))
	stat := adds + " " + delStyle.Render(fmt.Sprintf("-%d", f.dels))
	if c := m.comments.Count(m.anchorOf(tabChanges), f.Path); c > 0 {
		stat = cursorStyle.Render(fmt.Sprintf("●%d ", c)) + stat
	}
	if f.From != "" {
		name = f.From + " → " + name
	}
	return fileRow(indent, statusMark(f.Status), name, stat, w)
}

// entryRow renders entry e of the Files tab in the file list, w columns
// wide. The row shows the name in the tree, after indent.
func (m *model) entryRow(e vcs.Entry, indent, name string, w int) string {
	if e.Ignored {
		return dimStyle.Render(fileRow(indent, " ", name, "", w))
	}
	stat := ""
	if c := m.comments.Count(m.anchorOf(tabFiles), e.Path); c > 0 {
		stat = cursorStyle.Render(fmt.Sprintf("●%d", c))
	}
	return fileRow(indent, " ", name, stat, w)
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

// listStart returns the first row that the file list shows in h lines.
// The first line is the title.
func (m *model) listStart(h int) int {
	t := m.list()
	switch {
	case t == nil:
		return 0
	case t.free:
		return min(max(t.top, 0), max(len(t.rows)-(h-1), 0))
	}
	return max(0, t.cur-(h-2))
}

// fileRow returns " M name/of/file.go  +3 -1", with indent before the
// mark.
func fileRow(indent, mark, p, stat string, w int) string {
	room := w - ansi.StringWidth(indent) - lipgloss.Width(stat) - 4
	return " " + indent + mark + " " + fitName(p, room) + " " + stat
}

// fitName returns p in room columns, with spaces after it. If p is too
// long, fitName removes the directory first, then the start of the name.
func fitName(p string, room int) string {
	p = termtext.Quote(p)
	if n := ansi.StringWidth(p); n > room {
		if base := path.Base(p); ansi.StringWidth(base)+2 <= room {
			p = "…/" + base
		} else {
			p = "…" + ansi.TruncateLeft(p, n-room+1, "")
		}
	}
	// Pad by width, not by runes.
	return p + strings.Repeat(" ", max(room-ansi.StringWidth(p), 0))
}

// mainLines renders the visible part of the main pane. rowAt[y] is the row
// on screen line y, or -1 for a note, a comment, or the comment box. hits
// are the find hits that get a mark. The marks do not change rowAt.
func (m *model) mainLines(h int, hits []findHit) (lines []string, rowAt []int) {
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
	marked := map[int]bool{}
	for _, hit := range hits {
		if hit.line == 0 { // a hit in a fold has no row of its own
			marked[hit.row] = true
		}
	}
	for i := p.top; i < len(p.lines) && len(lines) < h; i++ {
		// A wrapped line shows its marks on its first row, and its
		// comments below its last row.
		t := m.target(i, diff.SideNew)
		add(m.rowMark(i, t, lo, hi, marked[i])+p.lines[i], i)
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
		return termtext.Quote(p.note) // the note can come from git or a forge
	case m.tab == tabPR && m.prf.inFlight:
		return "loading the pull request…"
	case m.tab == tabFiles && len(m.allFiles) == 0:
		return "no files"
	case m.tab == tabFiles && p.path != "":
		return "empty file"
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
// shows, or nil. hit is true for a find hit. The mark shows the cursor,
// the rows lo to hi, a find hit, or a comment.
func (m *model) rowMark(i int, t *targetLine, lo, hi int, hit bool) string {
	p := m.cur()
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
	return commentMark(m.comments.At(m.anchor(), p.path, t.side, t.line))
}

// commentsBelow returns the lines of the comments whose range ends on
// line t. The comment that the box edits does not show.
func (m *model) commentsBelow(t *targetLine) []string {
	var out []string
	for _, c := range m.comments.EndingAt(m.anchor(), m.cur().path, t.side, t.line) {
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
	if c == nil {
		return "  "
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
		prefix := "    💬 "
		if i > 0 {
			prefix = "       "
		}
		out = append(out, truncate(prefix+noteStyle.Render(l), m.mainWidth()))
	}
	return out
}

// ensureVisible scrolls the main pane so that the cursor and the comment
// box below it show. The box shows below the last row of its line. When
// the box is open, a line taller than the screen shows only its end. When
// the box is closed, the line shows only its first rows.
func (m *model) ensureVisible() {
	p := m.cur()
	h := m.mainHeight()
	if p.free {
		p.top = min(p.top, max(len(p.lines)-h, 0))
		return
	}
	p.cur = p.head(p.cur) // the cursor is on the first row of a wrapped line
	last, box := p.cur, 0
	if m.mode == modeComment {
		last, box = p.end(p.cur), lipgloss.Height(m.editor.View())
	}
	// Each row takes one line or more. Thus the last row cannot show if it is
	// h rows or more below the top.
	p.top = max(p.top, last-h+1)
	p.top = min(last, p.top, max(len(p.lines)-1, 0))
	for p.top < last {
		_, rowAt := m.mainLines(h, nil)
		idx := slices.Index(rowAt, p.end(p.cur))
		if idx >= 0 && idx+1+box <= h {
			return
		}
		p.top++
	}
}

// footer renders the notice, the status, and the help of the keys. The
// notice wraps, so that the user can read all of it. hits are the hits of
// the find band.
func (m *model) footer(hits []findHit) string {
	if m.notice != "" {
		notice := termtext.Quote(m.notice) // the notice can have a path
		return delStyle.Render(wrapWords(notice, m.w)) + "\n" + m.footerLine(hits)
	}
	return m.footerLine(hits)
}

// wrapWords breaks s into lines of width w or less, at spaces only.
// ansi.Wrap and lipgloss let a line end past w at a hyphen. Then the edge
// of the screen cuts off a "--". wrapWords cuts a word wider than w. A
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

// footerLine renders the status and the help of the keys, or the find
// band.
func (m *model) footerLine(hits []findHit) string {
	if m.mode == modeFind {
		return m.findBand(hits)
	}
	m.help.SetWidth(m.w)
	var badge string
	switch {
	case m.status != "":
		badge = cursorStyle.Render(m.status)
	case m.release != nil:
		badge = cursorStyle.Render(m.releaseBadge())
	case m.mode == modeComment && m.draft != nil:
		badge = cursorStyle.Render("✎ " + m.draft.Location())
	case m.comments.Len() > 0:
		badge = cursorStyle.Render("● " + plural(m.comments.Len(), "unsent comment"))
	}
	hk := helpKeys{
		k:      m.keys,
		mode:   m.mode,
		tab:    m.tab,
		split:  m.splitShown(),
		visual: m.cur().anchor >= 0,
		atFold: m.atFold(),
		armed:  m.armed,
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

// bodyHeight returns the number of lines between the header and the footer.
func (m *model) bodyHeight() int { return m.heightAbove(m.footer(m.shownHits())) }

// heightAbove returns the height of the body above footer.
func (m *model) heightAbove(footer string) int {
	return max(m.h-lipgloss.Height(footer)-2, 3)
}

// View renders the screen.
func (m *model) View() tea.View {
	if !m.sized {
		// A frame of the guessed size shows wrong for a moment.
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	hits := m.shownHits()
	footer := m.footer(hits)
	h := m.heightAbove(footer)
	hdr, _ := m.header()

	mh := m.mainHeightOf(h)
	ml, _ := m.mainLines(mh, hits)
	main := box(ml, m.mainWidth(), mh)
	body := main
	rule := dimStyle.Render(strings.Repeat("─", m.w))
	switch lw, lh := m.listWidth(), m.listHeight(h); {
	case lh > 0:
		list := box(m.listLines(lh-1), lw, lh-1)
		if m.listPos == positionTop {
			body = lipgloss.JoinVertical(lipgloss.Left, list, rule, main)
		} else {
			body = lipgloss.JoinVertical(lipgloss.Left, main, rule, list)
		}
	case lw > 0:
		list := box(m.listLines(h), lw, h)
		sep := dimStyle.Render(strings.TrimSuffix(strings.Repeat(" │ \n", h), "\n"))
		if m.listPos == positionLeft {
			body = lipgloss.JoinHorizontal(lipgloss.Top, list, sep, main)
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, main, sep, list)
		}
	}
	screen := hdr + "\n" + body + "\n" + rule + "\n" + footer

	if m.mode == modeOverlay && m.overlay != nil {
		ov, x, y, _ := m.overlayBox()
		c := lipgloss.NewCanvas(m.w, m.h)
		c.Compose(lipgloss.NewLayer(screen))
		c.Compose(lipgloss.NewLayer(ov).X(x).Y(y).Z(1))
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

// programStatus returns the status that rexdiff reports to Rex with OSC 7501.
// Rex shows it in its tabs and its sidebar.
func (m *model) programStatus() *tea.ProgramStatus {
	msg := "reviewing " + plural(len(m.changeFiles), "file")
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

// mainX returns the screen column where the main pane starts.
func (m *model) mainX() int {
	if m.listPos == positionLeft && m.listWidth() > 0 {
		return m.listWidth() + sepWidth
	}
	return 0
}

// mainY returns the screen line where the main pane starts.
func (m *model) mainY() int {
	if m.listPos == positionTop {
		return bodyY + m.listHeight(m.bodyHeight())
	}
	return bodyY
}

// listX returns the screen column where the file list starts.
func (m *model) listX() int {
	if m.listPos == positionRight {
		return m.mainWidth() + sepWidth
	}
	return 0
}

// listY returns the screen line where the file list starts. Below the
// main pane, the list starts under the rule.
func (m *model) listY() int {
	if m.listPos == positionBottom {
		return bodyY + m.mainHeight() + 1
	}
	return bodyY
}

// listRows returns the number of lines of the file list, with its title.
func (m *model) listRows() int {
	h := m.bodyHeight()
	if m.listPos.stacked() {
		return max(m.listHeight(h)-1, 0)
	}
	return h
}

// sepWidth is the width of the line " │ " between the panes.
const sepWidth = 3

// bodyY is the screen line where the body starts, below the header.
const bodyY = 1
