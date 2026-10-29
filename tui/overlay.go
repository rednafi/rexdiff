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

// overlayKind says what an overlay is for.
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

// How many rows the page and half-page keys move the cursor in the commit
// picker and the comment list. The page keys also scroll the search
// preview by itemsPage rows.
const (
	itemsPage = 15
	itemsHalf = 8
)

// overlayItem is one row in an overlay's list.
type overlayItem struct {
	label  string
	detail string // dim text shown after the label
	tag    string // a short marker at the end, for example "current"
	// id identifies what choosing the row selects, such as a path, ref,
	// commit or block. In the search, a row without an id cannot have the
	// cursor.
	id       string
	line     int  // search only: the line number of a code match
	isHeader bool // search only: the heading above a file's code matches
	comment  int  // comment list only: the ID of the comment
}

// overlay is a modal box that shows a list to choose from, a search or a
// prompt.
type overlay struct {
	kind       overlayKind
	title      string
	input      textinput.Model
	items      []overlayItem
	shown      []int // the indexes of the items that match the input
	cur        int
	rangeStart int // commit picker only: the item at the start of the range, or -1
	// loading is true while the commit picker waits for its commits, and
	// stop stops the listing of them.
	loading bool
	stop    context.CancelFunc
	agents  []rex.Agent
	search  searchState
	// itemsLo is the index of the first visible row, and itemsHi is one
	// past the last.
	itemsLo, itemsHi int
}

// newOverlay returns a new overlay of the given kind. If the kind has a
// text input, the input has focus.
func newOverlay(kind overlayKind, title string) *overlay {
	ti := textinput.New()
	ti.Prompt = "› "
	ov := &overlay{kind: kind, title: title, input: ti, rangeStart: -1}
	if ov.hasInput() {
		ov.input.Focus()
	}
	return ov
}

// hasInput reports whether the overlay has a text input. The agent,
// comment and commit lists and the quit prompt do not.
func (ov *overlay) hasInput() bool {
	switch ov.kind {
	case overlayAgents, overlayComments, overlayCommits, overlayQuit:
		return false
	}
	return true
}

// setItems replaces the items and shows those that match the input.
func (ov *overlay) setItems(items []overlayItem) {
	ov.items = items
	ov.filter()
}

// filter shows the items that match the input. The search shows all of
// its items, because they are already matches.
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

// curItem returns the item at the cursor, or nil.
func (ov *overlay) curItem() *overlayItem {
	if ov.cur < 0 || ov.cur >= len(ov.shown) {
		return nil
	}
	return &ov.items[ov.shown[ov.cur]]
}

// move moves the list cursor by d rows, skipping rows that cannot have the
// cursor and stopping at the ends.
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

// canHaveCursor reports whether row i of the list can have the cursor. In
// the search, file headings and the "… more" row cannot.
func (ov *overlay) canHaveCursor(i int) bool {
	return ov.kind != overlaySearch || ov.items[ov.shown[i]].id != ""
}

// openOverlay shows ov and sends key presses to it. It ends any drag, so
// that releasing the button does not open the comment box behind the
// overlay.
func (m *model) openOverlay(ov *overlay) {
	m.overlay, m.mode = ov, modeOverlay
	m.drag = dragState{}
}

// closeOverlay closes the overlay and sends key presses to the panes
// again. Closing the search stops the background highlighting of its
// preview.
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

// openInput opens a prompt of the given kind, with value as its initial
// text.
func (m *model) openInput(kind overlayKind, title, value string) {
	ov := newOverlay(kind, title)
	ov.input.SetValue(value)
	m.openOverlay(ov)
}

// openQuit asks the user to confirm quitting, which discards the comments.
func (m *model) openQuit() {
	title := plural(m.comments.Len(), "unsent comment") + " will be lost"
	m.openOverlay(newOverlay(overlayQuit, title))
}

// updateOverlay handles a key press while an overlay is open. In the
// search, it renders the preview of the selected result right away, so
// that the screen is drawn again while its colours arrive.
func (m *model) updateOverlay(msg tea.KeyPressMsg) tea.Cmd {
	cmd := m.updateOverlayKey(msg)
	if ov := m.overlay; ov != nil && ov.kind == overlaySearch {
		m.searchPreview(m.searchInner())
	}
	return cmd
}

// updateOverlayKey does the work of updateOverlay.
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
		// "$" replaces any number, and a digit typed after "$" starts a
		// new number.
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

// isCursorKey reports whether msg matches the cursor binding b. In an
// overlay with a text input, the letter keys of b type text instead.
func (ov *overlay) isCursorKey(msg tea.KeyPressMsg, b key.Binding) bool {
	return key.Matches(msg, b) && (!ov.hasInput() || !slices.Contains(letterMoveKeys, msg.String()))
}

// updateQuit handles a key press in the quit prompt. The QuitDiscard key
// quits. The Quit key does nothing, so that holding q down does not quit.
// The Send and Copy keys close the prompt and send or copy the comments.
// Any other key just closes the prompt.
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

// updateItems handles a key press in an overlay without a text input. In
// the agent picker, the digits 1 to 9 move the cursor to that agent; only
// Enter sends.
func (m *model) updateItems(msg tea.KeyPressMsg) {
	ov, s := m.overlay, msg.String()
	digit := len(s) == 1 && s[0] >= '1' && s[0] <= '9'
	if ov.kind == overlayAgents && digit {
		if i := int(s[0] - '1'); i < len(ov.shown) {
			ov.cur = i
		}
	}
}

// inputChanged updates the overlay after its input text changes.
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

// choose acts on the selected item or on the input text, depending on the
// kind of overlay.
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

// searchInner returns the width of the search box inside its border and
// padding.
func (m *model) searchInner() int { return m.w - 8 }

// overlayBox renders the overlay box and returns its screen position.
// Row i of the list is at line itemsY+i of the box, which mouse clicks use.
func (m *model) overlayBox() (box string, x, y, itemsY int) {
	ov := m.overlay
	w := min(max(m.w*2/3, 50), m.w-4)
	if ov.kind == overlaySearch {
		w = m.searchInner() + 4
	}
	inner := w - 4
	// The title takes one line, which itemsY accounts for.
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

// quitHint returns the key hints for the quit prompt.
func (m *model) quitHint() string {
	k, n := m.keys, m.comments.Len()
	return fmt.Sprintf("%s quit (%d pending) · esc cancel · %s send %d · %s copy",
		firstKey(k.QuitDiscard), n, firstKey(k.Send), n, firstKey(k.Copy))
}

// overlayItems renders at most limit rows of the overlay's list, inner
// columns wide. start is the index of the first rendered row.
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
		// An empty code search query matches nothing, which is not worth
		// reporting.
		if !ov.search.grep || ov.input.Value() != "" {
			rows = append(rows, dimStyle.Render("  no matches"))
		}
	case len(ov.shown) == 0 && ov.kind != overlayGotoLine:
		rows = append(rows, dimStyle.Render("  no matches"))
	}
	return rows, start
}
