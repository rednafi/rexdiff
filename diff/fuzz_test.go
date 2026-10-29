package diff_test

import (
	"image/color"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// paths are file names that select the syntax highlighting in the fuzz
// tests.
var paths = []string{"x.go", "x.py", "x.txt", "x.md", "x.html", "x.json", "Makefile", "x"}

// hostile holds file contents that could attack a terminal or break the
// layout.
var hostile = []string{
	"",
	"\n",
	"\n\n\n",
	"a\r\nb\r\n",
	"a\rb\n",
	"\tx := 1\n\t\ty\n",
	"a\x00b\n",
	"\x1b[31mred\x1b[0m\n",
	"\x1b]52;c;aGVsbG8=\x07\n",
	"\x1b]7501;state=idle\x1b\\\n",
	"\u009b31m\u009d0;title\u009c\n",
	"\x9b31m\xff\xfe\n",
	"日本語のテキスト😀👨‍👩‍👧\n",
	"a\u0301\u200d\u202e\n",
	"/*\u202e } \u2066if\u2069 \u2066*/\u200b\ufeff\u061c\u200e\u200f\n",
	strings.Repeat("中\u202e文\u2066", 40) + "\n",
	strings.Repeat("long ", 200) + "\n",
	"no newline at end",
}

// sgr matches the colour escape sequences that the renderer writes.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// isUnsafeControl reports whether r is a control character other than a
// tab or a newline.
func isUnsafeControl(r rune) bool {
	return r != '\t' && r != '\n' && unicode.IsControl(r)
}

// isBidiControl reports whether r is a bidirectional control character,
// which can reorder the text that a terminal shows.
func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069'
}

// fuzzOptions builds Options from fuzz inputs.
func fuzzOptions(layout, width, scrollX uint8, colored bool) diff.Options {
	o := diff.Options{
		Split:   layout&1 != 0,
		Wrap:    layout&2 != 0,
		Width:   int(width % 60),
		ScrollX: int(scrollX % 20),
	}
	if colored {
		o.Theme = diff.Theme{
			Background: color.Black,
			Foreground: color.White,
		}
	}
	return o
}

// checkLines checks that d has one line for each row, that each line fits
// the width in o, and that no line contains control characters other than
// colour sequences.
func checkLines(t *testing.T, d *diff.Diff, o diff.Options) {
	t.Helper()
	if len(screenLines(d)) != len(d.Rows) {
		t.Fatalf("%d lines for %d rows, want one line for each row",
			len(screenLines(d)), len(d.Rows))
	}
	for i, l := range screenLines(d) {
		if w := ansi.StringWidth(l); o.Width > 0 && w > o.Width {
			t.Errorf("line %d is %d cells wide, want %d or fewer: %q",
				i, w, o.Width, l)
		}
		rest := sgr.ReplaceAllString(l, "")
		if strings.ContainsFunc(rest, unicode.IsControl) {
			t.Errorf("line %d = %q, want no control characters", i, l)
		}
		if strings.ContainsFunc(rest, isBidiControl) {
			t.Errorf("line %d = %q, want no bidirectional controls", i, l)
		}
	}
}

// checkSameText checks that two renders of the same diff show the same
// rows and text when their lines are read in opposite orders. The colours
// can differ: a long line that misses its highlighting time limit shows
// plain.
func checkSameText(t *testing.T, a, b *diff.Diff) {
	t.Helper()
	want := screenLines(a)
	if !slices.Equal(a.Rows, b.Rows) {
		t.Fatalf("rows differ:\n first  %v\n second %v", a.Rows, b.Rows)
	}
	for i := range slices.Backward(b.Rows) {
		if got := ansi.Strip(b.Line(i)); got != ansi.Strip(want[i]) {
			t.Fatalf("line %d = %q, want %q", i, got, ansi.Strip(want[i]))
		}
	}
}

// checkRows checks that each row refers to existing lines, that line
// numbers increase, and that a continued row repeats the lines of the row
// above.
func checkRows(t *testing.T, rows []diff.Row, oldText, newText string) {
	t.Helper()
	oldCount, newCount := len(diff.SplitLines(oldText)), len(diff.SplitLines(newText))
	lastOld, lastNew := 0, 0
	for i, r := range rows {
		if r.Old < 0 || r.Old > oldCount || r.New < 0 || r.New > newCount {
			t.Fatalf("row %d = %+v, want Old in 0-%d and New in 0-%d",
				i, r, oldCount, newCount)
		}
		if r.Continued {
			if i == 0 || rows[i-1].Old != r.Old || rows[i-1].New != r.New {
				t.Fatalf("continued row %d = %+v, want the lines of the row above",
					i, r)
			}
			continue
		}
		oldBack := r.Old != 0 && r.Old <= lastOld
		newBack := r.New != 0 && r.New <= lastNew
		if oldBack || newBack {
			t.Fatalf("row %d = %+v, want it after old line %d and new line %d",
				i, r, lastOld, lastNew)
		}
		lastOld, lastNew = max(lastOld, r.Old), max(lastNew, r.New)
	}
}

// FuzzRender checks that the output of Render is well formed, that a lazy
// render matches it, and that CountLines agrees with a comment's diff
// lines.
func FuzzRender(f *testing.F) {
	for i, s := range hostile {
		f.Add(uint8(i), "a\nb\n", s, uint8(i), uint8(i*7), uint8(i%3), i%2 == 0)
		f.Add(uint8(i), s, "a\n"+s+"b\n", uint8(i+1), uint8(i*5), uint8(0), i%2 == 1)
	}
	f.Add(uint8(0), oldText, newText, uint8(3), uint8(1), uint8(0), false)
	f.Add(uint8(0), "\tx\n", "    x\n", uint8(0), uint8(0), uint8(0), false)
	f.Fuzz(func(
		t *testing.T,
		p uint8,
		oldSrc, newSrc string,
		layout, width, scrollX uint8,
		colored bool,
	) {
		o := fuzzOptions(layout, width, scrollX, colored)
		d := diff.Render(paths[int(p)%len(paths)], oldSrc, newSrc, o)
		checkLines(t, d, o)
		checkSameText(t, d, diff.Render(paths[int(p)%len(paths)], oldSrc, newSrc, o))
		checkRows(t, d.Rows, oldSrc, newSrc)
		if o.Width == 0 && o.ScrollX == 0 && !strings.Contains(oldSrc+newSrc, "…") {
			// Without a width limit, no line is cut.
			for i, l := range screenLines(d) {
				cut := strings.Contains(ansi.Strip(l), "…")
				if cut && d.Rows[i].Kind != diff.KindHunk {
					t.Fatalf("line %d = %q, want no cut with no width limit", i, l)
				}
			}
		}

		added, deleted := diff.CountLines(oldSrc, newSrc)
		if (added == 0 && deleted == 0) != (len(screenLines(d)) == 0) {
			t.Fatalf("Count = %d, %d and Render has %d lines, "+
				"want zero counts only for zero lines",
				added, deleted, len(screenLines(d)))
		}
		var plus, minus int
		for _, l := range snippet(d.Rows, oldSrc, newSrc) {
			switch l[0] {
			case '+':
				plus++
			case '-':
				minus++
			}
		}
		if plus != added || minus != deleted {
			t.Fatalf("the snippet has +%d -%d, want Count = +%d -%d",
				plus, minus, added, deleted)
		}
	})
}

// FuzzRenderApply checks that a diff with every fold expanded changes the
// old text into the new one: its rows list every old line and every new
// line once and in order, and each equal row joins two identical lines,
// line endings included.
func FuzzRenderApply(f *testing.F) {
	for _, s := range hostile {
		f.Add("a\nb\n", s)
		f.Add(s, "a\n"+s+"b\n")
	}
	f.Add(oldText, newText)
	f.Add("a\nb\nc\nd\n", "x\nb\ny\nz\nd\n")
	f.Add("a\r\nb\r\n", "a\nb\r\n")
	f.Add("x\ny\nz\n", "y\nx\nz")
	f.Fuzz(func(t *testing.T, oldSrc, newSrc string) {
		o := diff.Options{Expanded: func(diff.Fold) bool { return true }}
		d := diff.Render("x.txt", oldSrc, newSrc, o)
		oldLines := slices.Collect(strings.Lines(oldSrc))
		newLines := slices.Collect(strings.Lines(newSrc))
		if len(d.Rows) == 0 && oldSrc != newSrc {
			t.Fatalf("no rows for different texts %q and %q", oldSrc, newSrc)
		}
		nextOld, nextNew := 1, 1
		for i, r := range d.Rows {
			if r.Continued || r.Kind == diff.KindHunk {
				continue
			}
			if r.Kind != diff.KindAdded {
				if r.Old != nextOld {
					t.Fatalf("row %d = %+v, want old line %d", i, r, nextOld)
				}
				nextOld++
			}
			if r.Kind != diff.KindDeleted {
				if r.New != nextNew {
					t.Fatalf("row %d = %+v, want new line %d", i, r, nextNew)
				}
				nextNew++
			}
			if r.Kind == diff.KindEqual && oldLines[r.Old-1] != newLines[r.New-1] {
				t.Fatalf("equal row %d joins different lines %q and %q",
					i, oldLines[r.Old-1], newLines[r.New-1])
			}
		}
		if len(d.Rows) > 0 && (nextOld != len(oldLines)+1 || nextNew != len(newLines)+1) {
			t.Fatalf("the rows end before old line %d and new line %d, want %d and %d",
				nextOld, nextNew, len(oldLines)+1, len(newLines)+1)
		}
	})
}

// FuzzSnippetSameInLayouts checks that a comment gets the same diff lines
// from the split layout as from the unified layout.
func FuzzSnippetSameInLayouts(f *testing.F) {
	f.Add(oldText, newText)
	f.Add("a\nb\nc\nd\n", "x\nb\ny\nz\nd\n")
	f.Add("\tx\r\n", "\ty\n")
	f.Fuzz(func(t *testing.T, oldSrc, newSrc string) {
		u := diff.Render("x.txt", oldSrc, newSrc, diff.Options{})
		o := diff.Options{Split: true, Width: 30, Wrap: true}
		s := diff.Render("x.txt", oldSrc, newSrc, o)
		got, want := snippet(s.Rows, oldSrc, newSrc), snippet(u.Rows, oldSrc, newSrc)
		if !slices.Equal(got, want) {
			t.Fatalf("split snippet = %q, want the unified snippet %q",
				got, want)
		}
	})
}

// FuzzRenderFile checks that the output of RenderFile is well formed, that
// a lazy render matches it, and that it has one non-continued row for each
// source line.
func FuzzRenderFile(f *testing.F) {
	for i, s := range hostile {
		f.Add(uint8(i), s, uint8(i), uint8(i*7), uint8(i%3), i%2 == 0)
	}
	src := "package x\n\nfunc f() {\n\treturn\n}\n"
	f.Add(uint8(0), src, uint8(2), uint8(14), uint8(0), false)
	f.Fuzz(func(
		t *testing.T,
		p uint8,
		src string,
		layout, width, scrollX uint8,
		colored bool,
	) {
		o := fuzzOptions(layout, width, scrollX, colored)
		d := diff.RenderFile(paths[int(p)%len(paths)], src, fileOptions(o))
		checkLazyFile(t, paths[int(p)%len(paths)], src, o)
		checkLines(t, d, o)
		checkRows(t, d.Rows, src, src)
		n := 0
		for _, r := range d.Rows {
			if !r.Continued {
				n++
			}
		}
		if want := len(diff.SplitLines(src)); n != want {
			t.Fatalf("RenderFile gives %d rows, want %d", n, want)
		}
	})
}

// FuzzCountLines checks that CountLines returns zero for equal texts, the
// full line count against an empty text, and otherwise counts no larger
// than the texts' line counts.
func FuzzCountLines(f *testing.F) {
	for _, s := range hostile {
		f.Add(s, "a\n")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if added, deleted := diff.CountLines(a, a); added != 0 || deleted != 0 {
			t.Errorf("CountLines(%q, %q) = %d, %d, want 0, 0", a, a, added, deleted)
		}
		na, nb := len(diff.SplitLines(a)), len(diff.SplitLines(b))
		if added, deleted := diff.CountLines("", a); added != na || deleted != 0 {
			t.Errorf("CountLines(\"\", %q) = %d, %d, want %d, 0",
				a, added, deleted, na)
		}
		if added, deleted := diff.CountLines(a, ""); added != 0 || deleted != na {
			t.Errorf("CountLines(%q, \"\") = %d, %d, want 0, %d",
				a, added, deleted, na)
		}
		added, deleted := diff.CountLines(a, b)
		if added < 0 || deleted < 0 || added > nb || deleted > na {
			t.Errorf("CountLines(%q, %q) = %d, %d, want 0-%d, 0-%d",
				a, b, added, deleted, nb, na)
		}
	})
}

// FuzzComments checks that the review text and comment locations contain
// no control characters.
func FuzzComments(f *testing.F) {
	for i, s := range hostile {
		f.Add("a\nb\nc\n", s, "a.go", s, uint8(i), uint8(i%3))
	}
	f.Add(
		"a\nb\nc\n",
		"x\na\nb\nc\n",
		"a\x1b]52;c;aGk=\x07.go",
		"ok\x1b]7501;x\x07",
		uint8(1),
		uint8(1),
	)
	f.Fuzz(func(t *testing.T, oldText, newText, path, text string, start, size uint8) {
		lines := diff.SplitLines(oldText)
		if len(lines) == 0 {
			return
		}
		s := int(start) % len(lines)
		e := min(s+int(size), len(lines)-1)
		var cs diff.Comments
		c := commentOn(path, diff.SideNew, oldText, s+1, e+1)
		c.Text = text
		if c.Start != s+1 || c.End != e+1 {
			t.Errorf("commentOn lines %d-%d = %+v, want lines %d-%d",
				s+1, e+1, c, s+1, e+1)
		}
		cs.Add(c)
		cs.Add(comment(path, diff.SideOld, oldText, newText, text))
		r := cs.Review(text, nil)
		if strings.ContainsFunc(r, isUnsafeControl) {
			t.Errorf("Review = %q, want no control characters", r)
		}
		for _, c := range cs.All() {
			if l := c.Location(); strings.ContainsFunc(l, unicode.IsControl) {
				t.Errorf("Location = %q, want no control characters", l)
			}
		}
	})
}
