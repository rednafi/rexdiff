package tui

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/vcs"
)

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
	pending bool
	at      time.Time
	retry   retryTimer
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
	m.reloads.pending = false // the reload sees each change until now
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
	m.reloads.at = time.Time{}
	if m.changesLoaded && msg.err == nil {
		m.reloads.at = msg.start.Add(min(reloadPauseFactor*msg.elapsed, maxReloadPause))
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
	if e := m.curEntry(); e != nil {
		cur = e.Path
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
		m.reloads.retry = retryTimer{}
		return
	}
	m.reloads.retry.fail(time.Now())
	m.reloads.pending = true
}

type watchedMsg struct {
	w    *vcs.Watcher
	ch   vcs.Change
	err  error
	took time.Duration
	dirs []string
	file string
}

// watchChanges starts a check, unless one is in progress or a failed
// check waits for its retry. The check makes a watcher if there is none.
func (m *model) watchChanges() tea.Cmd {
	now := time.Now()
	if m.watching || now.Before(m.watchRetry.at) {
		return nil
	}
	m.watching = true
	w, repo, ctx, reset := m.watch, m.repo, m.ctx, m.watchReset
	m.watchReset = false
	dirs, file := m.includedPaths()
	paths := dirs
	if file != "" {
		paths = append(slices.Clip(dirs), file)
	}
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "watch")
		defer cancel()
		if w == nil {
			// This lists the files, which can take seconds in a large
			// repository.
			var err error
			if w, err = repo.Watch(ctx); err != nil {
				return watchedMsg{err: err, took: time.Since(now)}
			}
		}
		w.Include(paths)
		if reset {
			w.Reset()
		}
		ch, err := w.Changed(ctx)
		return watchedMsg{w: w, ch: ch, err: err, took: time.Since(now), dirs: dirs, file: file}
	}
}

func (m *model) onWatched(msg watchedMsg) tea.Cmd {
	m.watching = false
	m.log.Debug("watch", "took", msg.took, "files", msg.ch.Files, "refs", msg.ch.Refs)
	if msg.err != nil {
		m.log.Warn("watch failed", "err", msg.err)
		m.watch = nil
		m.watchRetry.fail(time.Now())
		// Any file can have changed. The PR has its own polling, so a
		// failed check does not report the refs.
		return m.onChange(vcs.Change{Files: true})
	}
	m.watch, m.watchRetry = msg.w, retryTimer{}
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
	check := m.watchChanges()
	if check == nil {
		m.reloadIfDue() // a pause or a retry can end without a check
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
	m.reloads.pending = m.reloads.pending || ch.Files || ch.Refs
	m.allFilesPending = m.allFilesPending || ch.Files
	m.reloadIfDue()
	if ch.Refs {
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
	case m.tab == tabChanges && m.reloads.pending && !m.reloads.running &&
		!now.Before(m.reloads.at) && !now.Before(m.reloads.retry.at):
		m.requestReloadIfChanged()
	case m.tab == tabAllFiles && m.allFilesPending:
		m.reloadAllFiles()
	}
}
