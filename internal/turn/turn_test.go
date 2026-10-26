package turn_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/turn"
)

// The tests come from turn.rs of herdr-reviewr.

// The states of one agent that give each state of a repository.
var (
	resting = []string{"idle"}
	working = []string{"working"}
	held    = []string{"blocked"}
)

// step is one call to a Tracker and the edges that it gives. With gap,
// the step calls Gap and not Observe.
type step struct {
	states         []string
	gap            bool
	started, ended bool
}

// TestFold checks how the states of the agents fold into one state. After a
// rest, only work starts a turn. After work, only a rest ends it.
func TestFold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		states []string
		// starts tells that the states start a turn after a rest. ends
		// tells that they end a turn after work. A held state does neither.
		starts, ends bool
	}{
		// No agent rests, so a fresh pane tracks the next turn it sees.
		{"no agent", nil, false, true},
		{"idle and working", []string{"idle", "working"}, true, false},
		{"blocked and working", []string{"blocked", "working"}, true, false},
		{"idle and done", []string{"idle", "done"}, false, true},
		{"blocked and idle", []string{"blocked", "idle"}, false, false},
		// An unknown state never rests, so it starts no turn.
		{"unknown", []string{"compacting"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var rest turn.Tracker
			rest.Observe(resting...)
			if started, _ := rest.Observe(tt.states...); started != tt.starts {
				t.Errorf("after a rest: started %v, want %v", started, tt.starts)
			}
			var work turn.Tracker
			work.Observe(resting...)
			work.Observe(working...)
			if _, ended := work.Observe(tt.states...); ended != tt.ends {
				t.Errorf("after work: ended %v, want %v", ended, tt.ends)
			}
		})
	}
}

// TestTracker checks the edges of a series of states.
func TestTracker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		steps []step
	}{
		{"empty repo starts on the first worker", []step{
			{states: nil},
			{states: working, started: true},
		}},
		// The first state never starts a turn.
		{"start after a rest", []step{
			{states: resting},
			{states: working, started: true},
		}},
		// A prompt in the middle of a turn does not start another turn.
		{"held back to work continues", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: working},
		}},
		{"end only after work", []step{
			{states: resting},                // no work before, so no end
			{states: working, started: true}, // a start is never an end
			{states: held},                   // a pause, not an end
			{states: working},
			{states: resting, ended: true},
		}},
		// The prompt keeps the turn open. A rest after it ends the turn once.
		{"end through held", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: resting, ended: true},
			{states: resting},
		}},
		// The start of this turn is not seen.
		{"lone first working starts nothing", []step{
			{states: working},
		}},
		// Nobody sees the agents in a gap, so the next work starts no turn.
		{"gap", []step{
			{states: resting},
			{gap: true},
			{states: working},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var tr turn.Tracker
			for i, st := range tt.steps {
				if st.gap {
					tr.Gap()
					continue
				}
				started, ended := tr.Observe(st.states...)
				if started != st.started || ended != st.ended {
					t.Errorf("step %d (%q): started %v, ended %v, want %v, %v",
						i, st.states, started, ended, st.started, st.ended)
				}
			}
		})
	}
}
