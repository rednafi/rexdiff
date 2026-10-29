package tui

import (
	"fmt"
	"path"
	"strconv"
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

// The colours are ANSI palette indexes, so they follow the terminal theme.
var (
	// dimColor is the colour of dim text and borders.
	dimColor     = lipgloss.Color("8")
	dimStyle     = lipgloss.NewStyle().Foreground(dimColor)
	boldStyle    = lipgloss.NewStyle().Bold(true)
	addedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	deletedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	noteStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))
	findStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	titleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // the file path above the diff
)

// truncate cuts s to at most w columns, ending with an ellipsis if it
// was cut.
func truncate(s string, w int) string { return ansi.Truncate(s, max(w, 0), "…") }

// dark reports whether the terminal background is dark. An unknown
// background counts as dark.
func (m *model) dark() bool {
	return m.bg == nil || tea.BackgroundColorMsg{Color: m.bg}.IsDark()
}

// highlightStyle returns the style for a highlighted row: a tint of the
// terminal background, or reverse video if the background is unknown.
func (m *model) highlightStyle() lipgloss.Style {
	if m.bg == nil {
		return lipgloss.NewStyle().Reverse(true)
	}
	tint := lipgloss.Lighten(m.bg, 0.15)
	if !m.dark() {
		tint = lipgloss.Darken(m.bg, 0.15)
	}
	return lipgloss.NewStyle().Bold(true).Background(tint)
}

// headerAction is the action that clicking part of the header performs.
type headerAction int

const (
	headerNone headerAction = iota
	headerTabChanges
	headerTabAllFiles
	headerTabPR
	headerScope0  // the first scope; add the scope's index for the others
	headerBase    = headerScope0 + headerAction(len(scopes))
	headerCommits = headerBase + 1
	headerAgent   = headerBase + 2
)

// headerSpan is a clickable part of the header.
type headerSpan struct {
	x0, x1 int
	action headerAction
}

// headerLine builds the top line and records its clickable parts.
type headerLine struct {
	b     strings.Builder
	spans []headerSpan
	x     int // the width so far, in columns
}

// add appends s to the line. Clicking s performs action, unless action is
// headerNone.
func (l *headerLine) add(s string, action headerAction) {
	w := lipgloss.Width(s)
	if action != headerNone {
		l.spans = append(l.spans, headerSpan{l.x, l.x + w, action})
	}
	l.b.WriteString(s)
	l.x += w
}

// header renders the top line and returns its clickable parts.
func (m *model) header() (string, []headerSpan) {
	var l headerLine
	for i, t := range []string{"Changes", "Files", "PR"} {
		// The key is bold and its label plain, as in the footer. The
		// labels of the other tabs are dim.
		name := dimStyle.Render(t)
		if tab(i) == m.tab {
			name = t
		}
		l.add(" "+boldStyle.Render(strconv.Itoa(i+1))+" "+name+" ", headerTabChanges+headerAction(i))
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
	// The right end shows where a review would be sent.
	if right := m.agentLabel(); right != "" {
		if pad := m.w - l.x - lipgloss.Width(right) - 1; pad > 0 {
			l.add(strings.Repeat(" ", pad), headerNone)
			l.add(right, headerAgent)
		}
	}
	return truncate(l.b.String(), m.w), l.spans
}

// headerScopes adds the scopes to the header, followed by what the current
// scope compares against.
func (m *model) headerScopes(l *headerLine) {
	for i, s := range scopes {
		style := dimStyle
		if s == m.scope {
			style = lipgloss.NewStyle()
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

// commitsLabel returns the header text naming the commits chosen for the
// commits scope.
func (m *model) commitsLabel() string {
	if m.newest == "" {
		return "pick ▾"
	}
	label := shortSHA(m.newest)
	if m.oldest != m.newest {
		label = shortSHA(m.oldest) + ".." + shortSHA(m.newest)
	}
	return label + dimStyle.Render(" ▾")
}

// shortSHA returns the first 7 characters of a commit hash.
func shortSHA(s string) string { return s[:min(len(s), 7)] }

// turnLabel returns the header text about the last turn. It returns ""
// for a turn saved by an earlier run, and when there is no turn, because
// the Changes tab then explains why.
func (m *model) turnLabel() string {
	if m.turn == nil || m.turn.at.IsZero() {
		return ""
	}
	ago := time.Since(m.turn.at).Round(time.Second)
	app := termtext.Quote(m.turn.app) // the agent reports the app name, so quote it
	if m.turn.running {
		return noteStyle.Render("● ") + app + dimStyle.Render(" working · "+ago.String())
	}
	return app + dimStyle.Render(" · "+ago.String()+" ago")
}

// agentLabel returns the header text saying how many agents the next
// review could be sent to.
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

// statusMark returns the status letter shown for a changed file in the
// file list.
func statusMark(s vcs.FileStatus) string {
	switch s {
	case vcs.StatusAdded:
		return addedStyle.Render("A")
	case vcs.StatusDeleted:
		return deletedStyle.Render("D")
	case vcs.StatusModified:
		return noteStyle.Render("M")
	case vcs.StatusRenamed:
		return noteStyle.Render("R")
	}
	return " "
}

// navigatorLines renders h lines of the file list: a title followed by the
// rows of the tree.
func (m *model) navigatorLines(h int) []string {
	t := m.curNavigator()
	if t == nil {
		return nil
	}
	w := m.navigatorWidth()
	title := fmt.Sprintf("Changes (%d)", max(len(m.changes.files), m.changes.tooMany))
	if m.tab == tabAllFiles {
		title = fmt.Sprintf("Files (%d)", m.allFiles.fileCount)
		if n := m.allFiles.more; n > 0 {
			title += fmt.Sprintf(" … %d more files not listed", n)
		}
	}
	// The title lines up with the status letters of the rows.
	out := []string{truncate(" "+headerStyle.Render(title), w)}
	for i := m.navigatorStart(h); i < len(t.rows) && len(out) < h; i++ {
		// A long name or count must not wrap the row onto the next line.
		r := truncate(m.navigatorRow(t.rows[i], w), w)
		if i == t.cur {
			r = m.highlightRow(r, w)
		}
		out = append(out, r)
	}
	return out
}

// navigatorRow renders row r of the file list, w columns wide.
func (m *model) navigatorRow(r tree.Row, w int) string {
	indent := strings.Repeat("  ", r.Depth)
	switch {
	case r.File < 0:
		return m.dirRow(r, indent, w)
	case m.tab == tabChanges:
		return m.changesRow(m.changes.files[r.File], indent, r.Name, w)
	}
	return m.allFilesRow(m.allFiles.entries[r.File], indent, r.Name, w)
}

// dirRow renders directory row r of the file list, w columns wide. In the
// Files tab, a collapsed directory that contains changes gets a dot.
func (m *model) dirRow(r tree.Row, indent string, w int) string {
	arrow, mark := "▸", ""
	if r.Expanded {
		arrow = "▾"
	}
	if m.tab == tabAllFiles && !r.Expanded && r.Changed {
		mark = noteStyle.Render("•")
	}
	room := w - ansi.StringWidth(indent) - lipgloss.Width(mark) - 4
	st := boldStyle
	if r.Ignored {
		st = dimStyle
	}
	name := st.Render(fitName(r.Name+"/", room))
	return " " + indent + dimStyle.Render(arrow) + " " + name + " " + mark
}

// changesRow renders changed file f in the file list, w columns wide. The
// row shows name, its name in the tree, after indent.
func (m *model) changesRow(f changedFile, indent, name string, w int) string {
	stat := f.stat()
	if c := m.comments.Count(m.anchorOf(tabChanges), f.Path); c > 0 {
		stat = accentStyle.Render(fmt.Sprintf("●%d ", c)) + stat
	}
	if f.OldPath != "" {
		name = f.OldPath + " → " + name
	}
	return navigatorRowText(indent, statusMark(f.Status), name, stat, w)
}

// allFilesRow renders entry e in the Files tab's file list, w columns
// wide. The row shows name, its name in the tree, after indent.
func (m *model) allFilesRow(e vcs.Entry, indent, name string, w int) string {
	if e.Ignored {
		return dimStyle.Render(navigatorRowText(indent, " ", name, "", w))
	}
	stat := ""
	if c := m.comments.Count(m.anchorOf(tabAllFiles), e.Path); c > 0 {
		stat = accentStyle.Render(fmt.Sprintf("●%d", c))
	}
	return navigatorRowText(indent, " ", name, stat, w)
}

// highlightRow highlights r, the row of the current file, w columns wide.
// When the file list has focus, a bar replaces the first space.
func (m *model) highlightRow(r string, w int) string {
	style := m.highlightStyle().Width(w)
	if !m.navigatorFocused {
		return style.Render(r)
	}
	return style.Render(accentStyle.Render("▎") + ansi.TruncateLeft(r, 1, ""))
}

// navigatorStart returns the first row that the file list shows in h
// lines, the first of which is the title.
func (m *model) navigatorStart(h int) int {
	if t := m.curNavigator(); t != nil {
		return t.start(h - 1)
	}
	return 0
}

// navigatorRowText returns a file list row such as
// " M name/of/file.go  +3 -1", with indent before the status letter.
func navigatorRowText(indent, mark, p, stat string, w int) string {
	room := w - ansi.StringWidth(indent) - lipgloss.Width(stat) - 4
	return " " + indent + mark + " " + fitName(p, room) + " " + stat
}

// fitName fits p into room columns, padding it with spaces. If p is too
// long, it drops the directory first, and then cuts the start of the name.
func fitName(p string, room int) string {
	if q := termtext.Quote(p); ansi.StringWidth(q) <= room {
		p = q
	} else if base := path.Base(q); ansi.StringWidth(base)+2 <= room {
		p = "…/" + base
	} else {
		p = "…" + lastColumns(q, room-1)
	}
	// Pad by display width, not by rune count. A wide character cut in
	// half leaves a column to pad.
	return p + strings.Repeat(" ", max(room-ansi.StringWidth(p), 0))
}

// lastColumns returns the end of s that fits in n columns. It drops a
// wide character that does not fit whole, so the result can be a column
// short of n.
func lastColumns(s string, n int) string {
	n = max(n, 0)
	w := ansi.StringWidth(s)
	cut := max(w-n, 0)
	end := ansi.TruncateLeft(s, cut, "")
	// TruncateLeft keeps a wide character that the cut falls inside.
	for ansi.StringWidth(end) > n {
		cut++
		end = ansi.TruncateLeft(s, cut, "")
	}
	return end
}

// mainLines renders the visible part of the main pane. rowAt[y] is the row
// shown on screen line y, or -1 for a note, a comment or the comment box.
// hits are the find matches to mark. The marks do not affect rowAt.
func (m *model) mainLines(h int, hits []findHit) (lines []string, rowAt []int) {
	p := m.curPane()
	if note := m.emptyNote(); note != "" {
		return m.noteLines(note, h)
	}
	lo, hi := m.highlightRange()
	add := func(l string, row int) {
		lines, rowAt = append(lines, l), append(rowAt, row)
	}
	// addBelow adds lines that show no source line, as long as there is
	// room.
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
		if hit.line == 0 { // a match inside a fold has no row of its own
			marked[hit.row] = true
		}
	}
	for i := p.top; i < p.count() && len(lines) < h; i++ {
		// A wrapped line shows its markers on its first row and its
		// comments below its last row.
		target := m.targetAt(i, diff.SideNew)
		add(m.rowMark(i, target, lo, hi, marked[i])+p.line(i), i)
		if target != nil && p.lineEnd(i) == i {
			addBelow(m.commentsBelow(i, target))
		}
		if m.mode == modeComment && i == p.lineEnd(p.cur) {
			addBelow(m.commentBox())
		}
	}
	return lines, rowAt
}

// noteLines renders note, the text of an empty pane, for a pane h lines
// high. The note is indented and wrapped, with the indent on every line.
// A blank line comes first if the pane, and the screen below the header,
// have room for it.
func (m *model) noteLines(note string, h int) (lines []string, rowAt []int) {
	wrapped := strings.Split(wrapWords(note, m.mainWidth()-2), "\n")
	if min(h, m.h-1-m.footerHeight()) > len(wrapped) {
		lines = append(lines, "")
	}
	for _, l := range wrapped {
		lines = append(lines, "  "+dimStyle.Render(l))
	}
	rowAt = make([]int, len(lines))
	for i := range rowAt {
		rowAt[i] = -1
	}
	return lines, rowAt
}

// emptyNote returns the text to show instead of an empty pane, or "" if
// the pane is not empty.
func (m *model) emptyNote() string {
	p := m.curPane()
	switch {
	case p.count() > 0:
		return ""
	case p.note != "":
		return termtext.Quote(p.note) // the note may come from git or a forge
	case m.tab == tabPR && m.pr.fetch.inFlight:
		return "loading the pull request…"
	case m.tab == tabAllFiles && m.allFiles.entries == nil && (m.allFiles.listing.running || m.allFiles.listing.want):
		return "loading files…"
	case m.tab == tabAllFiles && len(m.allFiles.entries) == 0:
		return "no files"
	case m.tab == tabAllFiles && p.path != "":
		return "empty file"
	}
	return ""
}

// highlightRange returns the first and last rows of the drag or the
// selection. With neither, it returns -1, -1.
func (m *model) highlightRange() (lo, hi int) {
	p := m.curPane()
	switch {
	case m.drag.active && m.drag.moved:
		return p.span(m.drag.startRow, p.cur)
	case p.selStart >= 0:
		return p.selRange()
	}
	return -1, -1
}

// rowMark returns the gutter marker for row i. target is the line that
// row i shows, or nil, and hit reports whether the row is a find match.
// The marker shows, in order of priority, the cursor, the highlighted
// rows lo to hi, a find match, or a comment.
func (m *model) rowMark(i int, target *targetLine, lo, hi int, hit bool) string {
	p := m.curPane()
	// The first row of a line taller than the pane can scroll above the
	// top, so the top row then shows the cursor while the line is in view.
	cur := i == p.cur || i == p.top && p.cur < p.top && p.lineEnd(p.cur) >= p.top
	switch {
	case cur && m.tab != tabPR:
		return accentStyle.Render("▶")
	case lo >= 0 && i >= lo && i <= hi:
		return accentStyle.Render("┃")
	case hit:
		return findStyle.Render("≈")
	case target == nil || p.cont(i):
		return commentMark(nil)
	}
	return commentMark(m.commentOn(i, target))
}

// commentOn returns a comment on row i, which shows target, or nil. In
// rendered Markdown, it returns a comment on any line of the block that
// contains row i.
func (m *model) commentOn(i int, target *targetLine) *diff.Comment {
	p := m.curPane()
	if p.removedMarker(i) {
		return nil
	}
	first, last := target.line, target.line
	if p.srcRows != nil {
		first, last = p.rows[p.lineStart(i)].New, p.rows[p.lineEnd(i)].New
	}
	for line := first; line <= last; line++ {
		if c := m.comments.At(m.curAnchor(), p.path, target.side, line); c != nil {
			return c
		}
	}
	return nil
}

// commentsBelow returns the rendered lines of the comments whose range
// ends on row i, which shows target. In rendered Markdown, these are the
// comments that end anywhere in the block of row i. The comment being
// edited in the comment box is left out.
func (m *model) commentsBelow(i int, target *targetLine) []string {
	p := m.curPane()
	if p.removedMarker(i) {
		return nil
	}
	first := target.line
	if p.srcRows != nil {
		first = p.rows[p.lineStart(i)].New
	}
	var out []string
	for line := first; line <= target.line; line++ {
		for _, c := range m.comments.EndingAt(m.curAnchor(), p.path, target.side, line) {
			if m.mode == modeComment && c.ID == m.draft.editID {
				continue
			}
			out = append(out, m.commentLines(c)...)
		}
	}
	return out
}

// commentMark returns the gutter marker for a line with comment c, or a
// space if c is nil.
func commentMark(c *diff.Comment) string {
	if c == nil {
		return " "
	}
	return accentStyle.Render("●")
}

// commentBox returns the lines of the comment box, indented to sit below
// the code.
func (m *model) commentBox() []string {
	var box []string
	for l := range strings.SplitSeq(m.commentInput.View(), "\n") {
		box = append(box, "    "+l)
	}
	return box
}

// commentLines returns the lines that display comment c below the lines
// it refers to. A stale comment starts with "stale".
func (m *model) commentLines(c *diff.Comment) []string {
	var out []string
	stale := m.commentStale(c)
	for i, l := range strings.Split(c.Text, "\n") {
		prefix := "    💬 "
		switch {
		case i > 0:
			prefix = "       "
		case stale:
			prefix += accentStyle.Render("stale") + " "
		}
		out = append(out, truncate(prefix+noteStyle.Render(l), m.mainWidth()))
	}
	return out
}

// scrollToCursor scrolls the main pane so that the cursor, and the comment
// box below it, are visible. The box appears below the last row of the
// cursor line. If a line is taller than the screen, only its end is shown
// while the box is open, and only its first rows while the box is closed.
// It also scrolls the file list so that its cursor is visible. Each view
// moves as little as possible.
func (m *model) scrollToCursor() {
	// A hidden file list keeps its top row for when it is shown again.
	if t, h := m.curNavigator(), m.navigatorRows()-1; t != nil && m.navigatorWidth() > 0 && h > 0 {
		t.scrollToCursor(h) // h excludes the title line
	}
	p := m.curPane()
	h := m.shownMainHeight()
	if !p.following {
		p.top = min(p.top, max(p.count()-h, 0))
		return
	}
	p.cur = p.lineStart(p.cur) // keep the cursor on the first row of a wrapped line
	last, box := p.cur, 0
	if m.mode == modeComment {
		// The line's comments appear between the line and the box.
		last = p.lineEnd(p.cur)
		box = lipgloss.Height(m.commentInput.View()) + m.linesBelow(last)
	}
	// Each row takes at least one line, so row last cannot be visible if
	// it is h or more rows below the top.
	p.top = min(keepInView(p.top, last, h), p.last())
	if p.top >= last {
		return
	}
	if m.emptyNote() != "" { // the note replaces all rows
		p.top = last
		return
	}
	// The end of the cursor line is visible if it lands on screen line 0
	// to h-1-box. Starting from that line, move the top up one row at a
	// time for as long as the end stays in that range. Each row takes its
	// own line plus the lines of the comments below it. The top never
	// moves above its current value.
	end := p.lineEnd(p.cur)
	top, used := end, 0
	for top > p.top {
		n := 1 + m.linesBelow(top-1)
		if used+n+1+box > h {
			break
		}
		used += n
		top--
	}
	p.top = min(top, last)
}

// linesBelow returns the number of comment lines shown below row i of the
// main pane, as mainLines adds them.
func (m *model) linesBelow(i int) int {
	p := m.curPane()
	target := m.targetAt(i, diff.SideNew)
	if target == nil || p.lineEnd(i) != i {
		return 0
	}
	return len(m.commentsBelow(i, target))
}

// footer renders the notice, the status and the key help. The notice is
// wrapped so that the user can read all of it. hits are the matches shown
// in the find bar.
func (m *model) footer(hits []findHit) string {
	line := m.footerLine(hits)
	if m.notice != "" {
		notice := termtext.Quote(m.notice) // the notice may contain a path
		line = deletedStyle.Render(wrapWords(notice, m.footerWidth())) + "\n" + line
	}
	return inset(line)
}

// footerWidth returns the width of the footer, not counting the blank
// column at its left. The header has the same blank column.
func (m *model) footerWidth() int { return max(m.w-1, 1) }

// inset adds a space at the start of each line of s.
func inset(s string) string { return " " + strings.ReplaceAll(s, "\n", "\n ") }

// wrapWords breaks s into lines at most w columns wide, breaking only at
// spaces. A word wider than w is cut, and a character wider than w gets a
// line of its own. It does not use [ansi.Wrap], because that can let a
// line run past w at a hyphen.
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

// footerLine renders the status and the key help, or the find bar when it
// is open.
func (m *model) footerLine(hits []findHit) string {
	if m.mode == modeFind {
		return m.findBand(hits)
	}
	w := m.footerWidth()
	m.help.SetWidth(w)
	var badge string
	switch {
	case m.flashText != "":
		badge = accentStyle.Render(m.flashText)
	case m.release != nil:
		badge = accentStyle.Render(m.releaseBadge())
	case m.mode == modeComment && m.draft != nil:
		badge = accentStyle.Render("✎ " + m.draft.Location())
	case m.comments.Len() > 0:
		badge = accentStyle.Render("● " + plural(m.comments.Len(), "unsent comment"))
	}
	hk := helpKeys{
		keys:                 m.keys,
		mode:                 m.mode,
		tab:                  m.tab,
		split:                m.splitShown(),
		selecting:            m.curPane().selStart >= 0,
		cursorOnFold:         m.cursorOnFold(),
		cursorInExpandedFold: m.canCollapseFold(),
		crossStep:            m.crossStep,
	}
	if m.help.ShowAll {
		h := m.help.View(hk)
		if badge != "" {
			h = badge + "\n" + h
		}
		return h
	}
	if badge != "" {
		m.help.SetWidth(max(w-lipgloss.Width(badge)-len(helpGap), 10))
		return truncate(badge+helpGap+m.help.View(hk), w)
	}
	return m.help.View(hk)
}

// bodyHeight returns the number of lines between the header and the
// footer, and at least 3.
func (m *model) bodyHeight() int { return max(m.h-m.footerHeight()-1, 3) }

// shownBodyHeight returns the number of body lines on screen. On a short
// terminal, View cuts the body to make room for the footer, so this can
// be less than bodyHeight.
func (m *model) shownBodyHeight() int {
	return min(m.bodyHeight(), max(m.h-1-m.footerHeight(), 0))
}

// shownMainHeight returns the number of rows of the main pane on screen,
// and at least 1. On a short terminal, View cuts the body, so this can be
// less than mainHeight.
func (m *model) shownMainHeight() int {
	room := m.shownBodyHeight() - m.titleHeight()
	if m.navigatorPosition == positionTop {
		room -= m.navigatorHeight(m.bodyHeight())
	}
	return max(min(m.mainHeight(), room), 1)
}

// footerHeight returns the height of the footer, as heightAbove measures
// it, without rendering the footer. Only the full key list can take more
// than one line, so it is the only part rendered.
func (m *model) footerHeight() int {
	n := 1
	if m.mode != modeFind && m.help.ShowAll {
		n = lipgloss.Height(m.footerLine(nil))
	}
	if m.notice != "" {
		n += lipgloss.Height(wrapWords(termtext.Quote(m.notice), m.footerWidth()))
	}
	return n
}

// heightAbove returns the height of the body above the rendered footer,
// and at least 3.
func (m *model) heightAbove(footer string) int {
	return max(m.h-lipgloss.Height(footer)-1, 3)
}

// View renders the screen.
func (m *model) View() tea.View {
	if !m.sized {
		// Draw nothing until the terminal reports its size, because a
		// frame drawn at the guessed size would flash up wrong.
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}
	hits := m.shownHits()
	footer := m.footer(hits)
	h := m.heightAbove(footer)
	hdr, _ := m.header()

	mh, mw := m.mainHeightOf(h), m.mainWidth()
	title := ""
	if m.titleHeight() > 0 {
		title = m.mainTitle()
	}
	// With no file to name, the note of an empty pane starts on the title
	// line, so that a short terminal still shows it.
	noteOnTitle := m.titleHeight() > 0 && title == "" && m.emptyNote() != ""
	if noteOnTitle {
		mh++
	}
	ml, _ := m.mainLines(mh, hits)
	main, mainExact := box(ml, mw, mh)
	if m.titleHeight() > 0 && !noteOnTitle {
		// Pad a title that fits to the full width of the pane.
		if tw := ansi.StringWidth(title); tw <= mw {
			title += strings.Repeat(" ", mw-tw)
		} else {
			mainExact = false
		}
		main = title + "\n" + main
	}
	body := main
	rule := dimStyle.Render(strings.Repeat("─", m.w))
	switch lw, lh := m.navigatorWidth(), m.navigatorHeight(h); {
	case lh > 0:
		navigator, _ := box(m.navigatorLines(lh-1), lw, lh-1)
		if m.navigatorPosition == positionTop {
			body = lipgloss.JoinVertical(lipgloss.Left, navigator, rule, main)
		} else {
			body = lipgloss.JoinVertical(lipgloss.Left, main, rule, navigator)
		}
	case lw > 0 && !m.navigatorPosition.stacked(): // a file list above or below with no rows is not shown
		navigator, navigatorExact := box(m.navigatorLines(h), lw, h)
		sep := dimStyle.Render(strings.TrimSuffix(strings.Repeat(" │\n", h), "\n"))
		parts := []string{main, sep, navigator}
		if m.navigatorPosition == positionLeft {
			parts = []string{navigator, sep, main}
		}
		if mainExact && navigatorExact {
			// Every line of each part is exactly as wide as the part, so
			// the lines can be joined without measuring.
			body = joinColumns(parts...)
		} else {
			body = lipgloss.JoinHorizontal(lipgloss.Top, parts...)
		}
	}
	// On a short terminal, the body keeps its minimum height and can push
	// the footer off the screen. Cut the body instead, so that the find
	// bar and the key help stay visible.
	if room := max(m.h-1-lipgloss.Height(footer), 0); lipgloss.Height(body) > room {
		body = strings.Join(strings.Split(body, "\n")[:room], "\n")
	}
	screen := hdr + "\n" + footer
	if body != "" {
		screen = hdr + "\n" + body + "\n" + footer
	}

	if m.overlay != nil {
		ov, x, y, _ := m.overlayBox()
		c := lipgloss.NewCanvas(m.w, m.h)
		c.Compose(lipgloss.NewLayer(screen))
		c.Compose(lipgloss.NewLayer(ov).X(x).Y(y).Z(1))
		screen = c.Render()
	}

	// On a small terminal, the parts keep their minimum sizes and can
	// overflow, so cut the screen to the terminal size.
	screen = fitScreen(screen, max(m.w, 1), max(m.h, 1))
	v := tea.NewView(screen)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.ProgramStatus = m.programStatus()
	return v
}

// box joins lines into a block of w columns and h lines. exact reports
// whether every line is exactly w columns wide; if not, a line with a wide
// character at the edge may be one column short.
func box(lines []string, w, h int) (block string, exact bool) {
	if out, ok := paddedBox(lines, w, h); ok {
		return out, true
	}
	return styledBox(lines, w, h), false
}

// joinColumns joins the blocks side by side, like
// [lipgloss.JoinHorizontal] but without measuring widths. Each block must
// have the same number of lines, all as wide as the block.
func joinColumns(blocks ...string) string {
	split := make([][]string, len(blocks))
	for i, b := range blocks {
		split[i] = strings.Split(b, "\n")
	}
	var b strings.Builder
	for i := range split[0] {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, lines := range split {
			if i < len(lines) {
				b.WriteString(lines[i])
			}
		}
	}
	return b.String()
}

// styledBox implements box with a lipgloss style. It handles any input.
func styledBox(lines []string, w, h int) string {
	st := lipgloss.NewStyle().Width(w).MaxWidth(w).Height(h).MaxHeight(h)
	return st.Render(strings.Join(lines, "\n"))
}

// paddedBox implements box for lines that already fit, by padding them as
// the style in styledBox would. It measures each line only once. ok is
// false if a line is too wide or contains a tab or carriage return, which
// the style would change.
func paddedBox(lines []string, w, h int) (string, bool) {
	if w <= 0 || h <= 0 {
		return "", false
	}
	var b strings.Builder
	b.Grow(h * (w + 1))
	n := 0
	for _, l := range lines {
		if n == h {
			break
		}
		lw := ansi.StringWidth(l)
		if lw > w || strings.ContainsAny(l, "\t\r\n") {
			return "", false
		}
		if n > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l)
		b.WriteString(strings.Repeat(" ", w-lw))
		n++
	}
	for ; n < h; n++ {
		if n > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat(" ", w))
	}
	return b.String(), true
}

// fitScreen cuts the screen to w columns and h lines, as a style with
// MaxWidth and MaxHeight would. Like that style, it pads each line to the
// width of the widest line.
func fitScreen(screen string, w, h int) string {
	slow := func() string { return lipgloss.NewStyle().MaxWidth(w).MaxHeight(h).Render(screen) }
	if strings.ContainsAny(screen, "\t\r") {
		return slow()
	}
	lines := strings.Split(screen, "\n")
	widths := make([]int, len(lines))
	widest := 0
	for i, l := range lines {
		widths[i] = ansi.StringWidth(l)
		widest = max(widest, widths[i])
	}
	if widest > w {
		return slow()
	}
	lines = lines[:min(len(lines), h)]
	for i, l := range lines {
		lines[i] = l + strings.Repeat(" ", widest-widths[i])
	}
	return strings.Join(lines, "\n")
}

// programStatus returns the status that rexdiff reports to Rex with OSC
// 7501. Rex shows it in its tabs and sidebar.
func (m *model) programStatus() *tea.ProgramStatus {
	msg := "reviewing " + plural(max(len(m.changes.files), m.changes.tooMany), "file")
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
	if m.navigatorPosition == positionLeft && m.navigatorWidth() > 0 {
		return m.navigatorWidth() + sepWidth
	}
	return 0
}

// mainY returns the screen line where the main pane starts.
func (m *model) mainY() int {
	if m.navigatorPosition == positionTop {
		return bodyY + m.navigatorHeight(m.bodyHeight()) + m.titleHeight()
	}
	return bodyY + m.titleHeight()
}

// navigatorX returns the screen column where the file list starts.
func (m *model) navigatorX() int {
	if m.navigatorPosition == positionRight {
		return m.mainWidth() + sepWidth
	}
	return 0
}

// navigatorY returns the screen line where the file list starts. Below
// the main pane, it starts under the rule.
func (m *model) navigatorY() int {
	if m.navigatorPosition == positionBottom {
		return bodyY + m.titleHeight() + m.mainHeight() + 1
	}
	return bodyY
}

// navigatorRows returns the number of lines in the file list, including
// its title.
func (m *model) navigatorRows() int {
	h := m.bodyHeight()
	if m.navigatorPosition.stacked() {
		return max(m.navigatorHeight(h)-1, 0)
	}
	return h
}

// sepWidth is the width of the line " │" between the panes. The file
// list has its own blank column at its left.
const sepWidth = 2

// bodyY is the screen line where the body starts, just below the header.
const bodyY = 1
