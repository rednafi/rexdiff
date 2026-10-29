package tui

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// Step sizes for the keys that scroll or resize.
const (
	scrollStep = 8 // the horizontal scroll step, in columns
	// navigatorWidthStep is the resize step for the file list: columns
	// when it is at the left or right, and a percentage of the body rows
	// when it is above or below.
	navigatorWidthStep = 4
)

// updateNormal handles a key press when no box is open. It runs the
// handler of the key's action and then, unless the action keeps the view,
// scrolls the view to the cursor.
func (m *model) updateNormal(msg tea.KeyPressMsg) tea.Cmd {
	a := m.keys.action(msg)
	if a == nil {
		m.scrollToCursor()
		return nil
	}
	cmd := a.handle(m)
	if !a.keepView {
		m.scrollToCursor()
	}
	return cmd
}

// colorsTick is how often the screen is drawn again while the shown file
// is being highlighted, so that lines drawn without colours get them
// soon. It is about one frame.
const colorsTick = 16 * time.Millisecond

// colorsMsg asks for the screen to be drawn again, because lines that
// were drawn without colours may have them now.
type colorsMsg struct{}

// awaitColors returns a command that sends colorsMsg after colorsTick if
// the last file shown with syntax highlighting is still being
// highlighted. It returns nil if it is not, or if a colorsMsg is already
// on its way. Update calls it after every message.
func (m *model) awaitColors() tea.Cmd {
	if m.colorsTicking || !m.cache.Highlighting() {
		return nil
	}
	m.colorsTicking = true
	return tea.Tick(colorsTick, func(time.Time) tea.Msg { return colorsMsg{} })
}

// onQuit quits, asking first if there are comments.
func (m *model) onQuit() tea.Cmd {
	if m.comments.Len() > 0 {
		m.openQuit()
		return nil
	}
	return tea.Quit
}

// onCancel undoes one thing per press: first the selection, then a
// pending jump to another file, then the full key list.
func (m *model) onCancel() tea.Cmd {
	if p := m.curPane(); m.tab != tabPR {
		switch {
		case p.selStart >= 0:
			p.selStart = -1
			return nil
		case m.crossStep != 0:
			m.crossStep = 0
			return nil
		}
	}
	m.help.ShowAll = false
	return nil
}

// onKeys shows or hides the full key list.
func (m *model) onKeys() tea.Cmd {
	m.help.ShowAll = !m.help.ShowAll
	return nil
}

// onTabChanges shows the Changes tab.
func (m *model) onTabChanges() tea.Cmd {
	m.setTab(tabChanges)
	return nil
}

// onTabAllFiles shows the Files tab.
func (m *model) onTabAllFiles() tea.Cmd {
	m.setTab(tabAllFiles)
	return nil
}

// onTabPR shows the PR tab.
func (m *model) onTabPR() tea.Cmd {
	m.setTab(tabPR)
	return nil
}

// The scope and picker keys do nothing on the PR tab.

// onScopeUncommitted shows the uncommitted changes.
func (m *model) onScopeUncommitted() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeUncommitted)
	}
	return nil
}

// onScopeBranch shows the changes on the branch.
func (m *model) onScopeBranch() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeBranch)
	}
	return nil
}

// onScopeLastTurn shows the changes made in the last agent turn.
func (m *model) onScopeLastTurn() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeLastTurn)
	}
	return nil
}

// onScopeCommits shows the chosen commits. If no commits have been
// chosen, it opens the commit picker.
func (m *model) onScopeCommits() tea.Cmd {
	switch {
	case m.tab == tabPR:
	case m.newest == "":
		return m.openCommitPicker()
	default:
		m.setScope(scopeCommits)
	}
	return nil
}

// onBasePick opens the base picker.
func (m *model) onBasePick() tea.Cmd {
	if m.tab == tabPR {
		return nil
	}
	return m.openBasePicker()
}

// onCommitPick opens the commit picker.
func (m *model) onCommitPick() tea.Cmd { return m.openCommitPicker() }

// onFocus moves focus between the file list and the main pane. If the
// file list is hidden, it shows it and gives it focus.
func (m *model) onFocus() tea.Cmd {
	if m.navigatorHidden && m.tab != tabPR {
		m.navigatorHidden, m.navigatorFocused = false, true
		m.rerender()
		return nil
	}
	m.navigatorFocused = !m.navigatorFocused
	return nil
}

// onDown moves the cursor down one line, or down one row in the file list.
func (m *model) onDown() tea.Cmd {
	if m.navigatorFocused && m.tab != tabPR {
		m.stepNavigatorRow(1)
	} else {
		m.moveCur(1)
	}
	return nil
}

// onUp moves the cursor up one line, or up one row in the file list.
func (m *model) onUp() tea.Cmd {
	if m.navigatorFocused && m.tab != tabPR {
		m.stepNavigatorRow(-1)
	} else {
		m.moveCur(-1)
	}
	return nil
}

// onPageDown moves the cursor down one screen.
func (m *model) onPageDown() tea.Cmd { return m.moveCur(m.mainHeight()) }

// onPageUp moves the cursor up one screen.
func (m *model) onPageUp() tea.Cmd { return m.moveCur(-m.mainHeight()) }

// onHalfDown moves the cursor down half a screen.
func (m *model) onHalfDown() tea.Cmd { return m.moveCur(m.mainHeight() / 2) }

// onHalfUp moves the cursor up half a screen.
func (m *model) onHalfUp() tea.Cmd { return m.moveCur(-m.mainHeight() / 2) }

// moveCur moves the cursor of the current pane by d rows, stopping at the
// first and last rows. A move down always leaves the cursor line. The
// cursor stays on the first row of a wrapped line, so a line taller than
// the screen scrolls instead: the view moves through its rows until it
// shows the end of the line going down, or its start going up. Moving up
// onto such a line shows its end.
func (m *model) moveCur(d int) tea.Cmd {
	p := m.curPane()
	h := max(m.mainHeight(), 1)
	first, last := p.lineStart(p.cur), p.lineEnd(p.cur)
	if end := last - h + 1; end > first { // the top row that shows the end of the line
		top := min(max(p.top, first), end)
		if d > 0 && top < end || d < 0 && top > first {
			p.top, p.following = min(max(top+d, first), end), false
			return nil
		}
	}
	to := min(max(p.cur+d, 0), p.last())
	if d > 0 {
		if last == p.last() { // no line below, so the view stays where it is
			p.following = p.top <= first
			return nil
		}
		to = max(to, last+1)
	}
	p.cur, p.following = to, true // the view follows the cursor again
	if first, last := p.lineStart(to), p.lineEnd(to); d < 0 && last-first+1 > h {
		p.cur, p.top, p.following = first, last-h+1, false
	}
	return nil
}

// onNextHunk moves the cursor to the next hunk.
func (m *model) onNextHunk() tea.Cmd {
	m.stepHunk(1)
	return nil
}

// onPrevHunk moves the cursor to the previous hunk.
func (m *model) onPrevHunk() tea.Cmd {
	m.stepHunk(-1)
	return nil
}

// onNextFile shows the next file.
func (m *model) onNextFile() tea.Cmd {
	m.stepFile(1)
	return nil
}

// onPrevFile shows the previous file.
func (m *model) onPrevFile() tea.Cmd {
	m.stepFile(-1)
	return nil
}

// onExpand expands the directory or fold at the cursor. On the PR tab, it
// expands every <details> block. Otherwise, when lines are not wrapped, it
// scrolls right.
func (m *model) onExpand() tea.Cmd {
	p := m.curPane()
	switch {
	case m.cursorOnDir():
		m.setDirExpanded(true)
	case m.cursorOnFold():
		m.expandFold(p.rows[p.cur].Fold)
	case m.tab == tabPR:
		m.pr.detailsExpanded = true
		m.renderPR()
	case !m.wrap:
		p.scrollX += scrollStep
		m.rerender()
	}
	return nil
}

// onCollapse collapses the directory at the cursor, or the expanded fold
// at the cursor once the view is scrolled fully left. On the PR tab, it
// collapses every <details> block. Otherwise, when lines are not wrapped,
// it scrolls left.
func (m *model) onCollapse() tea.Cmd {
	p := m.curPane()
	switch {
	case m.cursorOnDir():
		m.setDirExpanded(false)
	case m.canCollapseFold():
		m.collapseFold()
	case m.tab == tabPR:
		m.pr.detailsExpanded = false
		m.renderPR()
	case !m.wrap:
		p.scrollX = max(p.scrollX-scrollStep, 0)
		m.rerender()
	}
	return nil
}

// onWrap turns wrapping of long diff lines on or off and resets the
// horizontal scroll. It does nothing outside a diff, because rendered
// Markdown does its own wrapping.
func (m *model) onWrap() tea.Cmd {
	if m.curPane().diff == nil {
		return nil
	}
	m.wrap = !m.wrap
	m.changes.pane.scrollX, m.allFiles.pane.scrollX = 0, 0
	m.rerender()
	return nil
}

// onSelect starts a visual selection at the cursor, or clears the current
// one.
func (m *model) onSelect() tea.Cmd {
	p := m.curPane()
	switch {
	case p.selStart >= 0:
		p.selStart = -1
	case m.targetAt(p.cur, diff.SideNew) != nil:
		p.selStart = p.cur
	}
	return nil
}

// onComment opens the comment box for a new comment. It does nothing
// while the file list has focus.
func (m *model) onComment() tea.Cmd {
	if m.navigatorFocused {
		return nil
	}
	return m.openComment(diff.SideNew)
}

// onDelete deletes the comment on the cursor line. It does nothing while
// the file list has focus.
func (m *model) onDelete() tea.Cmd {
	if m.navigatorFocused {
		return nil
	}
	if c := m.curComment(); c != nil {
		m.comments.Delete(c.ID)
		m.flash("comment deleted")
	}
	return nil
}

// onNextComment moves the cursor to the next comment.
func (m *model) onNextComment() tea.Cmd {
	m.stepComment(1)
	return nil
}

// onPrevComment moves the cursor to the previous comment.
func (m *model) onPrevComment() tea.Cmd {
	m.stepComment(-1)
	return nil
}

// onComments opens the comment list.
func (m *model) onComments() tea.Cmd {
	m.openComments()
	return nil
}

// onSend starts sending the comments to an agent.
func (m *model) onSend() tea.Cmd { return m.startSend() }

// onCopy copies the comments to the clipboard.
func (m *model) onCopy() tea.Cmd {
	m.copyComments()
	return nil
}

// onSearch opens the search over file names and code.
func (m *model) onSearch() tea.Cmd {
	m.ensureAllFiles()
	m.openSearch()
	return nil
}

// onFind opens the find bar.
func (m *model) onFind() tea.Cmd { return m.openFind() }

// onGotoLine opens the go-to-line prompt.
func (m *model) onGotoLine() tea.Cmd {
	m.openInput(overlayGotoLine, "go to line (:0 first, :$ last)", "")
	return nil
}

// onRendered switches a Markdown file between rendered and source views.
// It does nothing unless the current file is Markdown with text to render.
func (m *model) onRendered() tea.Cmd {
	src, ok := m.markdownSource()
	switch {
	case !ok:
	case m.rendersNothing(src):
		m.flash("nothing here renders")
	default:
		m.rendered = !m.rendered
		m.rerender()
	}
	return nil
}

// onSplit switches the diff between the split and unified layouts. It
// turns split on only if the file allows it and the pane is wide enough.
// It shows a message only when the pane is too narrow.
func (m *model) onSplit() tea.Cmd {
	shown := m.splitShown()
	switch {
	case shown || m.codeWidth() >= minSplitWidth && m.splitAllowed():
		m.split = !shown
		m.rerender()
	case m.splitAllowed():
		m.flash("too narrow for split")
	}
	return nil
}

// onNavigatorPosition moves the file list to its next position.
func (m *model) onNavigatorPosition() tea.Cmd {
	m.moveNavigator()
	return nil
}

// onNavigatorHide hides or shows the file list.
func (m *model) onNavigatorHide() tea.Cmd {
	m.toggleNavigator()
	return nil
}

// onNavigatorGrow makes the file list larger.
func (m *model) onNavigatorGrow() tea.Cmd {
	m.resizeNavigator(navigatorWidthStep)
	return nil
}

// onNavigatorShrink makes the file list smaller.
func (m *model) onNavigatorShrink() tea.Cmd {
	m.resizeNavigator(-navigatorWidthStep)
	return nil
}

// onUpdate starts updating to the newer release. It does nothing unless
// the footer is announcing one.
func (m *model) onUpdate() tea.Cmd {
	if m.release == nil {
		return nil
	}
	return m.startUpdate()
}

// moveNavigator moves the file list clockwise around the main pane. A
// hidden file list does not move.
func (m *model) moveNavigator() {
	if m.navigatorHidden && m.tab != tabPR {
		return
	}
	m.navigatorPosition = m.navigatorPosition.next()
	m.rerender()
}

// toggleNavigator hides the file list and gives focus to the main pane,
// or shows the file list again. It does nothing on the PR tab.
func (m *model) toggleNavigator() {
	if m.tab == tabPR {
		return
	}
	m.navigatorHidden = !m.navigatorHidden
	if m.navigatorHidden {
		m.navigatorFocused = false
	}
	m.rerender()
}

// resizeNavigator grows the file list by d, or shrinks it if d is
// negative. A hidden file list keeps its size.
func (m *model) resizeNavigator(d int) {
	if m.navigatorHidden && m.tab != tabPR {
		return
	}
	if m.navigatorPosition.stacked() {
		m.navigatorPct = min(max(m.navigatorPct+d, minNavigatorPct), maxNavigatorStackPct)
	} else {
		m.navigatorWidthSet = m.clampNavigatorWidth(m.navigatorSideWidth() + d)
	}
	m.rerender()
}

// onOpenPR opens the pull request in the browser.
func (m *model) onOpenPR() tea.Cmd {
	pr := m.pr.pr
	if pr == nil || pr.URL == "" {
		return nil
	}
	kind := "PR"
	if pr.Forge == vcs.ForgeGitLab {
		kind = "MR"
	}
	return m.openURL(pr.URL, kind)
}

// openURL opens url in the browser and shows a message naming what was
// opened.
func (m *model) openURL(url, what string) tea.Cmd {
	wait, err := urlOpenerCmd(m.env, m.cfg, url)
	if err != nil {
		m.flash("open failed: " + err.Error())
		return nil
	}
	m.flash("opened " + what + " in browser")
	return wait
}

// onRefresh reloads the current tab.
func (m *model) onRefresh() tea.Cmd {
	m.requestReload()
	if m.tab == tabAllFiles {
		m.reloadAllFiles()
	}
	if m.tab != tabPR {
		return nil
	}
	cmd := m.requestPR(true)
	m.renderPR()
	return cmd
}

// setTab switches to tab t.
func (m *model) setTab(t tab) {
	m.tab = t
	m.drag = dragState{}
	// The PR tab has no find bar.
	if t == tabPR {
		m.closeFind()
	}
	if t != tabAllFiles {
		// No other tab shows the file that the Files tab highlights.
		m.cache.StopHighlighting()
	}
	switch t {
	case tabAllFiles:
		if m.allFiles.entries == nil || m.allFilesPending {
			m.reloadAllFiles()
		}
	case tabChanges:
		m.requestReloadIfChanged()
	}
	// The layout, wrapping or terminal colours may have changed while
	// another tab was shown.
	m.rerender()
}

// setScope switches to the Changes tab with scope s, starting at the
// first row.
func (m *model) setScope(s Scope) {
	// If loading the new scope fails, the error replaces the files of
	// the previous scope.
	m.tab, m.scope, m.expandedFolds, m.shownTrees = tabChanges, s, nil, ""
	m.cache.StopHighlighting()
	m.requestReload()
	m.changes.pane.reset()
	m.crossStep = 0
}

// cursorOnFold reports whether the cursor is on a fold in the Changes tab.
func (m *model) cursorOnFold() bool {
	p := m.curPane()
	return m.tab == tabChanges && p.cur < len(p.rows) && p.rows[p.cur].Fold.Lines > 0
}

// expandedFolds records the folds that the user has expanded in one file,
// as ranges of old line numbers, which stay valid while the agent edits
// the file. They apply only while the old content has the hash Old. The
// JSON names are used in the resume state.
type expandedFolds struct {
	Old    uint64   `json:"old"`
	Ranges [][2]int `json:"ranges"` // the first and last line of each range
}

// foldExpanded reports whether the lines of fold in the file at path are
// shown. They are shown if the user expanded the fold or if one of the
// comments is on one of its lines.
func (m *model) foldExpanded(path string, comments []*diff.Comment, fold diff.Fold) bool {
	// A fold that overlaps an expanded range is shown. Undoing a change
	// next to a range creates a larger fold, which is then shown in full.
	if o := m.expandedFolds[path]; o != nil && slices.ContainsFunc(o.Ranges, func(r [2]int) bool {
		return fold.Old <= r[1] && fold.Old+fold.Lines-1 >= r[0]
	}) {
		return true
	}
	for _, c := range comments {
		lo := fold.New
		if c.Side == diff.SideOld {
			lo = fold.Old
		}
		if c.Path == path && c.Start < lo+fold.Lines && c.End >= lo {
			return true
		}
	}
	return false
}

// expandFold shows the hidden lines of fold in the Changes tab and moves
// the cursor to the first of them. The selection stays on the same lines.
func (m *model) expandFold(fold diff.Fold) {
	p := &m.changes.pane
	f := m.curChangedFile()
	if f == nil {
		return
	}
	if m.expandedFolds == nil {
		m.expandedFolds = map[string]*expandedFolds{}
	}
	o := m.expandedFolds[f.Path]
	if o == nil {
		o = &expandedFolds{Old: f.oldHash}
		m.expandedFolds[f.Path] = o
	}
	o.Ranges = append(o.Ranges, [2]int{fold.Old, fold.Old + fold.Lines - 1})
	m.rerender()
	p.cur = max(p.rowOf(diff.SideNew, fold.New), 0)
}

// cursorInExpandedFold reports whether the cursor is on a line of a fold
// that the user has expanded in the Changes tab.
func (m *model) cursorInExpandedFold() bool {
	p := m.curPane()
	f := m.curChangedFile()
	if m.tab != tabChanges || f == nil || p.cur >= len(p.rows) {
		return false
	}
	o := m.expandedFolds[f.Path]
	row := p.rows[p.cur]
	line := row.Old
	if o == nil || line == 0 || row.Kind != diff.KindEqual || row.Fold.Lines > 0 {
		return false
	}
	return slices.ContainsFunc(o.Ranges, func(r [2]int) bool { return inRange(r, line) })
}

// canCollapseFold reports whether the collapse key would collapse the
// expanded fold at the cursor. That needs the cursor to be in an expanded
// fold and the view to be scrolled fully left.
func (m *model) canCollapseFold() bool {
	return m.curPane().scrollX == 0 && m.cursorInExpandedFold()
}

// collapseFold hides the expanded lines around the cursor again and moves
// the cursor to the fold that replaces them. A fold with a comment on it
// stays open. The caller must check canCollapseFold first.
func (m *model) collapseFold() {
	p := m.curPane()
	f := m.curChangedFile()
	o := m.expandedFolds[f.Path]
	line := p.rows[p.cur].Old
	o.Ranges = slices.DeleteFunc(o.Ranges, func(r [2]int) bool { return inRange(r, line) })
	if len(o.Ranges) == 0 {
		delete(m.expandedFolds, f.Path)
	}
	p.selStart = -1 // the selection could end inside the new fold
	m.rerender()
	if i := slices.IndexFunc(p.rows, func(r diff.Row) bool { return foldHas(r.Fold, diff.SideOld, line) }); i >= 0 {
		p.cur = i
	}
}

// curFileIndex returns the index of the current file in the current tab.
func (m *model) curFileIndex() int {
	if m.tab == tabAllFiles {
		return m.allFiles.cur
	}
	return m.changes.cur
}

// setCurFile selects file i in the current tab. If the file's row is
// visible in the file list, the list cursor moves to it. Any pending jump
// to another file is cancelled.
func (m *model) setCurFile(i int) {
	if t := m.curNavigator(); t != nil {
		if r := t.rowOf(i); r >= 0 {
			t.cur = r
		}
	}
	m.crossStep = 0
	switch m.tab {
	case tabChanges:
		if i < 0 || i >= len(m.changes.files) {
			return
		}
		m.changes.cur = i
		m.changes.pane.reset()
		m.renderChanges()
	case tabAllFiles:
		if i < 0 || i >= len(m.allFiles.entries) {
			return
		}
		m.allFiles.cur = i
		m.allFiles.pane.reset()
		m.renderAllFiles()
	}
	m.drag = dragState{}
}

// stepHunk moves the cursor to the next (d = 1) or previous (d = -1) hunk.
// When there are no more hunks in that direction, the first press arms a
// jump to the nearest file with changes, and a second press in the same
// direction opens that file at its first hunk in that direction. It does
// nothing while a selection is active.
func (m *model) stepHunk(d int) {
	armed := m.crossStep == d
	m.crossStep = 0
	p := &m.changes.pane
	if m.tab != tabChanges || p.selStart >= 0 {
		return
	}
	if i := nextHunkRow(p.rows, p.cur, d); i >= 0 {
		p.cur = i
		return
	}
	target := m.crossFile(d)
	if !armed {
		if target >= 0 {
			m.crossStep, m.crossPath = d, m.changes.files[target].Path
		}
		return
	}
	// A reload may have removed the target file or changed its index.
	crossing := func(f changedFile) bool { return f.Path == m.crossPath }
	if i := slices.IndexFunc(m.changes.files, crossing); i >= 0 {
		target = i
	}
	if target < 0 {
		return
	}
	m.setCurFile(target)
	from := -1
	if d < 0 {
		from = len(p.rows)
	}
	p.cur = max(nextHunkRow(p.rows, from, d), 0)
}

// nextHunkRow returns the first row of the next (d = 1) or previous
// (d = -1) hunk after row from, or -1 if there is none.
func nextHunkRow(rows []diff.Row, from, d int) int {
	isChanged := func(i int) bool {
		k := rows[i].Kind
		return k == diff.KindAdded || k == diff.KindDeleted || k == diff.KindChanged
	}
	for i := from + d; i >= 0 && i < len(rows); i += d {
		if isChanged(i) && (i == 0 || !isChanged(i-1)) {
			return i
		}
	}
	return -1
}

// crossFile returns the index of the nearest file in direction d that has
// a changed line, or -1. It follows the order of the file list, starting
// from the row of the shown file.
func (m *model) crossFile(d int) int {
	t := &m.changes.navigator
	from := t.rowOf(m.changes.cur)
	if from < 0 {
		from = t.cur
	}
	for r := t.nextFile(from, d); r >= 0; r = t.nextFile(r, d) {
		if i := t.rows[r].File; m.changes.files[i].added+m.changes.files[i].deleted > 0 {
			return i
		}
	}
	return -1
}

// stepComment moves the cursor to the next (d = 1) or previous (d = -1)
// comment in the open file, in row order, wrapping around at the ends. It
// skips comments whose lines are not shown.
func (m *model) stepComment(d int) {
	p := m.curPane()
	a := m.curAnchor()
	var stops []int // the first row of each comment
	for _, c := range m.comments.All() {
		if c.Anchor != a || c.Path != p.path || p.path == "" {
			continue
		}
		if r := p.rowOf(c.Side, c.Start); r >= 0 {
			stops = append(stops, r)
		}
	}
	if len(stops) == 0 {
		return
	}
	slices.Sort(stops)
	k := max(slices.IndexFunc(stops, func(r int) bool { return r > p.cur }), 0)
	if d < 0 {
		k = len(stops) - 1
		if i := slices.IndexFunc(stops, func(r int) bool { return r >= p.cur }); i > 0 {
			k = i - 1
		}
	}
	m.navigatorFocused = false
	p.cur, p.selStart = stops[k], -1
}

// jumpToComment shows comment c in the tab that it was made on.
func (m *model) jumpToComment(c *diff.Comment) {
	if c.Anchor.InDiff {
		m.jumpTo(c.Path, c.Side, c.Start)
	} else {
		m.jumpToFile(c.Path, c.Side, c.Start)
	}
	m.jumpToNearLine(c)
}

// jumpTo shows path with the cursor on line. It uses the Changes tab if
// the file has changes, and the Files tab otherwise.
func (m *model) jumpTo(path string, side diff.Side, line int) {
	inChanges := func(f changedFile) bool { return f.Path == path }
	if i := slices.IndexFunc(m.changes.files, inChanges); i >= 0 {
		m.tab = tabChanges
		m.cache.StopHighlighting()
		m.revealFile(path)
		m.setCurFile(i)
		m.expandFoldAt(side, line)
		jumpToLine(&m.changes.pane, side, line)
		return
	}
	m.jumpToFile(path, side, line)
}

// expandFoldAt expands the fold in the Changes tab that hides line on
// side.
func (m *model) expandFoldAt(side diff.Side, line int) {
	for _, r := range m.changes.pane.rows {
		if foldHas(r.Fold, side, line) {
			m.expandFold(r.Fold)
			return
		}
	}
}

// jumpToFile shows path in the Files tab with the cursor on line.
func (m *model) jumpToFile(path string, side diff.Side, line int) {
	if m.allFiles.entries == nil {
		// Jump once the entries have loaded.
		m.ensureAllFiles()
		m.allFiles.jump = &fileJump{path, side, line}
		return
	}
	i := slices.IndexFunc(m.allFiles.entries, func(e vcs.Entry) bool { return e.Path == path })
	if i < 0 {
		m.flash("no such file: " + path)
		return
	}
	m.tab = tabAllFiles
	m.revealFile(path)
	m.setCurFile(i)
	if side == diff.SideOld { // the Files tab shows only the new side
		m.flash("the removed line is not in the file now")
		return
	}
	jumpToLine(&m.allFiles.pane, diff.SideNew, line)
}

// jumpToLine moves the cursor of p to the row that shows line on side. It
// does nothing if no row shows it.
func jumpToLine(p *pane, side diff.Side, line int) {
	if i := p.rowOf(side, line); i >= 0 {
		p.cur = i
	}
}

// rowOf returns the first row of p that shows line on side, or -1. In
// rendered Markdown, a line inside a block maps to the first row of the
// block, as in gotoLine.
func (p *pane) rowOf(side diff.Side, line int) int {
	shows := func(r diff.Row) bool { return r.Line(side) == line }
	if i := slices.IndexFunc(p.rows, shows); i >= 0 {
		return i
	}
	i := slices.IndexFunc(p.srcRows, shows)
	if i < 0 {
		return -1
	}
	// Rendered Markdown shows only the new side. An old line maps to the
	// first new line from it on, or to the last new line before it.
	newLine := 1
	if j := slices.IndexFunc(p.srcRows[i:], func(r diff.Row) bool { return r.New > 0 }); j >= 0 {
		newLine = p.srcRows[i+j].New
	} else {
		for _, r := range p.srcRows[:i] {
			newLine = max(newLine, r.New)
		}
	}
	return p.lineStart(lineRow(p.rows, diff.SideNew, newLine))
}

// gotoLine moves the cursor to the line number in text: digits, clamped to
// the file, or "$" for the last line. The number is on the new side if any
// row has one, else the old side, or a row number in a pane without line
// numbers. It expands a fold that hides the line, and in rendered Markdown
// goes to the line's block.
func (m *model) gotoLine(text string) {
	p := m.curPane()
	n, ok := parseLineNum(text)
	if !ok || len(p.rows) == 0 {
		return
	}
	side := diff.SideOld
	if slices.ContainsFunc(p.rows, func(r diff.Row) bool { return r.New > 0 }) {
		side = diff.SideNew
	}
	last := 0
	for _, r := range p.rows {
		last = max(last, r.Line(side), foldEnd(r.Fold, side))
	}
	if last == 0 {
		p.cur = min(max(n, 1), len(p.rows)) - 1
		return
	}
	n = min(max(n, 1), last)
	if m.tab == tabChanges {
		m.expandFoldAt(side, n)
	}
	p.cur = p.lineStart(lineRow(p.rows, side, n))
}

// lineRow returns the row that shows line on side. If no row shows it, it
// returns the first row with a larger line number or, failing that, the
// last row that has a line number on side.
func lineRow(rows []diff.Row, side diff.Side, line int) int {
	if i := slices.IndexFunc(rows, func(r diff.Row) bool { return r.Line(side) == line }); i >= 0 {
		return i
	}
	if i := slices.IndexFunc(rows, func(r diff.Row) bool { return r.Line(side) > line }); i >= 0 {
		return i
	}
	for i, r := range slices.Backward(rows) {
		if r.Line(side) > 0 {
			return i
		}
	}
	return len(rows) - 1
}

// foldStart returns the first line that fold hides on side.
func foldStart(fold diff.Fold, side diff.Side) int {
	if side == diff.SideOld {
		return fold.Old
	}
	return fold.New
}

// foldEnd returns the last line that fold hides on side, or 0 if fold is
// empty.
func foldEnd(fold diff.Fold, side diff.Side) int {
	if fold.Lines == 0 {
		return 0
	}
	return foldStart(fold, side) + fold.Lines - 1
}

// foldHas reports whether fold hides line on side.
func foldHas(fold diff.Fold, side diff.Side, line int) bool {
	return fold.Lines > 0 && line >= foldStart(fold, side) && line <= foldEnd(fold, side)
}

// inRange reports whether line is in the range r of expanded lines,
// including both ends.
func inRange(r [2]int, line int) bool { return line >= r[0] && line <= r[1] }

// parseLineNum parses s, the text of the go-to-line prompt. "$" returns
// the largest int. ok is false unless s is all digits or "$".
func parseLineNum(s string) (n int, ok bool) {
	if s == "$" {
		return math.MaxInt, true
	}
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		n = math.MaxInt // too many digits for an int
	}
	return n, true
}

// isLineKey reports whether the go-to-line prompt accepts msg: a digit,
// "$", or a key that moves the text cursor or deletes.
func isLineKey(msg tea.KeyPressMsg) bool {
	if msg.Mod&^tea.ModShift != 0 {
		return false
	}
	switch msg.Code {
	case tea.KeyBackspace, tea.KeyDelete, tea.KeyLeft, tea.KeyRight, tea.KeyHome, tea.KeyEnd:
		return true
	}
	return len(msg.Text) == 1 && strings.ContainsAny(msg.Text, "0123456789$")
}

// pastedLine extracts a line number from text pasted into the go-to-line
// prompt. It strips surrounding quotes, brackets and punctuation. ok is
// false unless what remains is all digits or "$".
func pastedLine(text string) (line string, ok bool) {
	line = strings.Trim(strings.TrimSpace(text), "`'\"“”‘’()[]<>:.,;")
	_, ok = parseLineNum(line)
	return line, ok
}
