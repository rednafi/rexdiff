package diff_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// screenRune returns the rune of the plain line that covers column x.
func screenRune(plain string, x int) (rune, bool) {
	c := 0
	for _, r := range plain {
		w := ansi.StringWidth(string(r))
		if x < c+w {
			return r, true
		}
		c += w
	}
	return 0, false
}

// plainLine returns line i of d without the escapes. It stops the test if d
// has no line i.
func plainLine(t *testing.T, d *diff.Diff, i int) string {
	t.Helper()
	if i < 0 || i >= len(d.Lines) {
		t.Fatalf("line %d of a diff with %d lines", i, len(d.Lines))
	}
	return ansi.Strip(d.Lines[i])
}

// column returns the column of the first cell of needle in line i of d.
// With last, it finds the last needle.
func column(t *testing.T, d *diff.Diff, i int, needle string, last bool) int {
	t.Helper()
	plain := plainLine(t, d, i)
	k := strings.Index(plain, needle)
	if last {
		k = strings.LastIndex(plain, needle)
	}
	if k < 0 {
		t.Fatalf("line %d = %q, want %q in it", i, plain, needle)
	}
	return ansi.StringWidth(plain[:k])
}

// TestCell checks the source character of a cell, with tabs, an offset,
// the split layout, and a file.
func TestCell(t *testing.T) {
	t.Parallel()
	const before, after = "a\n\told_name := 1\n", "a\n\tnew_name := 1\n"
	u := diff.Render("x.go", before, after, diff.Options{Width: 80})
	off := diff.Render("x.go", before, after, diff.Options{Width: 80, Offset: 6})
	s := diff.Render("x.go", before, after, diff.Options{Width: 80, Split: true})
	f := diff.RenderFile("x.go", after, diff.Options{Width: 80})
	tests := []struct {
		name   string
		d      *diff.Diff
		row    int
		needle string
		last   bool
		side   diff.Side
		line   int
		char   int
	}{
		{"unified delete", u, 2, "old_name", false, diff.SideOld, 2, 1},
		{"unified insert", u, 3, "name", false, diff.SideNew, 2, 5},
		{"unified equal", u, 1, "a", false, diff.SideNew, 1, 0},
		{"offset", off, 3, "e :=", false, diff.SideNew, 2, 8},
		{"split old", s, 2, "old_name", false, diff.SideOld, 2, 1},
		{"split new", s, 2, "name", true, diff.SideNew, 2, 5},
		{"file", f, 1, "new_name", false, diff.SideNew, 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := column(t, tt.d, tt.row, tt.needle, tt.last)
			side, line, char, ok := tt.d.Cell(tt.row, x)
			if !ok || side != tt.side || line != tt.line || char != tt.char {
				t.Errorf("Cell(%d, %d) = %v, %d, %d, %v, want %v, %d, %d, true",
					tt.row, x, side, line, char, ok,
					tt.side, tt.line, tt.char)
			}
		})
	}
	// A cell of the tab gives the tab. A cell after the code gives the end.
	x := column(t, u, 3, "new", false)
	if _, _, char, _ := u.Cell(3, x-1); char != 0 {
		t.Errorf("the tab: char %d, want 0", char)
	}
	if _, _, char, ok := u.Cell(3, 79); !ok || char != 14 {
		t.Errorf("after the code: char %d, %v, want 14, true", char, ok)
	}
	// The numbers, the mark, and the hunk header are not code.
	for _, c := range []struct{ row, x int }{{3, 0}, {3, 4}, {3, x - 5}, {0, 20}, {9, 20}} {
		if _, _, _, ok := u.Cell(c.row, c.x); ok {
			t.Errorf("Cell(%d, %d) is code, want not code", c.row, c.x)
		}
	}
	// The other side of a split insert has no line.
	ins := diff.Render("x.go", "a\n", "a\nb\n", diff.Options{Width: 80, Split: true})
	if _, _, _, ok := ins.Cell(2, 10); ok {
		t.Error("the empty old side of an insert is code, want not code")
	}
}

// TestCellMatchesScreen checks that each code cell shows the character
// that Cell gives, in each layout, with wrap, and with an offset.
func TestCellMatchesScreen(t *testing.T) {
	t.Parallel()
	var words []string
	for i := range 30 {
		words = append(words, "w"+string(rune('a'+i%26))+strings.Repeat("x", i%4))
	}
	long := "\t" + strings.Join(words, " ") + " 日本 end"
	before, after := "a\nb\n", "a\n"+long+"\nb\n"
	opts := map[string]diff.Options{
		"cut":    {Width: 60},
		"offset": {Width: 60, Offset: 13},
		"wrap":   {Width: 60, Wrap: true},
	}
	src := map[diff.Side][]string{diff.SideOld: diff.Lines(before), diff.SideNew: diff.Lines(after)}
	for _, tt := range renderers(before, after) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for on, o := range opts {
				checkCells(t, on, tt.render(o), src)
			}
		})
	}
}

// checkCells checks that each code cell of d shows the character that
// Cell gives. The lines of src are the source of each side.
func checkCells(t *testing.T, on string, d *diff.Diff, src map[diff.Side][]string) {
	t.Helper()
	checked := 0
	for i, l := range d.Lines {
		plain := ansi.Strip(l)
		for x := range ansi.StringWidth(plain) {
			side, line, char, ok := d.Cell(i, x)
			if !ok {
				continue
			}
			got, _ := screenRune(plain, x)
			text := []rune(src[side][line-1])
			if char >= len(text) {
				if got != ' ' {
					t.Errorf("%s: line %d col %d shows %q, want past the end",
						on, i, x, got)
				}
				continue
			}
			want := text[char]
			if want == '\t' {
				want = ' '
			}
			if got != want && got != '…' {
				t.Errorf("%s: line %d col %d shows %q, want %q from Cell",
					on, i, x, got, want)
			}
			checked++
		}
	}
	if checked < 20 {
		t.Errorf("%s: %d code cells, want 20 or more", on, checked)
	}
}

// TestCellGraphemes checks Cell after grapheme clusters of more than one
// code point, in a cut line and in a wrapped line.
func TestCellGraphemes(t *testing.T) {
	t.Parallel()
	for _, g := range []string{"❤️", "👨‍👩‍👧", "🇺🇸"} {
		line := strings.Repeat(g+"x ", 20)
		src := []rune(line)
		for _, o := range []diff.Options{{Width: 200}, {Width: 40, Wrap: true}} {
			d := diff.RenderFile("x.txt", line+"\n", o)
			found := 0
			for i, l := range d.Lines {
				plain := ansi.Strip(l)
				for k := range len(plain) {
					if plain[k] != 'x' {
						continue
					}
					x := ansi.StringWidth(plain[:k])
					_, _, char, ok := d.Cell(i, x)
					if !ok || char >= len(src) || src[char] != 'x' {
						t.Errorf("%q wrap %v: Cell(%d, %d) = %d, %v, want the index of an x",
							g, o.Wrap, i, x, char, ok)
					}
					found++
				}
			}
			if found != 20 {
				t.Errorf("%q wrap %v: %d x cells, want 20", g, o.Wrap, found)
			}
		}
	}
}

// TestCellOffsetInWideChar checks Cell when the offset cuts a wide
// character. The content moves by the offset, so Cell agrees with the
// screen.
func TestCellOffsetInWideChar(t *testing.T) {
	t.Parallel()
	const line = "日本x"
	for _, off := range []int{1, 3} {
		o := diff.Options{Width: 40, Offset: off}
		for name, d := range map[string]*diff.Diff{
			"diff": diff.Render("x.txt", "", line+"\n", o),
			"file": diff.RenderFile("x.txt", line+"\n", o),
		} {
			row := len(d.Rows) - 1
			for char, needle := range []rune(line) {
				if !strings.ContainsRune(plainLine(t, d, row), needle) {
					continue
				}
				x := column(t, d, row, string(needle), false)
				if _, _, got, ok := d.Cell(row, x); !ok || got != char {
					t.Errorf("%s offset %d: Cell on %q = %d, %v, want %d, true",
						name, off, needle, got, ok, char)
				}
			}
		}
	}
}
