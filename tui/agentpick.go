package tui

// The agent picker lets the user choose which agent receives the review.

import (
	"fmt"
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

// openAgentPicker opens a list of agents for the user to send the review
// to. The cursor starts on the agent that was used last or, failing that,
// on the agent in the pane beside rexdiff.
func (m *model) openAgentPicker(agents []rex.Agent) {
	ov := newOverlay(overlayAgents, "send to which agent?")
	ov.agents = agents
	var items []overlayItem
	crossing := 0
	for i, a := range agents {
		detail := a.Status.App + " · " + string(a.Status.State)
		if a.Status.State == rex.StateBlocked {
			detail = a.Status.App + " · at a prompt"
		}
		if a.Window != "" {
			detail += " · " + a.Window
		}
		tag := ""
		switch a.Block {
		case m.lastSent:
			tag, crossing = "last used", i
		case m.beside:
			tag = "opened beside"
			if m.lastSent == "" {
				crossing = i
			}
		}
		items = append(items, overlayItem{
			label:  fmt.Sprintf("%d  %s", i+1, a.Name),
			detail: detail,
			tag:    tag,
			id:     a.Block,
		})
	}
	ov.setItems(items)
	ov.cur = crossing
	m.openOverlay(ov)
}

// agent returns the agent of the agent picker item it. ok is false if it
// is nil or matches no agent.
func (ov *overlay) agent(it *overlayItem) (a rex.Agent, ok bool) {
	if it == nil {
		return rex.Agent{}, false
	}
	i := slices.IndexFunc(ov.agents, func(a rex.Agent) bool { return a.Block == it.id })
	if i < 0 {
		return rex.Agent{}, false
	}
	return ov.agents[i], true
}
