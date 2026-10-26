package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/rednafi/rexdiff/diff"
)

// searchShare is the percent of the space of the search that the results
// take. The preview takes the rest. The value comes from herdr-reviewr.
const searchShare = 50

// preview is the file of the selected result of the search.
type preview struct {
	path  string
	line  int // the line of the code match, or 0 for a file
	width int
	lines []string // the file with line numbers, or one note
	hit   int      // the index in lines of the line of the match, or -1
	top   int      // the first line that shows
	// center is true until the first draw. That draw puts the match in
	// the middle.
	center bool
}

// searchLines renders the search, inner columns wide, as herdr-reviewr
// lays it out. The first line has the input and the chips. Below it are
// the results, a rule with the name of the file, and the preview of the
// file. Row i of the list shows at line listY+i of the box.
func (m *model) searchLines(inner int) (lines []string, listY int) {
	ov := m.overlay
	lines = []string{ov.searchBand(inner)}
	// avail is the height of the results and the preview. It leaves out
	// the box, the band, and the rule of the preview.
	avail := max(m.h-4, 6) - 2
	results := splitAxis(avail, searchShare)
	lines = append(lines, paneRule("results", inner))
	rows, start := m.overlayRows(inner, results-1)
	listY = len(lines) + 1 - start // 1 for the border
	lines = append(lines, rows[:min(len(rows), results-1)]...)
	for len(lines) < results+1 {
		lines = append(lines, "")
	}
	pv := m.searchPreview(inner)
	h := avail - results
	if pv == nil {
		lines = append(lines, paneRule("preview", inner), dimStyle.Render("  no preview"))
	} else {
		lines = append(lines, paneRule("preview · "+pv.path, inner))
		lines = append(lines, pv.show(h)...)
	}
	for len(lines) < avail+2 {
		lines = append(lines, "")
	}
	return lines, listY
}

// splitAxis returns the part of n that pct percent takes, as
// herdr-reviewr splits a pane. Each part gets at least 3 if n has room.
func splitAxis(n, pct int) int {
	if n < 6 {
		return n / 2
	}
	return min(max(n*pct/100, 3), n-3)
}

// paneRule returns a rule "─ label ───" with label, w columns wide.
func paneRule(label string, w int) string {
	label = " " + label + " "
	rest := max(w-lipgloss.Width(label)-1, 0)
	return truncate(dimStyle.Render("─")+label+dimStyle.Render(strings.Repeat("─", rest)), w)
}

// searchBand returns the first line of the search, inner columns wide.
// It has the input, and the chips at the right end.
func (o *overlay) searchBand(inner int) string {
	chips := o.chips()
	o.input.SetWidth(max(inner-lipgloss.Width(chips)-4, 1))
	in := o.input.View()
	pad := max(inner-lipgloss.Width(in)-lipgloss.Width(chips), 1)
	return truncate(in+strings.Repeat(" ", pad)+chips, inner)
}

// searchPreview returns the preview of the selected result of the search,
// width columns wide, or nil if no result is selected. It reads the file
// again only for another result or another width.
func (m *model) searchPreview(width int) *preview {
	s := &m.overlay.search
	it := m.overlay.selected()
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
		o := m.diffOptions(0)
		o.Width, o.Wrap = width-2, false // 2 for the mark of the match
		d := diff.RenderFile(it.id, content, o)
		pv.lines = d.Lines
		if it.line > 0 {
			pv.hit = slices.IndexFunc(d.Rows, func(r diff.Row) bool {
				return r.New == it.line && !r.Continued
			})
		}
	}
	s.preview = pv
	return pv
}

// show returns the h lines of pv that show. The line of the match has a
// mark. At the first draw, the match is in the middle.
func (pv *preview) show(h int) []string {
	if pv.center {
		pv.top, pv.center = max(pv.hit-h/2, 0), false
	}
	pv.top = max(min(pv.top, len(pv.lines)-h), 0)
	var out []string
	for i := pv.top; i < len(pv.lines) && len(out) < h; i++ {
		mark := "  "
		if i == pv.hit {
			mark = cursorStyle.Render("▶ ")
		}
		out = append(out, mark+pv.lines[i])
	}
	return out
}

// scroll moves the preview of the search by d lines.
func (pv *preview) scroll(d int) {
	pv.center = false
	pv.top = max(pv.top+d, 0)
}
