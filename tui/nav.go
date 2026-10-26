package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// Steps of the keys that move or resize, in columns.
const (
	scrollStep    = 8 // the sideways scroll
	listWidthStep = 4 // the width of the file list
)

// updateNormal handles a key when no box is open.
func (m *model) updateNormal(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	if m.find.active() {
		switch {
		case key.Matches(msg, k.FindNext):
			m.stepFind(1)
			return nil
		case key.Matches(msg, k.FindPrev):
			m.stepFind(-1)
			return nil
		case key.Matches(msg, k.Cancel):
			m.find = findState{}
			return nil
		}
	}
	var cmd tea.Cmd
	switch {
	case key.Matches(msg, k.QuitDiscard):
		return tea.Quit
	case key.Matches(msg, k.Quit):
		if m.comments.Len() > 0 {
			m.openConfirmQuit()
			return nil
		}
		return tea.Quit
	case key.Matches(msg, k.Cancel):
		m.cur().anchor = -1
		m.help.ShowAll = false
	case key.Matches(msg, k.Keys):
		m.help.ShowAll = !m.help.ShowAll
	default:
		cmd = m.actionKey(msg)
	}
	m.ensureVisible()
	return cmd
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
// whether it took the key.
func (m *model) scopeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := m.keys
	switch {
	case key.Matches(msg, k.TabChanges):
		m.setTab(tabChanges)
	case key.Matches(msg, k.TabFiles):
		m.setTab(tabFiles)
	case key.Matches(msg, k.TabPR):
		m.setTab(tabPR)
	case key.Matches(msg, k.ScopeUncommitted):
		m.setScope(ScopeUncommitted)
	case key.Matches(msg, k.ScopeBranch):
		m.setScope(ScopeBranch)
	case key.Matches(msg, k.ScopeTurn):
		m.setScope(ScopeLastTurn)
	case key.Matches(msg, k.ScopeCommits) && m.to == "":
		m.openCommitPicker()
	case key.Matches(msg, k.ScopeCommits):
		m.setScope(scopeCommits)
	case key.Matches(msg, k.BasePick):
		m.openBasePicker()
	case key.Matches(msg, k.CommitPick):
		m.openCommitPicker()
	default:
		return nil, false
	}
	return nil, true
}

// moveKey handles a key that moves the cursor, the file selection, or
// the sideways scroll. The wrap is here because it ends the sideways
// scroll. moveKey reports whether it took the key.
func (m *model) moveKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, p := m.keys, m.cur()
	last := max(len(p.lines)-1, 0)
	inList := m.focusList && m.tab != tabPR
	switch {
	case key.Matches(msg, k.Focus):
		m.focusList = !m.focusList
	case key.Matches(msg, k.Down) && inList:
		m.pickFile(m.listSel() + 1)
	case key.Matches(msg, k.Down):
		p.cur = min(p.end(p.cur)+1, last)
	case key.Matches(msg, k.Up) && inList:
		m.pickFile(m.listSel() - 1)
	case key.Matches(msg, k.Up):
		p.cur = max(p.cur-1, 0)
	case key.Matches(msg, k.PageDown):
		p.cur = min(p.cur+m.bodyHeight(), last)
	case key.Matches(msg, k.PageUp):
		p.cur = max(p.cur-m.bodyHeight(), 0)
	case key.Matches(msg, k.HalfDown):
		p.cur = min(p.cur+m.bodyHeight()/2, last)
	case key.Matches(msg, k.HalfUp):
		p.cur = max(p.cur-m.bodyHeight()/2, 0)
	case key.Matches(msg, k.Top):
		p.cur = 0
	case key.Matches(msg, k.Bottom):
		p.cur = last
	case key.Matches(msg, k.NextHunk):
		m.stepChange(1)
	case key.Matches(msg, k.PrevHunk):
		m.stepChange(-1)
	case key.Matches(msg, k.NextFile):
		m.pickFile(m.listSel() + 1)
	case key.Matches(msg, k.PrevFile):
		m.pickFile(m.listSel() - 1)
	case key.Matches(msg, k.Right) && m.onFold():
		m.expandFold(p.cur)
	case key.Matches(msg, k.Right) && !m.wrap:
		p.xoff += scrollStep
		m.rerender()
	case key.Matches(msg, k.Left) && !m.wrap:
		p.xoff = max(p.xoff-scrollStep, 0)
		m.rerender()
	case key.Matches(msg, k.Wrap) && p.diff != nil:
		// The wrap key works only on a diff, because rendered markdown
		// has its own wrap. The sideways scroll works only without wrap.
		m.wrap = !m.wrap
		m.changes.xoff, m.viewer.xoff = 0, 0
		m.rerender()
	default:
		return nil, false
	}
	return nil, true
}

// commentKey handles a key that adds, changes, or sends comments. It
// reports whether it took the key.
func (m *model) commentKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k, p := m.keys, m.cur()
	switch {
	case key.Matches(msg, k.Select) && p.anchor >= 0:
		p.anchor = -1
	case key.Matches(msg, k.Select):
		if m.target(p.cur, diff.SideNew) != nil {
			p.anchor = p.cur
		}
	case key.Matches(msg, k.Comment):
		return m.startComment(diff.SideNew), true
	case key.Matches(msg, k.Edit):
		return m.editOrOpen(), true
	case key.Matches(msg, k.Delete):
		if c := m.commentAtCursor(); c != nil {
			m.comments.Remove(c.ID)
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
		return m.copyComments(), true
	default:
		return nil, false
	}
	return nil, true
}

// viewKey handles a key that searches, changes the view, or opens or
// reloads something. It reports whether it took the key.
func (m *model) viewKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := m.keys
	switch {
	case key.Matches(msg, k.Search):
		m.loadFiles()
		m.openSearch()
	case key.Matches(msg, k.Find):
		m.openInput(overlayFind, "find in file", m.find.query)
	case key.Matches(msg, k.GotoLine):
		m.openInput(overlayGoto, "go to line (:0 first, :$ last)", "")
	case key.Matches(msg, k.Rendered):
		m.rendered = !m.rendered
		m.rerender()
	case key.Matches(msg, k.Split):
		m.split = !m.split
		m.rerender()
	case key.Matches(msg, k.NavPosition):
		m.listLeft = !m.listLeft
	case key.Matches(msg, k.NavHide):
		m.listHidden = !m.listHidden
		m.rerender()
	case key.Matches(msg, k.NavGrow):
		m.listW = min(m.listW+listWidthStep, m.w/2)
		m.rerender()
	case key.Matches(msg, k.NavShrink):
		m.listW = max(m.listW-listWidthStep, minListWidth)
		m.rerender()
	case key.Matches(msg, k.OpenPR):
		m.openPR()
	case key.Matches(msg, k.Refresh):
		return m.refresh(), true
	case m.release != nil && key.Matches(msg, k.Upgrade):
		return m.install(), true
	default:
		return nil, false
	}
	return nil, true
}

// openPR opens the pull request in the browser.
func (m *model) openPR() {
	if m.pr.pr == nil || m.pr.pr.URL == "" {
		return
	}
	if err := openURL(m.cfg, m.pr.pr.URL); err != nil {
		m.flash("open: " + err.Error())
	}
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
	m.pr.loading = true
	m.renderPR()
	return m.loadPR()
}

// setTab shows tab t.
func (m *model) setTab(t tab) {
	m.tab = t
	m.find, m.drag = findState{}, dragState{}
	switch t {
	case tabFiles:
		if m.allFiles == nil {
			m.reloadFiles()
		}
	case tabChanges:
		m.reloadIfChanged()
	}
	// The layout, the wrap, or the terminal colours can change in another
	// tab.
	m.rerender()
}

// setScope shows the Changes tab in scope s.
func (m *model) setScope(s Scope) {
	m.tab, m.scope, m.folds = tabChanges, s, nil
	m.reload()
	m.changes.cur, m.changes.top = 0, 0
}

// onFold tells if the cursor is on a fold of the Changes tab.
func (m *model) onFold() bool {
	p := m.cur()
	return m.tab == tabChanges && p.cur < len(p.rows) && p.rows[p.cur].Fold.Lines > 0
}

// showFold tells if the lines of fold f of the file at path show: the user
// expanded the fold, or a comment is on one of its lines.
func (m *model) showFold(path string, f diff.Fold) bool {
	if m.folds[path][f.New] {
		return true
	}
	for _, c := range m.comments.All() {
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

// expandFold shows the lines of the fold at row i of the Changes tab. A
// cursor on the fold moves to the first of them.
func (m *model) expandFold(i int) {
	p := &m.changes
	if m.folds == nil {
		m.folds = map[string]map[int]bool{}
	}
	if m.folds[p.path] == nil {
		m.folds[p.path] = map[int]bool{}
	}
	m.folds[p.path][p.rows[i].Fold.New] = true
	m.renderChanges()
}

// listSel returns the index of the selected file in the file list.
func (m *model) listSel() int {
	if m.tab == tabFiles {
		return m.allSel
	}
	return m.changeSel
}

// pickFile selects file i of the file list of the current tab.
func (m *model) pickFile(i int) {
	switch m.tab {
	case tabChanges:
		if i < 0 || i >= len(m.files) {
			return
		}
		m.changeSel = i
		m.changes.toStart()
		m.renderChanges()
	case tabFiles:
		if i < 0 || i >= len(m.allFiles) {
			return
		}
		m.allSel = i
		m.viewer.toStart()
		m.renderViewer()
	}
	m.find, m.drag = findState{}, dragState{}
}

// stepChange moves the cursor to the next (d = 1) or the previous (d = -1)
// block of changed rows.
func (m *model) stepChange(d int) {
	p := &m.changes
	if m.tab != tabChanges || len(p.rows) == 0 {
		return
	}
	isChange := func(i int) bool {
		k := p.rows[i].Kind
		return k == diff.KindInsert || k == diff.KindDelete || k == diff.KindChange
	}
	for i := p.cur + d; i >= 0 && i < len(p.rows); i += d {
		if isChange(i) && (i == 0 || !isChange(i-1)) {
			p.cur = i
			return
		}
	}
	// After the last change, go to the first change of the next file.
	if d > 0 && m.changeSel+1 < len(m.files) {
		m.pickFile(m.changeSel + 1)
		m.stepChange(1)
	}
}

// stepComment moves the cursor to the next (d = 1) or previous (d = -1)
// comment, by file and line. It wraps around.
func (m *model) stepComment(d int) {
	cs := m.comments.All()
	if len(cs) == 0 {
		return
	}
	p := m.cur()
	curPath, curLine := p.path, 0
	if t := m.target(p.cur, diff.SideNew); t != nil {
		curLine = t.line
	}
	idx := -1
	for i, c := range cs {
		after := c.Path > curPath || (c.Path == curPath && c.Start > curLine)
		if d > 0 && after {
			idx = i
			break
		}
		if d < 0 && !after && !(c.Path == curPath && c.Start == curLine) {
			idx = i
		}
	}
	if idx < 0 {
		idx = len(cs) - 1
		if d > 0 {
			idx = 0
		}
	}
	m.jumpTo(cs[idx].Path, cs[idx].Side, cs[idx].Start)
}

// jumpTo shows path with the cursor on line. It uses the Changes tab if
// the file has changes, else the Files tab.
func (m *model) jumpTo(path string, s diff.Side, line int) {
	inChanges := func(f changed) bool { return f.Path == path }
	if i := slices.IndexFunc(m.files, inChanges); i >= 0 {
		m.tab = tabChanges
		m.pickFile(i)
		cursorToLine(&m.changes, s, line)
		return
	}
	m.loadFiles()
	i := slices.IndexFunc(m.allFiles, func(e vcs.Entry) bool { return e.Path == path })
	if i < 0 {
		m.flash("no such file: " + diff.Quote(path))
		return
	}
	m.tab = tabFiles
	m.pickFile(i)
	if s == diff.SideOld { // the Files tab shows only the new side
		m.flash("the removed line is not in the file now")
		return
	}
	cursorToLine(&m.viewer, diff.SideNew, line)
}

// cursorToLine puts the cursor of p on the row that shows line on side s.
func cursorToLine(p *pane, s diff.Side, line int) {
	if i := p.rowOf(s, line); i >= 0 {
		p.cur = i
	}
}

// rowOf returns the first row of p that shows line on side s, or -1.
func (p *pane) rowOf(s diff.Side, line int) int {
	return slices.IndexFunc(p.rows, func(r diff.Row) bool { return r.Line(s) == line })
}

// stepFind moves the cursor to the next (d = 1) or the previous (d = -1)
// hit of the find. It wraps around.
func (m *model) stepFind(d int) {
	f, p := &m.find, m.cur()
	// A reload can make the pane shorter than the rows that matched.
	f.hits = slices.DeleteFunc(f.hits, func(h int) bool { return h >= len(p.lines) })
	if len(f.hits) == 0 {
		m.find = findState{}
		return
	}
	f.i = (min(f.i, len(f.hits)-1) + d + len(f.hits)) % len(f.hits)
	p.cur = f.hits[f.i]
}

// runFind finds q in the lines of the current pane, without case. It moves
// the cursor to the first hit at or below it.
func (m *model) runFind(q string) {
	p := m.cur()
	m.find = findState{query: q}
	if q == "" {
		return
	}
	lq := strings.ToLower(q)
	for i, l := range p.lines {
		if !strings.Contains(strings.ToLower(ansi.Strip(l)), lq) {
			continue
		}
		// The rows of a wrapped line make one hit.
		hits := m.find.hits
		if h := p.head(i); len(hits) == 0 || hits[len(hits)-1] != h {
			m.find.hits = append(hits, h)
		}
	}
	if !m.find.active() {
		m.flash("no match for " + q)
		return
	}
	atCursor := func(h int) bool { return h >= p.cur }
	m.find.i = max(slices.IndexFunc(m.find.hits, atCursor), 0)
	p.cur = m.find.hits[m.find.i]
}

// gotoLine moves the cursor to the line number in s: "0" is the first
// row, "$" the last row.
func (m *model) gotoLine(s string) {
	p := m.cur()
	switch s = strings.TrimSpace(s); s {
	case "0":
		p.cur = 0
		return
	case "$":
		p.cur = max(len(p.lines)-1, 0)
		return
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		m.flash("not a line number: " + s)
		return
	}
	// A line in a fold shows after the fold expands.
	for i, r := range p.rows {
		if f := r.Fold; p == &m.changes && n >= f.New && n < f.New+f.Lines {
			m.expandFold(i)
			break
		}
	}
	for i, r := range p.rows {
		if r.New == n || (r.New == 0 && r.Old == n) {
			p.cur = i
			return
		}
	}
	m.flash(fmt.Sprintf("line %d isn't shown", n))
}
