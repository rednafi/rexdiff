package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/search"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/rex"
)

type overlayKind int

const (
	overlayAgents overlayKind = iota
	overlayBase
	overlayCommits
	overlayComments
	overlaySearch
	overlayGotoLine
	overlayQuit
)

const (
	itemsPage = 15
	itemsHalf = 8
)

type overlayItem struct {
	label  string
	detail string
	tag    string
	// In the search, a row without an id cannot have the cursor.
	id       string
	line     int  // search only: the line number of a code match
	isHeader bool // search only: the heading above a file's code matches
	comment  int  // comment list only: the ID of the comment
}

type overlay struct {
	kind       overlayKind
	title      string
	input      textinput.Model
	items      []overlayItem
	shown      []int // indexes into items
	cur        int
	rangeStart int // commit picker only: the item at the start of the range, or -1
	loading    bool
	stop       context.CancelFunc
	agents     []rex.Agent
	search     searchState
	// itemsHi is one past the last visible row.
	itemsLo, itemsHi int
}

func newOverlay(kind overlayKind, title string) *overlay {
	ti := textinput.New()
	ti.Prompt = "› "
	ov := &overlay{kind: kind, title: title, input: ti, rangeStart: -1}
	if ov.hasInput() {
		ov.input.Focus()
	}
	return ov
}

func (ov *overlay) hasInput() bool {
	switch ov.kind {
	case overlayAgents, overlayComments, overlayCommits, overlayQuit:
		return false
	}
	return true
}

func (ov *overlay) setItems(items []overlayItem) {
	ov.items = items
	ov.filter()
}

// filter shows all items in the search, because they are already matches.
func (ov *overlay) filter() {
	q := ov.input.Value()
	ov.shown = ov.shown[:0]
	if q == "" || ov.kind == overlaySearch {
		for i := range ov.items {
			ov.shown = append(ov.shown, i)
		}
	} else {
		labels := make([]string, len(ov.items))
		for i, it := range ov.items {
			labels[i] = it.label + " " + it.detail
		}
		ov.shown = append(ov.shown, search.Indices(q, labels)...)
	}
	ov.cur = min(ov.cur, max(len(ov.shown)-1, 0))
}

func (ov *overlay) curItem() *overlayItem {
	if ov.cur < 0 || ov.cur >= len(ov.shown) {
		return nil
	}
	return &ov.items[ov.shown[ov.cur]]
}

func (ov *overlay) move(d int) {
	step := 1
	if d < 0 {
		step = -1
	}
	for ; d != 0; d -= step {
		i := ov.cur + step
		for i >= 0 && i < len(ov.shown) && !ov.canHaveCursor(i) {
			i += step
		}
		if i < 0 || i >= len(ov.shown) {
			return
		}
		ov.cur = i
	}
}

func (ov *overlay) canHaveCursor(i int) bool {
	return ov.kind != overlaySearch || ov.items[ov.shown[i]].id != ""
}

// openOverlay ends any drag. Then the button release does not open the
// comment box behind the overlay.
func (m *model) openOverlay(ov *overlay) {
	m.overlay, m.mode = ov, modeOverlay
	m.drag = dragState{}
}

func (m *model) closeOverlay() {
	if ov := m.overlay; ov != nil && ov.stop != nil {
		ov.stop()
	}
	if ov := m.overlay; ov != nil && ov.kind == overlaySearch && ov.search.preview != nil && ov.search.preview.file != nil {
		// The preview was the last file rendered, so the cache holds its
		// highlighter. A pane that shows the same file starts it again
		// for the lines that it draws.
		m.cache.StopHighlighting()
	}
	m.overlay, m.mode = nil, modeNormal
	m.stopGrep()
}

func (m *model) openInput(kind overlayKind, title, value string) {
	ov := newOverlay(kind, title)
	ov.input.SetValue(value)
	m.openOverlay(ov)
}

func (m *model) openQuit() {
	title := plural(m.comments.Len(), "unsent comment") + " will be lost"
	m.openOverlay(newOverlay(overlayQuit, title))
}

// updateOverlay renders the search preview at once. Then the screen draws
// again while its colours arrive.
func (m *model) updateOverlay(msg tea.KeyPressMsg) tea.Cmd {
	cmd := m.updateOverlayKey(msg)
	if ov := m.overlay; ov != nil && ov.kind == overlaySearch {
		m.searchPreview(m.searchInner())
	}
	return cmd
}

func (m *model) updateOverlayKey(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, overlayKeys
	switch ov.kind {
	case overlayQuit:
		return m.updateQuit(msg)
	case overlayComments:
		return m.updateComments(msg)
	case overlayCommits:
		return m.updateCommitPicker(msg)
	case overlaySearch:
		pv := ov.search.preview
		switch {
		case key.Matches(msg, k.ToggleSearchMode):
			ov.search.grep = !ov.search.grep
			ov.setSearchItems()
			return nil
		case key.Matches(msg, k.ScrollPreviewDown) && pv != nil:
			pv.scroll(itemsPage)
			return nil
		case key.Matches(msg, k.ScrollPreviewUp) && pv != nil:
			pv.scroll(-itemsPage)
			return nil
		}
	}
	switch {
	case key.Matches(msg, k.Close):
		m.closeOverlay()
		return nil
	case key.Matches(msg, k.Choose):
		return m.choose()
	case ov.isCursorKey(msg, k.Up):
		ov.move(-1)
		return nil
	case ov.isCursorKey(msg, k.Down):
		ov.move(1)
		return nil
	}
	if !ov.hasInput() {
		m.updateItems(msg)
		return nil
	}
	if ov.kind == overlayGotoLine {
		if !isLineKey(msg) {
			return nil
		}
		if msg.Text == "$" || (msg.Text != "" && ov.input.Value() == "$") {
			ov.input.SetValue("")
		}
	}
	var cmd tea.Cmd
	prev := ov.input.Value()
	ov.input, cmd = ov.input.Update(msg)
	if ov.input.Value() != prev {
		return tea.Batch(cmd, m.inputChanged())
	}
	return cmd
}

// isCursorKey ignores the letter keys of b in an overlay with a text
// input. These keys type text.
func (ov *overlay) isCursorKey(msg tea.KeyPressMsg, b key.Binding) bool {
	return key.Matches(msg, b) && (!ov.hasInput() || !slices.Contains(letterMoveKeys, msg.String()))
}

// updateQuit ignores the Quit key, so that holding q down does not quit.
// Only QuitDiscard quits.
func (m *model) updateQuit(msg tea.KeyPressMsg) tea.Cmd {
	k := m.keys
	switch {
	case key.Matches(msg, k.QuitDiscard):
		return tea.Quit
	case key.Matches(msg, k.Quit):
		return nil
	}
	m.closeOverlay()
	switch {
	case key.Matches(msg, k.Send):
		return m.startSend()
	case key.Matches(msg, k.Copy):
		m.copyComments()
	}
	return nil
}

func (m *model) updateItems(msg tea.KeyPressMsg) {
	ov, s := m.overlay, msg.String()
	digit := len(s) == 1 && s[0] >= '1' && s[0] <= '9'
	if ov.kind == overlayAgents && digit {
		if i := int(s[0] - '1'); i < len(ov.shown) {
			ov.cur = i
		}
	}
}

func (m *model) inputChanged() tea.Cmd {
	ov := m.overlay
	switch ov.kind {
	case overlaySearch:
		return m.startGrep()
	case overlayBase:
		ov.filter()
	}
	return nil
}

func (m *model) choose() tea.Cmd {
	ov := m.overlay
	it := ov.curItem()
	switch ov.kind {
	case overlayGotoLine:
		m.closeOverlay()
		m.gotoLine(ov.input.Value())
	case overlaySearch:
		m.closeOverlay()
		if it != nil && it.id != "" {
			m.addRecent(it.id)
			m.jumpTo(it.id, diff.SideNew, max(it.line, 1))
		}
	case overlayBase:
		ref, ok := m.baseRef(it)
		m.closeOverlay()
		if !ok {
			m.flash("no such ref: " + ref)
			return nil
		}
		ctx, done := m.gitContext("base save")
		err := m.repo.SaveBase(ctx, ref)
		done()
		if err != nil {
			m.flash("base save failed: " + err.Error())
			return nil
		}
		m.base = ref
		m.setScope(ScopeBranch)
	case overlayCommits:
		if it == nil {
			return nil
		}
		m.closeOverlay()
		m.oldest, m.newest = ov.commitRange(it)
		m.setScope(scopeCommits)
	case overlayComments:
		m.closeOverlay()
		if it == nil {
			break
		}
		if c := m.comments.Find(it.comment); c != nil {
			m.jumpToComment(c)
		}
	case overlayAgents:
		m.closeOverlay()
		if a, ok := ov.agent(it); ok {
			return m.pasteReview(a)
		}
	}
	m.scrollToCursor()
	return nil
}

func (m *model) searchInner() int { return m.w - 8 }

// overlayBox puts row i of the list at line itemsY+i of the box. Mouse
// clicks use this.
func (m *model) overlayBox() (box string, x, y, itemsY int) {
	ov := m.overlay
	w := min(max(m.w*2/3, 50), m.w-4)
	if ov.kind == overlaySearch {
		w = m.searchInner() + 4
	}
	inner := w - 4
	lines := []string{truncate(headerStyle.Render(ov.title), inner)}
	switch ov.kind {
	case overlayQuit:
		lines = append(lines, "", dimStyle.Render(m.quitHint()))
	case overlaySearch:
		lines, itemsY = m.searchLines(inner)
	default:
		if ov.hasInput() {
			ov.input.SetWidth(inner - 2)
			lines = append(lines, ov.input.View())
		}
		rows, start := m.overlayItems(inner, m.h-10)
		itemsY = len(lines) + 1 - start // + 1 for the top border
		lines = append(lines, rows...)
		switch ov.kind {
		case overlayComments:
			lines = append(lines, "", truncate(dimStyle.Render(m.commentsHint()), inner))
		case overlayCommits:
			lines = append(lines, "", truncate(dimStyle.Render(m.commitHint()), inner))
		}
	}
	// On a short terminal, cut the content so that the whole box, with its
	// borders, stays on the screen.
	lines = strings.Split(lipgloss.NewStyle().Width(inner).Render(strings.Join(lines, "\n")), "\n")
	lines = lines[:min(len(lines), max(m.h-2, 1))]
	box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(lines, "\n"))
	x = (m.w - lipgloss.Width(box)) / 2
	// The box leaves the header line free if there is room.
	free := m.h - lipgloss.Height(box)
	y = max(free/3, min(free, 1), 0)
	return box, x, y, itemsY
}

func (m *model) quitHint() string {
	k, n := m.keys, m.comments.Len()
	return fmt.Sprintf("%s quit (%d pending) · esc cancel · %s send %d · %s copy",
		firstKey(k.QuitDiscard), n, firstKey(k.Send), n, firstKey(k.Copy))
}

func (m *model) overlayItems(inner, limit int) (rows []string, start int) {
	ov := m.overlay
	maxRows := min(len(ov.shown), max(limit, 1))
	start = max(0, ov.cur-maxRows+1)
	ov.itemsLo, ov.itemsHi = start, start+maxRows
	for i := start; i < len(ov.shown) && i < start+maxRows; i++ {
		it := ov.items[ov.shown[i]]
		if !ov.canHaveCursor(i) {
			style := dimStyle
			if it.isHeader {
				style = headerStyle
			}
			rows = append(rows, "  "+truncate(style.Render(termtext.Quote(it.label)), inner-2))
			continue
		}
		tag := ""
		if it.tag != "" {
			tag = "  " + accentStyle.Render(it.tag)
		}
		if ov.kind == overlayCommits && ov.rangeStart == ov.shown[i] {
			tag += "  " + accentStyle.Render("range start")
		}
		text := termtext.Quote(it.label) + "  " + dimStyle.Render(termtext.Quote(it.detail)) + tag
		row := truncate(text, inner-2)
		if i == ov.cur {
			row = m.highlightStyle().Width(inner).Render("▶ " + row)
		} else {
			row = "  " + row
		}
		rows = append(rows, row)
	}
	switch {
	case ov.search.err != "":
		rows = append(rows, deletedStyle.Render("  "+termtext.Quote(ov.search.err)))
	case ov.loading:
		rows = append(rows, dimStyle.Render("  loading commits…"))
	case len(ov.shown) == 0 && ov.kind == overlayCommits:
		rows = append(rows, dimStyle.Render("  no commits yet"))
	case len(ov.shown) == 0 && ov.kind == overlaySearch:
		// Do not report that an empty code search matches nothing.
		if !ov.search.grep || ov.input.Value() != "" {
			rows = append(rows, dimStyle.Render("  no matches"))
		}
	case len(ov.shown) == 0 && ov.kind != overlayGotoLine:
		rows = append(rows, dimStyle.Render("  no matches"))
	}
	return rows, start
}
