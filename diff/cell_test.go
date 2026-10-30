package diff_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

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

func plainLine(t *testing.T, d *diff.Diff, i int) string {
	t.Helper()
	if i < 0 || i >= len(screenLines(d)) {
		t.Fatalf("line %d of a diff with %d lines", i, len(screenLines(d)))
	}
	return ansi.Strip(screenLines(d)[i])
}

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

func TestCell(t *testing.T) {
	t.Parallel()
	const oldSrc, newSrc = "a\n\told_name := 1\n", "a\n\tnew_name := 1\n"
	// Each subtest renders its own Diff, because a Diff is not safe for
	// concurrent use.
	render := func(o diff.Options) func() *diff.Diff {
		return func() *diff.Diff { return diff.Render("x.go", oldSrc, newSrc, o) }
	}
	u := render(diff.Options{Width: 80})
	off := render(diff.Options{Width: 80, ScrollX: 6})
	s := render(diff.Options{Width: 80, Split: true})
	f := func() *diff.Diff { return diff.RenderFile("x.go", newSrc, diff.FileOptions{Width: 80}) }
	tests := []struct {
		name   string
		d      func() *diff.Diff
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
			d := tt.d()
			x := column(t, d, tt.row, tt.needle, tt.last)
			side, line, char, ok := d.Cell(tt.row, x)
			if !ok || side != tt.side || line != tt.line || char != tt.char {
				t.Errorf("Cell(%d, %d) = %v, %d, %d, %v, want %v, %d, %d, true",
					tt.row, x, side, line, char, ok,
					tt.side, tt.line, tt.char)
			}
		})
	}
	ud := u()
	x := column(t, ud, 3, "new", false)
	if _, _, char, _ := ud.Cell(3, x-1); char != 0 {
		t.Errorf("the tab: char %d, want 0", char)
	}
	if _, _, char, ok := ud.Cell(3, 79); !ok || char != 14 {
		t.Errorf("after the code: char %d, %v, want 14, true", char, ok)
	}
	for _, c := range []struct{ row, x int }{{3, 0}, {3, 4}, {3, x - 5}, {0, 20}, {9, 20}} {
		if _, _, _, ok := ud.Cell(c.row, c.x); ok {
			t.Errorf("Cell(%d, %d) is code, want not code", c.row, c.x)
		}
	}
	ins := diff.Render("x.go", "a\n", "a\nb\n", diff.Options{Width: 80, Split: true})
	if _, _, _, ok := ins.Cell(2, 10); ok {
		t.Error("the empty old side of an insert is code, want not code")
	}
}

func TestCellMatchesScreen(t *testing.T) {
	t.Parallel()
	var words []string
	for i := range 30 {
		words = append(words, "w"+string(rune('a'+i%26))+strings.Repeat("x", i%4))
	}
	long := "\t" + strings.Join(words, " ") + " 日本 end"
	oldSrc, newSrc := "a\nb\n", "a\n"+long+"\nb\n"
	opts := map[string]diff.Options{
		"cut":    {Width: 60},
		"offset": {Width: 60, ScrollX: 13},
		"wrap":   {Width: 60, Wrap: true},
	}
	src := map[diff.Side][]string{diff.SideOld: diff.SplitLines(oldSrc), diff.SideNew: diff.SplitLines(newSrc)}
	for _, tt := range renderers(oldSrc, newSrc) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for on, o := range opts {
				checkCells(t, on, tt.render(o), src)
			}
		})
	}
}

func checkCells(t *testing.T, on string, d *diff.Diff, src map[diff.Side][]string) {
	t.Helper()
	checked := 0
	for i, l := range screenLines(d) {
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

func TestCellGraphemes(t *testing.T) {
	t.Parallel()
	for _, g := range []string{"❤️", "👨‍👩‍👧", "🇺🇸"} {
		line := strings.Repeat(g+"x ", 20)
		src := []rune(line)
		for _, o := range []diff.Options{{Width: 200}, {Width: 40, Wrap: true}} {
			d := diff.RenderFile("x.txt", line+"\n", fileOptions(o))
			found := 0
			for i, l := range screenLines(d) {
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

func TestCellOffsetInWideChar(t *testing.T) {
	t.Parallel()
	const line = "日本x"
	for _, off := range []int{1, 3} {
		o := diff.Options{Width: 40, ScrollX: off}
		for name, d := range map[string]*diff.Diff{
			"diff": diff.Render("x.txt", "", line+"\n", o),
			"file": diff.RenderFile("x.txt", line+"\n", fileOptions(o)),
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

func TestLocate(t *testing.T) {
	t.Parallel()
	line := "\tstart " + strings.Repeat("word ", 40) + "HIT end"
	at := strings.Index(line, "HIT")
	src := "a\n" + line + "\n"
	for _, tt := range []struct {
		name string
		o    diff.Options
		file bool
		row  int
	}{
		{"diff", diff.Options{Width: 60}, false, 2},
		{"diff wrapped", diff.Options{Width: 60, Wrap: true}, false, 2},
		{"split wrapped", diff.Options{Width: 90, Split: true, Wrap: true}, false, 2},
		{"file", diff.Options{Width: 60}, true, 1},
		{"file wrapped", diff.Options{Width: 60, Wrap: true}, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := diff.Render("x.txt", "a\n", src, tt.o)
			if tt.file {
				d = diff.RenderFile("x.txt", src, fileOptions(tt.o))
			}
			if len(d.Rows) == 0 {
				t.Fatal("no lines")
			}
			got, col := d.Locate(tt.row, diff.SideNew, at)
			// The tab before "start" shows as four spaces.
			codeAt := column(t, d, tt.row, "start", false) - len("    ")
			width := d.CodeWidth(diff.SideNew)
			if !strings.HasSuffix(tt.name, "wrapped") {
				if got != tt.row || col <= width {
					t.Errorf("Locate = %d, %d, want line %d and a column past the width %d", got, col, tt.row, width)
				}
				return
			}
			if got == tt.row || col >= width {
				t.Fatalf("Locate = %d, %d, want a later screen line and a column within the width %d", got, col, width)
			}
			if r, ok := screenRune(plainLine(t, d, got), codeAt+col); !ok || r != 'H' {
				t.Errorf("screen line %d shows %q at column %d, want 'H'", got, r, codeAt+col)
			}
		})
	}
}
