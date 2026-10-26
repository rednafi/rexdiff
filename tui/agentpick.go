package tui

// The agent picker picks the agent that gets the review.

import (
	"fmt"
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

// openAgentPicker lets the user pick the agent to send to. The cursor
// starts on the agent of the last send, else the agent beside the pane.
func (m *model) openAgentPicker(agents []rex.Agent) {
	ov := newOverlay(overlayAgents, "send to which agent?")
	ov.agents = agents
	var items []pickItem
	armed := 0
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
			tag, armed = "last used", i
		case m.beside:
			tag = "opened beside"
			if m.lastSent == "" {
				armed = i
			}
		}
		items = append(items, pickItem{
			label:  fmt.Sprintf("%d  %s", i+1, a.Name),
			detail: detail,
			tag:    tag,
			id:     a.Block,
		})
	}
	ov.setItems(items)
	ov.cur = armed
	m.open(ov)
}

// agent returns the agent of item it of the agent picker. If it is nil,
// ok is false.
func (o *overlay) agent(it *pickItem) (a rex.Agent, ok bool) {
	if it == nil {
		return rex.Agent{}, false
	}
	i := slices.IndexFunc(o.agents, func(a rex.Agent) bool { return a.Block == it.id })
	if i < 0 {
		return rex.Agent{}, false
	}
	return o.agents[i], true
}
