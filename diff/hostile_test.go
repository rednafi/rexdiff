package diff_test

import (
	"image/color"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// TestHostileContent checks the lines and the rows of each hostile file, in
// all layouts.
func TestHostileContent(t *testing.T) {
	t.Parallel()
	opts := []diff.Options{
		{},
		{Width: 20},
		{Width: 20, Wrap: true},
		{Width: 9, Wrap: true},
		{Split: true, Width: 30},
		{Split: true, Width: 30, Wrap: true},
		{Width: 20, Offset: 3},
	}
	for _, s := range hostile {
		for _, o := range opts {
			d := diff.Render("x.go", "a\n", s, o)
			checkLines(t, d, o)
			checkRows(t, d.Rows, "a\n", s)
			f := diff.RenderFile("x.go", s, o)
			checkLines(t, f, o)
			checkRows(t, f.Rows, s, s)
			if t.Failed() {
				t.Fatalf("content %q, options %+v", s, o)
			}
		}
	}
}

// TestEscapesAreVisible checks that an escape in a file shows as a symbol,
// so that the reader sees it.
func TestEscapesAreVisible(t *testing.T) {
	t.Parallel()
	tests := []struct{ src, want string }{
		{"\x1b]52;c;aGk=\x07\n", "␛]52;c;aGk=␇"},
		{"a\x1b[31mb\n", "a␛[31mb"},
		{"\x1b]7501;state=idle\x1b\\\n", "␛]7501;state=idle␛\\"},
		{"a\x00b\n", "a␀b"},
		{"\u009b31m\n", "\ufffd31m"},
		{"a\rb\n", "a␍b"},
	}
	for _, tt := range tests {
		d := diff.Render("x.txt", "", tt.src, diff.Options{})
		if got := plainLine(t, d, 1); !strings.Contains(got, tt.want) {
			t.Errorf("Render(%q) = %q, want it to show %q", tt.src, got, tt.want)
		}
		f := diff.RenderFile("x.txt", tt.src, diff.Options{})
		if len(f.Lines) != 1 || !strings.Contains(ansi.Strip(f.Lines[0]), tt.want) {
			t.Errorf("RenderFile(%q) = %q, want one line that shows %q",
				tt.src, f.Lines, tt.want)
		}
	}
}

// TestWrapWideText checks that wrapped CJK and emoji text fits the width
// and that no character is lost.
func TestWrapWideText(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("日本語😀", 10)
	for _, o := range []diff.Options{
		{Width: 25, Wrap: true},
		{Split: true, Width: 41, Wrap: true},
	} {
		d := diff.Render("x.txt", "", line+"\n", o)
		checkLines(t, d, o)
		var text strings.Builder
		for i, l := range d.Lines {
			if d.Rows[i].New == 1 {
				text.WriteString(ansi.Strip(l))
			}
		}
		got := strings.Join(strings.Fields(text.String()), "")
		if !strings.Contains(got, line) {
			t.Errorf("split %v: the wrapped lines = %q, want all of %q",
				o.Split, got, line)
		}
	}
}

// TestRenderFileLineCount checks the number of rows for each kind of line
// ending.
func TestRenderFileLineCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  string
		rows int
	}{
		{"", 0},
		{"\n", 1},
		{"\n\n\n", 3},
		{"a", 1},
		{"a\r\nb\r\n", 2},
		{"a\rb\rc", 1},
	}
	for _, tt := range tests {
		for _, path := range []string{"x.txt", "Makefile", "x.go"} {
			if d := diff.RenderFile(path, tt.src, diff.Options{}); len(d.Rows) != tt.rows {
				t.Errorf("RenderFile(%q, %q) has %d rows, want %d",
					path, tt.src, len(d.Rows), tt.rows)
			}
		}
	}
}

// TestRenderFileNarrow checks that wide text fits a width of 1 to 7 cells.
func TestRenderFileNarrow(t *testing.T) {
	t.Parallel()
	for w := 1; w < 8; w++ {
		o := diff.Options{Width: w, Wrap: true}
		checkLines(t, diff.RenderFile("x.txt", "日本\n 0000\n", o), o)
	}
}

// TestReviewHostileComment checks that Review quotes a hostile path and
// removes the control characters from the code and the text.
func TestReviewHostileComment(t *testing.T) {
	t.Parallel()
	c := comment("a\x1b]52;c;aGk=\x07.go", diff.SideNew,
		"",
		"x\x1b[2J\n",
		"ok\x1b]7501;state=idle\x07\r\nnext\u0085line",
	)
	want := `"a\x1b]52;c;aGk=\a.go":1` + "\n+x[2J\nok]7501;state=idle\nnextline"
	if got := review(c); got != want {
		t.Errorf("Review = %q, want %q", got, want)
	}
}

// TestBlackTint checks that an equal line with the text of a deleted line has
// no background when the tint of deleted lines is black.
func TestBlackTint(t *testing.T) {
	t.Parallel()
	black := color.RGBA{A: 255}
	th := diff.Theme{Background: black, Foreground: color.White, Red: black}
	d := diff.Render("a.txt", "x\ny\nx\n", "y\nx\n", diff.Options{Theme: th})
	for i, r := range d.Rows {
		if r.Kind == diff.KindEqual && strings.Contains(d.Lines[i], "48;2;0;0;0mx") {
			t.Errorf("equal line %d = %q, want no background of a deleted line",
				r.New, d.Lines[i])
		}
	}
}
