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

const (
	scrollStep = 8 // columns
	// navigatorWidthStep is in columns for a side list. For a list above
	// or below, it is a percentage of the body rows.
	navigatorWidthStep = 4
)

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

const colorsTick = 16 * time.Millisecond

type colorsMsg struct{}

func (m *model) awaitColors() tea.Cmd {
	if m.colorsTicking || !m.cache.Highlighting() {
		return nil
	}
	m.colorsTicking = true
	return tea.Tick(colorsTick, func(time.Time) tea.Msg { return colorsMsg{} })
}

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

func (m *model) onKeys() tea.Cmd {
	m.help.ShowAll = !m.help.ShowAll
	return nil
}

func (m *model) onTabChanges() tea.Cmd {
	m.setTab(tabChanges)
	return nil
}

func (m *model) onTabAllFiles() tea.Cmd {
	m.setTab(tabAllFiles)
	return nil
}

func (m *model) onTabPR() tea.Cmd {
	m.setTab(tabPR)
	return nil
}

func (m *model) onScopeUncommitted() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeUncommitted)
	}
	return nil
}

func (m *model) onScopeBranch() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeBranch)
	}
	return nil
}

func (m *model) onScopeLastTurn() tea.Cmd {
	if m.tab != tabPR {
		m.setScope(ScopeLastTurn)
	}
	return nil
}

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

func (m *model) onBasePick() tea.Cmd {
	if m.tab == tabPR {
		return nil
	}
	return m.openBasePicker()
}

func (m *model) onCommitPick() tea.Cmd { return m.openCommitPicker() }

func (m *model) onFocus() tea.Cmd {
	if m.navigatorHidden && m.tab != tabPR {
		m.navigatorHidden, m.navigatorFocused = false, true
		m.rerender()
		return nil
	}
	m.navigatorFocused = !m.navigatorFocused
	return nil
}

func (m *model) onDown() tea.Cmd {
	if m.navigatorFocused && m.tab != tabPR {
		m.stepNavigatorRow(1)
	} else {
		m.moveCur(1)
	}
	return nil
}

func (m *model) onUp() tea.Cmd {
	if m.navigatorFocused && m.tab != tabPR {
		m.stepNavigatorRow(-1)
	} else {
		m.moveCur(-1)
	}
	return nil
}

func (m *model) onPageDown() tea.Cmd { return m.moveCur(m.mainHeight()) }

func (m *model) onPageUp() tea.Cmd { return m.moveCur(-m.mainHeight()) }

func (m *model) onHalfDown() tea.Cmd { return m.moveCur(m.mainHeight() / 2) }

func (m *model) onHalfUp() tea.Cmd { return m.moveCur(-m.mainHeight() / 2) }

// moveCur moves the cursor by d rows. A move down always leaves the
// cursor line. The cursor stays on the first row of a wrapped line. So a
// line taller than the screen scrolls until its end or start shows. A move
// up onto such a line shows its end.
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
		if last == p.last() {
			p.following = p.top <= first
			return nil
		}
		to = max(to, last+1)
	}
	p.cur, p.following = to, true
	if first, last := p.lineStart(to), p.lineEnd(to); d < 0 && last-first+1 > h {
		p.cur, p.top, p.following = first, last-h+1, false
	}
	return nil
}

func (m *model) onNextHunk() tea.Cmd {
	m.stepHunk(1)
	return nil
}

func (m *model) onPrevHunk() tea.Cmd {
	m.stepHunk(-1)
	return nil
}

func (m *model) onNextFile() tea.Cmd {
	m.stepFile(1)
	return nil
}

func (m *model) onPrevFile() tea.Cmd {
	m.stepFile(-1)
	return nil
}

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

// onWrap does nothing outside a diff, because rendered Markdown does its
// own wrapping.
func (m *model) onWrap() tea.Cmd {
	if m.curPane().diff == nil {
		return nil
	}
	m.wrap = !m.wrap
	m.changes.pane.scrollX, m.allFiles.pane.scrollX = 0, 0
	m.rerender()
	return nil
}

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

func (m *model) onComment() tea.Cmd {
	if m.navigatorFocused {
		return nil
	}
	return m.openComment(diff.SideNew)
}

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

func (m *model) onNextComment() tea.Cmd {
	m.stepComment(1)
	return nil
}

func (m *model) onPrevComment() tea.Cmd {
	m.stepComment(-1)
	return nil
}

func (m *model) onComments() tea.Cmd {
	m.openComments()
	return nil
}

func (m *model) onSend() tea.Cmd { return m.startSend() }

func (m *model) onCopy() tea.Cmd {
	m.copyComments()
	return nil
}

func (m *model) onSearch() tea.Cmd {
	m.ensureAllFiles()
	m.openSearch()
	return nil
}

func (m *model) onFind() tea.Cmd { return m.openFind() }

func (m *model) onGotoLine() tea.Cmd {
	m.openInput(overlayGotoLine, "go to line (:0 first, :$ last)", "")
	return nil
}

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

func (m *model) onNavigatorPosition() tea.Cmd {
	m.moveNavigator()
	return nil
}

func (m *model) onNavigatorHide() tea.Cmd {
	m.toggleNavigator()
	return nil
}

func (m *model) onNavigatorGrow() tea.Cmd {
	m.resizeNavigator(navigatorWidthStep)
	return nil
}

func (m *model) onNavigatorShrink() tea.Cmd {
	m.resizeNavigator(-navigatorWidthStep)
	return nil
}

func (m *model) onUpdate() tea.Cmd {
	if m.release == nil {
		return nil
	}
	return m.startUpdate()
}

func (m *model) moveNavigator() {
	if m.navigatorHidden && m.tab != tabPR {
		return
	}
	m.navigatorPosition = m.navigatorPosition.next()
	m.rerender()
}

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

func (m *model) openURL(url, what string) tea.Cmd {
	wait, err := urlOpenerCmd(m.env, m.cfg, url)
	if err != nil {
		m.flash("open failed: " + err.Error())
		return nil
	}
	m.flash("opened " + what + " in browser")
	return wait
}

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

func (m *model) setTab(t tab) {
	m.tab = t
	m.drag = dragState{}
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

func (m *model) setScope(s Scope) {
	m.tab, m.scope, m.expandedFolds, m.shownTrees = tabChanges, s, nil, ""
	m.cache.StopHighlighting()
	m.requestReload()
	m.changes.pane.reset()
	m.crossStep = 0
}

func (m *model) cursorOnFold() bool {
	p := m.curPane()
	return m.tab == tabChanges && p.cur < len(p.rows) && p.rows[p.cur].Fold.Lines > 0
}

// expandedFolds uses old line numbers, because they stay valid while the
// agent edits the file. The ranges apply only while the old content has
// the hash Old.
type expandedFolds struct {
	Old    uint64   `json:"old"`
	Ranges [][2]int `json:"ranges"` // inclusive first and last lines
}

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

func (m *model) canCollapseFold() bool {
	return m.curPane().scrollX == 0 && m.cursorInExpandedFold()
}

// collapseFold needs canCollapseFold to be true.
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

func (m *model) curFileIndex() int {
	if m.tab == tabAllFiles {
		return m.allFiles.cur
	}
	return m.changes.cur
}

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

// stepHunk arms a jump to the nearest changed file when no hunk is left
// in direction d. A second press opens that file.
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

// crossFile follows the order of the file list, not of m.changes.files.
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

func (m *model) stepComment(d int) {
	p := m.curPane()
	a := m.curAnchor()
	var stops []int
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

func (m *model) jumpToComment(c *diff.Comment) {
	if c.Anchor.InDiff {
		m.jumpTo(c.Path, c.Side, c.Start)
	} else {
		m.jumpToFile(c.Path, c.Side, c.Start)
	}
	m.jumpToNearLine(c)
}

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

func (m *model) expandFoldAt(side diff.Side, line int) {
	for _, r := range m.changes.pane.rows {
		if foldHas(r.Fold, side, line) {
			m.expandFold(r.Fold)
			return
		}
	}
}

func (m *model) jumpToFile(path string, side diff.Side, line int) {
	if m.allFiles.entries == nil {
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

func jumpToLine(p *pane, side diff.Side, line int) {
	if i := p.rowOf(side, line); i >= 0 {
		p.cur = i
	}
}

// rowOf maps a line inside a rendered Markdown block to the first row of
// the block.
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

// gotoLine treats the number as a row number in a pane without line
// numbers.
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

func foldStart(fold diff.Fold, side diff.Side) int {
	if side == diff.SideOld {
		return fold.Old
	}
	return fold.New
}

func foldEnd(fold diff.Fold, side diff.Side) int {
	if fold.Lines == 0 {
		return 0
	}
	return foldStart(fold, side) + fold.Lines - 1
}

func foldHas(fold diff.Fold, side diff.Side, line int) bool {
	return fold.Lines > 0 && line >= foldStart(fold, side) && line <= foldEnd(fold, side)
}

func inRange(r [2]int, line int) bool { return line >= r[0] && line <= r[1] }

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

func pastedLine(text string) (line string, ok bool) {
	line = strings.Trim(strings.TrimSpace(text), "`'\"“”‘’()[]<>:.,;")
	_, ok = parseLineNum(line)
	return line, ok
}
