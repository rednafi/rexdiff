package diff_test

import (
	"fmt"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// TestHostileContent checks the lines and rows of [Render] and
// [RenderFile] for each hostile input, with several layout options.
func TestHostileContent(t *testing.T) {
	t.Parallel()
	opts := []diff.Options{
		{},
		{Width: 20},
		{Width: 20, Wrap: true},
		{Width: 9, Wrap: true},
		{Split: true, Width: 30},
		{Split: true, Width: 30, Wrap: true},
		{Width: 20, ScrollX: 3},
	}
	for _, s := range hostile {
		for _, o := range opts {
			d := diff.Render("x.go", "a\n", s, o)
			checkLines(t, d, o)
			checkRows(t, d.Rows, "a\n", s)
			f := diff.RenderFile("x.go", s, fileOptions(o))
			checkLines(t, f, o)
			checkRows(t, f.Rows, s, s)
			if t.Failed() {
				t.Fatalf("content %q, options %+v", s, o)
			}
		}
	}
}

// TestEscapesAreVisible checks that escape sequences and control
// characters in a file are shown as visible symbols.
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
		f := diff.RenderFile("x.txt", tt.src, diff.FileOptions{})
		if len(screenLines(f)) != 1 || !strings.Contains(ansi.Strip(screenLines(f)[0]), tt.want) {
			t.Errorf("RenderFile(%q) = %q, want one line that shows %q",
				tt.src, screenLines(f), tt.want)
		}
	}
}

// TestBidiControlsAreVisible checks that bidirectional controls and
// zero-width characters in a file are shown as their code points, so that
// they cannot reorder or hide the code on screen, in a highlighted file
// and in a plain one. The zero-width joiner and variation selectors stay,
// because emoji need them.
func TestBidiControlsAreVisible(t *testing.T) {
	t.Parallel()
	tests := []struct{ src, want string }{
		// A "Trojan Source" line: the override makes the comment appear
		// after the return on screen.
		{"if admin { /*\u202e } \u2066if user\u2069 \u2066*/ return\n", "/*<U+202E> } <U+2066>if user<U+2069> <U+2066>*/ return"},
		{"a\u202ab\u202bc\u202cd\u202de\n", "a<U+202A>b<U+202B>c<U+202C>d<U+202D>e"},
		{"a\u2067b\u2068c\n", "a<U+2067>b<U+2068>c"},
		{"a\u200eb\u200fc\u061cd\n", "a<U+200E>b<U+200F>c<U+061C>d"},
		{"\ufeffa\u200bb\u2060c\n", "<U+FEFF>a<U+200B>b<U+2060>c"},
		{"x := \"👨\u200d👩\u200d👧 ❤\ufe0f\"\n", "x := \"👨\u200d👩\u200d👧 ❤\ufe0f\""},
	}
	for _, tt := range tests {
		for _, name := range []string{"x.go", "x.txt"} {
			d := diff.Render(name, "", tt.src, diff.Options{})
			if got := plainLine(t, d, 1); !strings.Contains(got, tt.want) {
				t.Errorf("Render(%q, %q) = %q, want it to show %q", name, tt.src, got, tt.want)
			}
			f := diff.RenderFile(name, tt.src, diff.FileOptions{})
			if len(screenLines(f)) != 1 || !strings.Contains(ansi.Strip(screenLines(f)[0]), tt.want) {
				t.Errorf("RenderFile(%q, %q) = %q, want one line that shows %q",
					name, tt.src, screenLines(f), tt.want)
			}
		}
	}
}

// TestBidiControlCell checks that a click on any cell of a shown bidi
// control finds that control, and that a click after it finds the
// character after it.
func TestBidiControlCell(t *testing.T) {
	t.Parallel()
	d := diff.Render("x.txt", "", "a\u202eb\n", diff.Options{Width: 40})
	at := column(t, d, 1, "<U+202E>", false)
	for x := at; x < at+len("<U+202E>"); x++ {
		if _, _, i, ok := d.Cell(1, x); !ok || i != 1 {
			t.Errorf("Cell(1, %d) = index %d, %t, want 1, true", x, i, ok)
		}
	}
	if _, _, i, ok := d.Cell(1, at+len("<U+202E>")); !ok || i != 2 {
		t.Errorf("Cell after the control = index %d, %t, want 2, true", i, ok)
	}
}

// TestSlowLexers checks that files whose chroma lexers never stop, or
// take seconds, on some lines are drawn quickly. Each line used to take
// more than 40 s or never finish.
func TestSlowLexers(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, line string }{
		{"x.jungle", strings.Repeat(`"`, 100)},
		{"x.jungle", strings.Repeat("x := 1 // y", 10)},
		{"x.jsonata", strings.Repeat(`\`, 100)},
		{"x.ly", strings.Repeat("a", 1500)},
		{"x.mess", strings.Repeat("a", 1500)},
		{"x.dts", strings.Repeat("a", 1500)},
		{"x.service", strings.Repeat("a", 1500)},
	}
	for _, tt := range tests {
		within(t, 10*time.Second, tt.name, func() {
			for _, o := range []diff.Options{{}, {Width: 40, Wrap: true}} {
				diff.Render(tt.name, "", tt.line+"\n", o)
				diff.RenderFile(tt.name, tt.line+"\n", fileOptions(o))
			}
		})
	}
}

// TestSlowLines checks that a diff of long lines that a lexer takes
// seconds to highlight is drawn quickly. Without a time limit, the V
// shell lexer takes 2 s on each of these lines.
func TestSlowLines(t *testing.T) {
	t.Parallel()
	var src strings.Builder
	for i := range 4 {
		fmt.Fprintf(&src, "%s%d\n", strings.Repeat("/*a", 499), i)
	}
	within(t, 6*time.Second, "x.vsh", func() { diff.Render("x.vsh", "", src.String(), diff.Options{}) })
}

// within fails the test if f takes longer than limit. f keeps running
// after the failure.
func within(t *testing.T, limit time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("%s takes more than %s to draw", what, limit)
	}
}

// TestWrapWideText checks that wrapped CJK and emoji text fits the width
// and that no characters are lost.
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
		for i, l := range screenLines(d) {
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

// TestRenderFileLineCount checks the number of rows that [RenderFile]
// returns for different line endings.
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
			if d := diff.RenderFile(path, tt.src, diff.FileOptions{}); len(d.Rows) != tt.rows {
				t.Errorf("RenderFile(%q, %q) has %d rows, want %d",
					path, tt.src, len(d.Rows), tt.rows)
			}
		}
	}
}

// TestRenderFileNarrow checks that wrapped wide text fits widths of 1 to 7
// cells.
func TestRenderFileNarrow(t *testing.T) {
	t.Parallel()
	for w := 1; w < 8; w++ {
		o := diff.Options{Width: w, Wrap: true}
		checkLines(t, diff.RenderFile("x.txt", "日本\n 0000\n", fileOptions(o)), o)
	}
}

// TestReviewHostileComment checks that Review quotes a hostile path and
// removes control characters from the code and the comment text.
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

// TestBlackTint checks that an equal line with the same text as a deleted
// line has no background colour when deleted lines are tinted black.
func TestBlackTint(t *testing.T) {
	t.Parallel()
	black := color.RGBA{A: 255}
	theme := diff.Theme{Background: black, Foreground: color.White, Red: black}
	d := diff.Render("a.txt", "x\ny\nx\n", "y\nx\n", diff.Options{Theme: theme})
	for i, r := range d.Rows {
		if r.Kind == diff.KindEqual && strings.Contains(screenLines(d)[i], "48;2;0;0;0mx") {
			t.Errorf("equal line %d = %q, want no background of a deleted line",
				r.New, screenLines(d)[i])
		}
	}
}
