package diff_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// numbered returns lines "line 1" to "line n", with the lines in upper
// case changed to "LINE n".
func numbered(n int, upper ...int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		word := "line"
		for _, u := range upper {
			if u == i {
				word = "LINE"
			}
		}
		fmt.Fprintf(&b, "%s %d\n", word, i)
	}
	return b.String()
}

// layout writes the rows of d as one entry each: "@" and its fold for a
// hunk header, else the old and the new line number.
func layout(d *diff.Diff) []string {
	var out []string
	for _, r := range d.Rows {
		if r.Kind == diff.KindHunk {
			out = append(out, fmt.Sprintf("@%d/%d+%d", r.Fold.Old, r.Fold.New, r.Fold.Lines))
			continue
		}
		out = append(out, fmt.Sprintf("%d:%d", r.Old, r.New))
	}
	return out
}

// TestFolds checks that a long run of equal lines folds into the hunk
// header below it. Three lines show beside each change. A fold that Show
// accepts shows all its lines.
func TestFolds(t *testing.T) {
	old, new := numbered(40), numbered(40, 10, 30)
	fold := func(old, new, n int) string { return fmt.Sprintf("@%d/%d+%d", old, new, n) }
	lines := func(lo, hi int) []string {
		var out []string
		for i := lo; i <= hi; i++ {
			out = append(out, fmt.Sprintf("%d:%d", i, i))
		}
		return out
	}
	change := func(n int) []string { return []string{fmt.Sprintf("%d:0", n), fmt.Sprintf("0:%d", n)} }
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	top, middle, tail := fold(1, 1, 6), fold(14, 14, 13), fold(34, 34, 7)
	tests := []struct {
		name string
		show func(diff.Fold) bool
		want []string
	}{
		{"all folded", nil, join(
			[]string{top}, lines(7, 9), change(10), lines(11, 13),
			[]string{middle}, lines(27, 29), change(30), lines(31, 33),
			[]string{tail},
		)},
		{"middle shows", func(f diff.Fold) bool { return f.New == 14 }, join(
			[]string{top}, lines(7, 9), change(10), lines(11, 29), change(30), lines(31, 33),
			[]string{tail},
		)},
		{"top and tail show", func(f diff.Fold) bool { return f.New != 14 }, join(
			[]string{fold(0, 0, 0)}, lines(1, 9), change(10), lines(11, 13),
			[]string{middle}, lines(27, 29), change(30), lines(31, 40),
		)},
		{"all show", func(diff.Fold) bool { return true }, join(
			[]string{fold(0, 0, 0)}, lines(1, 9), change(10), lines(11, 29), change(30), lines(31, 40),
		)},
	}
	for _, tt := range tests {
		d := diff.Render("f.txt", old, new, diff.Options{Width: 60, Show: tt.show})
		if got := layout(d); strings.Join(got, " ") != strings.Join(tt.want, " ") {
			t.Errorf("%s:\n got %v\nwant %v", tt.name, got, tt.want)
		}
	}

	// The header tells how many lines hide. A fold at the end has no hunk
	// header. In the split layout, the header goes on across the right
	// side, and no line is wider than the screen.
	headers := func(o diff.Options) string {
		d := diff.Render("f.txt", old, new, o)
		var out []string
		for i, r := range d.Rows {
			if w := ansi.StringWidth(d.Lines[i]); w > o.Width {
				t.Errorf("split %v: line %d is %d cells wide", o.Split, i, w)
			}
			if r.Kind == diff.KindHunk {
				out = append(out, strings.TrimSpace(ansi.Strip(d.Lines[i])))
			}
		}
		return strings.Join(out, "\n")
	}
	want := "…   …   ⋯ 6 unmodified lines  @@ -7,7 +7,7 @@\n" +
		"…   …   ⋯ 13 unmodified lines  @@ -27,7 +27,7 @@\n" +
		"…   …   ⋯ 7 unmodified lines"
	if got := headers(diff.Options{Width: 60}); got != want {
		t.Errorf("headers:\n%s\nwant:\n%s", got, want)
	}
	want = "…   ⋯ 6 unmodified lines  @@ -7,7 +7,7 @@\n" +
		"…   ⋯ 13 unmodified lines  @@ -27,7 +27,7 @@\n" +
		"…   ⋯ 7 unmodified lines"
	if got := headers(diff.Options{Width: 60, Split: true}); got != want {
		t.Errorf("split headers:\n%s\nwant:\n%s", got, want)
	}
}

// TestFoldEdges checks the folds that show no line. One line between the
// margins of two changes shows, because a fold of one line saves no row.
// After an insert, the old and the new numbers of a fold differ. A file
// without equal lines has no fold.
func TestFoldEdges(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     string
	}{
		// Changes at 10 and 18 leave line 14 alone between the margins.
		{"one line", numbered(21), numbered(21, 10, 18),
			"@1/1+6 7:7 8:8 9:9 10:0 0:10 11:11 12:12 13:13 14:14 15:15 16:16 17:17 18:0 0:18 19:19 20:20 21:21"},
		{"insert at the top", numbered(12), "new\n" + numbered(12),
			"@0/0+0 0:1 1:2 2:3 3:4 @4/5+9"},
		{"all new", "", "a\nb\n", "@0/0+0 0:1 0:2"},
	}
	for _, tt := range tests {
		d := diff.Render("f.txt", tt.old, tt.new, diff.Options{Width: 80})
		if got := strings.Join(layout(d), " "); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
		// The split layout has the same folds.
		split := diff.Render("f.txt", tt.old, tt.new, diff.Options{Width: 80, Split: true})
		if got, want := folds(split), folds(d); !slices.Equal(got, want) {
			t.Errorf("%s: the split layout has folds %v, want %v", tt.name, got, want)
		}
	}
}

// folds returns the folds of the hunk headers of d.
func folds(d *diff.Diff) []diff.Fold {
	var out []diff.Fold
	for _, r := range d.Rows {
		if r.Kind == diff.KindHunk {
			out = append(out, r.Fold)
		}
	}
	return out
}
