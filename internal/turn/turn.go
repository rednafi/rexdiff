// Package turn detects the start and end of each agent turn in a
// repository. A turn starts when the agents go from resting to working. It
// ends when they rest again.
package turn

import (
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

type state int

const (
	// stateResting also means that there are no agents.
	stateResting state = iota
	stateWorking
	// stateHeld means that no agent works but one waits, for example at a
	// prompt. An open turn stays open.
	stateHeld
)

func isResting(s rex.State) bool { return s == rex.StateIdle || s == rex.StateDone }

func combine(states ...rex.State) state {
	if slices.Contains(states, rex.StateWorking) {
		return stateWorking
	}
	if slices.ContainsFunc(states, func(s rex.State) bool { return !isResting(s) }) {
		return stateHeld
	}
	return stateResting
}

// Tracker detects turns in a sequence of states. The zero Tracker is ready
// to use.
type Tracker struct {
	// prevResting starts false, so the first state never starts a turn.
	prevResting bool
	// worked stays set while held, because a turn can end after a held state.
	worked bool
}

// Observe records the states of all agents at one moment.
func (t *Tracker) Observe(states ...rex.State) (started, ended bool) {
	s := combine(states...)
	started = s == stateWorking && t.prevResting
	ended = t.worked && s == stateResting
	t.prevResting = s == stateResting
	switch s {
	case stateWorking:
		t.worked = true
	case stateResting:
		t.worked = false
	}
	return started, ended
}

// MarkGap records a time with no observations. The next state then cannot
// start a turn.
func (t *Tracker) MarkGap() { t.prevResting = false }
