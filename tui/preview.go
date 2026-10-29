package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/rednafi/rexdiff/diff"
)

// searchPct is the percentage of the search height used for the results.
// The preview gets the rest.
const searchPct = 50

// preview shows the file of the selected search result.
type preview struct {
	path  string
	line  int // the line of the code match, or 0 for a file name match
	width int
	lines []string   // a single note, or nil
	file  *diff.Diff // the file rendered with line numbers, or nil
	hit   int        // the index of the matching line, or -1
	top   int        // the first visible line
	// center is true until the first draw, which puts the match in the
	// middle.
	center bool
}

// searchLines renders the search, inner columns wide. The first line holds
// the input and the mode chips. Below it come the results, a rule with the
// file name, and the preview of the file. Row i of the list is at line
// itemsY+i of the box.
func (m *model) searchLines(inner int) (lines []string, itemsY int) {
	ov := m.overlay
	lines = []string{ov.searchBand(inner)}
	// avail is the height left for the results and the preview, after the
	// box border, the input line and the preview rule.
	avail := max(m.h-4, 6) - 2
	results := shareOf(avail, searchPct)
	lines = append(lines, paneRule("results", inner))
	rows, start := m.overlayItems(inner, results-1)
	itemsY = len(lines) + 1 - start // + 1 for the top border
	lines = append(lines, rows[:min(len(rows), results-1)]...)
	for len(lines) < results+1 {
		lines = append(lines, "")
	}
	pv := m.searchPreview(inner)
	h := avail - results
	ov.search.previewY = len(lines) + 1 // + 1 for the top border
	if pv == nil {
		lines = append(lines, paneRule("preview", inner), dimStyle.Render("  no preview"))
	} else {
		lines = append(lines, paneRule("preview · "+pv.path, inner))
		lines = append(lines, pv.show(h)...)
	}
	for len(lines) < avail+2 {
		lines = append(lines, "")
	}
	return lines, itemsY
}

// shareOf returns pct percent of n. If n is 6 or more, both the share and
// the rest are at least 3.
func shareOf(n, pct int) int {
	if n < 6 {
		return n / 2
	}
	return min(max(n*pct/100, 3), n-3)
}

// paneRule returns a rule "─ label ───" containing label, w columns wide.
func paneRule(label string, w int) string {
	label = " " + label + " "
	rest := max(w-lipgloss.Width(label)-1, 0)
	return truncate(dimStyle.Render("─")+label+dimStyle.Render(strings.Repeat("─", rest)), w)
}

// searchBand returns the first line of the search, inner columns wide: the
// input, with the mode chips at the right end.
func (ov *overlay) searchBand(inner int) string {
	chips := ov.chips()
	ov.input.SetWidth(max(inner-lipgloss.Width(chips)-4, 1))
	in := ov.input.View()
	pad := max(inner-lipgloss.Width(in)-lipgloss.Width(chips), 1)
	return truncate(in+strings.Repeat(" ", pad)+chips, inner)
}

// searchPreview returns the preview of the selected search result, width
// columns wide, or nil if no result is selected. It reads the file again
// only when the result or the width changes.
func (m *model) searchPreview(width int) *preview {
	s := &m.overlay.search
	it := m.overlay.curItem()
	if it == nil || it.id == "" {
		s.preview = nil
		return nil
	}
	if pv := s.preview; pv != nil && pv.path == it.id && pv.line == it.line && pv.width == width {
		return pv
	}
	pv := &preview{path: it.id, line: it.line, width: width, hit: -1, center: true}
	content, err := m.repo.ReadFile(it.id)
	note := fileNote(err)
	switch {
	case note != "":
		pv.lines = []string{dimStyle.Render("  " + note)}
	case err != nil || content == "":
		pv.lines = []string{dimStyle.Render("  no preview")}
	default:
		o := diff.FileOptions{Width: width - 2, Cache: &m.cache} // - 2 for the match marker
		d := diff.RenderFile(it.id, content, o)
		pv.file = d
		if it.line > 0 {
			pv.hit = slices.IndexFunc(d.Rows, func(r diff.Row) bool {
				return r.New == it.line && !r.Continued
			})
		}
	}
	s.preview = pv
	return pv
}

// show returns the h visible lines of pv, with a marker on the matching
// line. On the first draw, the match is centred.
func (pv *preview) show(h int) []string {
	if pv.center {
		pv.top, pv.center = max(pv.hit-h/2, 0), false
	}
	n := len(pv.lines)
	if pv.file != nil {
		n = len(pv.file.Rows)
	}
	pv.top = clampTop(pv.top, n, h)
	var out []string
	for i := pv.top; i < n && len(out) < h; i++ {
		mark := "  "
		if i == pv.hit {
			mark = accentStyle.Render("▶ ")
		}
		if pv.file != nil {
			out = append(out, mark+pv.file.Line(i))
		} else {
			out = append(out, mark+pv.lines[i])
		}
	}
	return out
}

// scroll scrolls the search preview by d lines.
func (pv *preview) scroll(d int) {
	pv.center = false
	pv.top = max(pv.top+d, 0)
}
