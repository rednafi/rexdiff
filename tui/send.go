package tui

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/internal/clipboard"
	"github.com/rednafi/rexdiff/internal/xctx"
	"github.com/rednafi/rexdiff/rex"
)

// maxSend is the largest review, in bytes, that rexdiff will paste into an
// agent. A larger review must be copied instead. Rex limits one paste to
// about 64 KB, so rex.Client.Paste sends a long review in parts.
const maxSend = 256 << 10

// fetchAgents returns a command that lists the agents in the Rex session,
// leaving out rexdiff panes. The command returns msg with the agents
// filled in. Set msg.send if the list is for sending the review.
func (m *model) fetchAgents(msg agentsMsg) tea.Cmd {
	c, ctx := m.rex, m.ctx
	msg.gen = m.agentsGen
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "agent list")
		defer cancel()
		agents, err := c.Agents(ctx)
		isRexdiff := func(a rex.Agent) bool { return a.Status.App == appName }
		agents = slices.DeleteFunc(agents, isRexdiff)
		msg.agents, msg.err = agents, err
		return msg
	}
}

// rexTimeout limits how long the Rex calls for one action can take.
const rexTimeout = 10 * time.Second

// inRepo reports whether dir is inside the repository. An empty dir means
// the directory is unknown, and counts as inside.
func (m *model) inRepo(dir string) bool { return dir == "" || m.repo.Contains(dir) }

// agent returns the state of the agent in block, adding an empty state if
// the block is new.
func (m *model) agent(block string) *agentState {
	a := m.agents[block]
	if a == nil {
		a = &agentState{}
		m.agents[block] = a
	}
	return a
}

// onEvent records the state of the agent in e's block and updates turn
// tracking. After a reconnection, it forgets all agents and lists them
// again, and the next state does not start a turn. An event with an error
// is only logged.
func (m *model) onEvent(e rex.Event) tea.Cmd {
	if e.Err != nil {
		m.log.Warn("rex events failed", "err", e.Err)
		return nil
	}
	if e.Reconnect {
		m.tracker.MarkGap()
		clear(m.agents)
		m.agentsSampled = false
		m.agentsGen++
		return m.fetchAgents(agentsMsg{})
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

// onAgents records the states of the listed agents. If the list is for a
// send, it sends the review to the only agent or lets the user choose one.
// A list taken before a reconnection does not update the states, because
// they may be out of date.
func (m *model) onAgents(msg agentsMsg) tea.Cmd {
	stale := msg.gen != m.agentsGen
	if stale && !msg.send {
		return nil // a new list follows the reconnection
	}
	if !stale {
		m.storeAgents(msg.agents)
	}
	switch {
	case stale:
		// A stale list says nothing about the current connection.
	case msg.err == nil:
		m.agentsSampled = true
	case !m.agentsSampled && !msg.send:
		// Turns cannot be tracked until a list succeeds, so try again
		// later.
		m.log.Warn("agent list failed", "err", msg.err)
		m.sampleRetry.fail(time.Now())
	}
	// storeAgents resets each agent's state, including any cwd request in
	// progress, so observeTurn may ask for the cwd again.
	turn := m.observeTurn()
	if !msg.send {
		return turn
	}
	m.sends--
	if msg.err != nil {
		m.flashCopy(msg.err.Error())
		return nil
	}
	// Prefer agents working in this repository. If there are none, use
	// every agent in the session.
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
		return m.pasteReview(here[0])
	case m.mode != modeNormal && !m.inCommentsOverlay():
		// The user may have opened the comment box or an overlay while the
		// agents were loading. A picker on top could hide the box and lose
		// the comment. The comment list is the exception: the picker can
		// replace it.
		m.flash("several agents: close the box, then press " +
			firstKey(m.keys.Send) + " to pick one")
		return nil
	}
	m.openAgentPicker(here)
	return nil
}

// storeAgents records the state of each agent in agents, replacing any
// earlier state.
func (m *model) storeAgents(agents []rex.Agent) {
	for _, a := range agents {
		*m.agent(a.Block) = agentState{
			app:    a.Status.App,
			state:  a.Status.State,
			since:  cmp.Or(a.Status.At, time.Now()),
			cwd:    a.Cwd,
			hasCwd: a.Cwd != "", // if false, askCwd asks Rex for it
		}
	}
}

// commentTexts returns the text of each comment, keyed by ID.
func (m *model) commentTexts() map[int]string {
	texts := map[int]string{}
	for _, c := range m.comments.All() {
		texts[c.ID] = c.Text
	}
	return texts
}

// reviewText returns the review text that is sent or copied.
func (m *model) reviewText() string {
	return m.comments.Review(m.cfg.ReviewPrefix, m.commentStale)
}

// pasteReview returns a command that pastes the review into agent a. It
// checks the agent's status first, because if the agent is waiting at a
// prompt, the prompt would receive the text.
func (m *model) pasteReview(a rex.Agent) tea.Cmd {
	m.sends++
	c, ctx := m.rex, m.ctx
	text, sent := m.reviewText(), m.commentTexts()
	focus := m.cfg.focusesAgent()
	return func() tea.Msg {
		ctx, cancel := xctx.WithTimeout(ctx, rexTimeout, "send")
		defer cancel()
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
		// Rex pastes a long review in parts. If a part fails after
		// others, the agent holds the start of the review, so say so: the
		// user should clear the agent's input before copying the review.
		err = c.Paste(ctx, a.Block, text)
		if partly, ok := errors.AsType[*rex.PasteError](err); ok {
			err = fmt.Errorf("only %d of %d parts reached %s, clear its input: %w",
				partly.Pasted, partly.Parts, a.Name, partly.Err)
			return sentMsg{agent: a, err: err}
		}
		if err != nil {
			return sentMsg{agent: a, err: fmt.Errorf("paste into %s: %w", a.Name, err)}
		}
		if focus {
			// Ignore any error, because the paste itself succeeded.
			_ = c.Focus(ctx, a.Block)
		}
		return sentMsg{agent: a, sent: sent}
	}
}

// onSent removes the sent comments if the send succeeded. Comments that
// the user added or changed during the send are kept.
func (m *model) onSent(msg sentMsg) {
	m.sends--
	if msg.err != nil {
		m.flashCopy(msg.err.Error())
		return
	}
	m.clearComments(msg.sent)
	m.lastSent = msg.agent.Block
	sent := plural(len(msg.sent), "comment")
	m.flash(fmt.Sprintf("sent %s to %s", sent, msg.agent.Name))
}

// startSend checks that the review can be sent and returns a command that
// lists the agents to send it to.
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
	return m.fetchAgents(agentsMsg{send: true})
}

// copyComments copies the review to the clipboard. A successful copy
// clears the comments, as sending does.
func (m *model) copyComments() {
	if m.comments.Len() == 0 {
		m.flash("no comments yet")
		return
	}
	texts := m.commentTexts()
	m.copyText(m.reviewText(), "copied "+plural(len(texts), "comment"), texts)
}

// copyTimeout limits how long copying to the clipboard can take.
const copyTimeout = 2 * time.Second

// copyText writes text to the clipboard, waiting at most copyTimeout, so
// that copies happen in order. On success, it removes the comments in
// texts and shows the message success. On failure, it logs the error and
// shows a short message.
func (m *model) copyText(text, success string, texts map[int]string) {
	ctx, cancel := xctx.WithTimeout(m.ctx, copyTimeout, "copy")
	defer cancel()
	err := clipboard.Write(ctx, m.env, text)
	switch {
	case errors.Is(err, clipboard.ErrNoTool):
		m.flash("copy failed: install wl-clipboard, xclip, or xsel")
	case err != nil:
		m.log.Warn("copy failed", "err", err)
		m.flash("copy failed")
	default:
		m.clearComments(texts)
		m.flash(success)
	}
}

// inCommentsOverlay reports whether the comment list is open.
func (m *model) inCommentsOverlay() bool {
	return m.overlay != nil && m.overlay.kind == overlayComments
}

// clearComments removes the comments in texts after a send or copy. A
// comment whose text no longer matches is kept, because the user edited it
// in the meantime. An open comment list is updated, or closed if no
// comments remain.
func (m *model) clearComments(texts map[int]string) {
	for id, text := range texts {
		if c := m.comments.Find(id); c != nil && c.Text == text {
			m.comments.Delete(id)
		}
	}
	if m.inCommentsOverlay() {
		if m.comments.Len() == 0 {
			m.closeOverlay()
			return
		}
		m.setCommentItems(m.overlay)
	}
}

// flashCopy shows why the review cannot be sent to an agent and suggests
// copying it instead.
func (m *model) flashCopy(why string) {
	m.flash(why + ", press " + firstKey(m.keys.Copy) + " to copy")
}

// plural returns n followed by word, adding an s if n is not 1.
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
