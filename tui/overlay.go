package tui

import (
	"context"
	"fmt"
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

// overlayKind tells what an overlay is for.
type overlayKind int

const (
	overlayAgents overlayKind = iota
	overlayBase
	overlayCommits
	overlayComments
	overlaySearch
	overlayGoto
	overlayQuit
)

// These are the steps of the page keys in the commit picker, in rows, as
// in herdr-reviewr.
const (
	pickerPage = 15
	pickerHalf = 8
)

// pickItem is one row of the list of an overlay.
type pickItem struct {
	label  string
	detail string // the dim text after the label
	tag    string // a short mark at the end, for example "current"
	// id is what the row picks, for example a path, a ref, a commit, or a
	// block. In the search, a row with no id cannot have the cursor.
	id      string
	line    int  // the search: the line of a code match
	head    bool // the search: the header of the code matches of a file
	comment int  // the comment list: the ID of the comment
}

// overlay is a modal box. It shows a list to pick from, a search, or a
// prompt.
type overlay struct {
	kind   overlayKind
	title  string
	input  textinput.Model
	items  []pickItem
	shown  []int // the indices of the items that match the input
	cur    int
	mark   int // the commit picker: the item at the start of the range, or -1
	agents []rex.Agent
	search searchState
	// rowsFrom and rowsTo are the index of the first row that shows and
	// the index after the last one.
	rowsFrom, rowsTo int
}

// newOverlay returns an overlay. If its kind has an input, the input has
// the focus.
func newOverlay(kind overlayKind, title string) *overlay {
	ti := textinput.New()
	ti.Prompt = "› "
	ov := &overlay{kind: kind, title: title, input: ti, mark: -1}
	if ov.hasInput() {
		ov.input.Focus()
	}
	return ov
}

// hasInput reports whether the overlay has a text input. The lists of
// agents, comments, and commits, and the prompt before a quit, have none.
func (o *overlay) hasInput() bool {
	switch o.kind {
	case overlayAgents, overlayComments, overlayCommits, overlayQuit:
		return false
	}
	return true
}

// setItems sets the items, and shows the ones that match the input.
func (o *overlay) setItems(items []pickItem) {
	o.items = items
	o.filter()
}

// filter shows the items that match the input. The search shows all its
// items, because they match already.
func (o *overlay) filter() {
	q := o.input.Value()
	o.shown = o.shown[:0]
	if q == "" || o.kind == overlaySearch {
		for i := range o.items {
			o.shown = append(o.shown, i)
		}
	} else {
		labels := make([]string, len(o.items))
		for i, it := range o.items {
			labels[i] = it.label + " " + it.detail
		}
		o.shown = append(o.shown, search.Indices(q, labels)...)
	}
	o.cur = min(o.cur, max(len(o.shown)-1, 0))
}

// selected returns the item at the cursor, or nil.
func (o *overlay) selected() *pickItem {
	if o.cur < 0 || o.cur >= len(o.shown) {
		return nil
	}
	return &o.items[o.shown[o.cur]]
}

// move moves the cursor of the list by d rows. It skips the rows that
// cannot have the cursor, and it stops at the ends.
func (o *overlay) move(d int) {
	step := 1
	if d < 0 {
		step = -1
	}
	for ; d != 0; d -= step {
		i := o.cur + step
		for i >= 0 && i < len(o.shown) && !o.pickable(i) {
			i += step
		}
		if i < 0 || i >= len(o.shown) {
			return
		}
		o.cur = i
	}
}

// pickable reports whether row i of the list can have the cursor. In the
// search, the headers of the files and the "… more" row cannot.
func (o *overlay) pickable(i int) bool {
	return o.kind != overlaySearch || o.items[o.shown[i]].id != ""
}

// open shows ov and gives the keys to it.
func (m *model) open(ov *overlay) {
	m.overlay, m.mode = ov, modeOverlay
}

// closeOverlay closes the overlay and gives the keys back to the panes.
func (m *model) closeOverlay() {
	m.overlay, m.mode = nil, modeNormal
	m.stopGrep(fmt.Errorf("code search: the search closes: %w", context.Canceled))
}

// openInput opens a prompt of kind with the text value.
func (m *model) openInput(kind overlayKind, title, value string) {
	ov := newOverlay(kind, title)
	ov.input.SetValue(value)
	m.open(ov)
}

// openConfirmQuit asks the user to confirm a quit that loses comments.
func (m *model) openConfirmQuit() {
	title := plural(m.comments.Len(), "unsent comment") + " will be lost"
	m.open(newOverlay(overlayQuit, title))
}

// updateOverlay handles a key while an overlay is open.
func (m *model) updateOverlay(msg tea.KeyPressMsg) tea.Cmd {
	ov, k := m.overlay, overlayKeys
	switch ov.kind {
	case overlayQuit:
		return m.updateQuit(msg)
	case overlayComments:
		return m.updateCommentList(msg)
	case overlayCommits:
		return m.updateCommitPicker(msg)
	case overlaySearch:
		pv := ov.search.preview
		switch {
		case key.Matches(msg, k.Flip):
			ov.search.code = !ov.search.code
			ov.showSearch()
			return nil
		case key.Matches(msg, k.PreviewDown) && pv != nil:
			pv.scroll(pickerPage)
			return nil
		case key.Matches(msg, k.PreviewUp) && pv != nil:
			pv.scroll(-pickerPage)
			return nil
		}
	}
	switch {
	case key.Matches(msg, k.Close):
		m.closeOverlay()
		return nil
	case key.Matches(msg, k.Choose):
		return m.chooseOverlay()
	case key.Matches(msg, k.Up):
		ov.move(-1)
		return nil
	case key.Matches(msg, k.Down):
		ov.move(1)
		return nil
	}
	if !ov.hasInput() {
		return m.updateList(msg)
	}
	if ov.kind == overlayGoto {
		if !isLineKey(msg) {
			return nil
		}
		// "$" stands alone, and a digit after "$" starts a number.
		if msg.Text == "$" || (msg.Text != "" && ov.input.Value() == "$") {
			ov.input.SetValue("")
		}
	}
	var cmd tea.Cmd
	before := ov.input.Value()
	ov.input, cmd = ov.input.Update(msg)
	if ov.input.Value() != before {
		return tea.Batch(cmd, m.inputChanged())
	}
	return cmd
}

// updateQuit handles a key in the prompt before a quit, as herdr-reviewr
// does. The key of QuitDiscard quits. The key of Quit does nothing, so a
// held q does not quit. The keys of Send and Copy close the prompt and
// send or copy. Any other key closes the prompt.
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

// updateList handles a key in an overlay that has no input. In the agent
// picker, the digits 1 to 9 pick an agent.
func (m *model) updateList(msg tea.KeyPressMsg) tea.Cmd {
	ov, k, s := m.overlay, overlayKeys, msg.String()
	digit := len(s) == 1 && s[0] >= '1' && s[0] <= '9'
	switch {
	case key.Matches(msg, k.ListDown):
		ov.move(1)
	case key.Matches(msg, k.ListUp):
		ov.move(-1)
	case ov.kind == overlayAgents && digit:
		if i := int(s[0] - '1'); i < len(ov.shown) {
			ov.cur = i
			return m.chooseOverlay()
		}
	}
	return nil
}

// inputChanged updates the overlay after a change of its input.
func (m *model) inputChanged() tea.Cmd {
	ov := m.overlay
	switch ov.kind {
	case overlaySearch:
		return m.runSearch()
	case overlayBase:
		ov.filter()
	}
	return nil
}

// chooseOverlay acts on the selected item or on the input.
func (m *model) chooseOverlay() tea.Cmd {
	ov := m.overlay
	it := ov.selected()
	switch ov.kind {
	case overlayGoto:
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
		if err := m.repo.SaveBase(m.ctx, ref); err != nil {
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
		m.from, m.to = ov.commitRange(it)
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
			return m.deliver(a)
		}
	}
	m.ensureVisible()
	return nil
}

// overlayBox renders the box of the overlay, and returns its position.
// Row i of the list shows at line listY+i of the box, for mouse hits.
func (m *model) overlayBox() (box string, x, y, listY int) {
	ov := m.overlay
	w := min(max(m.w*2/3, 50), m.w-4)
	if ov.kind == overlaySearch {
		w = m.w - 4
	}
	inner := w - 4
	// The title takes one line, for listY.
	lines := []string{truncate(headerStyle.Render(ov.title), inner)}
	switch ov.kind {
	case overlayQuit:
		lines = append(lines, "", dimStyle.Render(m.quitHint()))
	case overlaySearch:
		lines, listY = m.searchLines(inner)
	default:
		if ov.hasInput() {
			ov.input.SetWidth(inner - 2)
			lines = append(lines, ov.input.View())
		}
		rows, start := m.overlayRows(inner, m.h-10)
		listY = len(lines) + 1 - start // 1 for the border
		lines = append(lines, rows...)
		switch ov.kind {
		case overlayComments:
			lines = append(lines, "", truncate(dimStyle.Render(m.listHint()), inner))
		case overlayCommits:
			lines = append(lines, "", truncate(dimStyle.Render(m.commitHint()), inner))
		}
	}
	box = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(dimColor).
		Padding(0, 1).
		Width(w).
		Render(strings.Join(lines, "\n"))
	x = (m.w - lipgloss.Width(box)) / 2
	y = max((m.h-lipgloss.Height(box))/3, 1)
	return box, x, y, listY
}

// quitHint returns the keys of the prompt before a quit, as the footer
// of herdr-reviewr shows them.
func (m *model) quitHint() string {
	k, n := m.keys, m.comments.Len()
	return fmt.Sprintf("%s quit (%d pending) · esc cancel · %s send %d · %s copy",
		firstKey(k.QuitDiscard), n, firstKey(k.Send), n, firstKey(k.Copy))
}

// overlayRows renders at most limit rows of the list of the overlay,
// inner columns wide. start is the index of the first row.
func (m *model) overlayRows(inner, limit int) (rows []string, start int) {
	ov := m.overlay
	maxRows := min(len(ov.shown), max(limit, 1))
	start = max(0, ov.cur-maxRows+1)
	ov.rowsFrom, ov.rowsTo = start, start+maxRows
	for i := start; i < len(ov.shown) && i < start+maxRows; i++ {
		it := ov.items[ov.shown[i]]
		if !ov.pickable(i) {
			style := dimStyle
			if it.head {
				style = headerStyle
			}
			rows = append(rows, "  "+truncate(style.Render(termtext.Quote(it.label)), inner-2))
			continue
		}
		tag := ""
		if it.tag != "" {
			tag = "  " + cursorStyle.Render(it.tag)
		}
		if ov.kind == overlayCommits && ov.mark == ov.shown[i] {
			tag += "  " + cursorStyle.Render("range start")
		}
		text := termtext.Quote(it.label) + "  " + dimStyle.Render(termtext.Quote(it.detail)) + tag
		row := truncate(text, inner-2)
		if i == ov.cur {
			row = m.selStyle().Width(inner).Render("▶ " + row)
		} else {
			row = "  " + row
		}
		rows = append(rows, row)
	}
	switch {
	case ov.search.err != "":
		rows = append(rows, delStyle.Render("  "+termtext.Quote(ov.search.err)))
	case len(ov.shown) == 0 && ov.kind == overlayCommits:
		rows = append(rows, dimStyle.Render("  no commits yet"))
	case len(ov.shown) == 0 && ov.kind == overlaySearch:
		// As in herdr-reviewr, an empty query finds no code, and that is
		// not a miss.
		if !ov.search.code || ov.input.Value() != "" {
			rows = append(rows, dimStyle.Render("  no matches"))
		}
	case len(ov.shown) == 0 && ov.kind != overlayGoto:
		rows = append(rows, dimStyle.Render("  no matches"))
	}
	return rows, start
}
