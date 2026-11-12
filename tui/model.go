package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/termtext"
	"github.com/rednafi/rexdiff/internal/turn"
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/update"
	"github.com/rednafi/rexdiff/vcs"
)

type tab int

const (
	tabChanges tab = iota
	tabAllFiles
	tabPR
)

const scopeCommits Scope = "commits"

// scopes is in the order that the header shows them.
var scopes = [...]Scope{ScopeUncommitted, ScopeBranch, ScopeLastTurn, scopeCommits}

func (s Scope) label() string { return strings.ReplaceAll(string(s), "-", " ") }

type mode int

const (
	modeNormal mode = iota
	modeComment
	modeOverlay
	modeFind
)

// String returns the name of the mode, for logging.
func (md mode) String() string {
	switch md {
	case modeNormal:
		return "normal"
	case modeComment:
		return "comment"
	case modeOverlay:
		return "overlay"
	case modeFind:
		return "find"
	}
	return "mode(" + strconv.Itoa(int(md)) + ")"
}

type pane struct {
	// lines is nil with a diff, which renders lazily. Read through line and count.
	lines    []string
	rows     []diff.Row // rows[i] is the source line that line(i) displays
	diff     *diff.Diff // nil for Markdown or PR
	path     string     // "" if the pane takes no comments
	srcRows  []diff.Row
	cur      int
	top      int
	selStart int // the first row of the visual selection, or -1
	scrollX  int // in columns
	note     string
	renders  int // changes with the lines, so cached find results can tell
	// following keeps the cursor on screen. The mouse wheel clears it
	// until the next key press or click.
	following bool
}

func (p *pane) set(lines []string, rows []diff.Row) {
	p.lines, p.rows, p.diff, p.srcRows = lines, rows, nil, nil
	p.renders++
	p.clamp()
}

func (p *pane) clamp() {
	p.cur = min(p.cur, p.last())
	p.top = min(p.top, p.last())
	if p.selStart >= len(p.rows) {
		p.selStart = -1
	}
}

func (p *pane) count() int {
	if p.diff != nil {
		return len(p.diff.Rows)
	}
	return len(p.lines)
}

func (p *pane) last() int { return max(p.count()-1, 0) }

func (p *pane) line(i int) string {
	if p.diff != nil {
		return p.diff.Line(i)
	}
	return p.lines[i]
}

func (p *pane) cont(i int) bool { return i > 0 && i < len(p.rows) && p.rows[i].Continued }

func (p *pane) lineStart(i int) int {
	for p.cont(i) {
		i--
	}
	return i
}

func (p *pane) lineEnd(i int) int {
	for p.cont(i + 1) {
		i++
	}
	return i
}

func (p *pane) reset() { p.cur, p.top, p.selStart, p.scrollX = 0, 0, -1, 0 }

func (p *pane) setDiff(d *diff.Diff) {
	p.lines, p.rows, p.diff, p.srcRows = nil, d.Rows, d, nil
	p.renders++
	p.clamp()
}

func (p *pane) side(x int) (side diff.Side, onGutter bool) {
	if p.diff == nil {
		return diff.SideNew, false
	}
	return p.diff.SideAt(x)
}

type changedFile struct {
	vcs.File
	added, deleted int
	oldHash        uint64 // identifies the file's folds
}

func (f changedFile) stat() string {
	switch {
	case errors.Is(f.Err, vcs.ErrBinary):
		return dimStyle.Render("binary")
	case errors.Is(f.Err, vcs.ErrTooBig):
		return dimStyle.Render("too large")
	}
	return addedStyle.Render(fmt.Sprintf("+%d", f.added)) + " " +
		deletedStyle.Render(fmt.Sprintf("-%d", f.deleted))
}

type turnState struct {
	app     string
	tree    string // the working tree snapshot from the turn start
	running bool
	at      time.Time
}

type dragState struct {
	active                     bool
	moved                      bool
	gutter                     bool
	startRow                   int
	prefer                     diff.Side
	startY, startX, endY, endX int
	link                       string // the URL under the initial press, or ""
	sep                        bool   // the drag resizes the file list
}

// draft edits the comment with ID editID, if set. itemRow is the comment
// list row to return to, or -1.
type draft struct {
	diff.Comment
	editID  int
	itemRow int
}

type changesTab struct {
	files     []changedFile
	tooMany   int // the changed file count if there are too many to show, else 0
	cur       int
	navigator navigator
	pane      pane
}

type allFilesTab struct {
	entries              []vcs.Entry
	fileCount            int
	more                 int // how many entries the list leaves out, past maxListedFiles
	listing              listState
	listed               bool // a read of the entries has finished, even if it failed
	jump                 *fileJump
	cur                  int
	navigator            navigator
	pane                 pane
	shownText, shownPath string
	shownErr             error
}

type prTab struct {
	pr              *vcs.PR
	err             string
	fetch           prFetch
	pane            pane
	place           *resumePane
	detailsExpanded bool
}

type agentState struct {
	app    string
	state  rex.State // "" if the agent reports no status
	since  time.Time
	cwd    string // "" if Rex does not know it
	hasCwd bool
	asking bool
}

type model struct {
	// ctx comes from Run. The model keeps it for git and Rex calls,
	// because the tea.Model methods do not take a context.
	ctx  context.Context //nolint:containedctx // see above
	repo *vcs.Repo
	rex  *rex.Client // nil outside Rex
	log  *slog.Logger
	env  []string

	cfg    config
	keys   keyMap
	notice string

	installer   *update.Installer // nil if rexdiff cannot update itself
	release     *update.Release
	updatePhase updatePhase
	resumePath  string

	// The terminal size. Until the terminal reports it, w and h are a
	// guess and sized is false.
	w, h   int
	sized  bool
	bg, fg color.Color
	red    color.Color // the terminal's ANSI red, or nil
	green  color.Color // the terminal's ANSI green, or nil
	ticks  int

	// watch is nil before the first check and after a failed check. The
	// next check then makes a new watcher.
	watch    *vcs.Watcher
	watching bool
	// watchReset makes the next check call [vcs.Watcher.Reset] first. The
	// call is in the check command, because Reset waits for a check in
	// progress.
	watchReset      bool
	watchedDirs     []string
	watchedFile     string
	watchRetry      retryTimer
	allFilesPending bool
	reloads         reloadState
	shownTrees      string
	shownScope      Scope
	changesLoaded   bool
	held            []tea.Msg
	// The state from before a restart, until the first loads restore it.
	resumeChanges, resumeFiles *resumeState
	turnChecking               bool
	// turnGen counts turn starts and ends. A snapshot taken before the
	// latest start or end is stale.
	turnGen int

	tab           tab
	scope         Scope
	expandedFolds map[string]*expandedFolds
	base          string
	defaultBase   string
	baseFromFlag  bool
	oldest        string
	newest        string

	// turn is the latest turn that changed a file. pendingTurn has not
	// changed a file yet. Both can be nil.
	turn, pendingTurn *turnState
	// turnRaced is true if the last turn started too near a file write.
	// Its snapshot can include that write, so the scope shows no turn.
	turnRaced bool
	tracker   turn.Tracker
	// agentsSampled is true after the first agent listing. State changes
	// before then can be wrong, so the tracker gets no states until then.
	agentsSampled bool
	sampleRetry   retryTimer
	// agentsGen counts reconnections to Rex. An agent list with an older
	// generation was taken before a disconnection.
	agentsGen   int
	agentInRepo *bool // nil until known
	ownsDir     map[string]bool
	ownsErr     string // already logged

	changes  changesTab
	allFiles allFilesTab
	readErr  string // already logged
	pr       prTab

	split             bool
	wrap              bool
	rendered          bool
	navigatorPosition position
	navigatorHidden   bool
	navigatorFocused  bool
	// crossStep is 1 or -1 after ] or [ went past the last change. The
	// next press in that direction jumps to crossPath. Otherwise it is 0.
	crossStep int
	crossPath string
	// navigatorWidthSet is in columns, or 0 for the default share.
	navigatorWidthSet int
	navigatorPct      int
	cache             diff.Cache
	colorsTicking     bool

	comments     diff.Comments
	commentFiles map[string]fileCopy
	commentInput textarea.Model
	draft        *draft
	mode         mode
	overlay      *overlay
	recent       []string
	grepStop     context.CancelCauseFunc
	grepGen      int
	fileGen      int
	find         findState
	findSides    sideLines
	markdown     markdownCache
	drag         dragState
	click        clickState
	help         help.Model

	flashText  string
	flashUntil time.Time

	agents   map[string]*agentState
	sends    int
	lastSent string
	beside   string // the agent block in the pane beside rexdiff
}

func newModel(ctx context.Context, o Options, cfg config) model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys(defaultKeys().InsertNewline.Keys()...)

	m := model{
		ctx:          ctx,
		repo:         o.Repo,
		rex:          o.Rex,
		log:          o.Log,
		env:          o.Env,
		notice:       o.Notice,
		installer:    o.Installer,
		w:            120,
		h:            40,
		base:         o.Base,
		baseFromFlag: o.Base != "",
		wrap:         o.Wrap,
		commentInput: ta,
		help:         newHelp(),
		agents:       map[string]*agentState{},
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	// The flag wins over the saved base.
	gitCtx, done := xctx.WithTimeout(ctx, gitTimeout, "startup")
	defer done(nil)
	if m.base == "" {
		if b := o.Repo.SavedBase(gitCtx); b != "" {
			if _, err := o.Repo.ResolveRev(gitCtx, b); err == nil {
				m.base = b
			}
		}
	}
	m.defaultBase = o.Repo.DefaultBase(gitCtx)
	m.base = cmp.Or(m.base, m.defaultBase)
	if tree := o.Repo.SavedTurn(gitCtx); tree != "" {
		m.turn = &turnState{tree: tree}
	}
	if o.Rex != nil && o.Agent != "" {
		// If Rex does not know the agent, the picker starts with none selected.
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "rex resolve")
		m.beside, _ = o.Rex.ResolveBlock(ctx, o.Agent)
		cancel(nil)
	}
	m.changes.pane.selStart, m.allFiles.pane.selStart, m.pr.pane.selStart = -1, -1, -1
	m.followCursor()
	m.cfg = cfg
	m.keys = defaultKeys()
	m.keys.applyConfig(cfg.Keybindings)
	m.navigatorPosition, m.navigatorWidthSet, m.navigatorPct = cfg.FileList, cfg.FileListWidth, navigatorStackPct
	m.rendered = cfg.MarkdownView == markdownViewRendered
	m.split = cmp.Or(o.Layout, cfg.Layout) == LayoutSplit
	m.scope = cmp.Or(o.Scope, cfg.DefaultScope)
	return m
}

type (
	tickMsg struct{}

	// reloadDueMsg ends the pause after a reload. A change seen during the
	// pause then does not wait for the next tick.
	reloadDueMsg struct{}

	agentsMsg struct {
		agents []rex.Agent
		err    error
		send   bool // the list is for sending a review, not for updating states
		gen    int
	}
	sentMsg struct {
		agent rex.Agent
		sent  map[int]string
		err   error
	}
	grepMsg struct {
		gen  int
		hits []vcs.Match
		more bool
		err  error
	}
	prMsg struct {
		gen int
		pr  *vcs.PR
		err error
	}
	eventMsg      rex.Event
	editorPaneMsg struct {
		path, script string
		err          error
	}
	cwdMsg struct {
		block, cwd string
		err        error
	}
	editorDoneMsg struct {
		path string
		err  error
	}
	detachedMsg struct {
		what string
		err  error
	}
	releaseMsg struct {
		release update.Release
	}
	updatedMsg struct {
		err error
	}
)

var requestColors = []tea.Cmd{
	tea.RequestBackgroundColor,
	tea.RequestForegroundColor,
	requestPalette,
}

const colorsTick = 16 * time.Millisecond

type colorsMsg struct{}

func (m *model) awaitColors() tea.Cmd {
	if m.colorsTicking || !m.cache.Highlighting() {
		return nil
	}
	m.colorsTicking = true
	return tea.Tick(colorsTick, func(time.Time) tea.Msg { return colorsMsg{} })
}

const tickInterval = time.Second

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the ticks, the watcher and the first loads.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(), m.requestPR(true), m.watchChanges(), m.startReload(), m.startFiles()}
	m.renderPR()
	cmds = append(cmds, requestColors...)
	if m.rex != nil {
		cmds = append(cmds, m.fetchAgents(agentsMsg{}))
	}
	if m.installer != nil {
		cmds = append(cmds, m.fetchRelease())
	}
	return tea.Batch(cmds...)
}

const flashTimeout = 4 * time.Second

// flash quotes s, because s can come from Rex or git.
func (m *model) flash(s string) {
	m.flashText, m.flashUntil = termtext.Quote(s), time.Now().Add(flashTimeout)
}

const gitTimeout = 5 * time.Second

// gitContext is for a short git call in Update. End it with done(nil).
func (m *model) gitContext(what string) (ctx context.Context, done context.CancelCauseFunc) {
	return xctx.WithTimeout(m.ctx, gitTimeout, what)
}

// timed returns a command that runs fn with a time limit.
func (m *model) timed(d time.Duration, what string, fn func(context.Context) tea.Msg) tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, d, what)
		defer cancel(nil)
		return fn(ctx)
	}
}

// stoppable is like timed, but stop ends fn early. Give stop the reason.
func (m *model) stoppable(d time.Duration, what string, fn func(context.Context) tea.Msg) (cmd tea.Cmd, stop context.CancelCauseFunc) {
	ctx, stop := xctx.WithTimeout(m.ctx, d, what)
	return func() tea.Msg {
		defer stop(nil)
		return fn(ctx)
	}, stop
}

// The reasons to stop a command early.
var (
	errSuperseded = errors.New("replaced by a newer request")
	errClosed     = errors.New("list closed")
)

// Update handles a message. After a self-update, it restarts when no work
// can be lost.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.holdInput(msg) {
		m.held = append(m.held, msg)
	} else {
		cmd = m.handleMsg(msg)
	}
	cmd = tea.Batch(cmd, m.releaseInput(), m.startReload(), m.startFiles())
	// The colors only draw the screen again, so they are not pending work.
	colors := m.awaitColors()
	if m.updatePhase == updateDone {
		// The new process runs its own ticks and watcher checks. They are
		// not pending work.
		hasPending := cmd != nil
		switch msg.(type) {
		case tickMsg, watchedMsg, reloadDueMsg:
			hasPending = false
		}
		if m.restartIfUpdated(hasPending) {
			return m, tea.Quit
		}
	}
	return m, tea.Batch(cmd, colors)
}

func (m *model) waiting() bool {
	switch m.tab {
	case tabChanges:
		return !m.changesLoaded
	case tabAllFiles:
		return !m.allFiles.listed
	}
	return false
}

// holdInput lets a key that quits or switches tabs skip the wait. The
// user can then leave a slow tab.
func (m *model) holdInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg, tea.MouseClickMsg, tea.MouseMotionMsg,
		tea.MouseReleaseMsg, tea.MouseWheelMsg:
	default:
		return false
	}
	if len(m.held) > 0 {
		return true
	}
	if !m.waiting() {
		return false
	}
	k, ok := msg.(tea.KeyPressMsg)
	return !ok || m.mode != modeNormal || !key.Matches(k, m.keys.Quit, m.keys.QuitDiscard,
		m.keys.TabChanges, m.keys.TabAllFiles, m.keys.TabPR)
}

func (m *model) releaseInput() tea.Cmd {
	var cmds []tea.Cmd
	for len(m.held) > 0 && !m.waiting() {
		msg := m.held[0]
		m.held = m.held[1:]
		cmds = append(cmds, m.handleMsg(msg))
	}
	return tea.Batch(cmds...)
}

func (m *model) handleMsg(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case changesMsg:
		m.onChanges(msg)
		m.restoreChanges()
		if wait := time.Until(m.reloads.at); wait > 0 {
			cmd = tea.Tick(wait, func(time.Time) tea.Msg { return reloadDueMsg{} })
		}
	case reloadDueMsg:
		m.reloadIfDue()
	case filesMsg:
		cmd = m.onFiles(msg)
	case fileSearchMsg:
		m.onFileSearch(msg)
	case turnStartMsg:
		cmd = m.onTurnStart(msg)
	case turnCheckMsg:
		cmd = m.onTurnCheck(msg)
	case tea.WindowSizeMsg:
		m.click = clickState{}
		m.w, m.h, m.sized = msg.Width, msg.Height, true
		m.rerender()
	case tea.FocusMsg:
		m.requestReloadIfChanged()
	case tickMsg:
		cmd = tea.Batch(tick(), m.onTick())
	case colorsMsg:
		m.colorsTicking = false
	case watchedMsg:
		cmd = m.onWatched(msg)
	case tea.BackgroundColorMsg:
		if !sameColor(m.bg, msg.Color) {
			m.bg = msg.Color
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
		cmd = m.onEvent(rex.Event(msg))
	case prMsg:
		cmd = m.onPR(msg)
	case grepMsg:
		m.onGrep(msg)
	case branchesMsg:
		m.onBranches(msg)
	case commitsMsg:
		m.onCommits(msg)
	case releaseMsg:
		m.release = &msg.release
	case updatedMsg:
		m.onUpdated(msg)
	case editorDoneMsg:
		m.onEditorDone(msg)
	case detachedMsg:
		m.flash(msg.what + " failed: " + msg.err.Error())
	case cwdMsg:
		cmd = m.onCwd(msg)
	case editorPaneMsg:
		cmd = m.onEditorPane(msg)
	case tea.MouseClickMsg:
		// Find the clicked row on the current screen before the view
		// follows the cursor.
		cmd = m.onClick(msg.Mouse())
		if m.drag.sep {
			return cmd
		}
		m.followCursor()
	case tea.MouseMotionMsg:
		m.onDrag(msg.Mouse())
	case tea.MouseReleaseMsg:
		cmd = m.onMouseUp(msg.Mouse()) // the comment box it opens can be off screen
	case tea.MouseWheelMsg:
		m.onWheel(msg.Mouse())
	case tea.PasteMsg:
		cmd = m.onPaste(msg.Content)
	case tea.KeyPressMsg:
		m.click = clickState{}
		m.followCursor()
		return m.onKey(msg)
	}
	m.scrollToCursor()
	return cmd
}

func (m *model) followCursor() {
	m.changes.pane.following, m.allFiles.pane.following, m.pr.pane.following = true, true, true
	m.changes.navigator.following, m.allFiles.navigator.following = true, true
}

func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	m.log.Debug("key", "key", m.loggedKey(msg), "mode", m.mode, "scope", m.scope)
	// Keys other than ], [ or Esc cancel a pending jump to another file.
	// Keys in the quit prompt do not.
	inQuit := m.overlay != nil && m.overlay.kind == overlayQuit
	step := key.Matches(msg, m.keys.NextHunk, m.keys.PrevHunk)
	if !inQuit && !step && msg.Code != tea.KeyEscape {
		m.crossStep = 0
	}
	switch m.mode {
	case modeComment:
		return m.updateComment(msg)
	case modeOverlay:
		return m.updateOverlay(msg)
	case modeFind:
		return m.updateFind(msg)
	}
	return m.updateNormal(msg)
}

// loggedKey hides typed text from the debug log.
func (m *model) loggedKey(msg tea.KeyPressMsg) string {
	typing := m.mode == modeComment || m.mode == modeFind ||
		m.mode == modeOverlay && m.overlay != nil && m.overlay.hasInput()
	if typing && msg.Text != "" {
		return "text"
	}
	return msg.String()
}

func (m *model) onPaste(text string) tea.Cmd {
	text = unixLines(text)
	switch {
	case m.mode == modeComment:
		// The text area scrolls its view to the cursor only in Update.
		var cmd tea.Cmd
		m.commentInput, cmd = m.commentInput.Update(tea.PasteMsg{Content: text})
		return cmd
	case m.overlay != nil && m.overlay.kind == overlayGotoLine:
		if n, ok := pastedLine(text); ok {
			m.overlay.input.SetValue(n)
		}
	case m.overlay != nil && m.overlay.hasInput():
		m.overlay.input.SetValue(m.overlay.input.Value() + text)
		return m.inputChanged()
	case m.mode == modeFind:
		m.find.input.SetValue(m.find.input.Value() + text)
	}
	return nil
}

// unixLines exists because terminals often paste \r\n, which the inputs
// read as two line breaks.
func unixLines(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

func (m *model) curPane() *pane {
	switch m.tab {
	case tabAllFiles:
		return &m.allFiles.pane
	case tabPR:
		return &m.pr.pane
	}
	return &m.changes.pane
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	return ar == br && ag == bg && ab == bb
}
