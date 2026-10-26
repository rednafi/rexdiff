package tui

import (
	"cmp"
	"context"
	"image/color"
	"log/slog"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/update"
	"github.com/rednafi/rexdiff/vcs"
)

type tab int

const (
	tabChanges tab = iota
	tabFiles
	tabPR
)

// scopeCommits compares the commits that the user picked. It is not a
// value of Config.DefaultScope.
const scopeCommits Scope = "commits"

// scopes are the scopes in the order of the header.
var scopes = [...]Scope{ScopeUncommitted, ScopeBranch, ScopeLastTurn, scopeCommits}

// label returns the name of the scope in the header.
func (s Scope) label() string { return strings.ReplaceAll(string(s), "-", " ") }

// mode tells which part of the screen takes the keys.
type mode int

const (
	modeNormal mode = iota
	modeComment
	modeOverlay
)

// pane is one view that scrolls and takes comments: the rendered lines and
// the source lines that they show.
type pane struct {
	lines  []string
	rows   []diff.Row // rows[i] is the source line that lines[i] shows
	diff   *diff.Diff // the render, for mouse hits; nil for markdown and the PR
	path   string     // the file of the rows; "" if the pane takes no comments
	cur    int        // the row of the cursor
	top    int        // the first row on the screen
	anchor int        // the start of the visual selection, or -1
	xoff   int        // the sideways scroll, in columns
	note   string     // shown instead of the lines, if there are no lines
	// free is true after the wheel moves the view. Then the cursor can
	// be off the screen until the next key or click.
	free bool
}

// set shows lines that show rows. It keeps the cursor, the top, and the
// selection in the new lines.
func (p *pane) set(lines []string, rows []diff.Row) {
	p.lines, p.rows, p.diff = lines, rows, nil
	p.cur = min(p.cur, max(len(lines)-1, 0))
	p.top = min(p.top, max(len(lines)-1, 0))
	if p.anchor >= len(rows) { // the file is shorter than before
		p.anchor = -1
	}
}

// cont tells if line i continues the source line of the line above.
func (p *pane) cont(i int) bool { return i > 0 && i < len(p.rows) && p.rows[i].Continued }

// head returns the first line of the source line that line i shows.
func (p *pane) head(i int) int {
	for p.cont(i) {
		i--
	}
	return i
}

// end returns the last line of the source line that line i shows.
func (p *pane) end(i int) int {
	for p.cont(i + 1) {
		i++
	}
	return i
}

// toStart moves the cursor and the view to the first row. It clears the
// selection and the sideways scroll.
func (p *pane) toStart() { p.cur, p.top, p.anchor, p.xoff = 0, 0, -1, 0 }

// setDiff shows the lines of d.
func (p *pane) setDiff(d *diff.Diff) {
	p.set(d.Lines, d.Rows)
	p.diff = d
}

// side tells which side column x of the screen is on, and if x is on a line
// number.
func (p *pane) side(x int) (s diff.Side, onNumber bool) {
	if p.diff == nil {
		return diff.SideNew, false
	}
	return p.diff.SideAt(x)
}

// changed is a changed file with its line counts.
type changed struct {
	vcs.File
	adds, dels int
}

// turnState is the latest turn of an agent: a snapshot of the working tree
// when the agent started work.
type turnState struct {
	block   string
	app     string // the name of the agent, for example "pi"
	tree    string // the snapshot at the start of the turn
	running bool
	at      time.Time // the start of the turn
}

// findState is the result of a find in the current pane.
type findState struct {
	query string
	hits  []int // the first rows of the lines that match, in order
	i     int   // the hit at the cursor
}

// active tells if the find has hits, so that n and N step through them.
func (f *findState) active() bool { return len(f.hits) > 0 }

// dragState is a drag of the left mouse button in the main pane.
type dragState struct {
	active bool      // the button is down
	moved  bool      // the cursor moved off the start row
	gutter bool      // the drag started on the line numbers
	start  int       // the row where the drag started
	pref   diff.Side // the side of the line numbers or the code at the start
}

// draft is the comment that the user writes: a new one, or an edit of the
// comment with ID editID.
type draft struct {
	diff.Comment
	editID int
}

// prState is the content of the PR tab.
type prState struct {
	loading bool
	pr      *vcs.PullRequest
	err     string
}

// agentState is what the program knows about the agent in a block.
type agentState struct {
	state  rex.State // "" after the block closed
	cwd    string    // the working directory; "" if unknown
	hasCwd bool      // the program asked Rex for cwd
}

// model is the state of the program. It implements tea.Model.
type model struct {
	// ctx comes from Run. The methods of tea.Model have fixed signatures,
	// so the model keeps ctx for its git and Rex calls.
	ctx  context.Context
	repo *vcs.Repo
	rex  *rex.Client // nil outside Rex
	log  *slog.Logger

	cfg    config
	keys   keyMap
	notice string // a problem that the footer shows, or ""

	updater      Updater         // nil if rexdiff cannot update itself
	release      *update.Release // a newer release, or nil
	phase        updatePhase
	restartState string // the file of the state for the restart, or ""

	w, h    int
	bg, fg  color.Color
	red     color.Color // the ANSI red of the terminal, or nil
	green   color.Color // the ANSI green of the terminal, or nil
	dark    bool
	focused bool
	ticks   int

	tab   tab
	scope Scope
	// folds has the folds that the user expanded in the Changes tab, by
	// path and by the new line number of their first line.
	folds map[string]map[int]bool
	base  string
	from  string // the commits scope: the oldest commit that the user picked
	to    string // the commits scope: the newest commit that the user picked
	turn  *turnState

	files      []changed // the Changes tab
	changeSel  int
	shownTrees string      // the two trees of files, to notice a change
	allFiles   []vcs.Entry // the Files tab
	allSel     int
	viewed     string // the content in the Files tab, to notice edits

	changes, viewer, prPane pane
	pr                      prState
	prPlace                 *savedPane // the place of prPane after a restart

	split      bool // show the diff side by side
	wrap       bool
	rendered   bool // show markdown rendered
	listLeft   bool
	listHidden bool
	focusList  bool // the keys j and k move in the file list
	listW      int

	comments diff.Comments
	editor   textarea.Model
	draft    *draft
	mode     mode
	overlay  *overlay
	find     findState
	drag     dragState
	help     help.Model

	status     string
	statusTill time.Time

	agents   map[string]*agentState // by block
	sends    int                    // the steps of sends that run now
	lastSent string                 // the block of the last send
	beside   string                 // the agent block that the pane opened beside
}

// newModel returns the model of the program with the options o and the
// settings cfg.
func newModel(ctx context.Context, o Options, cfg config) model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys(defaultKeys().Newline.Keys()...)

	m := model{
		ctx:     ctx,
		repo:    o.Repo,
		rex:     o.Rex,
		log:     o.Log,
		notice:  o.Notice,
		updater: o.Updater,
		w:       120,
		h:       40,
		focused: true,
		base:    o.Base,
		wrap:    o.Wrap,
		editor:  ta,
		help:    newHelp(),
		agents:  map[string]*agentState{},
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.base == "" {
		m.base = o.Repo.DefaultBase(ctx)
	}
	if o.Rex != nil && o.Agent != "" {
		// If Rex does not know the agent, the picker selects no agent
		// first. The picker still lists all the agents.
		m.beside, _ = o.Rex.Resolve(ctx, o.Agent)
	}
	m.changes.anchor, m.viewer.anchor, m.prPane.anchor = -1, -1, -1
	m.cfg = cfg
	m.keys = defaultKeys()
	m.keys.applyConfig(cfg.Keybindings)
	m.listLeft = cfg.FileList == positionLeft
	m.listW = cfg.FileListWidth
	m.rendered = cfg.MarkdownView == markdownViewRendered
	// The flags win over the settings.
	m.split = cmp.Or(o.Layout, cfg.Layout) == LayoutSplit
	m.scope = cmp.Or(o.Scope, cfg.DefaultScope)
	return m
}

// Messages.
type (
	tickMsg   struct{}
	agentsMsg struct {
		agents []rex.Agent
		err    error
		send   bool // true if the list is for a send, not to learn the states
	}
	sentMsg struct {
		agent rex.Agent
		ids   []int // the IDs of the sent comments
		err   error
	}
	grepMsg struct {
		query string
		hits  []vcs.Match
	}
	prMsg struct {
		pr  *vcs.PullRequest
		err error
	}
	eventMsg      rex.Event
	editorDoneMsg struct{ err error }
)

// requestColors are the commands that ask the terminal for its colours.
// The replies change the tints of the diff.
var requestColors = []tea.Cmd{
	tea.RequestBackgroundColor,
	tea.RequestForegroundColor,
	requestPalette,
}

// tick returns a command that sends a tickMsg after d.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the ticks, asks for the colours of the terminal, and loads
// the pull request and the agents.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(time.Second), m.loadPR()}
	cmds = append(cmds, requestColors...)
	if m.rex != nil {
		cmds = append(cmds, m.listAgents(agentsMsg{}))
	}
	if m.updater != nil {
		cmds = append(cmds, m.checkRelease())
	}
	return tea.Batch(cmds...)
}

// flashTime is how long a flash shows in the footer.
const flashTime = 6 * time.Second

// flash shows s in the footer for a short time. The text can come from Rex
// or from git, so flash quotes it if it has control characters.
func (m *model) flash(s string) {
	m.status, m.statusTill = diff.Quote(s), time.Now().Add(flashTime)
}

// trees returns the two trees that the Changes tab compares in the scope.
// If there is nothing to compare, note tells why.
func (m *model) trees() (from, to, note string, err error) {
	ctx := m.ctx
	switch m.scope {
	case scopeCommits:
		if m.to == "" {
			return "", "", "no commits picked: press G to pick one or a range", nil
		}
		if from, err = m.repo.Parent(ctx, m.from); err != nil {
			return "", "", "", err
		}
		to, err = m.repo.Tree(ctx, m.to)
		return from, to, "", err
	case ScopeLastTurn:
		if m.turn == nil {
			note := "no agent turn seen yet: the next turn's changes will show here"
			return "", "", note, nil
		}
		from = m.turn.tree
	case ScopeBranch:
		if from, err = m.repo.MergeBase(ctx, m.base); err != nil {
			return "", "", "", err
		}
	default:
		if from, err = m.repo.Head(ctx); err != nil {
			return "", "", "", err
		}
	}
	to, err = m.repo.Snapshot(ctx)
	return from, to, "", err
}

// reload reads the changes of the scope again, even if the trees did not
// change.
func (m *model) reload() {
	m.shownTrees = "" // no trees are "", so reloadIfChanged reads them
	m.reloadIfChanged()
}

// reloadIfChanged reads the changes of the scope again if the trees
// changed.
func (m *model) reloadIfChanged() {
	from, to, note, err := m.trees()
	trees := from + ".." + to
	var files []vcs.File
	if err == nil && note == "" {
		if trees == m.shownTrees {
			return
		}
		files, err = m.repo.Diff(m.ctx, from, to)
	}
	if err != nil {
		note = err.Error()
	}
	if note != "" {
		m.changes.note, m.files, m.shownTrees = note, nil, ""
		m.renderChanges()
		return
	}
	var selPath string
	if f := m.changedFile(); f != nil {
		selPath = f.Path
	}
	m.files = make([]changed, len(files))
	m.shownTrees, m.changeSel, m.changes.note = trees, 0, ""
	for i, f := range files {
		c := &m.files[i]
		c.File = f
		if !f.Binary && !f.TooBig {
			c.adds, c.dels = diff.Count(f.Old, f.New)
		}
		if f.Path == selPath {
			m.changeSel = i
		}
		m.comments.Relocate(f.Path, f.Old, f.New)
	}
	if f := m.changedFile(); f == nil || f.Path != selPath {
		// Another file shows, so the cursor and the selection do not apply.
		m.changes.cur, m.changes.top, m.changes.anchor = 0, 0, -1
	}
	if len(m.files) == 0 {
		m.changes.note = "no changes in this scope · watching…"
	}
	m.renderChanges()
}

// loadFiles reads the list of all files if the model does not have it.
func (m *model) loadFiles() {
	if m.allFiles == nil {
		m.allFiles, _ = m.repo.Files(m.ctx) // an error shows an empty list
	}
}

// reloadFiles reads the list of all files again.
func (m *model) reloadFiles() {
	m.allFiles, _ = m.repo.Files(m.ctx) // an error shows an empty list
	m.allSel = min(m.allSel, max(len(m.allFiles)-1, 0))
	m.refreshViewer()
}

// Update handles a message. After an install, it restarts the program
// when no work can get lost.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.handle(msg)
	if m.phase != phaseInstalled {
		return m, cmd
	}
	// A tick only starts the next tick, so it is no work.
	pending := cmd
	if _, tick := msg.(tickMsg); tick {
		pending = nil
	}
	if m.restartIfReady(pending) {
		return m, tea.Quit
	}
	return m, cmd
}

// handle does the work of Update. Update then checks for a restart.
func (m *model) handle(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseClickMsg: // these can move the cursor
		m.changes.free, m.viewer.free, m.prPane.free = false, false, false
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.editor.SetWidth(max(m.mainWidth()-8, 20))
		m.rerender()
	case tea.FocusMsg:
		m.focused = true
		m.reloadIfChanged()
	case tea.BlurMsg:
		m.focused = false
	case tickMsg:
		cmd = m.onTick()
	case tea.BackgroundColorMsg:
		if !sameColor(m.bg, msg.Color) {
			m.bg, m.dark = msg.Color, msg.IsDark()
			m.rerender()
		}
	case tea.ForegroundColorMsg:
		if !sameColor(m.fg, msg.Color) {
			m.fg = msg.Color
			m.rerender()
		}
	case uv.UnknownOscEvent:
		m.onPalette(string(msg))
	case agentsMsg:
		cmd = m.onAgents(msg)
	case sentMsg:
		m.onSent(msg)
	case eventMsg:
		m.onEvent(rex.Event(msg))
	case prMsg:
		m.onPR(msg)
	case grepMsg:
		m.onGrep(msg)
	case *update.Release:
		m.release = msg
	case installedMsg:
		m.onInstalled(msg)
	case editorDoneMsg:
		if msg.err != nil {
			m.flash("editor: " + msg.err.Error())
		}
		m.rerender()
	case tea.MouseClickMsg:
		return m.onClick(msg.Mouse())
	case tea.MouseMotionMsg:
		m.onDrag(msg.Mouse())
	case tea.MouseReleaseMsg:
		return m.onRelease()
	case tea.MouseWheelMsg:
		m.onWheel(msg.Mouse())
	case tea.PasteMsg:
		cmd = m.onPaste(msg.Content)
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	m.ensureVisible()
	return cmd
}

// onKey gives a key to the part of the screen that takes the keys.
func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	m.log.Debug("key", "key", msg.String(), "mode", m.mode, "scope", m.scope)
	switch m.mode {
	case modeComment:
		return m.updateComment(msg)
	case modeOverlay:
		return m.updateOverlay(msg)
	}
	return m.updateNormal(msg)
}

// onGrep shows the code matches of the search, but only if the search
// still shows their query.
func (m *model) onGrep(msg grepMsg) {
	ov := m.overlay
	if ov != nil && ov.kind == overlaySearch && ov.input.Value() == msg.query {
		ov.setSearch(m.allFiles, msg.hits)
	}
}

// onPR shows the pull request that loadPR read.
func (m *model) onPR(msg prMsg) {
	m.pr = prState{pr: msg.pr}
	if msg.err != nil {
		m.pr.err = msg.err.Error()
	}
	m.renderPR()
	if p := m.prPlace; p != nil && msg.err == nil && msg.pr != nil { // from before a restart
		m.prPlace = nil
		m.prPane.restore(*p)
	}
}

// onPaste adds text to the comment box or to the input of the overlay.
func (m *model) onPaste(text string) tea.Cmd {
	switch {
	case m.mode == modeComment:
		m.editor.InsertString(text)
	case m.mode == modeOverlay && m.overlay != nil && m.overlay.hasInput():
		m.overlay.input.SetValue(m.overlay.input.Value() + text)
		return m.inputChanged()
	}
	return nil
}

// The intervals of the work of onTick, in ticks of one second.
const (
	blurredTicks = 5  // a reload while the program does not have the focus
	filesTicks   = 10 // a read of the list of all files
	colorTicks   = 3  // a request for the colours of the terminal
)

// onTick reloads the current tab, if it changed.
func (m *model) onTick() tea.Cmd {
	m.ticks++
	every := 1
	if !m.focused {
		every = blurredTicks
	}
	if m.ticks%every == 0 {
		switch m.tab {
		case tabChanges:
			m.reloadIfChanged()
		case tabFiles:
			if m.ticks%filesTicks == 0 {
				m.reloadFiles() // it renders the file too
			} else {
				m.refreshViewer()
			}
		}
	}
	if m.status != "" && time.Now().After(m.statusTill) {
		m.status = ""
	}
	cmds := []tea.Cmd{tick(time.Second)}
	if m.ticks%colorTicks == 0 { // follow the changes of the terminal theme
		cmds = append(cmds, requestColors...)
	}
	return tea.Batch(cmds...)
}

// cur returns the pane of the current tab.
func (m *model) cur() *pane {
	switch m.tab {
	case tabFiles:
		return &m.viewer
	case tabPR:
		return &m.prPane
	}
	return &m.changes
}

// onPalette takes the red or the green from an OSC 4 reply.
func (m *model) onPalette(reply string) {
	i, c, ok := paletteReply(reply)
	if !ok {
		return
	}
	switch {
	case i == 1 && !sameColor(m.red, c):
		m.red = c
	case i == 2 && !sameColor(m.green, c):
		m.green = c
	default:
		return
	}
	m.rerender()
}

// sameColor reports whether a and b are the same colour. Nil is the same
// only as nil.
func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	return ar == br && ag == bg && ab == bb
}
