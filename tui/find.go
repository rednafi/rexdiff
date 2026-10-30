package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/search"
)

const maxFindQuery = 256

// findState caches the matches of the last search in hits, because View
// needs them on every frame.
type findState struct {
	input textinput.Model
	hits  findCache
}

// findCache keys the matches on the query, the tab and the pane's render
// count. A pane's rows, lines, path and texts change only when it renders
// again, and each render increments the count.
type findCache struct {
	ok      bool
	q       string
	tab     tab
	renders int
	hits    []findHit
}

// findHit has row as the first pane row of the line. For a line inside a
// fold, row is the fold row and line is the new-side line number.
// Otherwise line is 0.
type findHit struct {
	row, line int
}

func (m *model) canFind() bool {
	if m.tab == tabPR {
		return false
	}
	p := m.curPane()
	for i := range p.rows {
		if p.canFindRow(i) {
			return true
		}
	}
	return false
}

func (p *pane) canFindRow(i int) bool {
	r := p.rows[i]
	return r.Old > 0 || r.New > 0 || r.Fold.Lines > 0 || (p.path == "" && i < p.count())
}

// openFind opens the bar even when there is nothing to find, such as for
// a binary file. Then the next keys go into the query and do not run
// commands.
func (m *model) openFind() tea.Cmd {
	ti := textinput.New()
	ti.CharLimit = maxFindQuery
	ti.Prompt = "find "
	ti.Placeholder = "Find in file…"
	m.find = findState{input: ti}
	m.mode, m.navigatorFocused = modeFind, false
	m.curPane().selStart = -1
	return m.find.input.Focus()
}

func (m *model) closeFind() {
	if m.mode == modeFind {
		m.mode = modeNormal
	}
	m.find = findState{}
}

func (m *model) updateFind(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Code {
	case tea.KeyEscape:
		m.closeFind()
		return nil
	case tea.KeyEnter, tea.KeyDown:
		m.stepFind(1)
		m.scrollToCursor() // otherwise the view only catches up on the next tick
		return nil
	case tea.KeyUp:
		m.stepFind(-1)
		m.scrollToCursor()
		return nil
	}
	var cmd tea.Cmd
	m.find.input, cmd = m.find.input.Update(msg)
	return cmd
}

func (m *model) findSource() (oldLines, newLines []string) {
	switch m.tab {
	case tabChanges:
		if f := m.curChangedFile(); f != nil {
			return m.findSides.of(f.OldText, f.NewText)
		}
	case tabAllFiles:
		return m.findSides.of("", m.allFiles.shownText)
	}
	return nil, nil
}

// sideLines caches the split lines of the last pair of sides. Then find
// does not split the file again on every frame.
type sideLines struct {
	old, new           string
	oldLines, newLines []string
}

func (s *sideLines) of(oldText, newText string) ([]string, []string) {
	if oldText != s.old || newText != s.new {
		*s = sideLines{oldText, newText, diff.SplitLines(oldText), diff.SplitLines(newText)}
	}
	return s.oldLines, s.newLines
}

// findHits matches the source text, not the line numbers. A line on both
// sides counts as one match.
func (m *model) findHits(q string) []findHit {
	if q == "" || !m.canFind() {
		return nil
	}
	p := m.curPane()
	next := findCache{ok: true, q: q, tab: m.tab, renders: p.renders}
	c := &m.find.hits
	if c.ok && c.q == next.q && c.tab == next.tab && c.renders == next.renders {
		return c.hits
	}
	oldLines, newLines := m.findSource()
	next.hits = p.findHits(q, oldLines, newLines)
	*c = next
	return next.hits
}

func (p *pane) findHits(q string, oldLines, newLines []string) []findHit {
	line := func(ls []string, n int) string {
		if n >= 1 && n <= len(ls) {
			return ls[n-1]
		}
		return ""
	}
	var hits []findHit
	for i, r := range p.rows {
		switch {
		case p.srcRows != nil:
			// In rendered Markdown, a match points at the start of its
			// block, just as the cursor does.
			if !r.Continued && p.blockMatches(i, q) {
				hits = append(hits, findHit{i, 0})
			}
		case r.Continued:
		case r.Fold.Lines > 0:
			for n := r.Fold.New; n < r.Fold.New+r.Fold.Lines; n++ {
				if search.Matches(line(newLines, n), q) {
					hits = append(hits, findHit{i, n})
				}
			}
		case r.Old > 0 || r.New > 0:
			newHit := r.New > 0 && search.Matches(line(newLines, r.New), q)
			oldHit := r.Old > 0 && search.Matches(line(oldLines, r.Old), q)
			if newHit || oldHit {
				hits = append(hits, findHit{i, 0})
			}
		case p.canFindRow(i):
			if search.Matches(ansi.Strip(p.line(i)), q) {
				hits = append(hits, findHit{i, 0})
			}
		}
	}
	return hits
}

func (p *pane) blockMatches(i int, q string) bool {
	return slices.ContainsFunc(p.lines[i:p.lineEnd(i)+1], func(l string) bool {
		return search.Matches(ansi.Strip(l), q)
	})
}

func (m *model) findPlace(hits []findHit) (hitsBefore int, on bool) {
	cur := m.curPane().lineStart(m.curPane().cur)
	for _, h := range hits {
		switch {
		case h.row < cur:
			hitsBefore++
		case h.row == cur && h.line == 0:
			return hitsBefore, true
		}
	}
	return hitsBefore, false
}

func (m *model) stepFind(d int) {
	hits := m.findHits(m.find.input.Value())
	if len(hits) == 0 {
		return
	}
	hitsBefore, on := m.findPlace(hits)
	n := len(hits)
	i := (hitsBefore + n - 1) % n
	if d > 0 {
		i = hitsBefore % n
		if on {
			i = (hitsBefore + 1) % n
		}
	}
	h, p := hits[i], m.curPane()
	if h.line == 0 {
		p.cur = h.row
	} else {
		m.expandFold(p.rows[h.row].Fold)
		jumpToLine(p, diff.SideNew, h.line)
	}
	m.showMatch(m.find.input.Value())
}

func (m *model) showMatch(q string) {
	p := m.curPane()
	if p.diff == nil || p.srcRows != nil {
		return
	}
	row := p.lineStart(p.cur)
	if row >= len(p.rows) {
		return
	}
	r := p.rows[row]
	oldLines, newLines := m.findSource()
	side, at := diff.SideNew, -1
	if r.New >= 1 && r.New <= len(newLines) {
		at = search.Index(newLines[r.New-1], q)
	}
	if at < 0 && r.Old >= 1 && r.Old <= len(oldLines) {
		side, at = diff.SideOld, search.Index(oldLines[r.Old-1], q)
	}
	if at < 0 {
		return
	}
	line, col := p.diff.Locate(row, side, at)
	if m.wrap {
		m.scrollToCursor()
		if h := max(m.mainHeight(), 1); line >= p.top+h {
			p.top = min(max(line-h/3, row), max(p.lineEnd(row)-h+1, row))
			p.following = false
		}
		return
	}
	w, qw := p.diff.CodeWidth(side), ansi.StringWidth(q)
	if w == 0 || (col >= p.scrollX && col+qw <= p.scrollX+w) {
		return
	}
	p.scrollX = max(col-w/3, 0)
	m.rerender()
}

func (m *model) shownHits() []findHit {
	if m.mode != modeFind {
		return nil
	}
	return m.findHits(m.find.input.Value())
}

func (m *model) findBand(hits []findHit) string {
	count := ""
	switch {
	case !m.canFind():
		count = "nothing to find here"
	case m.find.input.Value() != "":
		hitsBefore, on := m.findPlace(hits)
		switch {
		case len(hits) == 0:
			count = "no matches"
		case on:
			count = fmt.Sprintf("%d/%d", hitsBefore+1, len(hits))
		default:
			count = strconv.Itoa(len(hits))
		}
	}
	fw := m.footerWidth()
	w := max(fw-ansi.StringWidth(count)-1, 1)
	m.find.input.SetWidth(max(w-ansi.StringWidth(m.find.input.Prompt)-1, 1))
	band := truncate(m.find.input.View(), w)
	pad := strings.Repeat(" ", max(fw-ansi.StringWidth(band)-ansi.StringWidth(count), 0))
	return band + pad + dimStyle.Render(count)
}
