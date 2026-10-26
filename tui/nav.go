package tui

import (
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// These are the steps of the keys that move or resize.
const (
	scrollStep = 8 // the sideways scroll, in columns
	// listStep resizes the file list. It is in columns at a side, and in
	// percent of the body rows above or below.
	listStep = 4
)

// updateNormal handles a key when no box is open. As in herdr-reviewr,
// the key of QuitDiscard does nothing here. It only answers the prompt
// before a quit.
func (m *model) updateNormal(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	var cmd tea.Cmd
	switch {
	case key.Matches(msg, k.Quit):
		if m.comments.Len() > 0 {
			m.openConfirmQuit()
			return nil
		}
		return tea.Quit
	case key.Matches(msg, k.Cancel):
		m.escape()
	case key.Matches(msg, k.Keys):
		m.help.ShowAll = !m.help.ShowAll
	default:
		cmd = m.actionKey(msg)
	}
	m.ensureVisible()
	return cmd
}

// escape drops one layer with each press, as in herdr-reviewr: the
// selection, then the armed crossing, then the list of all keys.
func (m *model) escape() {
	if p := m.cur(); m.tab != tabPR {
		switch {
		case p.anchor >= 0:
			p.anchor = -1
			return
		case m.armed != 0:
			m.armed = 0
			return
		}
	}
	m.help.ShowAll = false
}

// actionKey gives a key to the handlers of the actions, by topic. The
// first handler that takes the key wins, as in one switch.
func (m *model) actionKey(msg tea.KeyPressMsg) tea.Cmd {
	handlers := []func(tea.KeyPressMsg) (tea.Cmd, bool){
		m.scopeKey,
		m.moveKey,
		m.commentKey,
		m.viewKey,
	}
	for _, handle := range handlers {
		if cmd, ok := handle(msg); ok {
			return cmd
		}
	}
	return nil
}

// scopeKey handles a key that changes the tab or the scope. It reports
// whether it takes the key. As in herdr-reviewr, the keys of the scopes
// and of the pickers do nothing on the PR tab.
func (m *model) scopeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := m.keys
	onPR := m.tab == tabPR && key.Matches(msg, k.ScopeUncommitted, k.ScopeBranch,
		k.ScopeLastTurn, k.ScopeCommits, k.BasePick, k.CommitPick)
	switch {
	case onPR:
	case key.Matches(msg, k.TabChanges):
		m.setTab(tabChanges)
	case key.Matches(msg, k.TabAllFiles):
		m.setTab(tabFiles)
	case key.Matches(msg, k.TabPR):
		m.setTab(tabPR)
	case key.Matches(msg, k.ScopeUncommitted):
		m.setScope(ScopeUncommitted)
	case key.Matches(msg, k.ScopeBranch):
		m.setScope(ScopeBranch)
	case key.Matches(msg, k.ScopeLastTurn):
		m.setScope(ScopeLastTurn)
	case key.Matches(msg, k.ScopeCommits) && m.to == "":
		m.openCommitPicker()
	case key.Matches(msg, k.ScopeCommits):
		m.setScope(scopeCommits)
	case key.Matches(msg, k.BasePick):
		return m.openBasePicker(), true
	case key.Matches(msg, k.CommitPick):
		m.openCommitPicker()
	default:
		return nil, false
	}
	return nil, true
}

// moveKey handles a key that moves the cursor, the file selection, or
// the sideways scroll. The wrap is here because it ends the sideways
// scroll. moveKey reports whether it takes the key.
func (m *model) moveKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, p := m.keys, m.cur()
	last := max(len(p.lines)-1, 0)
	inList := m.focusList && m.tab != tabPR
	switch {
	case key.Matches(msg, k.Focus) && m.listHiddenHere():
		// As in herdr-reviewr, tab shows a hidden list with the focus.
		m.listHidden, m.focusList = false, true
		m.rerender()
	case key.Matches(msg, k.Focus):
		m.focusList = !m.focusList
	case key.Matches(msg, k.Down) && inList:
		m.stepRow(1)
	case key.Matches(msg, k.Down):
		p.cur = min(p.end(p.cur)+1, last)
	case key.Matches(msg, k.Up) && inList:
		m.stepRow(-1)
	case key.Matches(msg, k.Up):
		p.cur = max(p.cur-1, 0)
	case key.Matches(msg, k.PageDown):
		p.cur = min(p.cur+m.mainHeight(), last)
	case key.Matches(msg, k.PageUp):
		p.cur = max(p.cur-m.mainHeight(), 0)
	case key.Matches(msg, k.HalfDown):
		p.cur = min(p.cur+m.mainHeight()/2, last)
	case key.Matches(msg, k.HalfUp):
		p.cur = max(p.cur-m.mainHeight()/2, 0)
	case key.Matches(msg, k.NextHunk):
		m.stepChange(1)
	case key.Matches(msg, k.PrevHunk):
		m.stepChange(-1)
	case key.Matches(msg, k.NextFile):
		m.stepFile(1)
	case key.Matches(msg, k.PrevFile):
		m.stepFile(-1)
	case key.Matches(msg, k.Expand) && m.atDir():
		m.setDir(true)
	case key.Matches(msg, k.Collapse) && m.atDir():
		m.setDir(false)
	case key.Matches(msg, k.Expand) && m.atFold():
		m.expandFold(p.rows[p.cur].Fold)
	case key.Matches(msg, k.Expand, k.Collapse) && m.tab == tabPR:
		// As in herdr-reviewr, right opens all the <details> blocks of
		// the PR tab, and left closes them.
		m.prDetails = key.Matches(msg, k.Expand)
		m.renderPR()
	case key.Matches(msg, k.Expand) && !m.wrap:
		p.xoff += scrollStep
		m.rerender()
	case key.Matches(msg, k.Collapse) && !m.wrap:
		p.xoff = max(p.xoff-scrollStep, 0)
		m.rerender()
	case key.Matches(msg, k.Wrap) && p.diff != nil:
		// The wrap key works only on a diff, because rendered markdown
		// has its own wrap. The sideways scroll works only without wrap.
		m.wrap = !m.wrap
		m.changePane.xoff, m.allPane.xoff = 0, 0
		m.rerender()
	default:
		return nil, false
	}
	return nil, true
}

// commentKey handles a key that adds, changes, or sends comments. It
// reports whether it takes the key.
func (m *model) commentKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, p := m.keys, m.cur()
	switch {
	case key.Matches(msg, k.Select) && p.anchor >= 0:
		p.anchor = -1
	case key.Matches(msg, k.Select):
		if m.target(p.cur, diff.SideNew) != nil {
			p.anchor = p.cur
		}
	case key.Matches(msg, k.Comment) && !m.focusList:
		// As in herdr-reviewr, a new comment and a delete need the focus
		// on the diff.
		return m.startComment(diff.SideNew), true
	case key.Matches(msg, k.Edit):
		return m.editOrOpen(), true
	case key.Matches(msg, k.Delete) && !m.focusList:
		if c := m.commentAtCursor(); c != nil {
			m.comments.Remove(c.ID)
			m.flash("comment deleted")
		}
	case key.Matches(msg, k.NextComment):
		m.stepComment(1)
	case key.Matches(msg, k.PrevComment):
		m.stepComment(-1)
	case key.Matches(msg, k.Comments):
		m.openCommentList()
	case key.Matches(msg, k.Send):
		return m.startSend(), true
	case key.Matches(msg, k.Copy):
		m.copyComments()
		return nil, true
	default:
		return nil, false
	}
	return nil, true
}

// viewKey handles a key that searches, changes the view, or opens or
// reloads something. It reports whether it takes the key.
func (m *model) viewKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := m.keys
	switch {
	case key.Matches(msg, k.Search):
		m.loadFiles()
		m.openSearch()
	case key.Matches(msg, k.Find):
		return m.openFind(), true
	case key.Matches(msg, k.GotoLine):
		m.openInput(overlayGoto, "go to line (:0 first, :$ last)", "")
	case key.Matches(msg, k.Rendered):
		// As in herdr-reviewr, m acts only on a markdown file with text.
		src, ok := m.markdownSource()
		switch {
		case !ok:
		case m.rendersNothing(src):
			m.flash("nothing here renders")
		default:
			m.rendered = !m.rendered
			m.rerender()
		}
	case key.Matches(msg, k.Split):
		// As in herdr-reviewr, | acts only if the file can show in split
		// at this width. Only a pane that is too narrow tells why.
		shown := m.splitShown()
		switch {
		case shown || m.codeWidth() >= splitMinWidth && m.splitAllowed():
			m.split = !shown
			m.rerender()
		case m.splitAllowed():
			m.flash("too narrow for split")
		}
	case key.Matches(msg, k.NavigatorPosition):
		m.moveList()
	case key.Matches(msg, k.NavigatorHide):
		m.hideList()
	case key.Matches(msg, k.NavigatorGrow):
		m.resizeList(listStep)
	case key.Matches(msg, k.NavigatorShrink):
		m.resizeList(-listStep)
	case key.Matches(msg, k.OpenPR):
		return m.openPR(), true
	case key.Matches(msg, k.Refresh):
		return m.refresh(), true
	case m.release != nil && key.Matches(msg, k.Update):
		return m.startUpdate(), true
	default:
		return nil, false
	}
	return nil, true
}

// listHiddenHere reports whether the user hides the file list on this
// tab. The PR tab never hides it.
func (m *model) listHiddenHere() bool { return m.listHidden && m.tab != tabPR }

// moveList moves the file list clockwise around the main pane. A hidden
// list does not move.
func (m *model) moveList() {
	if m.listHiddenHere() {
		return
	}
	m.listPos = m.listPos.next()
	m.rerender()
}

// hideList hides the file list and gives the focus to the main pane, or
// shows the list again. The PR tab does not hide it.
func (m *model) hideList() {
	if m.tab == tabPR {
		return
	}
	m.listHidden = !m.listHidden
	if m.listHidden {
		m.focusList = false
	}
	m.rerender()
}

// resizeList makes the file list larger by d, or smaller for a negative
// d. A hidden list keeps its size.
func (m *model) resizeList(d int) {
	if m.listHiddenHere() {
		return
	}
	if m.listPos.stacked() {
		m.listPct = min(max(m.listPct+d, listMinPct), listMaxStackPct)
	} else {
		m.listW = m.clampSide(m.sideWidth() + d)
	}
	m.rerender()
}

// openPR opens the pull request in the browser, and tells it as
// herdr-reviewr does.
func (m *model) openPR() tea.Cmd {
	pr := m.pr.pr
	if pr == nil || pr.URL == "" {
		return nil
	}
	kind := "PR"
	if pr.Forge == vcs.ForgeGitLab {
		kind = "MR"
	}
	return m.browse(pr.URL, kind)
}

// browse opens url in the browser, and tells that it opened what, as
// herdr-reviewr does.
func (m *model) browse(url, what string) tea.Cmd {
	wait, err := openURL(m.env, m.cfg, url)
	if err != nil {
		m.flash("open failed: " + err.Error())
		return nil
	}
	m.flash("opened " + what + " in browser")
	return wait
}

// refresh reads the current tab again.
func (m *model) refresh() tea.Cmd {
	m.reload()
	if m.tab == tabFiles {
		m.reloadFiles()
	}
	if m.tab != tabPR {
		return nil
	}
	cmd := m.requestPR(true)
	m.renderPR()
	return cmd
}

// setTab shows tab t.
func (m *model) setTab(t tab) {
	m.tab = t
	m.drag = dragState{}
	// As in herdr-reviewr, the PR tab has no find band.
	if t == tabPR {
		m.closeFind()
	}
	switch t {
	case tabFiles:
		if m.allFiles == nil || m.filesOwed {
			m.reloadFiles()
		}
	case tabChanges:
		m.reloadIfChanged()
	}
	// The layout, the wrap, or the terminal colours can change in another
	// tab.
	m.rerender()
}

// setScope shows the Changes tab in scope s, from its first row.
func (m *model) setScope(s Scope) {
	// The files of the last scope do not show while the new one fails.
	m.tab, m.scope, m.folds, m.shownTrees = tabChanges, s, nil, ""
	m.reload()
	m.changePane.toStart()
	m.armed = 0
}

// atFold reports whether the cursor is on a fold of the Changes tab.
func (m *model) atFold() bool {
	p := m.cur()
	return m.tab == tabChanges && p.cur < len(p.rows) && p.rows[p.cur].Fold.Lines > 0
}

// openFolds has the folds that the user expands in one file, as ranges of
// old line numbers. The old side does not change while the agent edits the
// file, so an edit keeps the lines of a range open. The ranges are for the
// old content with the hash Old. A different old content, for example
// after a commit, closes the ranges.
type openFolds struct {
	Old    uint64   `json:"old"`
	Ranges [][2]int `json:"ranges"` // the first and the last line of each range
}

// hash returns the 64-bit FNV-1a hash of s.
func hash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s)) // a hash.Hash does not fail
	return h.Sum64()
}

// expanded reports whether the lines of fold f of the file at path show.
// They show if the user expands the fold, or if a comment is on one of
// them.
func (m *model) expanded(path string, comments []*diff.Comment, f diff.Fold) bool {
	// A fold that overlaps a range shows. An undo of a change next to a
	// range makes a larger fold, which shows as a whole.
	if o := m.folds[path]; o != nil && slices.ContainsFunc(o.Ranges, func(r [2]int) bool {
		return f.Old <= r[1] && f.Old+f.Lines-1 >= r[0]
	}) {
		return true
	}
	for _, c := range comments {
		lo := f.New
		if c.Side == diff.SideOld {
			lo = f.Old
		}
		if c.Path == path && c.Start < lo+f.Lines && c.End >= lo {
			return true
		}
	}
	return false
}

// expandFold shows the lines of fold f in the Changes tab, and moves the
// cursor to the first of them. The selection stays on its lines.
func (m *model) expandFold(f diff.Fold) {
	p := &m.changePane
	file := m.selectedChange()
	if file == nil {
		return
	}
	if m.folds == nil {
		m.folds = map[string]*openFolds{}
	}
	o := m.folds[file.Path]
	if o == nil {
		o = &openFolds{Old: file.oldHash}
		m.folds[file.Path] = o
	}
	o.Ranges = append(o.Ranges, [2]int{f.Old, f.Old + f.Lines - 1})
	m.rerender()
	p.cur = max(p.rowOf(diff.SideNew, f.New), 0)
}

// listSel returns the index of the selected file in the file list.
func (m *model) listSel() int {
	if m.tab == tabFiles {
		return m.allSel
	}
	return m.changeSel
}

// pickFile selects file i of the file list of the current tab. The
// cursor of the list goes to the row of the file, if the row shows. The
// pick drops an armed crossing.
func (m *model) pickFile(i int) {
	if t := m.list(); t != nil {
		if r := t.rowOf(i); r >= 0 {
			t.cur = r
		}
	}
	m.armed = 0
	switch m.tab {
	case tabChanges:
		if i < 0 || i >= len(m.changeFiles) {
			return
		}
		m.changeSel = i
		m.changePane.toStart()
		m.renderChanges()
	case tabFiles:
		if i < 0 || i >= len(m.allFiles) {
			return
		}
		m.allSel = i
		m.allPane.toStart()
		m.renderViewer()
	}
	m.drag = dragState{}
}

// stepChange moves the cursor to the next (d = 1) or the previous (d = -1)
// block of changed rows, as in herdr-reviewr. Past the last block, one
// press arms a crossing to the next file with a change in that direction.
// A second press the same way crosses, and lands on the first block in
// that direction. A selection holds the cursor.
func (m *model) stepChange(d int) {
	armed := m.armed == d
	m.armed = 0
	p := &m.changePane
	if m.tab != tabChanges || p.anchor >= 0 {
		return
	}
	if i := changeRow(p.rows, p.cur, d); i >= 0 {
		p.cur = i
		return
	}
	target := m.crossTarget(d)
	if !armed {
		if target >= 0 {
			m.armed, m.armedPath = d, m.changeFiles[target].Path
		}
		return
	}
	// A reload can drop the armed file.
	isArmed := func(f changedFile) bool { return f.Path == m.armedPath }
	if i := slices.IndexFunc(m.changeFiles, isArmed); i >= 0 {
		target = i
	}
	if target < 0 {
		return
	}
	m.pickFile(target)
	from := -1
	if d < 0 {
		from = len(p.rows)
	}
	p.cur = max(changeRow(p.rows, from, d), 0)
}

// changeRow returns the first row of a block of changed rows, or -1. It
// starts at row from and goes in the direction d. A d of 1 finds the next
// block, and a d of -1 finds the previous block.
func changeRow(rows []diff.Row, from, d int) int {
	isChange := func(i int) bool {
		k := rows[i].Kind
		return k == diff.KindInsert || k == diff.KindDelete || k == diff.KindChange
	}
	for i := from + d; i >= 0 && i < len(rows); i += d {
		if isChange(i) && (i == 0 || !isChange(i-1)) {
			return i
		}
	}
	return -1
}

// crossTarget returns the index of the nearest file in direction d that
// has a changed line, or -1. It goes in the order of the rows of the file
// list, from the row of the file that shows.
func (m *model) crossTarget(d int) int {
	t := &m.changeTree
	from := t.rowOf(m.changeSel)
	if from < 0 {
		from = t.cur
	}
	for r := t.nextFile(from, d); r >= 0; r = t.nextFile(r, d) {
		if i := t.rows[r].File; m.changeFiles[i].adds+m.changeFiles[i].dels > 0 {
			return i
		}
	}
	return -1
}

// stepComment moves the cursor to the next (d = 1) or previous (d = -1)
// comment of the open file, by row. It wraps around in the file. As in
// herdr-reviewr, n and N step only through the comments on the rows that
// show.
func (m *model) stepComment(d int) {
	p := m.cur()
	a := m.anchor()
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
	m.focusList = false
	p.cur, p.anchor = stops[k], -1
}

// jumpToComment shows comment c in the tab of its anchor.
func (m *model) jumpToComment(c *diff.Comment) {
	if c.Anchor.InDiff {
		m.jumpTo(c.Path, c.Side, c.Start)
		return
	}
	m.jumpToFile(c.Path, c.Side, c.Start)
}

// jumpTo shows path with the cursor on line. It uses the Changes tab if
// the file has changes, else the Files tab.
func (m *model) jumpTo(path string, s diff.Side, line int) {
	inChanges := func(f changedFile) bool { return f.Path == path }
	if i := slices.IndexFunc(m.changeFiles, inChanges); i >= 0 {
		m.tab = tabChanges
		m.revealFile(path)
		m.pickFile(i)
		m.openFoldAt(s, line)
		cursorToLine(&m.changePane, s, line)
		return
	}
	m.jumpToFile(path, s, line)
}

// openFoldAt opens the fold of the Changes tab that has line on side s.
func (m *model) openFoldAt(s diff.Side, line int) {
	for _, r := range m.changePane.rows {
		if f := r.Fold; f.Lines > 0 && line >= foldStart(f, s) && line <= foldEnd(f, s) {
			m.expandFold(f)
			return
		}
	}
}

// jumpToFile shows path in the Files tab with the cursor on line.
func (m *model) jumpToFile(path string, s diff.Side, line int) {
	m.loadFiles()
	i := slices.IndexFunc(m.allFiles, func(e vcs.Entry) bool { return e.Path == path })
	if i < 0 {
		m.flash("no such file: " + path)
		return
	}
	m.tab = tabFiles
	m.revealFile(path)
	m.pickFile(i)
	if s == diff.SideOld { // the Files tab shows only the new side
		m.flash("the removed line is not in the file now")
		return
	}
	cursorToLine(&m.allPane, diff.SideNew, line)
}

// cursorToLine puts the cursor of p on the row that shows line on side s.
func cursorToLine(p *pane, s diff.Side, line int) {
	if i := p.rowOf(s, line); i >= 0 {
		p.cur = i
	}
}

// rowOf returns the first row of p that shows line on side s, or -1. In
// rendered markdown, a line in a block is on the first row of the block,
// as in gotoLine.
func (p *pane) rowOf(s diff.Side, line int) int {
	i := slices.IndexFunc(p.rows, func(r diff.Row) bool { return r.Line(s) == line })
	if i < 0 && p.src != nil && slices.ContainsFunc(p.src, func(r diff.Row) bool { return r.Line(s) == line }) {
		return p.head(lineRow(p.rows, s, line))
	}
	return i
}

// gotoLine moves the cursor to the line in text, as herdr-reviewr does.
// text is digits or "$" for the last line. The line is clamped to the
// file, so "0" is the first line. The line numbers are of the new side,
// or of the old side if no row has a new line. gotoLine opens the fold
// that has the line. In rendered markdown, the cursor goes to the block
// of the line. Without line numbers, text is the row on the screen.
func (m *model) gotoLine(text string) {
	p := m.cur()
	n, ok := lineNumber(text)
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
		m.openFoldAt(side, n)
	}
	p.cur = p.head(lineRow(p.rows, side, n))
}

// lineRow returns the row of line on side. If no row has the line, it
// returns the first row with a larger line, else the last row with a
// line.
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

// foldStart returns the first line of fold f on side.
func foldStart(f diff.Fold, side diff.Side) int {
	if side == diff.SideOld {
		return f.Old
	}
	return f.New
}

// foldEnd returns the last line of fold f on side, or 0 for no fold.
func foldEnd(f diff.Fold, side diff.Side) int {
	if f.Lines == 0 {
		return 0
	}
	return foldStart(f, side) + f.Lines - 1
}

// lineNumber returns the line in s, the text of the go-to-line prompt. "$"
// gives the largest int. ok is false if s is not digits or "$".
func lineNumber(s string) (n int, ok bool) {
	if s == "$" {
		return math.MaxInt, true
	}
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		n = math.MaxInt // too many digits
	}
	return n, true
}

// isLineKey reports whether the go-to-line prompt takes the key msg. It
// takes a digit, "$", and the keys that move in the text or delete.
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

// pastedLine returns the line in pasted text for the go-to-line prompt.
// It removes the quotes, the brackets, and the punctuation around the
// text. ok is false if the rest is not digits or "$".
func pastedLine(text string) (line string, ok bool) {
	line = strings.Trim(strings.TrimSpace(text), "`'\"“”‘’()[]<>:.,;")
	_, ok = lineNumber(line)
	return line, ok
}
