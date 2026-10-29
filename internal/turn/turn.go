// Package turn detects the start and end of each turn of the agents in a
// repository, based on the agents' states. A turn starts when the agents
// go from resting to working, and ends when they rest again.
package turn

import (
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

// state is the work state of a repository. combine computes it from the
// states of all agents in the repository.
type state int

const (
	// stateResting means that every agent is resting, or that there are no
	// agents.
	stateResting state = iota
	// stateWorking means that at least one agent is working.
	stateWorking
	// stateHeld means that no agent is working but at least one is not
	// resting, for example because it waits at a prompt. An open turn stays
	// open.
	stateHeld
)

// isResting reports whether s is a state between turns. A blocked or
// unknown state counts as part of a turn.
func isResting(s rex.State) bool { return s == rex.StateIdle || s == rex.StateDone }

// combine returns the state of a repository whose agents are in the given
// states. A single working agent puts the repository in stateWorking.
func combine(states ...rex.State) state {
	if slices.Contains(states, rex.StateWorking) {
		return stateWorking
	}
	if slices.ContainsFunc(states, func(s rex.State) bool { return !isResting(s) }) {
		return stateHeld
	}
	return stateResting
}

// Tracker detects the start and end of each turn in a sequence of states.
// The zero Tracker is ready to use.
type Tracker struct {
	// prevResting is true if the previous state was resting. It starts
	// false, so the first state never starts a turn.
	prevResting bool
	// worked is true if the repository has worked since its last rest. It
	// stays set while held, because a turn can end after a held state.
	worked bool
}

// Observe records the states of all agents in the repository at one
// moment. It reports whether a turn starts or ends. A turn starts when an
// agent starts working after all agents were resting. It ends when all
// agents rest after work, even if a held state came in between.
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

// MarkGap tells t that the agents were not observed for some time, so the
// next state cannot start a turn.
func (t *Tracker) MarkGap() { t.prevResting = false }
