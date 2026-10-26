package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/search"
)

// This file has the find band of herdr-reviewr. The band opens at the
// bottom of the screen and stays open. Enter or down goes to the next hit,
// up goes to the previous hit, and esc closes the band.

// findState is the query of the find band. The band is open in modeFind.
type findState struct {
	input textinput.Model
}

// findHit is a line that matches the query. row is its first row in the
// pane. A line in a fold that hides it has the row of the fold, and its
// new line in line. Else line is 0.
type findHit struct {
	row, line int
}

// findable reports whether the find band can open: on a file tab, with
// a row that shows text.
func (m *model) findable() bool {
	if m.tab == tabPR {
		return false
	}
	p := m.cur()
	for i := range p.rows {
		if p.hasContent(i) {
			return true
		}
	}
	return false
}

// hasContent reports whether row i of p shows text that a find can match.
// A note has no such text. A row of rendered markdown has no line, so
// its text is the rendered line.
func (p *pane) hasContent(i int) bool {
	r := p.rows[i]
	return r.Old > 0 || r.New > 0 || r.Fold.Lines > 0 || (p.path == "" && i < len(p.lines))
}

// openFind opens the find band with an empty query.
func (m *model) openFind() tea.Cmd {
	if !m.findable() {
		return nil
	}
	ti := textinput.New()
	ti.Prompt = "find "
	ti.Placeholder = "Find in file…"
	m.find = findState{input: ti}
	m.mode, m.focusList = modeFind, false
	m.cur().anchor = -1
	return m.find.input.Focus()
}

// closeFind closes the find band and drops the query. The cursor stays.
func (m *model) closeFind() {
	if m.mode == modeFind {
		m.mode = modeNormal
	}
	m.find = findState{}
}

// updateFind handles a key while the find band is open. As in
// herdr-reviewr, the other keys edit the query.
func (m *model) updateFind(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Code {
	case tea.KeyEscape:
		m.closeFind()
		return nil
	case tea.KeyEnter, tea.KeyDown:
		m.stepFind(1)
		return nil
	case tea.KeyUp:
		m.stepFind(-1)
		return nil
	}
	var cmd tea.Cmd
	m.find.input, cmd = m.find.input.Update(msg)
	return cmd
}

// paneSource returns the lines of the two sides of the file in the
// current pane.
func (m *model) paneSource() (before, after []string) {
	switch m.tab {
	case tabChanges:
		if f := m.selectedChange(); f != nil {
			return m.findLines.of(f.Old, f.New)
		}
	case tabFiles:
		return m.findLines.of("", m.viewed)
	}
	return nil, nil
}

// splitLines keeps the lines of the last two sides, so a find does not
// split the file again on each frame.
type splitLines struct {
	old, new           string
	oldLines, newLines []string
}

// of returns the lines of before and after.
func (s *splitLines) of(before, after string) ([]string, []string) {
	if before != s.old || after != s.new {
		*s = splitLines{before, after, diff.Lines(before), diff.Lines(after)}
	}
	return s.oldLines, s.newLines
}

// findHits returns the lines of the pane that match q, in order. It
// matches the source text of a line, and not the line numbers. A line on
// both sides, as in the split layout, is one hit.
func (m *model) findHits(q string) []findHit {
	if q == "" {
		return nil
	}
	p := m.cur()
	before, after := m.paneSource()
	line := func(ls []string, n int) string {
		if n >= 1 && n <= len(ls) {
			return ls[n-1]
		}
		return ""
	}
	var hits []findHit
	for i, r := range p.rows {
		switch {
		case r.Continued:
		case r.Fold.Lines > 0:
			for n := r.Fold.New; n < r.Fold.New+r.Fold.Lines; n++ {
				if search.Match(line(after, n), q) {
					hits = append(hits, findHit{i, n})
				}
			}
		case r.Old > 0 || r.New > 0:
			if (r.New > 0 && search.Match(line(after, r.New), q)) ||
				(r.Old > 0 && search.Match(line(before, r.Old), q)) {
				hits = append(hits, findHit{i, 0})
			}
		case p.hasContent(i):
			if search.Match(ansi.Strip(p.lines[i]), q) {
				hits = append(hits, findHit{i, 0})
			}
		}
	}
	return hits
}

// findPlace returns the number of hits before the cursor, and if the
// cursor is on a hit.
func (m *model) findPlace(hits []findHit) (before int, on bool) {
	cur := m.cur().head(m.cur().cur)
	for _, h := range hits {
		switch {
		case h.row < cur:
			before++
		case h.row == cur && h.line == 0:
			return before, true
		}
	}
	return before, false
}

// stepFind moves the cursor to the next (d = 1) or the previous (d = -1)
// hit. It wraps around. Forward skips the hit at the cursor. A hit in a
// fold opens the fold.
func (m *model) stepFind(d int) {
	hits := m.findHits(m.find.input.Value())
	if len(hits) == 0 {
		return
	}
	before, on := m.findPlace(hits)
	n := len(hits)
	i := (before + n - 1) % n
	if d > 0 {
		i = before % n
		if on {
			i = (before + 1) % n
		}
	}
	h, p := hits[i], m.cur()
	if h.line == 0 {
		p.cur = h.row
		return
	}
	m.expandFold(p.rows[h.row].Fold)
	cursorToLine(p, diff.SideNew, h.line)
}

// shownHits returns the hits of the query in the find band, or nil if
// the band is closed.
func (m *model) shownHits() []findHit {
	if m.mode != modeFind {
		return nil
	}
	return m.findHits(m.find.input.Value())
}

// findBand renders the find band: the query, and the count at the right.
// The count is "i/n" on a hit, n off a hit, or "no matches". hits are the
// hits of the query.
func (m *model) findBand(hits []findHit) string {
	count := ""
	if m.find.input.Value() != "" {
		before, on := m.findPlace(hits)
		switch {
		case len(hits) == 0:
			count = "no matches"
		case on:
			count = fmt.Sprintf("%d/%d", before+1, len(hits))
		default:
			count = strconv.Itoa(len(hits))
		}
	}
	w := max(m.w-ansi.StringWidth(count)-1, 1)
	m.find.input.SetWidth(max(w-ansi.StringWidth(m.find.input.Prompt)-1, 1))
	band := truncate(m.find.input.View(), w)
	pad := strings.Repeat(" ", max(m.w-ansi.StringWidth(band)-ansi.StringWidth(count), 0))
	return band + pad + dimStyle.Render(count)
}
