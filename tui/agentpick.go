package tui

import (
	"fmt"
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

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
