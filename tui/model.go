package tui

import (
	"cmp"
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"slices"
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
	"github.com/rednafi/rexdiff/rex"
	"github.com/rednafi/rexdiff/update"
	"github.com/rednafi/rexdiff/vcs"
)

// tab is a tab of the program: Changes, Files, or PR.
type tab int

const (
	tabChanges tab = iota
	tabFiles
	tabPR
)

// scopeCommits compares the commits that the user picks. It is not a
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
	modeFind // the find band
)

// String returns the name of the mode, for the log.
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

// pane is one view that scrolls and takes comments. It has the rendered
// lines and the source lines that they show.
type pane struct {
	lines  []string
	rows   []diff.Row // rows[i] is the source line that lines[i] shows
	diff   *diff.Diff // the render for mouse hits, or nil for Markdown or PR
	path   string     // the file of the rows, or "" for a pane with no comments
	cur    int        // the row of the cursor
	top    int        // the first row on the screen
	anchor int        // the start of the visual selection, or -1
	xoff   int        // the sideways scroll, in columns
	note   string     // the text that shows if there are no lines
	// free is true after the wheel moves the view. Then the cursor can
	// be off the screen until the next key or click.
	free bool
}

// set shows lines, and rows tells which source lines they show. It keeps
// the cursor, the top, and the selection in the new lines.
func (p *pane) set(lines []string, rows []diff.Row) {
	p.lines, p.rows, p.diff = lines, rows, nil
	p.cur = min(p.cur, max(len(lines)-1, 0))
	p.top = min(p.top, max(len(lines)-1, 0))
	if p.anchor >= len(rows) { // the file is shorter than before
		p.anchor = -1
	}
}

// cont reports whether line i continues the source line of the line above.
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

// changedFile is a changed file with its line counts and the hash of its
// old content, which names its folds.
type changedFile struct {
	vcs.File
	adds, dels int
	oldHash    uint64
}

// turnState is a turn of the agents in the repository. It keeps a
// snapshot of the working tree from the start of the turn.
type turnState struct {
	block   string // the first agent that works in the turn
	app     string // the name of that agent, for example "pi"
	tree    string // the snapshot at the start of the turn
	running bool
	at      time.Time // the start of the turn
}

// dragState is a drag of the left mouse button in the main pane.
type dragState struct {
	active bool      // the button is down
	moved  bool      // the cursor has left the start row
	gutter bool      // the drag starts on the line numbers
	start  int       // the row where the drag starts
	pref   diff.Side // the side of the line numbers or the code at the start
}

// draft is the comment that the user writes. It is a new comment, or an
// edit of the comment with ID editID. An edit from the comment list goes
// back to the list at row listRow. Else listRow is -1.
type draft struct {
	diff.Comment
	editID  int
	listRow int
}

// prState is the content of the PR tab.
type prState struct {
	loading bool
	pr      *vcs.PullRequest
	err     string
}

// agentState is what the program knows about the agent in a block.
type agentState struct {
	app    string    // the name of the agent, for example "pi"
	state  rex.State // "" if the program sends no status
	since  time.Time // when Rex got the state
	cwd    string    // the working directory, or "" if Rex does not know it
	hasCwd bool      // the program has the answer of Rex for cwd
	asking bool      // a question to Rex for cwd runs
}

// model is the state of the program. It implements tea.Model.
type model struct {
	// ctx comes from Run. The methods of tea.Model have fixed signatures,
	// so the model keeps ctx for its git and Rex calls.
	ctx  context.Context
	repo *vcs.Repo
	rex  *rex.Client // nil outside Rex
	log  *slog.Logger
	env  []string // for the editor, the URL opener, and the clipboard

	cfg    config
	keys   keyMap
	notice string // a problem that the footer shows, or ""

	updater    Updater         // nil if rexdiff cannot update itself
	release    *update.Release // a newer release, or nil
	phase      updatePhase
	resumePath string // the file of the resume state for the restart, or ""

	// The size of the terminal. Until the terminal tells it, w and h are
	// a guess, and sized is false.
	w, h   int
	sized  bool
	bg, fg color.Color
	red    color.Color // the ANSI red of the terminal, or nil
	green  color.Color // the ANSI green of the terminal, or nil
	ticks  int

	// watch tells when the files or the refs change, or is nil. If it is
	// nil, a tick reloads the current tab every reloadPoll, after polled.
	// After a failure of the watcher, a tick starts it again.
	watch      *vcs.Watcher
	polled     time.Time
	watchRetry retryTimer
	// reloadOwed is true if a change is seen but the Changes tab does not
	// show it yet. A reload waits until reloadAt, after the pause of the
	// last one, and until the retry, after a failure.
	reloadOwed  bool
	filesOwed   bool // the same for the list of all files
	reloadAt    time.Time
	reloadRetry retryTimer
	loads       reloads
	// turnChecking is true while a check of the pending turn runs.
	turnChecking bool

	tab   tab
	scope Scope
	// folds has the folds that the user expands in the Changes tab, by path.
	folds map[string]*openFolds
	base  string
	// baseFlag is true if the base comes from the flag. Then the user
	// cannot pick another.
	baseFlag bool
	from     string // the commits scope: the oldest commit that the user picks
	to       string // the commits scope: the newest commit that the user picks

	// turn is the last turn that changes a file, or nil. The last-turn
	// scope compares with it. pending is a turn that starts but changes
	// no file yet, or nil.
	turn, pending *turnState
	// raced is true if the last turn starts too close to a write. Then
	// its start snapshot can have the write, and the scope shows no turn.
	raced   bool
	tracker turn.Tracker
	// sampled is true after the first list of the agents. An edge before
	// it can be wrong, so the tracker gets no states before it.
	sampled bool
	// After a failed list, a tick lists the agents again, as in
	// herdr-reviewr.
	sampleRetry retryTimer
	// agentsGen counts the reconnects to Rex. A list of the agents with
	// an older gen is from before a gap.
	agentsGen int
	// agentsHere points to true if an agent works in the repository. It
	// is nil if the program does not know.
	agentsHere *bool
	memberDirs map[string]bool // the working directories of the members

	changeFiles []changedFile // the Changes tab
	changeSel   int           // the file that shows in the Changes tab
	shownTrees  string        // the two trees of files, to notice a change
	shownScope  Scope         // the scope of changeFiles
	changeTree  fileListTree
	allFiles    []vcs.Entry // the Files tab
	allSel      int
	allTree     fileListTree
	// viewed and viewedPath are the content and the path in the Files
	// tab, to notice edits. The path of the pane is "" for Markdown.
	viewed     string
	viewedPath string

	changePane, allPane, prPane pane
	pr                          prState
	prf                         prFetch
	prPlace                     *resumePane // prPane's place after a restart

	split      bool // show the diff side by side
	wrap       bool
	rendered   bool // show markdown rendered
	listPos    position
	listHidden bool
	focusList  bool // the keys j and k move in the file list
	// armed is 1 or -1 after a ] or a [ past the last change arms a
	// crossing to the file at armedPath. Else it is 0.
	armed     int
	armedPath string
	// listW is the width of the file list at a side, in columns, or 0 for
	// the default share of the width.
	listW   int
	listPct int        // the share of the body rows, above or below
	cache   diff.Cache // the syntax colours, for a fast sideways scroll

	comments diff.Comments
	editor   textarea.Model
	draft    *draft
	mode     mode
	overlay  *overlay
	// recent has the files that the user picks in the search in this
	// session, the last pick first.
	recent     []string
	grepCancel context.CancelCauseFunc // stops the code search that runs
	grepGen    int                     // the last code search
	find       findState
	findLines  splitLines // the lines that find searches
	md         mdRenderer // the last markdown renderer
	drag       dragState
	click      clickChain
	help       help.Model

	status     string
	statusTill time.Time

	agents   map[string]*agentState // by block
	sends    int                    // the steps of the sends that run
	lastSent string                 // the block of the last send
	beside   string                 // the agent block beside the pane
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
		ctx:      ctx,
		repo:     o.Repo,
		rex:      o.Rex,
		log:      o.Log,
		env:      o.Env,
		notice:   o.Notice,
		updater:  o.Updater,
		w:        120,
		h:        40,
		base:     o.Base,
		baseFlag: o.Base != "",
		wrap:     o.Wrap,
		editor:   ta,
		help:     newHelp(),
		agents:   map[string]*agentState{},
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	// As in herdr-reviewr, the flag wins over the saved base, and a saved
	// base that does not resolve does not count.
	if m.base == "" {
		if b := o.Repo.SavedBase(ctx); b != "" {
			if _, err := o.Repo.Resolve(ctx, b); err == nil {
				m.base = b
			}
		}
	}
	if m.base == "" {
		m.base = o.Repo.DefaultBase(ctx)
	}
	if tree := o.Repo.SavedTurn(ctx); tree != "" {
		m.turn = &turnState{tree: tree}
	}
	if o.Rex != nil && o.Agent != "" {
		// If Rex does not know the agent, the picker selects no agent
		// first. The picker still lists all the agents.
		m.beside, _ = o.Rex.Resolve(ctx, o.Agent)
	}
	m.changePane.anchor, m.allPane.anchor, m.prPane.anchor = -1, -1, -1
	m.cfg = cfg
	m.keys = defaultKeys()
	m.keys.applyConfig(cfg.Keybindings)
	m.listPos, m.listW, m.listPct = cfg.FileList, cfg.FileListWidth, listStackPct
	m.rendered = cfg.MarkdownView == markdownViewRendered
	// The flags win over the settings.
	m.split = cmp.Or(o.Layout, cfg.Layout) == LayoutSplit
	m.scope = cmp.Or(o.Scope, cfg.DefaultScope)
	return m
}

// These are the messages of the program.
type (
	tickMsg   struct{}
	agentsMsg struct {
		agents []rex.Agent
		err    error
		send   bool // true if the list is for a send, not to learn the states
		gen    int  // the agentsGen at the start of the list
	}
	sentMsg struct {
		agent rex.Agent
		ids   []int // the IDs of the sent comments
		err   error
	}
	grepMsg struct {
		gen  int // the search whose result shows. Other searches are stale.
		hits []vcs.Match
		more bool // git finds more matches than hits has
		err  error
	}
	prMsg struct {
		gen int // the read, see prFetch
		pr  *vcs.PullRequest
		err error
	}
	eventMsg rex.Event
	// splitMsg is the answer of Rex to the split for the editor of path.
	splitMsg struct {
		path, script string
		err          error
	}
	// cwdMsg is the answer of Rex for the working directory of an agent.
	cwdMsg struct {
		block, cwd string
		err        error
	}
	// editorDoneMsg tells that the editor of path in this pane stops.
	editorDoneMsg struct {
		path string
		err  error
	}
	// detachedMsg tells that a detached program, for what, fails.
	detachedMsg struct {
		what string
		err  error
	}
	// releaseMsg has a release that is newer than this rexdiff.
	releaseMsg struct {
		release update.Release
	}
	// updatedMsg reports the end of the update to the release.
	updatedMsg struct {
		err error
	}
)

// requestColors are the commands that ask the terminal for its colours.
// The replies change the tints of the diff.
var requestColors = []tea.Cmd{
	tea.RequestBackgroundColor,
	tea.RequestForegroundColor,
	requestPalette,
}

// tickInterval is the time between two ticks.
const tickInterval = time.Second

// tick returns a command that sends a tickMsg after d.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the ticks, asks for the colours of the terminal, and loads
// the pull request and the agents.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(tickInterval), m.requestPR(true)}
	cmds = append(cmds, requestColors...)
	if m.rex != nil {
		cmds = append(cmds, m.listAgents(agentsMsg{}))
	}
	if m.updater != nil {
		cmds = append(cmds, m.checkRelease())
	}
	return tea.Batch(cmds...)
}

// flashTime is how long a flash shows in the footer. herdr-reviewr uses
// the same time.
const flashTime = 4 * time.Second

// flash shows s in the footer for a short time. The text can come from Rex
// or from git, so flash quotes it if it has control characters.
func (m *model) flash(s string) {
	m.status, m.statusTill = termtext.Quote(s), time.Now().Add(flashTime)
}

// reload reads the changes of the scope again, even if the trees do not
// change.
// A reload that runs is then stale, because the view can change.
func (m *model) reload() {
	l := &m.loads
	l.want, l.force, l.stale = true, true, l.running
}

// reloadIfChanged reads the changes of the scope again if the trees differ
// from the shown trees.
func (m *model) reloadIfChanged() { m.loads.want = true }

// reloads follows the reloads of the Changes tab. A reload runs git, so
// it runs in a command, one at a time. A request during a reload starts
// one more reload after it.
type reloads struct {
	gen     int  // the reload whose result shows. Other reloads are stale.
	running bool // the reload gen runs
	want    bool // a reload is due
	// The due reload shows the changes, also for equal trees.
	force bool
	// The view changes while the reload gen runs, so its result is old.
	stale bool
}

// loadNow reads the changes at once, for the first screen. Nothing shows
// yet, so the wait blocks nothing.
func (m *model) loadNow() {
	m.loads.gen++
	m.loads.want, m.loads.force = false, false
	m.onChanges(m.reloadInput(true).run(m.ctx))
}

// reloadTimeout is the longest time of a reload. A git that hangs then
// fails the reload, and the next tick tries again.
const reloadTimeout = time.Minute

// startReload returns a command for the reload that is due, if no reload
// runs. While the comment box is open, the reload waits until the box
// closes, as in herdr-reviewr.
func (m *model) startReload() tea.Cmd {
	l := &m.loads
	if !l.want || l.running || m.mode == modeComment {
		return nil
	}
	l.gen++
	l.running, l.want, l.stale = true, false, false
	in, ctx := m.reloadInput(l.force), m.ctx
	l.force = false
	return func() tea.Msg { return in.run(ctx) }
}

// reloadInput returns what a reload needs, copied from the model, so that
// the reload can run in a command.
func (m *model) reloadInput(force bool) reloadInput {
	in := reloadInput{
		repo: m.repo, gen: m.loads.gen, scope: m.scope,
		base: m.base, from: m.from, to: m.to, force: force, shown: m.shownTrees,
		prev: make(map[string]changedFile, len(m.changeFiles)),
	}
	for _, c := range m.changeFiles {
		in.prev[c.Path] = c
	}
	switch {
	case m.scope == scopeCommits && m.to == "":
		in.note = "no commits picked: press " + firstKey(m.keys.CommitPick) +
			" to pick one or a range"
	case m.scope == ScopeLastTurn && m.turn == nil:
		in.note = m.turnWaitMessage()
	case m.scope == ScopeLastTurn:
		in.turnTree = m.turn.tree
	}
	return in
}

// reloadInput is the input of a reload. note tells why there is nothing
// to compare, or is "".
type reloadInput struct {
	repo                 *vcs.Repo
	gen                  int
	scope                Scope
	base, from, to, note string
	turnTree, shown      string
	force                bool
	prev                 map[string]changedFile // the shown files, by path
}

// changesMsg is the result of a reload.
type changesMsg struct {
	gen   int
	trees string // the two trees, as "from..to"
	same  bool   // the trees are the shown trees, and the reload is not forced
	files []changedFile
	note  string
	err   error
	took  time.Duration
}

// run reads the changes of in. It counts the lines of each file that is
// not in in.prev with the same content.
func (in reloadInput) run(ctx context.Context) changesMsg {
	start := time.Now()
	ctx, cancel := context.WithCancelCause(ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, reloadTimeout,
		fmt.Errorf("reload: %v timeout: %w", reloadTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	defer cancel(fmt.Errorf("reload: done: %w", context.Canceled))
	msg := changesMsg{gen: in.gen, note: in.note}
	if msg.note == "" {
		from, to, err := in.trees(ctx)
		msg.trees, msg.err = from+".."+to, err
		msg.same = err == nil && msg.trees == in.shown && !in.force
		if err == nil && !msg.same {
			msg.files, msg.err = in.changedFiles(ctx, from, to)
		}
	}
	msg.took = time.Since(start)
	return msg
}

// trees returns the IDs of the two trees that the Changes tab compares in
// the scope.
func (in reloadInput) trees(ctx context.Context) (from, to string, err error) {
	switch in.scope {
	case scopeCommits:
		if from, err = in.repo.ParentTree(ctx, in.from); err != nil {
			return "", "", err
		}
		to, err = in.repo.Tree(ctx, in.to)
		return from, to, err
	case ScopeLastTurn:
		from = in.turnTree
	case ScopeBranch:
		from, err = in.repo.MergeBaseTree(ctx, in.base)
	default:
		from, err = in.repo.HeadTree(ctx)
	}
	if err != nil {
		return "", "", err
	}
	to, err = in.repo.Snapshot(ctx)
	return from, to, err
}

// changedFiles returns the changes from the tree from to the tree to,
// with their line counts.
func (in reloadInput) changedFiles(ctx context.Context, from, to string) ([]changedFile, error) {
	files, err := in.repo.Diff(ctx, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]changedFile, len(files))
	for i, f := range files {
		c := &out[i]
		if p, ok := in.prev[f.Path]; ok && p.Old == f.Old && p.New == f.New {
			*c = p // the same change, so the counts stay
		} else if !f.Binary && !f.TooBig {
			c.adds, c.dels = diff.Count(f.Old, f.New)
			c.oldHash = hash(f.Old)
		}
		c.File = f
	}
	return out, nil
}

// onChanges shows the result of a reload. After a failure, the next tick
// tries again after a wait. As in herdr-reviewr, a failure keeps the files
// of the scope that show, and the error shows in their place only if no
// files show. A change of the view during the reload starts one more.
func (m *model) onChanges(msg changesMsg) {
	l := &m.loads
	if msg.gen != l.gen {
		return
	}
	l.running = false
	if l.stale || m.mode == modeComment {
		// As in herdr-reviewr, the view does not change under the comment
		// box. The reload runs again later.
		l.want, l.stale = true, false
		return
	}
	m.reloadAt = time.Now().Add(reloadPauseFactor * msg.took)
	m.retry(msg.err)
	note, err := msg.note, msg.err
	if err == nil && note == "" && msg.same {
		return
	}
	if err != nil && len(m.changeFiles) > 0 && m.shownScope == m.scope {
		m.flash("refresh failed: " + err.Error())
		return
	}
	if err != nil {
		note = err.Error()
	}
	m.shownScope = m.scope
	if note != "" {
		m.changePane.note, m.changeFiles, m.shownTrees = note, nil, ""
		m.buildTrees()
		m.renderChanges()
		return
	}
	var selPath string
	if f := m.selectedChange(); f != nil {
		selPath = f.Path
	}
	m.changeFiles = msg.files
	m.shownTrees, m.changePane.note = msg.trees, ""
	m.changeSel = slices.IndexFunc(m.changeFiles, func(c changedFile) bool { return c.Path == selPath })
	m.buildTrees()
	if f := m.selectedChange(); f == nil || f.Path != selPath {
		// Another file shows, so the cursor and the selection do not apply.
		m.changePane.cur, m.changePane.top, m.changePane.anchor = 0, 0, -1
	}
	if len(m.changeFiles) == 0 {
		m.changePane.note = "no changes"
	}
	m.renderChanges()
}

// loadFiles reads the list of all files if the model does not have it.
func (m *model) loadFiles() {
	if m.allFiles == nil {
		m.readFiles()
	}
}

// reloadFiles reads the list of all files again.
func (m *model) reloadFiles() {
	m.filesOwed = false
	m.readFiles()
	m.refreshViewer()
}

// readFiles reads the list of all files. The selected file stays. On an
// error, it keeps the old list and shows the error.
func (m *model) readFiles() {
	files, err := m.repo.Files(m.ctx)
	if err != nil {
		m.flash("refresh failed: " + err.Error())
		return
	}
	var sel string
	if m.allSel < len(m.allFiles) {
		sel = m.allFiles[m.allSel].Path
	}
	m.allFiles = m.withIgnored(files)
	m.allSel = slices.IndexFunc(m.allFiles, func(e vcs.Entry) bool { return e.Path == sel })
	m.buildFileTree()
}

// buildTrees makes the rows of the file lists after a change of the
// Changes tab. The Files tab marks the directories with a change.
func (m *model) buildTrees() {
	m.buildChangeTree()
	if m.allFiles != nil {
		m.buildFileTree()
	}
}

// Update handles a message. After an update, Update restarts the program
// when no work can get lost.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.handle(msg)
	if m.phase != phaseUpdated {
		return m, cmd
	}
	// The work of a tick runs again in the new process, so it is not
	// pending work.
	pending := cmd
	if _, ok := msg.(tickMsg); ok {
		pending = nil
	}
	if m.restartIfReady(pending) {
		return m, tea.Quit
	}
	return m, cmd
}

// handle does the work of Update. Update then checks for a restart.
func (m *model) handle(msg tea.Msg) tea.Cmd {
	cmd := m.handleMsg(msg)
	return tea.Batch(cmd, m.startReload())
}

// handleMsg handles msg. A reload that it asks for starts after it.
func (m *model) handleMsg(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case changesMsg:
		m.onChanges(msg)
	case turnCheckMsg:
		m.onTurnCheck(msg)
	case tea.WindowSizeMsg:
		m.click = clickChain{}
		m.w, m.h, m.sized = msg.Width, msg.Height, true
		m.rerender()
	case tea.FocusMsg:
		m.reloadIfChanged()
	case tickMsg:
		cmd = tea.Batch(tick(tickInterval), m.onTick())
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
	case splitMsg:
		cmd = m.onSplit(msg)
	case tea.MouseClickMsg:
		m.followCursor()
		return m.onClick(msg.Mouse())
	case tea.MouseMotionMsg:
		m.onDrag(msg.Mouse())
	case tea.MouseReleaseMsg:
		cmd = m.onMouseUp() // the comment box can open below the screen
	case tea.MouseWheelMsg:
		m.onWheel(msg.Mouse())
	case tea.PasteMsg:
		cmd = m.onPaste(msg.Content)
	case tea.KeyPressMsg:
		m.click = clickChain{}
		m.followCursor()
		return m.onKey(msg)
	}
	m.ensureVisible()
	return cmd
}

// followCursor makes the panes keep the cursor on the screen again. A key
// or a click can move the cursor.
func (m *model) followCursor() {
	m.changePane.free, m.allPane.free, m.prPane.free = false, false, false
}

// onKey gives a key to the part of the screen that takes the keys.
func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	m.log.Debug("key", "key", msg.String(), "mode", m.mode, "scope", m.scope)
	// As in herdr-reviewr, a key other than ], [, or esc drops an armed
	// crossing. The keys of the prompt before a quit do not.
	inQuit := m.mode == modeOverlay && m.overlay != nil && m.overlay.kind == overlayQuit
	step := key.Matches(msg, m.keys.NextHunk, m.keys.PrevHunk)
	if !inQuit && !step && msg.Code != tea.KeyEscape {
		m.armed = 0
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

// onPaste adds text to the comment box or to the input of the overlay.
func (m *model) onPaste(text string) tea.Cmd {
	switch {
	case m.mode == modeComment:
		m.editor.InsertString(text)
	case m.mode == modeOverlay && m.overlay != nil && m.overlay.kind == overlayGoto:
		if n, ok := pastedLine(text); ok {
			m.overlay.input.SetValue(n)
		}
	case m.mode == modeOverlay && m.overlay != nil && m.overlay.hasInput():
		m.overlay.input.SetValue(m.overlay.input.Value() + text)
		return m.inputChanged()
	case m.mode == modeFind:
		m.find.input.SetValue(m.find.input.Value() + text)
	}
	return nil
}

// colorTicks is the interval of the requests for the colours of the
// terminal, in ticks of one second.
const colorTicks = 3

// The waits of a reload, as in herdr-reviewr.
const (
	// reloadPauseFactor sets the pause after a reload on a tick. The
	// next reload waits this many times as long as the last one took.
	// The pause of the watcher in vcs has the same value, but it is a
	// different pause. Thus each package has its own constant.
	reloadPauseFactor = 20
	retryFirst        = time.Second // the first wait after a failure
	retryMax          = time.Minute // the longest wait after a failure
)

// reloadPoll is the time between two reloads without a watcher, as in
// herdr-reviewr.
const reloadPoll = 5 * time.Second

// retryTimer tells when to try again after a failure. Each failure
// doubles the wait, from retryFirst to retryMax.
type retryTimer struct {
	at   time.Time     // the next try, or zero
	wait time.Duration // the wait after the next failure, or 0
}

// fail sets the next try after a failure at now.
func (r *retryTimer) fail(now time.Time) {
	w := max(r.wait, retryFirst)
	r.at, r.wait = now.Add(w), min(2*w, retryMax)
}

// due reports whether a try waits and its time is at or before now.
func (r *retryTimer) due(now time.Time) bool {
	return !r.at.IsZero() && !now.Before(r.at)
}

// retry sets the wait before the next reload after the error err. Each
// failure doubles the wait, from 1 s to 60 s. A reload with no error
// clears the wait.
func (m *model) retry(err error) {
	if err == nil {
		m.reloadRetry = retryTimer{}
		return
	}
	m.reloadRetry.fail(time.Now())
	m.reloadOwed = true
}

// dropWatch drops the watcher after the error err. A tick starts it again
// after a wait.
func (m *model) dropWatch(err error) {
	m.log.Warn("watch failed", "err", err)
	m.watch = nil
	m.watchRetry.fail(time.Now())
}

// startWatch starts the watcher of the repository.
func (m *model) startWatch() error {
	w, err := m.repo.Watch(m.ctx)
	if err != nil {
		return err
	}
	m.watch, m.watchRetry.at = w, time.Time{}
	return nil
}

// watchChanges tells what the watcher sees. Without a watcher, it tells
// that both change every reloadPoll. After an error, it drops the watcher
// and tells that both change.
func (m *model) watchChanges() vcs.Change {
	all := vcs.Change{Files: true, Refs: true}
	now := time.Now()
	if m.watch == nil && m.watchRetry.due(now) {
		if err := m.startWatch(); err != nil {
			m.dropWatch(err)
		}
	}
	if m.watch == nil {
		if now.Sub(m.polled) < reloadPoll {
			return vcs.Change{}
		}
		m.polled = now
		return all
	}
	m.watch.Show(m.shownPaths())
	ch, err := m.watch.Changed(m.ctx)
	m.log.Debug("watch", "took", time.Since(now), "files", ch.Files, "refs", ch.Refs)
	if err != nil {
		m.dropWatch(err)
		m.polled = now
		return all
	}
	m.watchRetry = retryTimer{}
	return ch
}

// onTick reloads the current tab if the watcher sees a change. It
// returns the work of the tick. Update adds the next tick.
func (m *model) onTick() tea.Cmd {
	m.ticks++
	ch := m.watchChanges()
	var cmds []tea.Cmd
	if ch.Files && m.pending != nil {
		cmds = append(cmds, m.checkTurnLater())
	}
	m.reloadOwed = m.reloadOwed || ch.Files || ch.Refs
	m.filesOwed = m.filesOwed || ch.Files
	now := time.Now()
	m.reloadDue()
	if m.status != "" && now.After(m.statusTill) {
		m.status = ""
	}
	// Without a watcher, each tick reports a change of the refs. Then only
	// the poll reads the pull request.
	if ch.Refs && m.watch != nil {
		cmds = append(cmds, m.refsChanged())
	}
	if m.rex != nil && !m.sampled && m.sampleRetry.due(now) {
		m.sampleRetry.at = time.Time{} // one list at a time
		cmds = append(cmds, m.listAgents(agentsMsg{}))
	}
	if m.tab == tabPR && time.Since(m.prf.polled) >= prPoll {
		cmds = append(cmds, m.requestPR(false))
	}
	if m.ticks%colorTicks == 0 { // follow the changes of the terminal theme
		cmds = append(cmds, requestColors...)
	}
	return tea.Batch(cmds...)
}

// reloadDue reloads the current tab if a change waits for it. On the
// Changes tab, a reload waits for the pause after the last one, and for
// the wait after a failure. As in herdr-reviewr, the view does not change
// while the comment box is open.
func (m *model) reloadDue() {
	if m.mode == modeComment {
		return
	}
	now := time.Now()
	switch {
	case m.tab == tabChanges && m.reloadOwed && !m.loads.running &&
		!now.Before(m.reloadAt) && !now.Before(m.reloadRetry.at):
		m.reloadOwed = false
		m.reloadIfChanged() // onChanges sets reloadAt after the pause
	case m.tab == tabFiles && m.filesOwed:
		m.reloadFiles() // reloadFiles renders the file too
	case m.tab == tabFiles && m.watch == nil:
		m.refreshViewer() // without a watcher, each tick reads the file
	}
}

// cur returns the pane of the current tab.
func (m *model) cur() *pane {
	switch m.tab {
	case tabFiles:
		return &m.allPane
	case tabPR:
		return &m.prPane
	}
	return &m.changePane
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
