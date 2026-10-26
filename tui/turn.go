package tui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/rex"
)

// member is an agent that works in the repository.
type member struct {
	block, app string
	state      rex.State
	since      time.Time // when Rex got the state
}

// members returns the agents that work in the repository, in the order
// of their blocks. As in herdr-reviewr, an agent is a member if its
// working directory is absolute and in this working tree. If the place of
// an agent is not known, members returns false, and the block of the
// agent if Rex can tell its place.
func (m *model) members() (ms []member, ask string, ok bool) {
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
			// Each event asks again. The log has each error once.
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

// askCwd returns a command that asks Rex for the working directory of the
// agent in block. The answer comes as a cwdMsg.
func (m *model) askCwd(block string) tea.Cmd {
	a := m.agents[block]
	if a.asking {
		return nil
	}
	a.asking = true
	c, ctx := m.rex, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, rexTimeout,
			fmt.Errorf("rex cwd: %v timeout: %w", rexTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("rex cwd: done: %w", context.Canceled))
		cwd, err := c.Cwd(ctx, block)
		return cwdMsg{block: block, cwd: cwd, err: err}
	}
}

// onCwd keeps the working directory of an agent, and follows the turns
// with it. After an error, the next state asks again.
func (m *model) onCwd(msg cwdMsg) tea.Cmd {
	a := m.agents[msg.block]
	if a == nil {
		return nil // the block closed
	}
	a.asking = false
	if msg.err != nil {
		m.log.Debug("agent cwd failed", "block", msg.block, "err", msg.err)
		return nil
	}
	a.cwd, a.hasCwd = msg.cwd, true
	return m.observeTurn()
}

// owns reports whether dir is in this working tree. It keeps the
// directories that are, because their top directory does not move.
func (m *model) owns(dir string) (bool, error) {
	if m.memberDirs[dir] {
		return true, nil
	}
	in, err := m.repo.Owns(m.ctx, dir)
	if in {
		if m.memberDirs == nil {
			m.memberDirs = map[string]bool{}
		}
		m.memberDirs[dir] = true
	}
	return in, err
}

// observeTurn gives the state of the repository to the tracker, and
// follows the start and the end of a turn.
func (m *model) observeTurn() tea.Cmd {
	if !m.sampled {
		return nil
	}
	ms, ask, ok := m.members()
	if ask != "" {
		return m.askCwd(ask)
	}
	if !ok {
		return nil // keep the last state, as herdr-reviewr does
	}
	here := len(ms) > 0
	if m.agentsHere == nil || *m.agentsHere != here {
		m.agentsHere = &here
		if m.scope == ScopeLastTurn && m.turn == nil {
			m.reload() // show the new wait message
		}
	}
	states := make([]string, len(ms))
	for i, a := range ms {
		states[i] = string(a.state)
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

// turnWaitMessage tells why the last-turn scope has no turn. The messages
// come from herdr-reviewr.
func (m *model) turnWaitMessage() string {
	if m.raced {
		return "the last turn started before rexdiff could snapshot it"
	}
	if m.agentsHere != nil && !*m.agentsHere {
		return "no agent works here"
	}
	return "waiting for the first turn"
}

// raceBefore is how long before Rex gets the working state the agent can
// already write, as in herdr-reviewr.
const raceBefore = time.Second

// startTurn returns a command that takes a snapshot of the working tree
// at the start of a turn of the members ms. The snapshot comes as a
// turnStartMsg.
func (m *model) startTurn(ms []member) tea.Cmd {
	m.pending = nil
	m.turnGen++
	i := slices.IndexFunc(ms, func(a member) bool { return a.state == rex.StateWorking })
	gen, a, repo, ctx := m.turnGen, ms[i], m.repo, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, reloadTimeout,
			fmt.Errorf("turn snapshot: %v timeout: %w", reloadTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("turn snapshot: done: %w", context.Canceled))
		tree, err := repo.Snapshot(ctx)
		return turnStartMsg{gen: gen, agent: a, tree: tree, snapped: time.Now(), err: err}
	}
}

// turnStartMsg is the snapshot at the start of a turn.
type turnStartMsg struct {
	gen     int    // the value of model.turnGen at the start
	agent   member // the first agent of the turn that works
	tree    string
	snapped time.Time // the end of the snapshot
	err     error
}

// onTurnStart makes the turn of msg pending. The turn stays pending until
// it changes a file. As in herdr-reviewr, a write from raceBefore the start
// to the snapshot can be in the snapshot. Then the turn is not known, and
// the scope says so. A start or an end after the start of msg drops msg.
func (m *model) onTurnStart(msg turnStartMsg) tea.Cmd {
	if msg.gen != m.turnGen {
		return nil
	}
	if msg.err != nil {
		m.log.Warn("turn snapshot failed", "err", msg.err)
		return nil
	}
	a := msg.agent
	if m.watch != nil {
		// A write after the snapshot does not hide a write before it.
		w := m.watch.LastWrite(msg.snapped)
		if !w.Before(a.since.Add(-raceBefore)) && !w.After(msg.snapped) {
			m.log.Debug("turn raced", "block", a.block, "write", w)
			return m.dropTurn()
		}
	}
	m.log.Debug("turn start", "block", a.block, "tree", msg.tree)
	m.pending = &turnState{
		app:     a.app,
		tree:    msg.tree,
		running: true,
		at:      time.Now(),
	}
	return nil
}

// checkTurnLater returns a command that takes a snapshot for the pending
// turn. onTurnCheck makes the turn the last turn if the working tree
// differs from its start. A check at the end of the turn always runs.
// Else one check runs at a time.
func (m *model) checkTurnLater(end bool) tea.Cmd {
	if m.turnChecking && !end {
		return nil
	}
	m.turnChecking = m.turnChecking || !end
	p, gen, repo, ctx := m.pending, m.turnGen, m.repo, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, reloadTimeout,
			fmt.Errorf("turn check: %v timeout: %w", reloadTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("turn check: done: %w", context.Canceled))
		tree, err := repo.Snapshot(ctx)
		return turnCheckMsg{turn: p, gen: gen, end: end, tree: tree, err: err}
	}
}

// turnCheckMsg is a snapshot for the pending turn.
type turnCheckMsg struct {
	turn *turnState
	gen  int  // the value of model.turnGen at the check
	end  bool // the check is at the end of the turn
	tree string
	err  error
}

// onTurnCheck makes the turn of msg the last turn if the working tree
// differs from its start. Before the end, the turn must still be pending.
// After the end, no new turn must start. Else the last turn stays.
func (m *model) onTurnCheck(msg turnCheckMsg) tea.Cmd {
	if !msg.end {
		m.turnChecking = false
	}
	switch {
	case msg.err != nil:
		m.log.Warn("turn change check failed", "err", msg.err)
	case msg.tree == msg.turn.tree:
	case msg.end && msg.gen == m.turnGen, !msg.end && msg.turn == m.pending:
		return m.promote(msg.turn)
	}
	return nil
}

// dropTurn forgets the last turn, because a new turn starts that is not
// known. It returns a command that deletes the saved tree.
func (m *model) dropTurn() tea.Cmd {
	m.turn, m.raced = nil, true
	if m.scope == ScopeLastTurn {
		m.reload()
	}
	return m.saveTurn("")
}

// promote makes the turn t the last turn. It returns a command that saves
// its tree.
func (m *model) promote(t *turnState) tea.Cmd {
	m.turn, m.raced = t, false
	if m.pending == t {
		m.pending = nil
	}
	if m.scope == ScopeLastTurn {
		m.reload()
	}
	return m.saveTurn(t.tree)
}

// saveTurn returns a command that saves tree as the tree of the last turn.
// An empty tree deletes the saved tree.
func (m *model) saveTurn(tree string) tea.Cmd {
	repo, ctx, log := m.repo, m.ctx, m.log
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, reloadTimeout,
			fmt.Errorf("turn save: %v timeout: %w", reloadTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("turn save: done: %w", context.Canceled))
		if err := repo.SaveTurn(ctx, tree); err != nil {
			log.Warn("turn baseline ref write failed", "err", err)
		}
		return nil
	}
}

// endTurn ends the turn. It returns a command that checks the pending turn.
// The turn becomes the last turn if it changes the working tree. After the
// end, a change is not from the turn.
func (m *model) endTurn() tea.Cmd {
	m.turnGen++ // a start that is not pending yet stays out
	var cmd tea.Cmd
	if m.pending != nil {
		m.pending.running = false
		cmd = m.checkTurnLater(true)
		m.pending = nil
	}
	if m.turn != nil {
		m.turn.running = false
	}
	if m.scope == ScopeLastTurn {
		m.reload()
	}
	return cmd
}
