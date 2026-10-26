package diff_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// numbered returns lines "line 1" to "line n". The lines in upper are
// "LINE 1" and so on.
func numbered(n int, upper ...int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		word := "line"
		if slices.Contains(upper, i) {
			word = "LINE"
		}
		fmt.Fprintf(&b, "%s %d\n", word, i)
	}
	return b.String()
}

// foldText writes a hunk header with fold f, for example "@14/14+13".
func foldText(f diff.Fold) string { return fmt.Sprintf("@%d/%d+%d", f.Old, f.New, f.Lines) }

// layout writes each row of d as one entry. A hunk header is "@" and its
// fold. Another row is its old and its new line number.
func layout(d *diff.Diff) []string {
	var out []string
	for _, r := range d.Rows {
		if r.Kind == diff.KindHunk {
			out = append(out, foldText(r.Fold))
			continue
		}
		out = append(out, fmt.Sprintf("%d:%d", r.Old, r.New))
	}
	return out
}

// headers returns the text of the hunk headers of d.
func headers(d *diff.Diff) string {
	var out []string
	for i, r := range d.Rows {
		if r.Kind == diff.KindHunk {
			out = append(out, strings.TrimSpace(ansi.Strip(d.Lines[i])))
		}
	}
	return strings.Join(out, "\n")
}

// TestFolds checks that a long run of equal lines folds into the hunk
// header below it.
func TestFolds(t *testing.T) {
	t.Parallel()
	before, after := numbered(40), numbered(40, 10, 30)
	header := func(o, n, lines int) []string { return []string{foldText(diff.Fold{Old: o, New: n, Lines: lines})} }
	lines := func(lo, hi int) []string {
		var out []string
		for i := lo; i <= hi; i++ {
			out = append(out, fmt.Sprintf("%d:%d", i, i))
		}
		return out
	}
	change := func(n int) []string { return []string{fmt.Sprintf("%d:0", n), fmt.Sprintf("0:%d", n)} }
	// Three lines show beside each change. A fold that Expanded accepts shows
	// all of its lines.
	top, middle, tail, none := header(1, 1, 6), header(14, 14, 13), header(34, 34, 7), header(0, 0, 0)
	tests := []struct {
		name     string
		expanded func(diff.Fold) bool
		want     []string
	}{
		{"all folded", nil, slices.Concat(
			top, lines(7, 9), change(10), lines(11, 13),
			middle, lines(27, 29), change(30), lines(31, 33),
			tail,
		)},
		{"middle shows", func(f diff.Fold) bool { return f.New == 14 }, slices.Concat(
			top, lines(7, 9), change(10), lines(11, 29), change(30), lines(31, 33),
			tail,
		)},
		{"top and tail show", func(f diff.Fold) bool { return f.New != 14 }, slices.Concat(
			none, lines(1, 9), change(10), lines(11, 13),
			middle, lines(27, 29), change(30), lines(31, 40),
		)},
		{"all show", func(diff.Fold) bool { return true }, slices.Concat(
			none, lines(1, 9), change(10), lines(11, 29), change(30), lines(31, 40),
		)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := diff.Options{Width: 60, Expanded: tt.expanded}
			d := diff.Render("f.txt", before, after, o)
			if got := layout(d); !slices.Equal(got, tt.want) {
				t.Errorf("layout = \n%v\nwant:\n%v", got, tt.want)
			}
		})
	}

	// The header gives the number of hidden lines. A fold at the end has no
	// hunk header. In the split layout, the header continues across the
	// right side. No line is wider than the screen.
	for _, tt := range []struct {
		o    diff.Options
		want string
	}{
		{diff.Options{Width: 60}, "…   …   ⋯ 6 unmodified lines  @@ -7,7 +7,7 @@\n" +
			"…   …   ⋯ 13 unmodified lines  @@ -27,7 +27,7 @@\n" +
			"…   …   ⋯ 7 unmodified lines"},
		{diff.Options{Width: 60, Split: true}, "…   ⋯ 6 unmodified lines  @@ -7,7 +7,7 @@\n" +
			"…   ⋯ 13 unmodified lines  @@ -27,7 +27,7 @@\n" +
			"…   ⋯ 7 unmodified lines"},
	} {
		d := diff.Render("f.txt", before, after, tt.o)
		if got := headers(d); got != tt.want {
			t.Errorf("split %v: headers = \n%s\nwant:\n%s",
				tt.o.Split, got, tt.want)
		}
		for i, l := range d.Lines {
			if w := ansi.StringWidth(l); w > tt.o.Width {
				t.Errorf("split %v: line %d is %d cells wide, want %d or fewer",
					tt.o.Split, i, w, tt.o.Width)
			}
		}
	}
}

// TestFoldEdges checks the folds that show no line.
func TestFoldEdges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		old, new string
		want     string
	}{
		// Changes at 10 and 18 leave line 14 alone between the margins. The
		// line shows, because a fold of one line saves no row.
		{
			"one line", numbered(21), numbered(21, 10, 18),
			"@1/1+6 7:7 8:8 9:9 10:0 0:10 11:11 12:12 13:13 14:14 15:15 16:16 17:17 18:0 0:18 19:19 20:20 21:21",
		},
		// After an insert, the old and the new numbers of a fold differ.
		{
			"insert at the top", numbered(12), "new\n" + numbered(12),
			"@0/0+0 0:1 1:2 2:3 3:4 @4/5+9",
		},
		// A file without equal lines has no fold.
		{"all new", "", "a\nb\n", "@0/0+0 0:1 0:2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := diff.Render("f.txt", tt.old, tt.new, diff.Options{Width: 80})
			if got := strings.Join(layout(d), " "); got != tt.want {
				t.Errorf("layout = \n%s\nwant:\n%s", got, tt.want)
			}
			// The split layout has the same folds.
			o := diff.Options{Width: 80, Split: true}
			split := diff.Render("f.txt", tt.old, tt.new, o)
			if got, want := folds(split), folds(d); !slices.Equal(got, want) {
				t.Errorf("split folds = %v, want %v", got, want)
			}
		})
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
