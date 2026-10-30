package tui

import (
	"slices"
	"strings"

	"github.com/rednafi/rexdiff/internal/tree"
	"github.com/rednafi/rexdiff/vcs"
)

type navigator struct {
	entries []tree.Entry
	rows    []tree.Row
	cur     int
	// following is false after a mouse wheel scroll. Then the cursor can
	// go off screen until the next key press or click.
	top       int
	following bool
	// toggled holds the directories that the user switched away from
	// expandAll.
	expandAll bool
	toggled   map[string]bool
}

// build keeps the cursor on the same directory or file. If that row is
// gone, it moves to the file with index cur, then to the first file.
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

func (t *navigator) scrollToCursor(h int) {
	h = max(h, 1)
	if t.following {
		t.top = keepInView(t.top, t.cur, h)
	}
	t.top = t.start(h)
}

// keepInView needs h of at least 1.
func keepInView(top, cur, h int) int { return min(cur, max(top, cur-h+1)) }

func clampTop(top, n, h int) int { return max(min(top, n-h), 0) }

func (t *navigator) start(h int) int {
	return clampTop(t.top, len(t.rows), max(h, 1))
}

// curFile returns -1 if the cursor is on a directory.
func (t *navigator) curFile() int {
	if t.cur >= len(t.rows) {
		return -1
	}
	return t.rows[t.cur].File
}

func (t *navigator) fileOr(cur int) int {
	if i := t.curFile(); i >= 0 {
		return i
	}
	return max(cur, 0)
}

func (t *navigator) curDir() (tree.Row, bool) {
	if t.cur >= len(t.rows) || t.rows[t.cur].File >= 0 {
		return tree.Row{}, false
	}
	return t.rows[t.cur], true
}

func (t *navigator) rowOf(i int) int {
	if i < 0 {
		return -1
	}
	return slices.IndexFunc(t.rows, func(r tree.Row) bool { return r.File == i })
}

func (t *navigator) nextFile(from, d int) int {
	for r := from + d; r >= 0 && r < len(t.rows); r += d {
		if t.rows[r].File >= 0 {
			return r
		}
	}
	return -1
}

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

func (t *navigator) reveal(path string) bool {
	changed := false
	for dir, _, ok := strings.CutLast(path, "/"); ok; dir, _, ok = strings.CutLast(dir, "/") {
		changed = t.setExpanded(dir, true) || changed
	}
	return changed
}

func (m *model) curNavigator() *navigator {
	switch m.tab {
	case tabChanges:
		return &m.changes.navigator
	case tabAllFiles:
		return &m.allFiles.navigator
	}
	return nil
}

func (m *model) buildChangesNavigator() {
	es := make([]tree.Entry, len(m.changes.files))
	for i, f := range m.changes.files {
		es[i] = tree.Entry{Path: f.Path, Changed: true}
	}
	t := &m.changes.navigator
	t.build(es, true, m.changes.cur)
	m.changes.cur = t.fileOr(m.changes.cur)
}

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

func (m *model) buildNavigator() {
	switch m.tab {
	case tabChanges:
		m.buildChangesNavigator()
	case tabAllFiles:
		m.buildAllFilesNavigator()
	}
}

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

func expandedIgnoredDir(e vcs.Entry, expanded map[string]bool) (string, bool) {
	dir, isDir := strings.CutSuffix(e.Path, "/")
	return dir, isDir && e.Ignored && expanded[dir]
}

func (m *model) cursorOnDir() bool {
	if t := m.curNavigator(); t != nil && m.navigatorFocused {
		_, ok := t.curDir()
		return ok
	}
	return false
}

// setDirExpanded reads the contents of an ignored directory it expands.
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

func (m *model) revealFile(path string) {
	if t := m.curNavigator(); t != nil && t.reveal(path) {
		m.buildNavigator()
	}
}
