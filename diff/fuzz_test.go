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

// paths select the lexers that the fuzz tests use.
var paths = []string{"x.go", "x.py", "x.txt", "x.md", "x.html", "x.json", "Makefile", "x"}

// hostile is file content that can attack a terminal or break the layout.
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
	strings.Repeat("long ", 200) + "\n",
	"no newline at end",
}

// sgr matches the colour sequences that the renderer writes.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// isUnsafeControl reports whether r is a control character other than a
// tab or a newline.
func isUnsafeControl(r rune) bool {
	return r != '\t' && r != '\n' && unicode.IsControl(r)
}

// fuzzOptions makes Options from the fuzz values.
func fuzzOptions(layout, width, offset uint8, colored bool) diff.Options {
	o := diff.Options{
		Split:  layout&1 != 0,
		Wrap:   layout&2 != 0,
		Width:  int(width % 60),
		Offset: int(offset % 20),
	}
	if colored {
		o.Theme = diff.Theme{
			Background: color.Black,
			Foreground: color.White,
		}
	}
	return o
}

// checkLines checks the invariants that all rendered output must keep.
func checkLines(t *testing.T, d *diff.Diff, o diff.Options) {
	t.Helper()
	if len(d.Lines) != len(d.Rows) {
		t.Fatalf("%d lines for %d rows, want one line for each row",
			len(d.Lines), len(d.Rows))
	}
	for i, l := range d.Lines {
		if w := ansi.StringWidth(l); o.Width > 0 && w > o.Width {
			t.Errorf("line %d is %d cells wide, want %d or fewer: %q",
				i, w, o.Width, l)
		}
		rest := sgr.ReplaceAllString(l, "")
		if strings.ContainsFunc(rest, unicode.IsControl) {
			t.Errorf("line %d = %q, want no control characters", i, l)
		}
	}
}

// checkRows checks that each row points at real lines, in order.
func checkRows(t *testing.T, rows []diff.Row, before, after string) {
	t.Helper()
	nOld, nNew := len(diff.Lines(before)), len(diff.Lines(after))
	lastOld, lastNew := 0, 0
	for i, r := range rows {
		if r.Old < 0 || r.Old > nOld || r.New < 0 || r.New > nNew {
			t.Fatalf("row %d = %+v, want Old in 0-%d and New in 0-%d",
				i, r, nOld, nNew)
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

// FuzzRender checks the invariants of Render. It also checks that Count
// agrees with the snippet.
func FuzzRender(f *testing.F) {
	for i, s := range hostile {
		f.Add(uint8(i), "a\nb\n", s, uint8(i), uint8(i*7), uint8(i%3), i%2 == 0)
		f.Add(uint8(i), s, "a\n"+s+"b\n", uint8(i+1), uint8(i*5), uint8(0), i%2 == 1)
	}
	f.Add(uint8(0), oldSrc, newSrc, uint8(3), uint8(1), uint8(0), false)
	f.Add(uint8(0), "\tx\n", "    x\n", uint8(0), uint8(0), uint8(0), false)
	f.Fuzz(func(
		t *testing.T,
		p uint8,
		before, after string,
		layout, width, offset uint8,
		colored bool,
	) {
		o := fuzzOptions(layout, width, offset, colored)
		d := diff.Render(paths[int(p)%len(paths)], before, after, o)
		checkLines(t, d, o)
		checkRows(t, d.Rows, before, after)
		if o.Width == 0 && o.Offset == 0 && !strings.Contains(before+after, "…") {
			// With no width limit, no line is cut.
			for i, l := range d.Lines {
				cut := strings.Contains(ansi.Strip(l), "…")
				if cut && d.Rows[i].Kind != diff.KindHunk {
					t.Fatalf("line %d = %q, want no cut with no width limit", i, l)
				}
			}
		}

		added, deleted := diff.Count(before, after)
		if (added == 0 && deleted == 0) != (len(d.Lines) == 0) {
			t.Fatalf("Count = %d, %d and Render has %d lines, "+
				"want zero counts only for zero lines",
				added, deleted, len(d.Lines))
		}
		var plus, minus int
		for _, l := range snippet(d.Rows, before, after) {
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

// FuzzSnippetSameInLayouts checks that the split and the unified layouts give
// the same snippet.
func FuzzSnippetSameInLayouts(f *testing.F) {
	f.Add(oldSrc, newSrc)
	f.Add("a\nb\nc\nd\n", "x\nb\ny\nz\nd\n")
	f.Add("\tx\r\n", "\ty\n")
	f.Fuzz(func(t *testing.T, before, after string) {
		u := diff.Render("x.txt", before, after, diff.Options{})
		o := diff.Options{Split: true, Width: 30, Wrap: true}
		s := diff.Render("x.txt", before, after, o)
		got, want := snippet(s.Rows, before, after), snippet(u.Rows, before, after)
		if !slices.Equal(got, want) {
			t.Fatalf("split snippet = %q, want the unified snippet %q",
				got, want)
		}
	})
}

// FuzzRenderFile checks that RenderFile gives one row for each source line.
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
		layout, width, offset uint8,
		colored bool,
	) {
		o := fuzzOptions(layout, width, offset, colored)
		d := diff.RenderFile(paths[int(p)%len(paths)], src, o)
		checkLines(t, d, o)
		checkRows(t, d.Rows, src, src)
		n := 0
		for _, r := range d.Rows {
			if !r.Continued {
				n++
			}
		}
		if want := len(diff.Lines(src)); n != want {
			t.Fatalf("RenderFile gives %d rows, want %d", n, want)
		}
	})
}

// FuzzCount checks that the counts of Count are in the range of the line
// counts.
func FuzzCount(f *testing.F) {
	for _, s := range hostile {
		f.Add(s, "a\n")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if added, deleted := diff.Count(a, a); added != 0 || deleted != 0 {
			t.Errorf("Count(%q, %q) = %d, %d, want 0, 0", a, a, added, deleted)
		}
		na, nb := len(diff.Lines(a)), len(diff.Lines(b))
		if added, deleted := diff.Count("", a); added != na || deleted != 0 {
			t.Errorf("Count(\"\", %q) = %d, %d, want %d, 0",
				a, added, deleted, na)
		}
		if added, deleted := diff.Count(a, ""); added != 0 || deleted != na {
			t.Errorf("Count(%q, \"\") = %d, %d, want 0, %d",
				a, added, deleted, na)
		}
		added, deleted := diff.Count(a, b)
		if added < 0 || deleted < 0 || added > nb || deleted > na {
			t.Errorf("Count(%q, %q) = %d, %d, want 0-%d, 0-%d",
				a, b, added, deleted, nb, na)
		}
	})
}

// FuzzComments checks that the review text has no control characters.
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
	f.Fuzz(func(t *testing.T, before, after, path, text string, start, size uint8) {
		lines := diff.Lines(before)
		if len(lines) == 0 {
			return
		}
		s := int(start) % len(lines)
		e := min(s+int(size), len(lines)-1)
		var cs diff.Comments
		c := commentOn(path, diff.SideNew, before, s+1, e+1)
		c.Text = text
		if c.Start != s+1 || c.End != e+1 {
			t.Errorf("commentOn lines %d-%d = %+v, want lines %d-%d",
				s+1, e+1, c, s+1, e+1)
		}
		cs.Add(c)
		cs.Add(comment(path, diff.SideOld, before, after, text))
		r := cs.Review(text)
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
