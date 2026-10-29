package turn_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/turn"
	"github.com/rednafi/rexdiff/rex"
)

// Agent states that put a repository in each of its states.
var (
	resting = []rex.State{"idle"}
	working = []rex.State{"working"}
	held    = []rex.State{"blocked"}
)

// step is one call to a Tracker and the turn start and end it should
// report. If gap is true, the step calls MarkGap instead of Observe.
type step struct {
	states         []rex.State
	gap            bool
	started, ended bool
}

// TestCombine checks how the states of several agents combine into one
// repository state. After a rest, only work starts a turn. After work,
// only a rest ends it.
func TestCombine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		states []rex.State
		// starts reports whether the states start a turn after a rest. ends
		// reports whether they end a turn after work. A held state does
		// neither.
		starts, ends bool
	}{
		// Having no agents counts as a rest, so a new pane tracks the next
		// turn.
		{"no agent", nil, false, true},
		{"idle and working", []rex.State{"idle", "working"}, true, false},
		{"blocked and working", []rex.State{"blocked", "working"}, true, false},
		{"idle and done", []rex.State{"idle", "done"}, false, true},
		{"blocked and idle", []rex.State{"blocked", "idle"}, false, false},
		// An unknown state never counts as a rest, and it does not start a
		// turn.
		{"unknown", []rex.State{"compacting"}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var rest turn.Tracker
			rest.Observe(resting...)
			if started, _ := rest.Observe(tt.states...); started != tt.starts {
				t.Errorf("after a rest: Observe(%q) started = %v, want %v", tt.states, started, tt.starts)
			}
			var work turn.Tracker
			work.Observe(resting...)
			work.Observe(working...)
			if _, ended := work.Observe(tt.states...); ended != tt.ends {
				t.Errorf("after work: Observe(%q) ended = %v, want %v", tt.states, ended, tt.ends)
			}
		})
	}
}

// TestTracker checks the turn starts and ends that a Tracker reports for
// sequences of states.
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
		// The first state observed never starts a turn.
		{"start after a rest", []step{
			{states: resting},
			{states: working, started: true},
		}},
		// A prompt in the middle of a turn does not start a new turn.
		{"held back to work continues", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: working},
		}},
		{"end only after work", []step{
			{states: resting},                // no earlier work, so no end
			{states: working, started: true}, // a start is never also an end
			{states: held},                   // a pause, not an end
			{states: working},
			{states: resting, ended: true},
		}},
		// The prompt keeps the turn open. A rest after it ends the turn,
		// and only once.
		{"end through held", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: resting, ended: true},
			{states: resting},
		}},
		// The start of this turn was not observed.
		{"lone first working starts nothing", []step{
			{states: working},
		}},
		// The agents are not observed during a gap, so the next work does
		// not start a turn.
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
					tr.MarkGap()
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
