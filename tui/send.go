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

// maxSend is in bytes. Rex limits one paste to about 64 KB, so
// rex.Client.Paste sends a long review in parts.
const maxSend = 256 << 10

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

const rexTimeout = 10 * time.Second

// inRepo counts an empty dir, which is unknown, as inside.
func (m *model) inRepo(dir string) bool { return dir == "" || m.repo.Contains(dir) }

func (m *model) agent(block string) *agentState {
	a := m.agents[block]
	if a == nil {
		a = &agentState{}
		m.agents[block] = a
	}
	return a
}

// onEvent forgets all agents after a reconnection and lists them again.
// Then the next state does not start a turn.
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

// onAgents ignores the states in a list from before a reconnection,
// because they can be out of date.
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
		// A picker on top of the comment box could hide it and lose the
		// comment. The picker can replace the comment list.
		m.flash("several agents: close the box, then press " +
			firstKey(m.keys.Send) + " to pick one")
		return nil
	}
	m.openAgentPicker(here)
	return nil
}

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

func (m *model) commentTexts() map[int]string {
	texts := map[int]string{}
	for _, c := range m.comments.All() {
		texts[c.ID] = c.Text
	}
	return texts
}

func (m *model) reviewText() string {
	return m.comments.Review(m.cfg.ReviewPrefix, m.commentStale)
}

// pasteReview checks the status first, because an agent at a prompt
// would get the text in its prompt.
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
		// If a part fails after others, the agent holds the start of the
		// review. Tell the user to clear the agent's input.
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

// onSent keeps comments that the user added or changed during the send.
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

func (m *model) copyComments() {
	if m.comments.Len() == 0 {
		m.flash("no comments yet")
		return
	}
	texts := m.commentTexts()
	m.copyText(m.reviewText(), "copied "+plural(len(texts), "comment"), texts)
}

const copyTimeout = 2 * time.Second

// copyText waits for the copy, so that copies happen in order.
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

func (m *model) inCommentsOverlay() bool {
	return m.overlay != nil && m.overlay.kind == overlayComments
}

// clearComments keeps a comment whose text no longer matches, because the
// user edited it in the meantime.
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

func (m *model) flashCopy(why string) {
	m.flash(why + ", press " + firstKey(m.keys.Copy) + " to copy")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
