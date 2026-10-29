package tui

// The navigator is the file list. It shows the files of a tab as a tree of
// directories, and its cursor moves over both directories and files. The
// main pane shows the last file that the cursor was on.

import (
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/internal/tree"
	"github.com/rednafi/rexdiff/vcs"
)

// navigator is the file list of a tab.
type navigator struct {
	entries []tree.Entry
	rows    []tree.Row
	cur     int // the cursor row
	// top is the first row on screen. It moves only as far as needed to
	// keep the cursor visible. following is true while the view keeps the
	// cursor on screen. The mouse wheel sets it to false, which lets the
	// cursor go off screen until the next key press or click.
	top       int
	following bool
	// expandAll is true if directories start expanded. toggled holds the
	// directories that the user has switched away from that default.
	expandAll bool
	toggled   map[string]bool
}

// build rebuilds the rows from es. The cursor stays on the same directory
// or file. If that row is gone, or the cursor is -1, the cursor moves to
// the file with index cur or, failing that, to the first file.
func (t *navigator) build(es []tree.Entry, expandAll bool, cur int) {
	var dir, path string
	if t.cur >= 0 && t.cur < len(t.rows) {
		if r := t.rows[t.cur]; r.File < 0 {
			dir = r.Dir
		} else {
			path = t.entries[r.File].Path
		}
	}
	t.entries, t.expandAll = es, expandAll
	t.rows = tree.Build(es, t.toggled, expandAll)
	t.cur = slices.IndexFunc(t.rows, func(r tree.Row) bool {
		if r.File < 0 {
			return dir != "" && r.Dir == dir
		}
		return path != "" && es[r.File].Path == path
	})
	if t.cur < 0 {
		t.cur = t.rowOf(cur)
	}
	if t.cur < 0 {
		t.cur = t.nextFile(-1, 1)
	}
	t.cur = max(t.cur, 0)
}

// scrollToCursor scrolls a list h rows high as little as possible so that
// the cursor is visible. If the list is not following the cursor, it only
// keeps top within the rows.
func (t *navigator) scrollToCursor(h int) {
	h = max(h, 1)
	if t.following {
		t.top = keepInView(t.top, t.cur, h)
	}
	t.top = t.start(h)
}

// keepInView returns the new top row of a view h rows high, moved as
// little as possible from top so that row cur is visible. h must be at
// least 1.
func keepInView(top, cur, h int) int { return min(cur, max(top, cur-h+1)) }

// clampTop clamps top for a view h rows high over n rows, keeping the last
// screen full when possible.
func clampTop(top, n, h int) int { return max(min(top, n-h), 0) }

// start returns the first visible row in a view h rows high: top, clamped
// to the rows.
func (t *navigator) start(h int) int {
	return clampTop(t.top, len(t.rows), max(h, 1))
}

// curFile returns the index of the file at the cursor, or -1 if the
// cursor is on a directory.
func (t *navigator) curFile() int {
	if t.cur >= len(t.rows) {
		return -1
	}
	return t.rows[t.cur].File
}

// fileOr returns the index of the file at the cursor, or cur if the
// cursor is on a directory. It never returns less than 0.
func (t *navigator) fileOr(cur int) int {
	if i := t.curFile(); i >= 0 {
		return i
	}
	return max(cur, 0)
}

// curDir returns the row at the cursor and true if it is a directory.
func (t *navigator) curDir() (tree.Row, bool) {
	if t.cur >= len(t.rows) || t.rows[t.cur].File >= 0 {
		return tree.Row{}, false
	}
	return t.rows[t.cur], true
}

// rowOf returns the row of the file with index i, or -1 if it has no
// visible row.
func (t *navigator) rowOf(i int) int {
	if i < 0 {
		return -1
	}
	return slices.IndexFunc(t.rows, func(r tree.Row) bool { return r.File == i })
}

// nextFile returns the first file row after row from in direction d (1 or
// -1), or -1 if there is none.
func (t *navigator) nextFile(from, d int) int {
	for r := from + d; r >= 0 && r < len(t.rows); r += d {
		if t.rows[r].File >= 0 {
			return r
		}
	}
	return -1
}

// setExpanded expands or collapses the directory dir. It reports whether
// anything changed.
func (t *navigator) setExpanded(dir string, expanded bool) bool {
	// A toggled directory is in the opposite state to expandAll.
	toggled := t.expandAll != expanded
	if t.toggled[dir] == toggled {
		return false
	}
	if toggled {
		if t.toggled == nil {
			t.toggled = map[string]bool{}
		}
		t.toggled[dir] = true
	} else {
		delete(t.toggled, dir)
	}
	return true
}

// reveal expands every directory above path. It reports whether any of
// them changed.
func (t *navigator) reveal(path string) bool {
	changed := false
	for dir, _, ok := strings.CutLast(path, "/"); ok; dir, _, ok = strings.CutLast(dir, "/") {
		changed = t.setExpanded(dir, true) || changed
	}
	return changed
}

// curNavigator returns the file list of the current tab, or nil on the PR
// tab.
func (m *model) curNavigator() *navigator {
	switch m.tab {
	case tabChanges:
		return &m.changes.navigator
	case tabAllFiles:
		return &m.allFiles.navigator
	}
	return nil
}

// buildChangesNavigator rebuilds the file list of the Changes tab. If the
// cursor ends up on a file, that file becomes the current file.
func (m *model) buildChangesNavigator() {
	es := make([]tree.Entry, len(m.changes.files))
	for i, f := range m.changes.files {
		es[i] = tree.Entry{Path: f.Path, Changed: true}
	}
	t := &m.changes.navigator
	t.build(es, true, m.changes.cur)
	m.changes.cur = t.fileOr(m.changes.cur)
}

// buildAllFilesNavigator rebuilds the file list of the Files tab. Files
// that are in the Changes tab are marked as changed. If the cursor ends up
// on a file, that file becomes the current file.
func (m *model) buildAllFilesNavigator() {
	changed := make(map[string]bool, len(m.changes.files))
	for _, f := range m.changes.files {
		changed[f.Path] = true
	}
	es := make([]tree.Entry, len(m.allFiles.entries))
	m.allFiles.fileCount = 0
	for i, e := range m.allFiles.entries {
		p, isDir := strings.CutSuffix(e.Path, "/")
		es[i] = tree.Entry{Path: p, Changed: changed[p], Ignored: e.Ignored, Dir: isDir}
		if !isDir {
			m.allFiles.fileCount++
		}
	}
	t := &m.allFiles.navigator
	t.build(es, false, m.allFiles.cur)
	m.allFiles.cur = min(t.fileOr(m.allFiles.cur), max(len(m.allFiles.entries)-1, 0))
}

// buildNavigator rebuilds the file list of the current tab.
func (m *model) buildNavigator() {
	switch m.tab {
	case tabChanges:
		m.buildChangesNavigator()
	case tabAllFiles:
		m.buildAllFilesNavigator()
	}
}

// withIgnored adds to es the contents of each ignored directory in
// expanded, the directories that the user has expanded in the Files tab,
// and sorts es by path.
func withIgnored(repo *vcs.Repo, es []vcs.Entry, expanded map[string]bool) []vcs.Entry {
	// The loop also visits the entries it appends, so that expanded
	// ignored directories nested inside others are read too.
	for i := 0; i < len(es); i++ {
		if dir, ok := expandedIgnoredDir(es[i], expanded); ok {
			es = append(es, repo.IgnoredDir(dir)...)
		}
	}
	slices.SortStableFunc(es, func(a, b vcs.Entry) int { return strings.Compare(a.Path, b.Path) })
	return es
}

// includedPaths returns the paths to pass to [vcs.Watcher.Include] while
// the Files tab is shown: the ignored directories that the user has
// expanded, and the open file. On other tabs, it returns nil and "".
func (m *model) includedPaths() (dirs []string, file string) {
	if m.tab != tabAllFiles {
		return nil, ""
	}
	for _, e := range m.allFiles.entries {
		if dir, ok := expandedIgnoredDir(e, m.allFiles.navigator.toggled); ok {
			dirs = append(dirs, dir)
		}
	}
	if m.allFiles.cur < len(m.allFiles.entries) {
		if e := m.allFiles.entries[m.allFiles.cur]; !strings.HasSuffix(e.Path, "/") {
			file = e.Path
		}
	}
	return dirs, file
}

// expandedIgnoredDir returns the path of e without its trailing "/", and
// reports whether e is an ignored directory in expanded, the directories
// that the user has expanded in the Files tab.
func expandedIgnoredDir(e vcs.Entry, expanded map[string]bool) (string, bool) {
	dir, isDir := strings.CutSuffix(e.Path, "/")
	return dir, isDir && e.Ignored && expanded[dir]
}

// cursorOnDir reports whether the file list has focus and its cursor is on
// a directory.
func (m *model) cursorOnDir() bool {
	if t := m.curNavigator(); t != nil && m.navigatorFocused {
		_, ok := t.curDir()
		return ok
	}
	return false
}

// setDirExpanded expands or collapses the directory at the file list
// cursor. Expanding an ignored directory reads its contents.
func (m *model) setDirExpanded(expanded bool) {
	t := m.curNavigator()
	r, ok := t.curDir()
	if !ok || !t.setExpanded(r.Dir, expanded) {
		return
	}
	if expanded && m.tab == tabAllFiles && r.Ignored {
		m.allFiles.listing.want = true
	}
	m.buildNavigator()
}

// stepNavigatorRow moves the file list cursor by d rows. If the cursor
// lands on a file, the main pane shows it.
func (m *model) stepNavigatorRow(d int) {
	t := m.curNavigator()
	if t == nil || len(t.rows) == 0 {
		return
	}
	t.cur = min(max(t.cur+d, 0), len(t.rows)-1)
	if i := t.curFile(); i >= 0 && i != m.curFileIndex() {
		m.setCurFile(i)
	}
}

// stepFile shows the next (d = 1) or previous (d = -1) file in the file
// list, skipping directories. It starts from the file list cursor when the
// file list has focus, and from the shown file otherwise.
func (m *model) stepFile(d int) {
	t := m.curNavigator()
	if t == nil {
		return
	}
	from := t.cur
	if r := t.rowOf(m.curFileIndex()); !m.navigatorFocused && r >= 0 {
		from = r
	}
	if r := t.nextFile(from, d); r >= 0 {
		m.setCurFile(t.rows[r].File)
	}
}

// clickNavigatorRow handles a click on row r of the file list. Clicking a
// file shows it, and clicking a directory expands or collapses it.
func (m *model) clickNavigatorRow(r int) {
	t := m.curNavigator()
	if t == nil || r < 0 || r >= len(t.rows) {
		return
	}
	m.navigatorFocused = true
	t.cur = r
	if dir, ok := t.curDir(); ok {
		m.setDirExpanded(!dir.Expanded)
		return
	}
	m.setCurFile(t.rows[r].File)
}

// revealFile expands the directories above path in the current tab's file
// list, so that its row is visible.
func (m *model) revealFile(path string) {
	if t := m.curNavigator(); t != nil && t.reveal(path) {
		m.buildNavigator()
	}
}
