package tui

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
// lines found so far, with a "+" for more. A search with no match yet
// continues until its first match or grepTimeout.
const (
	grepBudget  = 80 * time.Millisecond
	grepTimeout = 2 * time.Second
)

type searchState struct {
	grep      bool // true in code mode
	paths     []string
	files     []string
	fileTotal int // all matching files, listed or not
	hits      []vcs.Match
	hitsMore  bool // git found more matches than hits holds
	err       string
	preview   *preview
	previewY  int
}

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

func (m *model) openSearch() {
	ov := newOverlay(overlaySearch, "")
	ov.input.Placeholder = "Search files and code…"
	ov.search.paths = searchPaths(m.allFiles.entries)
	m.openOverlay(ov)
	m.refreshSearch() // an empty query needs no command
}

func searchPaths(es []vcs.Entry) []string {
	var paths []string
	for _, e := range es {
		if !e.Ignored {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

// maxSyncPaths is the most paths that the file search does on the UI
// goroutine. With more, a fuzzy search holds up the screen. So it runs in
// a command.
const maxSyncPaths = 20_000

type fileSearchMsg struct {
	gen   int // the search. Results of older searches are stale.
	files []string
	total int
}

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

// refreshSearch keeps the old code matches until the new search finishes.
// A blank query clears them.
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

func (m *model) addRecent(path string) {
	rest := slices.DeleteFunc(m.recent, func(p string) bool { return p == path })
	m.recent = slices.Concat([]string{path}, rest[:min(len(rest), search.MaxFiles-1)])
}

func (m *model) stopGrep() {
	m.grepGen++
	if m.grepCancel != nil {
		m.grepCancel()
		m.grepCancel = nil
	}
}

func (m *model) onGrep(msg grepMsg) {
	ov := m.overlay
	if ov == nil || ov.kind != overlaySearch || msg.gen != m.grepGen {
		return
	}
	s := &ov.search
	s.hits, s.hitsMore, s.err = msg.hits, msg.more, ""
	if msg.err != nil {
		s.hits, s.hitsMore, s.err = nil, false, msg.err.Error()
	}
	if s.grep {
		ov.setSearchItems()
	}
}
