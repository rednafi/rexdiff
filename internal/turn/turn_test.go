package turn_test

import (
	"testing"

	"github.com/rednafi/rexdiff/internal/turn"
	"github.com/rednafi/rexdiff/rex"
)

var (
	resting = []rex.State{"idle"}
	working = []rex.State{"working"}
	held    = []rex.State{"blocked"}
)

type step struct {
	states         []rex.State
	gap            bool
	started, ended bool
}

func TestCombine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		states       []rex.State
		starts, ends bool
	}{
		// No agents counts as a rest, so a new pane tracks the next turn.
		{"no agent", nil, false, true},
		{"idle and working", []rex.State{"idle", "working"}, true, false},
		{"blocked and working", []rex.State{"blocked", "working"}, true, false},
		{"idle and done", []rex.State{"idle", "done"}, false, true},
		{"blocked and idle", []rex.State{"blocked", "idle"}, false, false},
		// An unknown state is not a rest.
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
		{"start after a rest", []step{
			{states: resting},
			{states: working, started: true},
		}},
		{"held back to work continues", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: working},
		}},
		{"end only after work", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: working},
			{states: resting, ended: true},
		}},
		{"end through held", []step{
			{states: resting},
			{states: working, started: true},
			{states: held},
			{states: resting, ended: true},
			{states: resting},
		}},
		{"lone first working starts nothing", []step{
			{states: working},
		}},
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
