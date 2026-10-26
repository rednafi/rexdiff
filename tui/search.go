package tui

// The search finds files by name and lines of code, as in herdr-reviewr.
// It shows in an overlay.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/search"
	"github.com/rednafi/rexdiff/vcs"
)

// The times of a code search, as in herdr-reviewr. After codeBudget, the
// search shows the lines that it finds, and a "+" tells that there can be
// others. A search with no line yet goes on until its first line, or
// until codeTimeout.
const (
	codeBudget  = 80 * time.Millisecond
	codeTimeout = 2 * time.Second
)

// searchState has the results of the search. As in herdr-reviewr, the
// search has two modes. It shows the files that match by name, or the
// lines of the code that contain the query.
type searchState struct {
	code      bool     // the mode that shows the code matches
	paths     []string // the files that the search can find
	files     []string // the files that match
	fileTotal int      // the number of the files that match
	hits      []vcs.Match
	hitsMore  bool   // git finds more matches than hits has
	err       string // the error of the code search
	preview   *preview
}

// showSearch shows the results of the search in its mode, with the
// cursor on the first result. As in herdr-reviewr, the code matches show
// under a header for each file. A "… more" row tells that the list does
// not show all the results. An error shows in place of the results.
func (o *overlay) showSearch() {
	s := &o.search
	var items []pickItem
	switch {
	case s.err != "":
	case !s.code:
		for _, p := range s.files {
			items = append(items, pickItem{label: p, id: p})
		}
		if s.fileTotal > len(s.files) {
			items = append(items, pickItem{label: "… more"})
		}
	default:
		for i, h := range s.hits {
			if i == 0 || h.Path != s.hits[i-1].Path {
				items = append(items, pickItem{label: h.Path, head: true})
			}
			items = append(items, pickItem{
				label: fmt.Sprintf("%5d: %s", h.Line, h.Text),
				id:    h.Path,
				line:  h.Line,
			})
		}
		if s.hitsMore {
			items = append(items, pickItem{label: "… more"})
		}
	}
	o.setItems(items)
	o.cur = 0
	if len(o.shown) > 0 && !o.pickable(0) {
		o.move(1)
	}
}

// chips returns the chips of the modes of the search, as in
// herdr-reviewr: "files N │ code M". A "+" after M tells that git finds
// more matches. The chip of the mode in use is bright.
func (o *overlay) chips() string {
	s := &o.search
	files, code := "files", "code"
	if s.err == "" {
		files = fmt.Sprintf("files %d", s.fileTotal)
		code = fmt.Sprintf("code %d", len(s.hits))
		if s.hitsMore {
			code += "+"
		}
	}
	on := cursorStyle.Underline(true)
	if s.code {
		return dimStyle.Render(files+" │ ") + on.Render(code)
	}
	return on.Render(files) + dimStyle.Render(" │ "+code)
}

// openSearch opens the search of the files and the code. With no query,
// it lists the files.
func (m *model) openSearch() {
	ov := newOverlay(overlaySearch, "")
	ov.input.Placeholder = "Search files and code…"
	for _, e := range m.allFiles {
		if !e.Ignored {
			ov.search.paths = append(ov.search.paths, e.Path)
		}
	}
	m.open(ov)
	m.runSearch()
}

// runSearch finds the files that match the query of the search, and
// starts a code search for the query. It stops the code search of the
// last query. Until the new code search ends, the old code matches show.
// An empty query finds no code.
func (m *model) runSearch() tea.Cmd {
	ov := m.overlay
	s, q := &ov.search, ov.input.Value()
	s.files, s.fileTotal = search.Files(s.paths, m.recent, q, search.MaxFiles)
	s.err = ""
	m.stopGrep()
	// A blank grep is noise, as in herdr-reviewr.
	if strings.TrimSpace(q) == "" {
		s.hits, s.hitsMore = nil, false
		ov.showSearch()
		return nil
	}
	ov.showSearch()
	ctx, cancel := context.WithTimeout(m.ctx, codeTimeout)
	m.grepCancel = cancel
	repo, gen := m.repo, m.grepGen
	return func() tea.Msg {
		defer cancel()
		hits, more, err := repo.Grep(ctx, q, search.MaxCodeLines, codeBudget)
		return grepMsg{gen: gen, hits: hits, more: more, err: err}
	}
}

// addRecent puts path first in the list of the files that the search
// shows for an empty query.
func (m *model) addRecent(path string) {
	rest := slices.DeleteFunc(m.recent, func(p string) bool { return p == path })
	m.recent = append([]string{path}, rest[:min(len(rest), search.MaxFiles-1)]...)
}

// stopGrep stops the code search that runs, if any. Its result then does
// not show.
func (m *model) stopGrep() {
	m.grepGen++
	if m.grepCancel != nil {
		m.grepCancel()
		m.grepCancel = nil
	}
}

// onGrep keeps the code matches of the search, but only if the search
// still shows their query. The list shows them in the code mode.
func (m *model) onGrep(msg grepMsg) {
	ov := m.overlay
	if ov == nil || ov.kind != overlaySearch || msg.gen != m.grepGen {
		return
	}
	s := &ov.search
	s.hits, s.hitsMore, s.err = msg.hits, msg.more, ""
	if msg.err != nil {
		// Show only the error, and no old results under it.
		s.hits, s.hitsMore, s.err = nil, false, msg.err.Error()
	}
	// The list of the file matches stays, with its cursor. The chips
	// show the count of the code matches.
	if s.code {
		ov.showSearch()
	}
}
