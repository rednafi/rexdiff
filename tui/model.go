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
	ctx  context.Context
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

	// watch is nil if there is no watcher. A tick then reloads each
	// reloadInterval since polled.
	watch         *vcs.Watcher
	watchStarting bool
	watching      bool
	// watchReset makes the next check call [vcs.Watcher.Reset] first. The
	// call is in the check command, because Reset waits for a check in
	// progress.
	watchReset      bool
	watchedDirs     []string
	watchedFile     string
	polled          time.Time
	watchRetry      retryTimer
	reloadPending   bool
	allFilesPending bool
	reloadAt        time.Time
	reloadRetry     retryTimer
	reloads         reloadState
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

	changes    changesTab
	shownTrees string
	shownScope Scope
	allFiles   allFilesTab
	readErr    string // already logged
	pr         prTab

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
	grepCancel   context.CancelFunc
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
		// If Rex does not know the agent, the picker starts with none selected.
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
	m.split = cmp.Or(o.Layout, cfg.Layout) == LayoutSplit
	m.scope = cmp.Or(o.Scope, cfg.DefaultScope)
	return m
}

type (
	tickMsg    struct{}
	watchedMsg struct {
		ch   vcs.Change
		err  error
		at   time.Time
		took time.Duration
		dirs []string
		file string
	}

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

const tickInterval = time.Second

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{} })
}

// Init starts the ticks, the watcher and the first loads.
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{tick(tickInterval), m.requestPR(true), m.startWatch(), m.startReload(), m.startFiles()}
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

// requestReload reloads even if the trees are the same. The watcher did
// not cause it, so the next check resets the watcher.
func (m *model) requestReload() {
	l := &m.reloads
	l.want, l.force, l.stale = true, true, l.running
	m.watchReset = true
}

func (m *model) requestReloadIfChanged() { m.reloads.want = true }

type reloadState struct {
	gen     int
	running bool
	want    bool
	force   bool // show the changes even if the trees are the same
	stale   bool // the view changed during reload gen
}

const gitTimeout = 5 * time.Second

func (m *model) gitContext(what string) (ctx context.Context, done func()) {
	return xctx.WithTimeout(m.ctx, gitTimeout, what)
}

// reloadTimeout stops a reload if git hangs.
const reloadTimeout = time.Minute

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

type reloadInput struct {
	repo                       *vcs.Repo
	gen                        int
	scope                      Scope
	base, oldest, newest, note string
	turnTree, shownTrees       string
	force                      bool
	prevFiles                  map[string]changedFile
}

type changesMsg struct {
	gen       int
	trees     string // the two trees, as "from..to"
	unchanged bool
	files     []changedFile
	note      string
	tooMany   int
	err       error
	start     time.Time
	elapsed   time.Duration
}

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

func (in reloadInput) changes(ctx context.Context, oldTree, newTree string) ([]changedFile, error) {
	files, err := in.repo.Diff(ctx, oldTree, newTree)
	if err != nil {
		return nil, err
	}
	out := make([]changedFile, len(files))
	var count []int
	for i, f := range files {
		c := &out[i]
		if p, ok := in.prevFiles[f.Path]; ok && p.OldText == f.OldText && p.NewText == f.NewText {
			*c = p
		} else if f.Err == nil {
			count = append(count, i)
		}
		c.File = f
	}
	// Each goroutine writes only its own elements of out, so no lock is
	// necessary.
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

func textHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// onChanges keeps the shown files after a failure. The error replaces
// them only if no files show.
func (m *model) onChanges(msg changesMsg) {
	l := &m.reloads
	if msg.gen != l.gen {
		return
	}
	l.running = false
	if l.stale || m.mode == modeComment {
		l.want, l.stale = true, false
		return
	}
	// The first load is uncached, so its duration tells nothing about
	// later reloads. After a failure, the retry delay applies instead.
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
		// A note for too many files keeps the trees. A reload of the same
		// trees then does nothing.
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
		m.changes.pane.cur, m.changes.pane.top, m.changes.pane.selStart = 0, 0, -1
	}
	if len(m.changes.files) == 0 {
		m.changes.pane.note = "no changes"
	}
	m.renderChanges()
}

func (m *model) ensureAllFiles() {
	if m.allFiles.entries == nil {
		m.allFiles.listing.want = true
	}
}

func (m *model) reloadAllFiles() {
	m.allFilesPending = false
	m.allFiles.listing.want = true
	m.reloadShownFile()
}

type listState struct {
	gen     int
	running bool
	want    bool
}

// maxListedFiles exists because the file list tree takes time in
// proportion to the entries.
const maxListedFiles = 200_000

type filesMsg struct {
	gen     int
	entries []vcs.Entry
	more    int
	err     error
}

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

type fileJump struct {
	path string
	side diff.Side
	line int
}

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

func (m *model) buildBothNavigators() {
	m.buildChangesNavigator()
	if m.allFiles.entries != nil {
		m.buildAllFilesNavigator()
	}
}

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
	// The colours only draw the screen again, so they are not pending work.
	colors := m.awaitColors()
	if m.updatePhase != updateDone {
		return m, tea.Batch(cmd, colors)
	}
	// The new process runs its own ticks and watcher checks. They are not
	// pending work.
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
		m.followCursor()
		m.scrollToCursor()
		return cmd
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

// unixLines exists because terminals often paste \r\n, which the inputs
// read as two line breaks.
func unixLines(s string) string {
	return strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
}

// colorTicks is in one-second ticks.
const colorTicks = 3

const (
	// The next reload starts reloadPauseFactor times the last duration
	// after the last start. Reloads thus use at most 1/reloadPauseFactor
	// of the time.
	reloadPauseFactor = 20
	maxReloadPause    = 30 * time.Second
	minRetryDelay     = time.Second
	maxRetryDelay     = time.Minute
)

const reloadInterval = 5 * time.Second

type retryTimer struct {
	at   time.Time
	wait time.Duration
}

func (r *retryTimer) fail(now time.Time) {
	w := max(r.wait, minRetryDelay)
	r.at, r.wait = now.Add(w), min(2*w, maxRetryDelay)
}

func (r *retryTimer) due(now time.Time) bool {
	return !r.at.IsZero() && !now.Before(r.at)
}

func (m *model) retry(err error) {
	if err == nil {
		m.reloadRetry = retryTimer{}
		return
	}
	m.reloadRetry.fail(time.Now())
	m.reloadPending = true
}

func (m *model) stopWatch(err error) {
	m.log.Warn("watch failed", "err", err)
	m.watch = nil
	m.watchRetry.fail(time.Now())
}

type watchStartedMsg struct {
	w   *vcs.Watcher
	err error
}

// startWatch runs in a command, because listing the files can take
// seconds in a large repository.
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

func (m *model) onWatchStarted(msg watchStartedMsg) {
	m.watchStarting = false
	if msg.err != nil {
		m.stopWatch(msg.err)
		return
	}
	m.watch, m.watchRetry.at = msg.w, time.Time{}
}

// one, it reports files and refs as changed each reloadInterval.
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

func (m *model) onWatched(msg watchedMsg) tea.Cmd {
	m.watching = false
	m.log.Debug("watch", "took", msg.took, "files", msg.ch.Files, "refs", msg.ch.Refs)
	if msg.err != nil {
		m.stopWatch(msg.err)
		m.polled = msg.at
		return m.onChange(vcs.Change{Files: true, Refs: true})
	}
	m.watchRetry = retryTimer{}
	// The watcher misses writes to a path before Include adds it. So when
	// the watched paths change, read them again.
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
		m.sampleRetry.at = time.Time{}
		cmds = append(cmds, m.fetchAgents(agentsMsg{}))
	}
	if m.tab == tabPR && time.Since(m.pr.fetch.polled) >= prInterval {
		cmds = append(cmds, m.requestPR(false))
	}
	if m.ticks%colorTicks == 0 {
		cmds = append(cmds, requestColors...)
	}
	return tea.Batch(cmds...)
}

func (m *model) onChange(ch vcs.Change) tea.Cmd {
	var cmds []tea.Cmd
	if ch.Files && m.pendingTurn != nil {
		cmds = append(cmds, m.scheduleTurnCheck(false))
	}
	m.reloadPending = m.reloadPending || ch.Files || ch.Refs
	m.allFilesPending = m.allFilesPending || ch.Files
	m.reloadIfDue()
	// Without a watcher, every tick reports a ref change. The PR then
	// uses its own polling.
	if ch.Refs && m.watch != nil {
		cmds = append(cmds, m.reloadPR())
	}
	return tea.Batch(cmds...)
}

func (m *model) reloadIfDue() {
	if m.mode == modeComment {
		return
	}
	now := time.Now()
	switch {
	case m.tab == tabChanges && m.reloadPending && !m.reloads.running &&
		!now.Before(m.reloadAt) && !now.Before(m.reloadRetry.at):
		m.reloadPending = false
		m.requestReloadIfChanged()
	case m.tab == tabAllFiles && m.allFilesPending:
		m.reloadAllFiles()
	case m.tab == tabAllFiles && m.watch == nil:
		m.reloadShownFile() // without a watcher, every tick reads the file again
	}
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
