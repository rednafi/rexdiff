package tui

import (
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/rex"
)

type member struct {
	block, app string
	state      rex.State
	since      time.Time // when Rex received the state
}

// If a directory is not known, ok is false. Then ask is that agent's block
// if Rex can tell the directory.
func (m *model) turnMembers() (ms []member, ask string, ok bool) {
	for _, block := range slices.Sorted(maps.Keys(m.agents)) {
		a := m.agents[block]
		if a.state == "" || a.app == appName || (m.rex != nil && block == m.rex.Block()) {
			continue
		}
		if !a.hasCwd {
			if m.rex == nil {
				return nil, "", false
			}
			return nil, block, false
		}
		in, err := m.owns(a.cwd)
		if err != nil {
			// Every event tries again, so log each distinct error only
			// once.
			if text := err.Error(); text != m.ownsErr {
				m.ownsErr = text
				m.log.Warn("agent membership failed", "err", err)
			}
			return nil, "", false
		}
		if in {
			ms = append(ms, member{block, a.app, a.state, a.since})
		}
	}
	m.ownsErr = ""
	return ms, "", true
}

func (m *model) askCwd(block string) tea.Cmd {
	a := m.agents[block]
	if a.asking {
		return nil
	}
	a.asking = true
	c, ctx := m.rex, m.ctx
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "rex cwd")
		defer cancel()
		cwd, err := c.Cwd(ctx, block)
		return cwdMsg{block: block, cwd: cwd, err: err}
	}
}

// onCwd lets the next state change ask again after an error.
func (m *model) onCwd(msg cwdMsg) tea.Cmd {
	a := m.agents[msg.block]
	if a == nil {
		return nil // the block has closed
	}
	a.asking = false
	if msg.err != nil {
		m.log.Debug("agent cwd failed", "block", msg.block, "err", msg.err)
		return nil
	}
	a.cwd, a.hasCwd = msg.cwd, true
	return m.observeTurn()
}

// owns caches each answer. The top of the working tree does not move, and
// every Rex event asks again on the UI goroutine.
func (m *model) owns(dir string) (bool, error) {
	if in, ok := m.ownsDir[dir]; ok {
		return in, nil
	}
	ctx, done := m.gitContext("owns")
	in, err := m.repo.Owns(ctx, dir)
	done()
	if err == nil {
		if m.ownsDir == nil {
			m.ownsDir = map[string]bool{}
		}
		m.ownsDir[dir] = in
	}
	return in, err
}

func (m *model) observeTurn() tea.Cmd {
	if !m.agentsSampled {
		return nil
	}
	ms, ask, ok := m.turnMembers()
	if ask != "" {
		return m.askCwd(ask)
	}
	if !ok {
		return nil // keep the previous state
	}
	here := len(ms) > 0
	if m.agentInRepo == nil || *m.agentInRepo != here {
		m.agentInRepo = new(here)
		if m.scope == ScopeLastTurn && m.turn == nil {
			m.requestReload() // show the updated waiting message
		}
	}
	states := make([]rex.State, len(ms))
	for i, a := range ms {
		states[i] = a.state
	}
	started, ended := m.tracker.Observe(states...)
	if started {
		return m.startTurn(ms)
	}
	if ended {
		return m.endTurn()
	}
	return nil
}

func (m *model) turnWaitNote() string {
	if m.turnRaced {
		return "the last turn started before rexdiff could snapshot it"
	}
	if m.agentInRepo != nil && !*m.agentInRepo {
		return "no agent works here"
	}
	return "waiting for the first turn"
}

// raceBefore is how long an agent can write files before Rex receives the
// working state.
const raceBefore = time.Second

func (m *model) startTurn(ms []member) tea.Cmd {
	m.pendingTurn = nil
	m.turnGen++
	// A turn starts only when some member is working, so i is valid.
	i := slices.IndexFunc(ms, func(a member) bool { return a.state == rex.StateWorking })
	gen, a, repo, watch, ctx := m.turnGen, ms[i], m.repo, m.watch, m.ctx
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "turn snapshot")
		defer cancel()
		tree, err := repo.SnapshotTree(ctx)
		msg := turnStartMsg{gen: gen, agent: a, tree: tree, snapped: time.Now(), err: err}
		if watch != nil && err == nil {
			// LastWrite waits for a watcher check, so do not call it in
			// Update. A write after the snapshot does not hide one before it.
			w := watch.LastWrite(msg.snapped)
			msg.write = w
			msg.raced = !w.Before(a.since.Add(-raceBefore)) && !w.After(msg.snapped)
		}
		return msg
	}
}

type turnStartMsg struct {
	gen     int    // the value of model.turnGen when the snapshot started
	agent   member // the first working agent in the turn
	tree    string
	snapped time.Time // when the snapshot finished
	write   time.Time // the last write before snapped, or zero
	raced   bool      // write happened in the window just before the snapshot
	err     error
}

// onTurnStart keeps the turn pending until it changes a file. A write from
// raceBefore before the start up to the snapshot can be in the snapshot.
// Then it forgets the turn.
func (m *model) onTurnStart(msg turnStartMsg) tea.Cmd {
	if msg.gen != m.turnGen {
		return nil
	}
	if msg.err != nil {
		m.log.Warn("turn snapshot failed", "err", msg.err)
		return nil
	}
	a := msg.agent
	if msg.raced {
		m.log.Debug("turn raced", "block", a.block, "write", msg.write)
		return m.forgetTurn()
	}
	m.log.Debug("turn start", "block", a.block, "tree", msg.tree)
	m.pendingTurn = &turnState{
		app:     a.app,
		tree:    msg.tree,
		running: true,
		at:      time.Now(),
	}
	return nil
}

func (m *model) scheduleTurnCheck(end bool) tea.Cmd {
	if m.turnChecking && !end {
		return nil
	}
	m.turnChecking = m.turnChecking || !end
	p, gen, repo, ctx := m.pendingTurn, m.turnGen, m.repo, m.ctx
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "turn check")
		defer cancel()
		tree, err := repo.SnapshotTree(ctx)
		return turnCheckMsg{turn: p, gen: gen, end: end, tree: tree, err: err}
	}
}

type turnCheckMsg struct {
	turn *turnState
	gen  int  // the value of model.turnGen when the check started
	end  bool // the check was made at the end of the turn
	tree string
	err  error
}

func (m *model) onTurnCheck(msg turnCheckMsg) tea.Cmd {
	if !msg.end {
		m.turnChecking = false
	}
	switch {
	case msg.err != nil:
		m.log.Warn("turn change check failed", "err", msg.err)
	case msg.tree == msg.turn.tree:
		// Nothing changed during the turn.
	case msg.end && msg.gen == m.turnGen:
		return m.promoteTurn(msg.turn)
	case !msg.end && msg.turn == m.pendingTurn:
		return m.promoteTurn(msg.turn)
	}
	return nil
}

// forgetTurn is for a new turn whose start state is not known.
func (m *model) forgetTurn() tea.Cmd {
	m.turn, m.turnRaced = nil, true
	if m.scope == ScopeLastTurn {
		m.requestReload()
	}
	return m.saveTurn("")
}

func (m *model) promoteTurn(t *turnState) tea.Cmd {
	m.turn, m.turnRaced = t, false
	if m.pendingTurn == t {
		m.pendingTurn = nil
	}
	if m.scope == ScopeLastTurn {
		m.requestReload()
	}
	return m.saveTurn(t.tree)
}

// saveTurn deletes the saved tree if tree is empty.
func (m *model) saveTurn(tree string) tea.Cmd {
	repo, ctx, log := m.repo, m.ctx, m.log
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, reloadTimeout, "turn save")
		defer cancel()
		if err := repo.SaveTurn(ctx, tree); err != nil {
			log.Warn("turn baseline ref write failed", "err", err)
		}
		return nil
	}
}

// endTurn checks the pending turn. Changes after the end do not belong to
// the turn.
func (m *model) endTurn() tea.Cmd {
	m.turnGen++ // ignore a turn start whose snapshot has not arrived yet
	var cmd tea.Cmd
	if m.pendingTurn != nil {
		m.pendingTurn.running = false
		cmd = m.scheduleTurnCheck(true)
		m.pendingTurn = nil
	}
	if m.turn != nil {
		m.turn.running = false
	}
	if m.scope == ScopeLastTurn {
		m.requestReload()
	}
	return cmd
}
