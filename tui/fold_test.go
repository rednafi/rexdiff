package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/rednafi/rexdiff/diff"
	"github.com/rednafi/rexdiff/vcs"
)

// newFoldModel returns a model that shows f.txt, a file of 40 lines with
// changes at lines 10 and 30. The diff folds lines 1 to 6, 14 to 26, and
// 34 to 40.
func newFoldModel(t *testing.T) model {
	t.Helper()
	dir := t.TempDir()
	lines := func(upper ...int) string {
		var b strings.Builder
		for i := 1; i <= 40; i++ {
			word := "line"
			if slices.Contains(upper, i) {
				word = "LINE"
			}
			fmt.Fprintf(&b, "%s %d\n", word, i)
		}
		return b.String()
	}
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", lines())
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "init")
	write(t, dir, "f.txt", lines(10, 30))
	repo, err := vcs.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), Options{Repo: repo, Layout: LayoutUnified}, defaultConfig())
	m.reload()
	return step(t, m, tea.WindowSizeMsg{Width: 100, Height: 70})
}

// foldsOf returns the number of lines of each fold that hides in p.
func foldsOf(p *pane) []int {
	var out []int
	for _, r := range p.rows {
		if r.Fold.Lines > 0 {
			out = append(out, r.Fold.Lines)
		}
	}
	return out
}

// rowOf returns the row of new line n in p, or -1.
func rowOf(p *pane, n int) int {
	return slices.IndexFunc(p.rows, func(r diff.Row) bool { return r.New == n && r.Kind != diff.KindHunk })
}

// TestExpandFold checks that → on a fold shows its lines, and moves the
// cursor to the first of them. The footer tells about the key. On a line
// of code, → scrolls sideways.
func TestExpandFold(t *testing.T) {
	m := newFoldModel(t)
	p := &m.changes
	if got := foldsOf(p); !slices.Equal(got, []int{6, 13, 7}) {
		t.Fatalf("folds = %v, want [6 13 7]", got)
	}
	if !shows(m, "⋯ 13 unmodified lines", "→ show the lines") {
		t.Fatalf("the screen does not show the fold and its key:\n%s", view(m))
	}
	m = press(t, m, "]") // the first change, line 10
	if shows(m, "→ show the lines") {
		t.Error("the footer tells about → on a line of code")
	}
	m = press(t, m, "]", "k", "k", "k", "k") // the second change, then up to its header
	if r := m.changes.rows[m.changes.cur]; r.Fold.Lines != 13 || !shows(m, "→ show the lines") {
		t.Fatalf("the cursor is on %+v, want the middle fold", r)
	}
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	p = &m.changes
	if got := foldsOf(p); !slices.Equal(got, []int{6, 7}) {
		t.Errorf("after →, folds = %v, want [6 7]", got)
	}
	if r := p.rows[p.cur]; r.New != 14 {
		t.Errorf("after →, the cursor is on %+v, want line 14", r)
	}
	for n := 14; n <= 26; n++ {
		if rowOf(p, n) < 0 {
			t.Errorf("line %d does not show", n)
		}
	}
	// On a line of code, → scrolls.
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.changes.xoff == 0 || !slices.Equal(foldsOf(&m.changes), []int{6, 7}) {
		t.Errorf("→ on code: xoff %d, folds %v; want a scroll and no change",
			m.changes.xoff, foldsOf(&m.changes))
	}
}

// TestGotoLineInFold checks that : with a line in a fold shows the fold and
// moves the cursor to the line.
func TestGotoLineInFold(t *testing.T) {
	m := newFoldModel(t)
	m = press(t, m, ":")
	m = step(t, m, tea.KeyPressMsg{Text: "20", Code: '2'})
	m = press(t, m, "enter")
	p := &m.changes
	if r := p.rows[p.cur]; r.New != 20 {
		t.Errorf("the cursor is on %+v, want line 20", r)
	}
	if got := foldsOf(p); !slices.Equal(got, []int{6, 7}) {
		t.Errorf("folds = %v, want [6 7]", got)
	}
}

// TestClickFold checks that a click on a fold shows its lines, also in the
// split layout and with wrap.
func TestClickFold(t *testing.T) {
	for _, keys := range [][]string{nil, {"|"}, {"w"}} {
		m := press(t, newFoldModel(t), keys...)
		tail := slices.IndexFunc(m.changes.rows, func(r diff.Row) bool { return r.Fold.Lines == 7 })
		m = click(t, m, m.mainX()+10, screenY(t, m, tail))
		if got := foldsOf(&m.changes); !slices.Equal(got, []int{6, 13}) {
			t.Errorf("keys %v: after the click, folds = %v, want [6 13]", keys, got)
		}
		if rowOf(&m.changes, 40) < 0 {
			t.Errorf("keys %v: line 40 does not show", keys)
		}
	}
}

// TestFoldKeepsComment checks that a fold with a comment on one of its
// lines shows. A new scope folds the lines again, but the comment must
// stay on the screen.
func TestFoldKeepsComment(t *testing.T) {
	m := newFoldModel(t)
	m.changes.cur = slices.IndexFunc(m.changes.rows, func(r diff.Row) bool { return r.Fold.Lines == 13 })
	m = step(t, m, tea.KeyPressMsg{Code: tea.KeyRight})
	m.changes.cur = rowOf(&m.changes, 20)
	m = press(t, m, "c")
	m = step(t, m, tea.KeyPressMsg{Text: "why", Code: 'w'})
	m = press(t, m, "enter")
	if m.comments.Len() != 1 {
		t.Fatalf("%d comments, want 1", m.comments.Len())
	}
	m = press(t, m, "b", "u") // the branch scope, then back
	if m.folds != nil {
		t.Errorf("a new scope keeps the expanded folds %v", m.folds)
	}
	if rowOf(&m.changes, 20) < 0 || !shows(m, "why") {
		t.Errorf("the commented line 20 does not show:\n%s", view(m))
	}
	if got := foldsOf(&m.changes); !slices.Equal(got, []int{6, 7}) {
		t.Errorf("folds = %v, want [6 7]", got)
	}
}
