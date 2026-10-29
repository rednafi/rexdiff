package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image/color"
	"log/slog"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// tab is one of the program's tabs: Changes, Files or PR.
type tab int

const (
	tabChanges tab = iota
	tabAllFiles
	tabPR
)

// scopeCommits shows the commits that the user chooses in the commit
// picker. It cannot be used as the default scope.
const scopeCommits Scope = "commits"

// scopes lists the scopes in the order the header shows them.
var scopes = [...]Scope{ScopeUncommitted, ScopeBranch, ScopeLastTurn, scopeCommits}

// label returns the name that the header shows for s.
func (s Scope) label() string { return strings.ReplaceAll(string(s), "-", " ") }

// mode says which part of the screen receives key presses.
type mode int

const (
	modeNormal mode = iota
	modeComment
	modeOverlay
	modeFind // the find bar
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

// pane is a scrollable view that the user can comment on. It holds the
// rendered lines and the source rows they display.
type pane struct {
	// lines holds the lines of a pane without a diff. A pane with a diff
	// renders each line from diff when it is first needed. Read lines
	// through line and count.
	lines    []string
	rows     []diff.Row // rows[i] is the source line that line(i) displays
	diff     *diff.Diff // the rendered diff, which renders the lines and maps clicks; nil for Markdown or PR
	path     string     // the file the rows come from, or "" if the pane takes no comments
	srcRows  []diff.Row // the source rows behind rendered Markdown, or nil
	cur      int        // the cursor row
	top      int        // the first row on screen
	selStart int        // the first row of the visual selection, or -1
	scrollX  int        // the horizontal scroll, in columns
	note     string     // the text shown when there are no lines
	renders  int        // incremented whenever the lines change, so cached find results can tell
	// following is true while the view keeps the cursor on screen. The
	// mouse wheel sets it to false, which lets the cursor go off screen
	// until the next key press or click.
	following bool
}

// set replaces the lines and the source rows they display. It keeps the
// cursor, top row and selection within the new lines.
func (p *pane) set(lines []string, rows []diff.Row) {
	p.lines, p.rows, p.diff, p.srcRows = lines, rows, nil, nil
	p.renders++
	p.clamp()
}

// clamp moves the cursor, top row and selection back within the lines.
func (p *pane) clamp() {
	p.cur = min(p.cur, p.last())
	p.top = min(p.top, p.last())
	if p.selStart >= len(p.rows) { // the file got shorter
		p.selStart = -1
	}
}

// count returns the number of lines in the pane.
func (p *pane) count() int {
	if p.diff != nil {
		return len(p.diff.Rows)
	}
	return len(p.lines)
}

// last returns the index of the last line in the pane, or 0 if it has none.
func (p *pane) last() int { return max(p.count()-1, 0) }

// line returns line i of the pane. With a diff, the line is rendered the
// first time it is needed.
func (p *pane) line(i int) string {
	if p.diff != nil {
		return p.diff.Line(i)
	}
	return p.lines[i]
}

// cont reports whether line i is a continuation of the source line above
// it, as happens with wrapped lines.
func (p *pane) cont(i int) bool { return i > 0 && i < len(p.rows) && p.rows[i].Continued }

// lineStart returns the first pane line of the source line shown at line i.
func (p *pane) lineStart(i int) int {
	for p.cont(i) {
		i--
	}
	return i
}

// lineEnd returns the last pane line of the source line shown at line i.
func (p *pane) lineEnd(i int) int {
	for p.cont(i + 1) {
		i++
	}
	return i
}

// reset moves the cursor and view to the first row, and clears the
// selection and horizontal scroll.
func (p *pane) reset() { p.cur, p.top, p.selStart, p.scrollX = 0, 0, -1, 0 }

// setDiff makes the pane display d.
func (p *pane) setDiff(d *diff.Diff) {
	p.lines, p.rows, p.diff, p.srcRows = nil, d.Rows, d, nil
	p.renders++
	p.clamp()
}

// side returns the diff side at screen column x, and reports whether x is
// in the line number gutter.
func (p *pane) side(x int) (side diff.Side, onGutter bool) {
	if p.diff == nil {
		return diff.SideNew, false
	}
	return p.diff.SideAt(x)
}

// changedFile is a changed file with its added and deleted line counts
// and a hash of its old content. The hash identifies the file's folds.
type changedFile struct {
	vcs.File
	added, deleted int
	oldHash        uint64
}

// stat returns the counts of the added and deleted lines of f, such as
// "+3 -1". For a file that rexdiff does not diff, it says why.
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

// turnState describes an agent turn in the repository, with a snapshot of
// the working tree taken when the turn started.
type turnState struct {
	app     string // the name of the first agent in the turn, for example "pi"
	tree    string // the snapshot taken when the turn started
	running bool
	at      time.Time // when the turn started
}

// dragState tracks a drag with the left mouse button in the main pane.
type dragState struct {
	active   bool      // the button is held down
	moved    bool      // the cursor has left the starting row
	gutter   bool      // the drag started in the line number gutter
	startRow int       // the row where the drag started
	prefer   diff.Side // the diff side where the drag started
	// The screen line and code column where the button was pressed and
	// where the pointer is now.
	startY, startX, endY, endX int
	link                       string // the URL under the initial press, or ""
}

// draft is the comment that the user is writing: either a new comment or
// an edit of the comment with ID editID. If the edit started from the
// comment list, itemRow is the list row to return to. Otherwise itemRow is
// -1.
type draft struct {
	diff.Comment
	editID  int
	itemRow int
}

// changesTab holds the state of the Changes tab.
type changesTab struct {
	files []changedFile
	// tooMany is the number of changed files when there are too many to
	// show, and 0 otherwise.
	tooMany   int
	cur       int // the index of the file being shown
	navigator navigator
	pane      pane
}

// allFilesTab holds the state of the Files tab.
type allFilesTab struct {
	entries   []vcs.Entry
	fileCount int // how many entries are files, for the list title
	more      int // how many entries the list leaves out, past maxListedFiles
	listing   listState
	listed    bool // a read of the entries has finished, even if it failed
	// jump is a jump to a file that waits for the entries to load, or nil.
	jump      *fileJump
	cur       int
	navigator navigator
	pane      pane
	// shownText and shownPath are the content and path of the file being
	// shown, used to detect edits. shownErr is the error from reading it.
	shownText, shownPath string
	shownErr             error
}

// prTab holds the state of the PR tab.
type prTab struct {
	pr              *vcs.PR
	err             string
	fetch           prFetch
	pane            pane
	place           *resumePane // the scroll position to restore in pane after a restart
	detailsExpanded bool        // whether the PR's <details> blocks are expanded
}

// agentState is what rexdiff knows about the agent in a Rex block.
type agentState struct {
	app    string    // the name of the agent, for example "pi"
	state  rex.State // "" if the agent reports no status
	since  time.Time // when Rex received the state
	cwd    string    // the working directory, or "" if Rex does not know it
	hasCwd bool      // Rex has answered the request for cwd
	asking bool      // a request to Rex for cwd is in progress
}

// model is the state of the program. It implements [tea.Model].
type model struct {
	// ctx comes from Run. The model stores it for git and Rex calls,
	// because the tea.Model methods do not take a context.
	ctx  context.Context
	repo *vcs.Repo
	rex  *rex.Client // nil outside Rex
	log  *slog.Logger
	env  []string // the environment for the editor, URL opener and clipboard

	cfg    config
	keys   keyMap
	notice string // a problem to show in the footer, or ""

	installer   *update.Installer // nil if rexdiff cannot update itself
	release     *update.Release   // a newer release, or nil
	updatePhase updatePhase
	resumePath  string // the file that saves the state across a restart, or ""

	// The terminal size. Until the terminal reports it, w and h are a
	// guess and sized is false.
	w, h   int
	sized  bool
	bg, fg color.Color
	red    color.Color // the terminal's ANSI red, or nil
	green  color.Color // the terminal's ANSI green, or nil
	ticks  int

	// watch reports changes to files and refs, or is nil. While it is
	// nil, a tick reloads the current tab once reloadInterval has passed
	// since polled. If the watcher fails, a later tick starts it again.
	watch         *vcs.Watcher
	watchStarting bool // a command is starting the watcher
	watching      bool // a check of watch is in progress
	// watchReset makes the next check call [vcs.Watcher.Reset] first. The
	// check calls it in its command, because Reset waits for a check in
	// progress.
	watchReset bool
	// watchedDirs and watchedFile are the expanded ignored directories and
	// the open file of the Files tab that the last check covered.
	watchedDirs []string
	watchedFile string
	polled      time.Time
	watchRetry  retryTimer
	// reloadPending is true if the Changes tab is not yet showing a
	// change. The reload waits until reloadAt and, after a failure, until
	// the retry.
	reloadPending   bool
	allFilesPending bool // like reloadPending, for the Files tab
	reloadAt        time.Time
	reloadRetry     retryTimer
	reloads         reloadState
	changesLoaded   bool // the first reload has finished
	// held is the input that waits for the tab to load; see holdInput.
	held []tea.Msg
	// resumeChanges and resumeFiles hold the state of the previous process
	// after a restart, until the first reload and the first file list have
	// loaded and its places are restored. Otherwise they are nil.
	resumeChanges, resumeFiles *resumeState
	// turnChecking is true while the pending turn is being checked.
	turnChecking bool
	// turnGen counts turn starts and ends. A snapshot taken before the
	// latest start or end is stale.
	turnGen int

	tab   tab
	scope Scope
	// expandedFolds holds the folds that the user has expanded in the
	// Changes tab, keyed by path.
	expandedFolds map[string]*expandedFolds
	base          string
	// defaultBase is the default branch found at startup, for the base
	// picker.
	defaultBase string
	// baseFromFlag is true if the base was set by a flag, in which case
	// the user cannot choose another.
	baseFromFlag bool
	oldest       string // the oldest commit chosen for the commits scope
	newest       string // the newest commit chosen for the commits scope

	// turn is the latest turn that changed a file, or nil. The last-turn
	// scope compares against it. pendingTurn is a turn that has started
	// but not yet changed a file, or nil.
	turn, pendingTurn *turnState
	// turnRaced is true if the last turn started too close to a file
	// write. Its starting snapshot may then include that write, so the
	// scope shows no turn.
	turnRaced bool
	tracker   turn.Tracker
	// agentsSampled is true once the agents have been listed for the
	// first time. State changes seen before then may be wrong, so the
	// tracker receives no states until then.
	agentsSampled bool
	// sampleRetry schedules the next agent listing after a failed one.
	sampleRetry retryTimer
	// agentsGen counts reconnections to Rex. An agent list with an older
	// generation was taken before a disconnection.
	agentsGen int
	// agentInRepo points to true if an agent is in the repository, and to
	// false if none is. It is nil until this is known.
	agentInRepo *bool
	ownsDir     map[string]bool // the answers of owns, by directory
	ownsErr     string          // the last error from owns, already logged

	changes    changesTab
	shownTrees string // the two trees being compared, used to detect a change
	shownScope Scope  // the scope that changes.files was loaded for
	allFiles   allFilesTab
	readErr    string // the last read error, already logged
	pr         prTab

	split             bool // show the diff side by side
	wrap              bool
	rendered          bool // show Markdown rendered
	navigatorPosition position
	navigatorHidden   bool
	navigatorFocused  bool // j and k move in the file list
	// crossStep is 1 or -1 when ] or [ was pressed past the last change
	// and the next press in the same direction will jump to the file at
	// crossPath. Otherwise it is 0.
	crossStep int
	crossPath string
	// navigatorWidthSet is the width in columns of the file list when it
	// is at the left or right, or 0 for the default share of the width.
	navigatorWidthSet int
	navigatorPct      int        // the percentage of body rows used by the file list when above or below
	cache             diff.Cache // the highlighted lines and diff, reused between renders
	colorsTicking     bool       // a colorsMsg is on its way; see [model.awaitColors]

	comments diff.Comments
	// commentFiles holds the files of the comments made outside the
	// Changes tab, by path, so that checking whether a comment is stale
	// reads its file again only after the file changes.
	commentFiles map[string]fileCopy
	commentInput textarea.Model
	draft        *draft
	mode         mode
	overlay      *overlay
	// recent holds the files that the user opened from the search in this
	// session, most recent first.
	recent     []string
	grepCancel context.CancelFunc // stops the code search in progress
	grepGen    int                // identifies the latest code search
	fileGen    int                // identifies the latest search of file names
	find       findState
	findSides  sideLines     // the lines that find searches
	markdown   markdownCache // the most recently used Markdown renderer
	drag       dragState
	click      clickState
	help       help.Model

	flashText  string
	flashUntil time.Time

	agents   map[string]*agentState // keyed by block
	sends    int                    // the number of send steps in progress
	lastSent string                 // the block that the last review was sent to
	beside   string                 // the agent block in the pane beside rexdiff
}

// newModel returns the program's model for the options o and the settings
// cfg.
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
	// The flag takes priority over the saved base. A saved base that does
	// not resolve is ignored.
	ctx, done := m.gitContext("startup")
	defer done()
	if m.base == "" {
		if b := o.Repo.SavedBase(ctx); b != "" {
			if _, err := o.Repo.ResolveRev(ctx, b); err == nil {
				m.base = b
			}
		}
	}
	m.defaultBase = o.Repo.DefaultBase(ctx)
	if m.base == "" {
		m.base = m.defaultBase
	}
	if tree := o.Repo.SavedTurn(ctx); tree != "" {
		m.turn = &turnState{tree: tree}
	}
	if o.Rex != nil && o.Agent != "" {
		// If Rex does not know the agent, the picker starts with no agent
		// selected, but still lists every agent.
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "rex resolve")
		m.beside, _ = o.Rex.ResolveBlock(ctx, o.Agent)
		cancel()
	}
	m.changes.pane.selStart, m.allFiles.pane.selStart, m.pr.pane.selStart = -1, -1, -1
	m.followCursor()
	m.cfg = cfg
	m.keys = defaultKeys()
	m.keys.applyConfig(cfg.Keybindings)
	m.navigatorPosition, m.navigatorWidthSet, m.navigatorPct = cfg.FileList, cfg.FileListWidth, navigatorStackPct
	m.rendered = cfg.MarkdownView == markdownViewRendered
	// Flags take priority over the settings.
	m.split = cmp.Or(o.Layout, cfg.Layout) == LayoutSplit
	m.scope = cmp.Or(o.Scope, cfg.DefaultScope)
	return m
}

// Messages used by the program.
type (
	tickMsg    struct{}
	watchedMsg struct {
		ch   vcs.Change
		err  error
		at   time.Time     // the tick that started the check
		took time.Duration // how long the check took
		// The expanded ignored directories and the open file of the Files
		// tab that the check covered.
		dirs []string
		file string
	}

	// reloadDueMsg reports that the pause after a reload has ended, so a
	// change seen during the pause need not wait for the next tick.
	reloadDueMsg struct{}

	agentsMsg struct {
		agents []rex.Agent
		err    error
		send   bool // true if the list is for sending a review, not for updating states
		gen    int  // the value of agentsGen when the listing started
	}
	sentMsg struct {
		agent rex.Agent
		sent  map[int]string // the text of each sent comment, keyed by ID
		err   error
	}
	grepMsg struct {
		gen  int // identifies the search; results of older searches are stale
		hits []vcs.Match
		more bool // git found more matches than hits holds
		err  error
	}
	prMsg struct {
		gen int // identifies the fetch; see prFetch
		pr  *vcs.PR
		err error
	}
	eventMsg rex.Event
	// editorPaneMsg is the result of asking Rex to open a split for the
	// editor of path.
	editorPaneMsg struct {
		path, script string
		err          error
	}
	// cwdMsg is Rex's answer to a request for an agent's working
	// directory.
	cwdMsg struct {
		block, cwd string
		err        error
	}
	// editorDoneMsg reports that the editor for path, running in this
	// pane, has exited.
	editorDoneMsg struct {
		path string
		err  error
	}
	// detachedMsg reports that a detached program failed. what describes
	// the program's purpose.
	detachedMsg struct {
		what string
		err  error
	}
	// releaseMsg carries a release that is newer than the running rexdiff.
	releaseMsg struct {
		release update.Release
	}
	// updatedMsg reports that the update to the new release has finished.
	updatedMsg struct {
		err error
	}
)

// requestColors are the commands that ask the terminal for its colours.
// The replies set the colours used for the diff.
var requestColors = []tea.Cmd{
	tea.RequestBackgroundColor,
	tea.RequestForegroundColor,
	requestPalette,
}

// tickInterval is the time between ticks.
const tickInterval = time.Second

// tick returns a command that sends a tickMsg after d.
func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the ticks and the watcher, asks the terminal for its
// colours, and loads the changes, the pull request and the agents.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(tickInterval), m.requestPR(true), m.startWatch(), m.startReload(), m.startFiles()}
	// After a restart the PR tab may be the first tab shown, so render it
	// now to show that loading is in progress.
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

// flashTimeout is how long a flash message stays in the footer.
const flashTimeout = 4 * time.Second

// flash shows s in the footer for flashTimeout. It quotes s if it contains
// control characters, because s can come from Rex or git.
func (m *model) flash(s string) {
	m.flashText, m.flashUntil = termtext.Quote(s), time.Now().Add(flashTimeout)
}

// requestReload asks for the changes in the scope to be read again, even
// if the trees have not changed. Any reload in progress becomes stale. The
// watcher did not cause this reload, so its next check resets it.
func (m *model) requestReload() {
	l := &m.reloads
	l.want, l.force, l.stale = true, true, l.running
	m.watchReset = true
}

// requestReloadIfChanged asks for the changes in the scope to be read
// again if the trees differ from the ones shown.
func (m *model) requestReloadIfChanged() { m.reloads.want = true }

// reloadState tracks reloads of the Changes tab. Each reload runs git in
// a command, one at a time. A request made during a reload starts another
// reload once it finishes.
type reloadState struct {
	gen     int  // identifies the latest reload; results of older reloads are stale
	running bool // reload gen is in progress
	want    bool // a reload is due
	// The due reload shows the changes even if the trees are unchanged.
	force bool
	// The view changed while reload gen was running, so its result is
	// out of date.
	stale bool
}

// gitTimeout limits a short git command that runs on the UI goroutine,
// such as one that resolves a ref.
const gitTimeout = 5 * time.Second

// gitContext returns a context for a short git command on the UI
// goroutine, which ends after gitTimeout. Call done when the command has
// finished.
func (m *model) gitContext(what string) (ctx context.Context, done func()) {
	return xctx.WithTimeout(m.ctx, gitTimeout, what)
}

// reloadTimeout limits how long a reload can take. If git hangs, the
// reload fails and the next tick tries again.
const reloadTimeout = time.Minute

// startReload returns a command for the due reload, or nil if none is due
// or one is already running. While the comment box is open, the reload
// waits until the box closes.
func (m *model) startReload() tea.Cmd {
	l := &m.reloads
	if !l.want || l.running || m.mode == modeComment {
		return nil
	}
	l.gen++
	l.running, l.want, l.stale = true, false, false
	in, ctx := m.reloadInput(l.force), m.ctx
	l.force = false
	return func() tea.Msg { return in.run(ctx) }
}

// reloadInput copies what a reload needs from the model, so that the
// reload can run in a command.
func (m *model) reloadInput(force bool) reloadInput {
	in := reloadInput{
		repo: m.repo, gen: m.reloads.gen, scope: m.scope,
		base: m.base, oldest: m.oldest, newest: m.newest, force: force, shownTrees: m.shownTrees,
		prevFiles: make(map[string]changedFile, len(m.changes.files)),
	}
	for _, f := range m.changes.files {
		in.prevFiles[f.Path] = f
	}
	switch {
	case m.scope == scopeCommits && m.newest == "":
		in.note = "no commits picked: press " + firstKey(m.keys.CommitPick) +
			" to pick one or a range"
	case m.scope == ScopeLastTurn && m.turn == nil:
		in.note = m.turnWaitNote()
	case m.scope == ScopeLastTurn:
		in.turnTree = m.turn.tree
	}
	return in
}

// reloadInput is the input to a reload. note explains why there is
// nothing to compare, or is "".
type reloadInput struct {
	repo                       *vcs.Repo
	gen                        int
	scope                      Scope
	base, oldest, newest, note string
	turnTree, shownTrees       string
	force                      bool
	prevFiles                  map[string]changedFile // the files currently shown, keyed by path
}

// changesMsg is the result of a reload.
type changesMsg struct {
	gen       int
	trees     string // the two trees, as "from..to"
	unchanged bool   // the trees match those shown and the reload was not forced
	files     []changedFile
	note      string
	tooMany   int // the number of changed files, if there are too many to show
	err       error
	start     time.Time     // when the reload started
	elapsed   time.Duration // how long it took
}

// run reads the changes described by in. It counts added and deleted
// lines only for files whose content differs from in.prevFiles.
func (in reloadInput) run(ctx context.Context) changesMsg {
	msg := changesMsg{gen: in.gen, note: in.note, start: time.Now()}
	ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "reload")
	defer cancel()
	if msg.note == "" {
		oldTree, newTree, err := in.trees(ctx)
		msg.trees, msg.err = oldTree+".."+newTree, err
		msg.unchanged = err == nil && msg.trees == in.shownTrees && !in.force
		if err == nil && !msg.unchanged {
			msg.files, msg.err = in.changes(ctx, oldTree, newTree)
		}
		if e, ok := errors.AsType[*vcs.TooManyFilesError](msg.err); ok {
			msg.note, msg.tooMany, msg.err = fmt.Sprintf("%d files changed — too many to show", e.Count), e.Count, nil
		}
	}
	msg.elapsed = time.Since(msg.start)
	return msg
}

// trees returns the IDs of the two trees that the Changes tab compares
// for the scope.
func (in reloadInput) trees(ctx context.Context) (oldTree, newTree string, err error) {
	switch in.scope {
	case scopeCommits:
		if oldTree, err = in.repo.ParentTree(ctx, in.oldest); err != nil {
			return "", "", err
		}
		newTree, err = in.repo.Tree(ctx, in.newest)
		return oldTree, newTree, err
	case ScopeLastTurn:
		oldTree = in.turnTree
	case ScopeBranch:
		oldTree, err = in.repo.MergeBaseTree(ctx, in.base)
	default:
		oldTree, err = in.repo.HeadTree(ctx)
	}
	if err != nil {
		return "", "", err
	}
	newTree, err = in.repo.SnapshotTree(ctx)
	return oldTree, newTree, err
}

// changes returns the changed files between oldTree and newTree, with
// their line counts.
func (in reloadInput) changes(ctx context.Context, oldTree, newTree string) ([]changedFile, error) {
	files, err := in.repo.Diff(ctx, oldTree, newTree)
	if err != nil {
		return nil, err
	}
	out := make([]changedFile, len(files))
	var count []int // indexes of the files whose lines need counting
	for i, f := range files {
		c := &out[i]
		if p, ok := in.prevFiles[f.Path]; ok && p.OldText == f.OldText && p.NewText == f.NewText {
			*c = p // unchanged, so reuse the previous counts
		} else if f.Err == nil {
			count = append(count, i)
		}
		c.File = f
	}
	// Count in parallel on all cores. Each goroutine writes only to its
	// own elements of out, so no locking is needed.
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(count)) {
		wg.Go(func() {
			for k := int(next.Add(1)) - 1; k < len(count); k = int(next.Add(1)) - 1 {
				c := &out[count[k]]
				c.added, c.deleted = diff.CountLines(c.OldText, c.NewText)
				c.oldHash = textHash(c.OldText)
			}
		})
	}
	wg.Wait()
	return out, nil
}

// textHash returns the 64-bit FNV-1a hash of s.
func textHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s)) // writing to a hash.Hash never fails
	return h.Sum64()
}

// onChanges shows the result of a reload. After a failure, a later tick
// tries again after a delay. A failure keeps the files that are shown, and
// the error replaces them only if no files are shown. If the view changed
// during the reload, another reload starts.
func (m *model) onChanges(msg changesMsg) {
	l := &m.reloads
	if msg.gen != l.gen {
		return
	}
	l.running = false
	if l.stale || m.mode == modeComment {
		// Do not change the view while the comment box is open. The reload
		// runs again later.
		l.want, l.stale = true, false
		return
	}
	// The first load reads files and objects that are not yet cached, so
	// its duration says nothing about later reloads. After a failure, the
	// retry delay applies instead.
	m.reloadAt = time.Time{}
	if m.changesLoaded && msg.err == nil {
		m.reloadAt = msg.start.Add(min(reloadPauseFactor*msg.elapsed, maxReloadPause))
	}
	m.changesLoaded = true
	m.retry(msg.err)
	note, err := msg.note, msg.err
	if err == nil && note == "" && msg.unchanged {
		return
	}
	if err != nil && len(m.changes.files) > 0 && m.shownScope == m.scope {
		m.flash("refresh failed: " + err.Error())
		return
	}
	if err != nil {
		note = err.Error()
	}
	m.shownScope = m.scope
	if note != "" {
		// A note for too many files comes with the trees, so that the next
		// reload of the same trees does nothing.
		m.changes.pane.note, m.changes.files, m.shownTrees = note, nil, ""
		m.changes.tooMany = msg.tooMany
		if err == nil {
			m.shownTrees = msg.trees
		}
		m.buildBothNavigators()
		m.renderChanges()
		return
	}
	var curPath string
	if f := m.curChangedFile(); f != nil {
		curPath = f.Path
	}
	m.changes.files, m.changes.tooMany = msg.files, 0
	m.shownTrees, m.changes.pane.note = msg.trees, ""
	m.changes.cur = slices.IndexFunc(m.changes.files, func(f changedFile) bool { return f.Path == curPath })
	m.buildBothNavigators()
	if f := m.curChangedFile(); f == nil || f.Path != curPath {
		// A different file is now shown, so reset the cursor and the
		// selection, but keep the horizontal scroll.
		m.changes.pane.cur, m.changes.pane.top, m.changes.pane.selStart = 0, 0, -1
	}
	if len(m.changes.files) == 0 {
		m.changes.pane.note = "no changes"
	}
	m.renderChanges()
}

// ensureAllFiles starts reading the entries of the Files tab if they
// have not been read yet.
func (m *model) ensureAllFiles() {
	if m.allFiles.entries == nil {
		m.allFiles.listing.want = true
	}
}

// reloadAllFiles reads the shown file of the Files tab again, and starts
// reading the entries again.
func (m *model) reloadAllFiles() {
	m.allFilesPending = false
	m.allFiles.listing.want = true
	m.reloadShownFile()
}

// listState tracks the reads of the entries of the Files tab. Each read
// runs git in a command, one at a time. A request made during a read
// starts another read once it finishes.
type listState struct {
	gen     int  // identifies the latest read; results of older reads are stale
	running bool // read gen is in progress
	want    bool // a read is due
}

// maxListedFiles is the most entries that the Files tab lists. Building
// the tree of the file list takes time in proportion to the entries.
const maxListedFiles = 200_000

// filesMsg is the result of reading the entries of the Files tab. more
// counts the entries left out past maxListedFiles.
type filesMsg struct {
	gen     int
	entries []vcs.Entry
	more    int
	err     error
}

// startFiles returns a command for the due read of the entries of the
// Files tab, or nil if none is due or one is already running. While the
// comment box is open, the read waits until the box closes.
func (m *model) startFiles() tea.Cmd {
	l := &m.allFiles.listing
	if !l.want || l.running || m.mode == modeComment {
		return nil
	}
	l.gen++
	l.running, l.want = true, false
	repo, ctx, gen := m.repo, m.ctx, l.gen
	expanded := maps.Clone(m.allFiles.navigator.toggled)
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "file list")
		defer cancel()
		es, err := repo.Files(ctx)
		if err != nil {
			return filesMsg{gen: gen, err: err}
		}
		es = withIgnored(repo, es, expanded)
		more := max(len(es)-maxListedFiles, 0)
		return filesMsg{gen: gen, entries: es[:len(es)-more], more: more}
	}
}

// fileJump is a jump to a line of a file in the Files tab.
type fileJump struct {
	path string
	side diff.Side
	line int
}

// onFiles shows the entries read for the Files tab, keeping the current
// file selected. On error, it keeps the old list and shows the error. It
// then does the jump that waited for the entries, if any. It returns the
// command that searches the new entries if the search is open.
func (m *model) onFiles(msg filesMsg) tea.Cmd {
	l, af := &m.allFiles.listing, &m.allFiles
	if msg.gen != l.gen {
		return nil
	}
	l.running, af.listed = false, true
	jump := af.jump
	af.jump = nil
	if msg.err != nil {
		m.flash("file list failed: " + msg.err.Error())
		return nil
	}
	var cur string
	if af.cur < len(af.entries) {
		cur = af.entries[af.cur].Path
	}
	s := m.resumeFiles
	if s != nil {
		cur = s.FilePath
		af.navigator.cur = -1 // put the list cursor on the current file
	}
	af.entries, af.more = msg.entries, msg.more
	if af.entries == nil {
		af.entries = []vcs.Entry{} // read, though empty
	}
	af.cur = slices.IndexFunc(af.entries, func(e vcs.Entry) bool { return e.Path == cur })
	m.buildAllFilesNavigator()
	m.reloadShownFile()
	if s != nil {
		m.resumeFiles = nil
		af.pane.restore(s.Viewer)
	}
	if jump != nil {
		m.jumpToFile(jump.path, jump.side, jump.line)
	}
	if ov := m.overlay; ov != nil && ov.kind == overlaySearch {
		ov.search.paths = searchPaths(af.entries)
		cmd := m.findFiles()
		ov.setSearchItems()
		return cmd
	}
	return nil
}

// buildBothNavigators rebuilds the rows of both file lists after the
// Changes tab changes, because the Files tab marks directories that
// contain changes.
func (m *model) buildBothNavigators() {
	m.buildChangesNavigator()
	if m.allFiles.entries != nil {
		m.buildAllFilesNavigator()
	}
}

// Update handles a message. After a self-update, it restarts the program
// once no work can be lost.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.holdInput(msg) {
		m.held = append(m.held, msg)
	} else {
		cmd = m.handleMsg(msg)
	}
	// Start any reload that handling the messages requested.
	cmd = tea.Batch(cmd, m.releaseInput(), m.startReload(), m.startFiles())
	// The colours only draw the screen again, so they are not pending work.
	colors := m.awaitColors()
	if m.updatePhase != updateDone {
		return m, tea.Batch(cmd, colors)
	}
	// The new process runs ticks and watcher checks itself, so they do
	// not count as pending work.
	hasPending := cmd != nil
	switch msg.(type) {
	case tickMsg, watchedMsg, reloadDueMsg:
		hasPending = false
	}
	if m.restartIfUpdated(hasPending) {
		return m, tea.Quit
	}
	return m, tea.Batch(cmd, colors)
}

// waiting reports whether the shown tab is still loading the data that
// key presses act on: the first changes, or the first list of the Files
// tab.
func (m *model) waiting() bool {
	switch m.tab {
	case tabChanges:
		return !m.changesLoaded
	case tabAllFiles:
		return !m.allFiles.listed
	}
	return false
}

// holdInput reports whether Update must hold the input msg until the tab
// stops [model.waiting], as if the program were busy loading. Input is
// then handled in the order it came. A key that quits or switches tabs
// does not wait if no other input is held, so the user can leave a tab
// that loads slowly.
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

// releaseInput handles the held input, in order, until a tab is waiting
// again.
func (m *model) releaseInput() tea.Cmd {
	var cmds []tea.Cmd
	for len(m.held) > 0 && !m.waiting() {
		msg := m.held[0]
		m.held = m.held[1:]
		cmds = append(cmds, m.handleMsg(msg))
	}
	return tea.Batch(cmds...)
}

// handleMsg does the work of Update for msg.
func (m *model) handleMsg(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case changesMsg:
		m.onChanges(msg)
		m.restoreChanges()
		if wait := time.Until(m.reloadAt); wait > 0 {
			cmd = tea.Tick(wait, func(time.Time) tea.Msg { return reloadDueMsg{} })
		}
	case reloadDueMsg:
		m.reloadIfDue()
	case filesMsg:
		cmd = m.onFiles(msg)
	case fileSearchMsg:
		m.onFileSearch(msg)
	case watchStartedMsg:
		m.onWatchStarted(msg)
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
		cmd = tea.Batch(tick(tickInterval), m.onTick())
	case colorsMsg:
		m.colorsTicking = false // Update asks for the next one
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
		// Find the clicked row on the current screen first, then make the
		// view follow the cursor.
		cmd = m.onClick(msg.Mouse())
		m.followCursor()
		m.scrollToCursor()
		return cmd
	case tea.MouseMotionMsg:
		m.onDrag(msg.Mouse())
	case tea.MouseReleaseMsg:
		cmd = m.onMouseUp(msg.Mouse()) // may open the comment box, which can be off screen
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

// followCursor makes every pane and file list keep its cursor on screen
// again.
func (m *model) followCursor() {
	m.changes.pane.following, m.allFiles.pane.following, m.pr.pane.following = true, true, true
	m.changes.navigator.following, m.allFiles.navigator.following = true, true
}

// onKey passes a key press to the part of the screen that has focus.
func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	m.log.Debug("key", "key", m.loggedKey(msg), "mode", m.mode, "scope", m.scope, "cur", m.curPane().cur, "top", m.curPane().top, "h", m.mainHeight(), "shownH", m.shownMainHeight(), "follow", m.curPane().following)
	// Any key other than ], [ or Esc cancels a pending jump to another
	// file, except keys pressed in the quit prompt.
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

// loggedKey returns the name of the key press msg for the debug log. While
// the user types into the comment box or another text input, the log must
// not hold what they type, so it names only keys that type no text, such
// as enter, and gives "text" for the others.
func (m *model) loggedKey(msg tea.KeyPressMsg) string {
	typing := m.mode == modeComment || m.mode == modeFind ||
		m.mode == modeOverlay && m.overlay != nil && m.overlay.hasInput()
	if typing && msg.Text != "" {
		return "text"
	}
	return msg.String()
}

// onPaste inserts pasted text into the comment box, the overlay input or
// the find bar. Line breaks become \n; the one-line inputs turn them into
// spaces.
func (m *model) onPaste(text string) tea.Cmd {
	text = unixLines(text)
	switch {
	case m.mode == modeComment:
		// The text area scrolls its view to the cursor only in Update.
		var cmd tea.Cmd
		m.commentInput, cmd = m.commentInput.Update(tea.PasteMsg{Content: text})
		m.scrollToCursor()
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

// unixLines replaces each \r\n and each lone \r in s with \n. Terminals
// often paste \r\n, which the inputs would otherwise read as two line
// breaks.
func unixLines(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// colorTicks is how often, in one-second ticks, rexdiff asks the terminal
// for its colours.
const colorTicks = 3

// Delays between reloads.
const (
	// reloadPauseFactor sets the pause between reloads: the next reload
	// starts this many times as long as the last one took after the start
	// of the last one, but at most maxReloadPause after it, so reloading
	// takes at most 1/reloadPauseFactor of the time. The watcher in vcs
	// has its own pause.
	reloadPauseFactor = 20
	maxReloadPause    = 30 * time.Second
	minRetryDelay     = time.Second // the first delay after a failure
	maxRetryDelay     = time.Minute // the longest delay after a failure
)

// reloadInterval is the time between reloads when there is no watcher.
const reloadInterval = 5 * time.Second

// retryTimer schedules the next attempt after a failure. Each failure
// doubles the delay, from minRetryDelay up to maxRetryDelay.
type retryTimer struct {
	at   time.Time     // the time of the next attempt, or zero
	wait time.Duration // the delay to use after the next failure, or 0
}

// fail records a failure at now and schedules the next attempt.
func (r *retryTimer) fail(now time.Time) {
	w := max(r.wait, minRetryDelay)
	r.at, r.wait = now.Add(w), min(2*w, maxRetryDelay)
}

// due reports whether an attempt is scheduled at or before now.
func (r *retryTimer) due(now time.Time) bool {
	return !r.at.IsZero() && !now.Before(r.at)
}

// retry schedules the next reload after the failure err, with a delay
// that doubles on each failure. A nil err clears the delay.
func (m *model) retry(err error) {
	if err == nil {
		m.reloadRetry = retryTimer{}
		return
	}
	m.reloadRetry.fail(time.Now())
	m.reloadPending = true
}

// stopWatch logs err and drops the watcher. A later tick starts it again
// after a delay.
func (m *model) stopWatch(err error) {
	m.log.Warn("watch failed", "err", err)
	m.watch = nil
	m.watchRetry.fail(time.Now())
}

// watchStartedMsg carries a new watcher, or the error from starting it.
type watchStartedMsg struct {
	w   *vcs.Watcher
	err error
}

// startWatch returns a command that starts watching the repository for
// changes. Starting lists the files, which can take seconds in a large
// repository.
func (m *model) startWatch() tea.Cmd {
	m.watchStarting = true
	repo, ctx := m.repo, m.ctx
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "watch start")
		defer cancel()
		w, err := repo.Watch(ctx)
		return watchStartedMsg{w: w, err: err}
	}
}

// onWatchStarted keeps the new watcher. After an error, a later tick
// tries again.
func (m *model) onWatchStarted(msg watchStartedMsg) {
	m.watchStarting = false
	if msg.err != nil {
		m.stopWatch(msg.err)
		return
	}
	m.watch, m.watchRetry.at = msg.w, time.Time{}
}

// watchChanges reports the changes seen on a tick. With a watcher, it
// returns a command that checks the watcher, and onWatched handles the
// result. Only one check runs at a time, and a tick during a check sees no
// change. Without a watcher, it reports that both files and refs changed
// once every reloadInterval. While the watcher starts, it reports no
// change: the first check of the new watcher reports both kinds.
func (m *model) watchChanges() (vcs.Change, tea.Cmd) {
	now := time.Now()
	switch {
	case m.watchStarting:
		return vcs.Change{}, nil
	case m.watch == nil && m.watchRetry.due(now):
		return vcs.Change{}, m.startWatch()
	case m.watch == nil && now.Sub(m.polled) < reloadInterval:
		return vcs.Change{}, nil
	case m.watch == nil:
		m.polled = now
		return vcs.Change{Files: true, Refs: true}, nil
	case m.watching:
		return vcs.Change{}, nil
	}
	m.watching = true
	w, ctx, reset := m.watch, m.ctx, m.watchReset
	m.watchReset = false
	dirs, file := m.includedPaths()
	paths := dirs
	if file != "" {
		paths = append(slices.Clip(dirs), file)
	}
	return vcs.Change{}, func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "watch")
		defer cancel()
		w.Include(paths)
		if reset {
			w.Reset()
		}
		ch, err := w.Changed(ctx)
		return watchedMsg{ch: ch, err: err, at: now, took: time.Since(now), dirs: dirs, file: file}
	}
}

// onWatched handles the result of a watcher check. After an error, it
// drops the watcher and treats both files and refs as changed.
func (m *model) onWatched(msg watchedMsg) tea.Cmd {
	m.watching = false
	m.log.Debug("watch", "took", msg.took, "files", msg.ch.Files, "refs", msg.ch.Refs)
	if msg.err != nil {
		m.stopWatch(msg.err)
		m.polled = msg.at
		return m.onChange(vcs.Change{Files: true, Refs: true})
	}
	m.watchRetry = retryTimer{}
	// The watcher misses writes made to a path before Include added it, so
	// when the watched paths change, the Files tab rereads what the new
	// paths cover: just the shown file if only the shown file is new,
	// otherwise the file list as well. The reload waits while another tab
	// is shown or the comment box is open.
	sameDirs := slices.Equal(msg.dirs, m.watchedDirs)
	if !sameDirs || msg.file != m.watchedFile {
		switch {
		case m.tab != tabAllFiles || m.mode == modeComment:
			m.allFilesPending = true
		case sameDirs:
			m.reloadShownFile()
		default:
			m.reloadAllFiles()
		}
	}
	m.watchedDirs, m.watchedFile = msg.dirs, msg.file
	return m.onChange(msg.ch)
}

// onTick checks for changes and does the periodic work. Update schedules
// the next tick.
func (m *model) onTick() tea.Cmd {
	m.ticks++
	ch, check := m.watchChanges()
	if check == nil {
		check = m.onChange(ch)
	}
	cmds := []tea.Cmd{check}
	now := time.Now()
	if m.flashText != "" && now.After(m.flashUntil) {
		m.flashText = ""
	}
	if m.rex != nil && !m.agentsSampled && m.sampleRetry.due(now) {
		m.sampleRetry.at = time.Time{} // only one listing at a time
		cmds = append(cmds, m.fetchAgents(agentsMsg{}))
	}
	if m.tab == tabPR && time.Since(m.pr.fetch.polled) >= prInterval {
		cmds = append(cmds, m.requestPR(false))
	}
	if m.ticks%colorTicks == 0 { // pick up changes to the terminal theme
		cmds = append(cmds, requestColors...)
	}
	return tea.Batch(cmds...)
}

// onChange reloads the current tab if ch reports a change or one is
// pending. Each tick calls it once.
func (m *model) onChange(ch vcs.Change) tea.Cmd {
	var cmds []tea.Cmd
	if ch.Files && m.pendingTurn != nil {
		cmds = append(cmds, m.scheduleTurnCheck(false))
	}
	m.reloadPending = m.reloadPending || ch.Files || ch.Refs
	m.allFilesPending = m.allFilesPending || ch.Files
	m.reloadIfDue()
	// Without a watcher, every tick reports a ref change, so leave the
	// pull request to its own polling.
	if ch.Refs && m.watch != nil {
		cmds = append(cmds, m.reloadPR())
	}
	return tea.Batch(cmds...)
}

// reloadIfDue reloads the current tab if a change is pending. On the
// Changes tab, the reload also waits for the pause after the last reload
// and for any retry delay. Nothing is reloaded while the comment box is
// open.
func (m *model) reloadIfDue() {
	if m.mode == modeComment {
		return
	}
	now := time.Now()
	switch {
	case m.tab == tabChanges && m.reloadPending && !m.reloads.running &&
		!now.Before(m.reloadAt) && !now.Before(m.reloadRetry.at):
		m.reloadPending = false
		m.requestReloadIfChanged() // onChanges sets the next reloadAt
	case m.tab == tabAllFiles && m.allFilesPending:
		m.reloadAllFiles() // also renders the shown file
	case m.tab == tabAllFiles && m.watch == nil:
		m.reloadShownFile() // without a watcher, every tick reads the file again
	}
}

// curPane returns the pane of the current tab.
func (m *model) curPane() *pane {
	switch m.tab {
	case tabAllFiles:
		return &m.allFiles.pane
	case tabPR:
		return &m.pr.pane
	}
	return &m.changes.pane
}

// sameColor reports whether a and b are the same colour, ignoring alpha.
// A nil colour equals only another nil.
func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	return ar == br && ag == bg && ab == bb
}
