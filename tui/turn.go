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
			m.log.Warn("agent membership failed", "err", err)
			return nil, "", false
		}
		if in {
			ms = append(ms, member{block, a.app, a.state, a.since})
		}
	}
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
	states := make([]rex.State, len(ms))
	for i, a := range ms {
		states[i] = a.state
	}
	started, ended := m.tracker.Observe(states...)
	if started {
		m.startTurn(ms)
	}
	if ended {
		m.endTurn()
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

// startTurn takes a snapshot of the working tree at the start of a turn
// of the members ms. The turn stays pending until it changes a file. As in
// herdr-reviewr, a write from raceBefore the start to the snapshot can be
// in the snapshot. Then the turn is not known, and the scope says so.
func (m *model) startTurn(ms []member) {
	m.pending = nil
	tree, err := m.repo.Snapshot(m.ctx)
	if err != nil {
		m.log.Warn("turn snapshot failed", "err", err)
		return
	}
	snapped := time.Now()
	i := slices.IndexFunc(ms, func(a member) bool { return a.state == rex.StateWorking })
	if m.watch != nil {
		w := m.watch.LastWrite()
		if !w.Before(ms[i].since.Add(-raceBefore)) && !w.After(snapped) {
			m.log.Debug("turn raced", "block", ms[i].block, "write", w)
			m.dropTurn()
			return
		}
	}
	m.log.Debug("turn start", "block", ms[i].block, "tree", tree)
	m.pending = &turnState{
		block:   ms[i].block,
		app:     ms[i].app,
		tree:    tree,
		running: true,
		at:      time.Now(),
	}
}

// checkTurnLater returns a command that takes a snapshot for the pending
// turn. onTurnCheck makes the turn the last turn if the working tree
// differs from its start.
func (m *model) checkTurnLater() tea.Cmd {
	if m.turnChecking {
		return nil // one check at a time
	}
	m.turnChecking = true
	p, repo, ctx := m.pending, m.repo, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, reloadTimeout,
			fmt.Errorf("turn check: %v timeout: %w", reloadTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("turn check: done: %w", context.Canceled))
		tree, err := repo.Snapshot(ctx)
		return turnCheckMsg{turn: p, tree: tree, err: err}
	}
}

// turnCheckMsg is a snapshot for the pending turn.
type turnCheckMsg struct {
	turn *turnState
	tree string
	err  error
}

// onTurnCheck makes the turn of msg the last turn if it is still pending
// and the working tree differs from its start.
func (m *model) onTurnCheck(msg turnCheckMsg) {
	m.turnChecking = false
	switch {
	case msg.err != nil:
		m.log.Warn("turn change check failed", "err", msg.err)
	case msg.turn == m.pending && msg.tree != msg.turn.tree:
		m.promote()
	}
}

// checkTurn makes the pending turn the last turn if the working tree
// differs from its start. Else the last turn stays. The end of a turn
// checks at once, before the turn stops being pending.
func (m *model) checkTurn() {
	tree, err := m.repo.Snapshot(m.ctx)
	if err != nil {
		m.log.Warn("turn change check failed", "err", err)
		return
	}
	if tree != m.pending.tree {
		m.promote()
	}
}

// dropTurn forgets the last turn, because a new turn starts that is not
// known.
func (m *model) dropTurn() {
	m.turn, m.raced = nil, true
	if err := m.repo.SaveTurn(m.ctx, ""); err != nil {
		m.log.Warn("turn baseline ref delete failed", "err", err)
	}
	if m.scope == ScopeLastTurn {
		m.reload()
	}
}

// promote makes the pending turn the last turn, and saves its tree.
func (m *model) promote() {
	m.turn, m.pending, m.raced = m.pending, nil, false
	if err := m.repo.SaveTurn(m.ctx, m.turn.tree); err != nil {
		m.log.Warn("turn baseline ref write failed", "err", err)
	}
	if m.scope == ScopeLastTurn {
		m.reload()
	}
}

// endTurn ends the turn. A pending turn that changes the working tree
// becomes the last turn. After the end, a change is not from the turn.
func (m *model) endTurn() {
	if m.pending != nil {
		m.checkTurn()
		m.pending = nil
	}
	if m.turn != nil {
		m.turn.running = false
	}
	if m.scope == ScopeLastTurn {
		m.reload()
	}
}
