// Package turn finds the start and the end of each turn of the agents in
// a repository, from the states of the agents. A turn starts when the
// agents go from rest to work. It ends when they rest again. The rules
// come from herdr-reviewr.
package turn

import (
	"slices"

	"github.com/rednafi/rexdiff/rex"
)

// state is the work state of a repository. fold makes it from the states
// of all the agents in the repository.
type state int

const (
	// stateResting tells that each agent rests, or that there is no agent.
	stateResting state = iota
	// stateWorking tells that at least one agent works.
	stateWorking
	// stateHeld tells that no agent works, but one does not rest. For
	// example, it waits at a prompt. A turn that is open stays open.
	stateHeld
)

// rests reports whether s is a state between turns. A blocked agent and an
// unknown state are in the middle of a turn.
func rests(s rex.State) bool { return s == rex.StateIdle || s == rex.StateDone }

// fold returns the state of a repository whose agents are in the states
// states. One working agent makes the repository work.
func fold(states ...rex.State) state {
	if slices.Contains(states, rex.StateWorking) {
		return stateWorking
	}
	if slices.ContainsFunc(states, func(s rex.State) bool { return !rests(s) }) {
		return stateHeld
	}
	return stateResting
}

// Tracker finds the start and the end of each turn in a series of states.
// The zero Tracker is ready to use.
type Tracker struct {
	// prevResting is true if the last state is resting. It is false at
	// first, so the first state starts no turn.
	prevResting bool
	// worked is true if the repository works after its last rest. An end
	// can come through held, but a start cannot. Thus the two edges keep
	// different memories.
	worked bool
}

// Observe takes the states of all the agents in the repository at one
// time. A turn starts when one agent works after all rest. A turn ends
// when all rest after work, also through a prompt.
func (t *Tracker) Observe(states ...rex.State) (started, ended bool) {
	s := fold(states...)
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

// Gap tells the Tracker that nobody sees the agents for some time. Then
// the next state cannot start a turn.
func (t *Tracker) Gap() { t.prevResting = false }
