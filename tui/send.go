package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/clipboard"
	"github.com/rednafi/rexdiff/rex"
)

// maxSend is the largest review, in bytes, that rexdiff pastes into an
// agent. The user must copy a larger review.
const maxSend = 256 << 10

// listAgents returns a command that lists the agents of the session, other
// than rexdiff panes. The command returns msg with the agents. Set
// msg.send to send the review to them.
func (m *model) listAgents(msg agentsMsg) tea.Cmd {
	c, ctx := m.rex, m.ctx
	msg.gen = m.agentsGen
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, rexTimeout,
			fmt.Errorf("agent list: %v timeout: %w", rexTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("agent list: done: %w", context.Canceled))
		agents, err := c.Agents(ctx)
		isRexdiff := func(a rex.Agent) bool { return a.Status.App == appName }
		agents = slices.DeleteFunc(agents, isRexdiff)
		msg.agents, msg.err = agents, err
		return msg
	}
}

// rexTimeout is the longest time of the calls to Rex for one action. A
// Rex that does not answer then fails the action.
const rexTimeout = 10 * time.Second

// inRepo reports whether dir is in the repository. An empty dir is
// unknown, and counts as in the repository.
func (m *model) inRepo(dir string) bool { return dir == "" || m.repo.Contains(dir) }

// agent returns the state of the agent in block. If the block is new, agent
// adds it.
func (m *model) agent(block string) *agentState {
	a := m.agents[block]
	if a == nil {
		a = &agentState{}
		m.agents[block] = a
	}
	return a
}

// onEvent keeps the state of the agent in the block of e, and follows the
// turns. After a reconnect, it forgets the agents and lists them again,
// as herdr-reviewr does after a lost connection. Nobody sees the agents
// in the gap, so the next state starts no turn. A list from before the
// gap is stale. An event with an error only goes to the log.
func (m *model) onEvent(e rex.Event) tea.Cmd {
	if e.Err != nil {
		m.log.Warn("rex events failed", "err", e.Err)
		return nil
	}
	if e.Reconnect {
		m.tracker.Gap()
		clear(m.agents)
		m.sampled = false
		m.agentsGen++
		return m.listAgents(agentsMsg{})
	}
	if e.Closed {
		delete(m.agents, e.Block)
	} else {
		app, state := e.Status.App, e.Status.State
		m.log.Debug("status", "block", e.Block, "app", app, "state", state)
		a := m.agent(e.Block)
		a.app, a.state, a.since = app, state, cmp.Or(e.Status.At, time.Now())
	}
	return m.observeTurn()
}

// onAgents keeps the states of the agents. For a send, onAgents sends the
// review to the agent, or lets the user pick one. A list from before a
// reconnect does not change the states, because they can be old.
func (m *model) onAgents(msg agentsMsg) tea.Cmd {
	stale := msg.gen != m.agentsGen
	if stale && !msg.send {
		return nil // the list after the reconnect follows
	}
	if !stale {
		m.keepAgents(msg.agents)
	}
	switch {
	case stale:
	case msg.err == nil:
		m.sampled = true
	case !m.sampled && !msg.send:
		// Without a list, the turns do not start. Thus list again later.
		m.log.Warn("agent list failed", "err", msg.err)
		m.sampleRetry.fail(time.Now())
	}
	// A send drops the question for a cwd. The next state asks again.
	turn := m.observeTurn()
	if !msg.send {
		return turn
	}
	m.sends--
	if msg.err != nil {
		m.flashCopy(msg.err.Error())
		return nil
	}
	// Prefer the agents in this repository. Else use all the agents of the
	// session.
	var here []rex.Agent
	for _, a := range msg.agents {
		if m.inRepo(a.Cwd) {
			here = append(here, a)
		}
	}
	if len(here) == 0 {
		here = msg.agents
	}
	switch {
	case len(here) == 0:
		m.flashCopy("no agent in this session")
		return nil
	case len(here) == 1:
		return m.deliver(here[0])
	case m.mode != modeNormal && !m.inCommentList():
		// The user can open the comment box or an overlay while the agents
		// load. A picker on top can hide the box and lose the comment. The
		// picker can take the place of the comment list, as in
		// herdr-reviewr.
		m.flash("several agents: close the box, then press " +
			firstKey(m.keys.Send) + " to pick one")
		return nil
	}
	m.openAgentPicker(here)
	return nil
}

// keepAgents keeps the states of the agents of a list.
func (m *model) keepAgents(agents []rex.Agent) {
	for _, a := range agents {
		*m.agent(a.Block) = agentState{
			app:    a.Status.App,
			state:  a.Status.State,
			since:  cmp.Or(a.Status.At, time.Now()),
			cwd:    a.Cwd,
			hasCwd: a.Cwd != "", // else askCwd asks Rex again
		}
	}
}

// commentIDs returns the IDs of the comments.
func (m *model) commentIDs() []int {
	var ids []int
	for _, c := range m.comments.All() {
		ids = append(ids, c.ID)
	}
	return ids
}

// reviewText returns the text of the review that a send pastes.
func (m *model) reviewText() string {
	return m.comments.Review(m.cfg.ReviewPrefix)
}

// deliver returns a command that pastes the review into agent a. It checks
// the agent again before the paste, because a prompt of the agent can take
// the text.
func (m *model) deliver(a rex.Agent) tea.Cmd {
	m.sends++
	c, ctx := m.rex, m.ctx
	text, ids := m.reviewText(), m.commentIDs()
	focus := m.cfg.focusesAgent()
	return func() tea.Msg {
		ctx, cancel := context.WithCancelCause(ctx)
		ctx, cancelTimeout := context.WithTimeoutCause(ctx, rexTimeout,
			fmt.Errorf("send: %v timeout: %w", rexTimeout, context.DeadlineExceeded))
		defer cancelTimeout()
		defer cancel(fmt.Errorf("send: done: %w", context.Canceled))
		status, err := c.Status(ctx, a.Block)
		if err != nil {
			return sentMsg{agent: a, err: fmt.Errorf("check %s: %w", a.Name, err)}
		}
		if status == (rex.Status{}) {
			return sentMsg{agent: a, err: fmt.Errorf("%s closed", a.Name)}
		}
		if status.State == rex.StateBlocked {
			err := fmt.Errorf("answer %s's prompt first", a.Name)
			return sentMsg{agent: a, err: err}
		}
		if err := c.Paste(ctx, a.Block, text); err != nil {
			return sentMsg{agent: a, err: fmt.Errorf("paste into %s: %w", a.Name, err)}
		}
		if focus {
			// Ignore the error, because the paste works.
			_ = c.Focus(ctx, a.Block)
		}
		return sentMsg{agent: a, ids: ids}
	}
}

// onSent removes the sent comments if the send worked. The comments that
// the user adds or changes during the send stay. A saved edit gives a
// comment a new ID, and msg does not have that ID.
func (m *model) onSent(msg sentMsg) {
	m.sends--
	if msg.err != nil {
		m.flashCopy(msg.err.Error())
		return
	}
	m.clearComments(msg.ids)
	m.lastSent = msg.agent.Block
	sent := plural(len(msg.ids), "comment")
	m.flash(fmt.Sprintf("sent %s to %s", sent, msg.agent.Name))
}

// startSend lists the agents to send the review to.
func (m *model) startSend() tea.Cmd {
	switch {
	case m.comments.Len() == 0:
		m.flash("no comments yet")
		return nil
	case len(m.reviewText()) > maxSend:
		m.flashCopy("review too large to send")
		return nil
	case m.rex == nil:
		m.flashCopy("not in Rex")
		return nil
	case m.sends > 0:
		m.flash("a send runs")
		return nil
	}
	m.flash("sending…")
	m.sends++
	return m.listAgents(agentsMsg{send: true})
}

// copyComments copies the review to the clipboard. A good copy clears
// the comments, as a send does.
func (m *model) copyComments() {
	if m.comments.Len() == 0 {
		m.flash("no comments yet")
		return
	}
	ids := m.commentIDs()
	m.copyText(m.reviewText(), "copied "+plural(len(ids), "comment"), ids)
}

// copyTimeout is the longest time of a copy.
const copyTimeout = 2 * time.Second

// copyText writes text to the clipboard. If the copy works, it removes
// the comments ids and shows done. As in herdr-reviewr, the copy runs
// before the next frame, so two copies keep their order. A clipboard tool
// that does not answer fails after copyTimeout. As in herdr-reviewr, the
// message of a failure names only the fix that the user can make. The log
// has the error.
func (m *model) copyText(text, done string, ids []int) {
	ctx, cancel := context.WithCancelCause(m.ctx)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, copyTimeout,
		fmt.Errorf("copy: %v timeout: %w", copyTimeout, context.DeadlineExceeded))
	defer cancelTimeout()
	defer cancel(fmt.Errorf("copy: done: %w", context.Canceled))
	err := clipboard.Write(ctx, m.env, text)
	switch {
	case errors.Is(err, clipboard.ErrNoTool):
		m.flash("copy failed: install wl-clipboard, xclip, or xsel")
	case err != nil:
		m.log.Warn("copy failed", "err", err)
		m.flash("copy failed")
	default:
		m.clearComments(ids)
		m.flash(done)
	}
}

// inCommentList reports whether the comment list is open.
func (m *model) inCommentList() bool {
	return m.mode == modeOverlay && m.overlay != nil && m.overlay.kind == overlayComments
}

// clearComments removes the comments ids after a send or a copy. An open
// comment list shows the rest, or closes if no comment stays.
func (m *model) clearComments(ids []int) {
	for _, id := range ids {
		m.comments.Remove(id)
	}
	if m.inCommentList() {
		if m.comments.Len() == 0 {
			m.closeOverlay()
			return
		}
		m.fillCommentList(m.overlay)
	}
}

// flashCopy shows why the review cannot go to an agent, and tells the
// user to copy it.
func (m *model) flashCopy(why string) {
	m.flash(why + ", press " + firstKey(m.keys.Copy) + " to copy")
}

// plural returns n and word, with an s if n is not 1.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
