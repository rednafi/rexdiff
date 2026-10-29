package tui

// The search finds files by name and lines of code. It appears in an
// overlay.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/search"
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

// Time limits for a code search. After grepBudget, the search shows the
// lines found so far, with a "+" to say there may be more. A search that
// has found nothing keeps going until its first match or until
// grepTimeout.
const (
	grepBudget  = 80 * time.Millisecond
	grepTimeout = 2 * time.Second
)

// searchState holds the search results. The search has two modes: one
// shows the files whose names match, and the other shows the lines of code
// that contain the query.
type searchState struct {
	grep      bool     // true in code mode
	paths     []string // the files that the search can find
	files     []string // the matching files that are listed
	fileTotal int      // the total number of matching files
	hits      []vcs.Match
	hitsMore  bool   // git found more matches than hits holds
	err       string // the code search error
	preview   *preview
	// previewY is the line in the search box where the preview, with its
	// rule, starts.
	previewY int
}

// setSearchItems lists the results for the current mode, with the cursor
// on the first result. Code matches are grouped under a heading for each
// file. A "… more" row means the list does not include every result. An
// error replaces the results.
func (ov *overlay) setSearchItems() {
	s := &ov.search
	var items []overlayItem
	switch {
	case s.err != "":
		// The overlay shows the error instead of a list.
	case !s.grep:
		for _, p := range s.files {
			items = append(items, overlayItem{label: p, id: p})
		}
		if s.fileTotal > len(s.files) {
			items = append(items, overlayItem{label: "… more"})
		}
	default:
		for i, h := range s.hits {
			if i == 0 || h.Path != s.hits[i-1].Path {
				items = append(items, overlayItem{label: h.Path, isHeader: true})
			}
			items = append(items, overlayItem{
				label: fmt.Sprintf("%5d: %s", h.Line, h.Text),
				id:    h.Path,
				line:  h.Line,
			})
		}
		if s.hitsMore {
			items = append(items, overlayItem{label: "… more"})
		}
	}
	ov.setItems(items)
	ov.cur = 0
	if len(ov.shown) > 0 && !ov.canHaveCursor(0) {
		ov.move(1)
	}
}

// chips returns the labels of the two search modes: "files N │ code M".
// A "+" after M means git found more matches. The label of the current
// mode is highlighted.
func (ov *overlay) chips() string {
	s := &ov.search
	files, code := "files", "code"
	if s.err == "" {
		files = fmt.Sprintf("files %d", s.fileTotal)
		code = fmt.Sprintf("code %d", len(s.hits))
		if s.hitsMore {
			code += "+"
		}
	}
	on := accentStyle.Underline(true)
	if s.grep {
		return dimStyle.Render(files+" │ ") + on.Render(code)
	}
	return on.Render(files) + dimStyle.Render(" │ "+code)
}

// openSearch opens the search over file names and code. The query starts
// empty, so it lists files and searches no code.
func (m *model) openSearch() {
	ov := newOverlay(overlaySearch, "")
	ov.input.Placeholder = "Search files and code…"
	ov.search.paths = searchPaths(m.allFiles.entries)
	m.openOverlay(ov)
	m.refreshSearch() // an empty query needs no command
}

// searchPaths returns the paths of the entries that the search can find:
// those that git does not ignore.
func searchPaths(es []vcs.Entry) []string {
	var paths []string
	for _, e := range es {
		if !e.Ignored {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// maxSyncPaths is the most paths that the file search goes through on the
// UI goroutine. With more, a fuzzy search is slow enough to hold up the
// screen, so it runs in a command.
const maxSyncPaths = 20_000

// fileSearchMsg carries the files that match a search query.
type fileSearchMsg struct {
	gen   int // identifies the search; results of older searches are stale
	files []string
	total int
}

// findFiles finds the files that match the search query. With more than
// maxSyncPaths paths, it returns a command that finds them instead, and
// the list keeps the previous files until onFileSearch shows the new ones.
func (m *model) findFiles() tea.Cmd {
	s, q := &m.overlay.search, m.overlay.input.Value()
	m.fileGen++
	if q == "" || len(s.paths) <= maxSyncPaths {
		s.files, s.fileTotal = search.Files(s.paths, m.recent, q, search.MaxFiles)
		return nil
	}
	paths, recent, gen := s.paths, slices.Clone(m.recent), m.fileGen
	return func() tea.Msg {
		files, total := search.Files(paths, recent, q, search.MaxFiles)
		return fileSearchMsg{gen: gen, files: files, total: total}
	}
}

// onFileSearch shows the files found by the latest file search, unless
// the search has closed since. In code mode, only the count changes.
func (m *model) onFileSearch(msg fileSearchMsg) {
	ov := m.overlay
	if ov == nil || ov.kind != overlaySearch || msg.gen != m.fileGen {
		return
	}
	s := &ov.search
	s.files, s.fileTotal = msg.files, msg.total
	if !s.grep {
		ov.setSearchItems()
	}
}

// refreshSearch finds the files that match the search query, cancels the
// code search for the previous query and updates the list. It returns the
// query, and the command that finds the files if [model.findFiles]
// returns one. The previous code matches stay on screen until the new
// search finishes, except that a blank query clears them.
func (m *model) refreshSearch() (query string, cmd tea.Cmd) {
	ov := m.overlay
	s, q := &ov.search, ov.input.Value()
	cmd = m.findFiles()
	s.err = ""
	m.stopGrep()
	if strings.TrimSpace(q) == "" {
		s.hits, s.hitsMore = nil, false
	}
	ov.setSearchItems()
	return q, cmd
}

// startGrep refreshes the search list with [model.refreshSearch] and
// returns a command that searches the code for the query. A blank query
// searches no code and returns nil.
func (m *model) startGrep() tea.Cmd {
	q, files := m.refreshSearch()
	// A blank query would match nearly every line.
	if strings.TrimSpace(q) == "" {
		return files
	}
	ctx, cancel := xctx.WithTimeout(m.ctx, grepTimeout, "code search")
	m.grepCancel = cancel
	repo, gen := m.repo, m.grepGen
	return tea.Batch(files, func() tea.Msg {
		defer cancel()
		hits, more, err := repo.Grep(ctx, q, search.MaxGrepLines, grepBudget)
		return grepMsg{gen: gen, hits: hits, more: more, err: err}
	})
}

// addRecent moves path to the front of the recent files, which the search
// lists for an empty query.
func (m *model) addRecent(path string) {
	rest := slices.DeleteFunc(m.recent, func(p string) bool { return p == path })
	m.recent = slices.Concat([]string{path}, rest[:min(len(rest), search.MaxFiles-1)])
}

// stopGrep cancels the code search in progress, if any, and makes sure
// its result is ignored.
func (m *model) stopGrep() {
	m.grepGen++
	if m.grepCancel != nil {
		m.grepCancel()
		m.grepCancel = nil
	}
}

// onGrep stores the code matches in the search, unless the search has
// closed or the query has changed since. The list shows them in code mode.
func (m *model) onGrep(msg grepMsg) {
	ov := m.overlay
	if ov == nil || ov.kind != overlaySearch || msg.gen != m.grepGen {
		return
	}
	s := &ov.search
	s.hits, s.hitsMore, s.err = msg.hits, msg.more, ""
	if msg.err != nil {
		// Show only the error, without older results below it.
		s.hits, s.hitsMore, s.err = nil, false, msg.err.Error()
	}
	// In file mode, keep the list and its cursor as they are. The mode
	// labels still show the new number of code matches.
	if s.grep {
		ov.setSearchItems()
	}
}
