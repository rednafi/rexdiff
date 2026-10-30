package diff_test

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

const (
	oldText = "a\nb\nc\n"
	newText = "a\nB\nc\nd\n"
)

func screenLines(d *diff.Diff) []string {
	var lines []string
	for i := range len(d.Rows) {
		lines = append(lines, d.Line(i))
	}
	return lines
}

func snippet(rows []diff.Row, oldText, newText string) []string {
	var cs diff.Comments
	cs.Add(diff.NewComment("x", diff.SideNew, rows, oldText, newText))
	lines := strings.Split(cs.Review("", nil), "\n")
	return lines[1 : len(lines)-1] // drop the location and the empty comment text
}

func isEqual(r diff.Row) bool { return r.Kind == diff.KindEqual }

func kinds(rows []diff.Row) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteByte("@=+-~"[r.Kind])
	}
	return b.String()
}

type renderer struct {
	name   string
	render func(o diff.Options) *diff.Diff
}

func renderers(oldText, newText string) []renderer {
	return []renderer{
		{"unified", func(o diff.Options) *diff.Diff {
			return diff.Render("x.go", oldText, newText, o)
		}},
		{"split", func(o diff.Options) *diff.Diff {
			o.Split = true
			return diff.Render("x.go", oldText, newText, o)
		}},
		{"file", func(o diff.Options) *diff.Diff {
			return diff.RenderFile("x.go", newText, fileOptions(o))
		}},
	}
}

func TestRenderRowsFollowLines(t *testing.T) {
	t.Parallel()
	const width = 80
	themes := []diff.Theme{
		{},
		{Background: color.White, Foreground: color.Black},
		{Background: color.Black, Foreground: color.White},
	}
	tests := []struct {
		split bool
		kinds string
		rows  []diff.Row
	}{
		{
			split: false,
			kinds: "@=-+=+",
			rows: []diff.Row{
				{Kind: diff.KindHunk},
				{Kind: diff.KindEqual, Old: 1, New: 1},
				{Kind: diff.KindDeleted, Old: 2},
				{Kind: diff.KindAdded, New: 2},
				{Kind: diff.KindEqual, Old: 3, New: 3},
				{Kind: diff.KindAdded, New: 4},
			},
		},
		{
			split: true,
			kinds: "@=~=+",
			rows: []diff.Row{
				{Kind: diff.KindHunk},
				{Kind: diff.KindEqual, Old: 1, New: 1},
				{Kind: diff.KindChanged, Old: 2, New: 2},
				{Kind: diff.KindEqual, Old: 3, New: 3},
				{Kind: diff.KindAdded, New: 4},
			},
		},
	}
	for ti, theme := range themes {
		for _, tt := range tests {
			name := fmt.Sprintf("split %v, theme %d", tt.split, ti)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				o := diff.Options{Split: tt.split, Width: width, Theme: theme}
				d := diff.Render("x.go", oldText, newText, o)
				if got := kinds(d.Rows); got != tt.kinds {
					t.Errorf("kinds = %q, want %q", got, tt.kinds)
				}
				for i, r := range tt.rows {
					if i < len(d.Rows) && d.Rows[i] != r {
						t.Errorf("row %d = %+v, want %+v", i, d.Rows[i], r)
					}
				}
				if len(screenLines(d)) != len(d.Rows) {
					t.Errorf("%d lines for %d rows, want one line for each row",
						len(screenLines(d)), len(d.Rows))
				}
				for i, l := range screenLines(d) {
					if w := ansi.StringWidth(l); w > width {
						t.Errorf("line %d is %d cells wide, want %d or fewer",
							i, w, width)
					}
				}
			})
		}
	}
}

// TestRenderKinds treats a tab replaced with spaces as a change, as
// CountLines and git do.
func TestRenderKinds(t *testing.T) {
	t.Parallel()
	tests := []struct{ old, new, want string }{
		{oldText, oldText, ""},
		{"\tx\n", "    x\n", "@-+"},
	}
	for _, tt := range tests {
		d := diff.Render("x.go", tt.old, tt.new, diff.Options{Width: 80})
		if got := kinds(d.Rows); got != tt.want || len(screenLines(d)) != len(d.Rows) {
			t.Errorf("Render(%q, %q): kinds %q, %d lines, want %q, one line each",
				tt.old, tt.new, got, len(screenLines(d)), tt.want)
		}
	}
}

func TestRenderLineEnding(t *testing.T) {
	t.Parallel()
	d := diff.Render("x.txt", "a\nb\r\nc\r\n", "a\r\nb\nc\r\n", diff.Options{Width: 80})
	var got []string
	for i, r := range d.Rows {
		words := strings.Fields(plainLine(t, d, i))
		got = append(got, fmt.Sprintf("%c %s", "@=+-~"[r.Kind], words[len(words)-1]))
	}
	want := []string{"@ @@", "- a", "- b^M", "+ a^M", "+ b", "= c"}
	if !slices.Equal(got, want) {
		t.Errorf("Render rows = %q, want %q", got, want)
	}
}

func TestRenderTabsAndOffset(t *testing.T) {
	t.Parallel()
	d := diff.Render("x.go", "", "\tx := 1\n", diff.Options{Width: 80})
	if got := plainLine(t, d, 1); !strings.Contains(got, "▎     x := 1") {
		t.Errorf("tab: line = %q, want %q in it", got, "▎     x := 1")
	}
	d = diff.Render("x.go", "", "\tx := 1\n", diff.Options{Width: 80, ScrollX: 6})
	got := plainLine(t, d, 1)
	if strings.Contains(got, "x :=") || !strings.Contains(got, "= 1") {
		t.Errorf("offset 6: line = %q, want \"= 1\" and no \"x :=\"", got)
	}
}

func TestMarks(t *testing.T) {
	t.Parallel()
	const oldSrc, newSrc = "a\nb\nc\n", "a\nB\nc\n"
	theme := diff.Theme{
		Background: color.Black,
		Green:      color.RGBA{G: 0xff, A: 0xff},
		Red:        color.RGBA{R: 0xff, A: 0xff},
	}
	tests := []struct {
		name  string
		o     diff.Options
		lines []string
	}{
		{"unified bar", diff.Options{Width: 40}, []string{
			" 1  1   a", " 2    ▎ b", "    2 ▎ B", " 3  3   c",
		}},
		{"unified signs", diff.Options{Width: 40, Signs: true}, []string{
			" 1  1   a", " 2    - b", "    2 + B", " 3  3   c",
		}},
		{"split bar", diff.Options{Width: 40, Split: true}, []string{
			" 1   a               1   a",
			" 2 ▎ b               2 ▎ B",
			" 3   c               3   c",
		}},
		{"split signs", diff.Options{Width: 40, Split: true, Signs: true}, []string{
			" 1   a               1   a",
			" 2 - b               2 + B",
			" 3   c               3   c",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.o.Theme = theme
			d := diff.Render("x.txt", oldSrc, newSrc, tt.o)
			if len(screenLines(d)) != 1+len(tt.lines) {
				t.Fatalf("%d lines, want %d", len(screenLines(d)), 1+len(tt.lines))
			}
			for i, want := range tt.lines {
				got := strings.TrimRight(ansi.Strip(screenLines(d)[i+1]), " ")
				if got != want {
					t.Errorf("line %d = %q, want %q", i+1, got, want)
				}
			}
			got := markColors(screenLines(d)[1:])
			if !slices.Equal(got, []string{"31", "32"}) {
				t.Errorf("the marks have colours %q, want 31 and 32", got)
			}
		})
	}
	d := diff.Render("x.txt", "", "abcdefgh\n", diff.Options{Width: 40, ScrollX: 2})
	if got := plainLine(t, d, 1); !strings.Contains(got, "▎…cdefgh") {
		t.Errorf("offset: line = %q, want ▎…cdefgh", got)
	}
	for _, signs := range []bool{false, true} {
		d := diff.Render("x.txt", oldSrc, newSrc, diff.Options{Width: 40, Signs: signs})
		got := snippet(d.Rows[2:5], oldSrc, newSrc)
		if want := []string{"-b", "+B", " c"}; !slices.Equal(got, want) {
			t.Errorf("signs %v: snippet = %q, want %q", signs, got, want)
		}
	}
}

func markColors(lines []string) []string {
	var out []string
	for _, l := range lines {
		for {
			i := strings.IndexAny(l, "▎+-")
			if i < 0 {
				break
			}
			_, sgr, _ := strings.CutLast(l[:i], "\x1b[")
			fg, _, _ := strings.Cut(strings.TrimSuffix(sgr, "m"), ";")
			out = append(out, fg)
			l = l[i+1:]
		}
	}
	return out
}

func TestSideAt(t *testing.T) {
	t.Parallel()
	u := diff.Render("x.go", oldText, newText, diff.Options{Width: 80})
	split := diff.Options{Split: true, Width: 80}
	s := diff.Render("x.go", oldText, newText, split)
	f := diff.RenderFile("x.go", oldText, diff.FileOptions{Width: 80})
	tests := []struct {
		name     string
		d        *diff.Diff
		x        int
		side     diff.Side
		onNumber bool
	}{
		{"unified old number", u, 0, diff.SideOld, true},
		{"unified new number", u, 4, diff.SideNew, true},
		{"unified code", u, 10, diff.SideNew, false},
		{"split old number", s, 1, diff.SideOld, true},
		{"split old code", s, 10, diff.SideOld, false},
		{"split new number", s, 41, diff.SideNew, true},
		{"split new code", s, 60, diff.SideNew, false},
		{"file number", f, 1, diff.SideNew, true},
		{"file code", f, 5, diff.SideNew, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			side, on := tt.d.SideAt(tt.x)
			if side != tt.side || on != tt.onNumber {
				t.Errorf("SideAt(%d) = %v, %v, want %v, %v",
					tt.x, side, on, tt.side, tt.onNumber)
			}
		})
	}
}

func TestCountLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		old, new       string
		added, deleted int
	}{
		{oldText, newText, 2, 1},
		{"", "a\nb\n", 2, 0},
		{"a\r\nb\r\n", "a\nb\n", 2, 2},
		{oldText, oldText, 0, 0},
	}
	for _, tt := range tests {
		a, d := diff.CountLines(tt.old, tt.new)
		if a != tt.added || d != tt.deleted {
			t.Errorf("CountLines(%q, %q) = %d, %d, want %d, %d",
				tt.old, tt.new, a, d, tt.added, tt.deleted)
		}
	}
}

// TestManySpreadEdits covers a past bug. A 10,000-line file with 100
// one-line edits marked thousands of equal lines as changed. The
// 50,000-line files use the fast diff.
func TestManySpreadEdits(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ lines, edits int }{{10_000, 100}, {10_000, 1000}, {50_000, 100}, {50_000, 1000}} {
		t.Run(fmt.Sprint(size.lines, "x", size.edits), func(t *testing.T) {
			t.Parallel()
			var o, n strings.Builder
			every := size.lines / size.edits
			for i := range size.lines {
				line := fmt.Sprintf("line %d\n", i)
				o.WriteString(line)
				if i%every == every/2 {
					line = fmt.Sprintf("edited %d\n", i)
				}
				n.WriteString(line)
			}
			oldSrc, newSrc := o.String(), n.String()
			want := size.edits
			if a, d := diff.CountLines(oldSrc, newSrc); a != want || d != want {
				t.Errorf("CountLines = +%d -%d, want +%d -%d", a, d, want, want)
			}
			u := diff.Render("x.txt", oldSrc, newSrc, diff.Options{Width: 80})
			if a, d := countKind(u.Rows, diff.KindAdded), countKind(u.Rows, diff.KindDeleted); a != want || d != want {
				t.Errorf("unified: %d added and %d deleted rows, want %d and %d", a, d, want, want)
			}
			s := diff.Render("x.txt", oldSrc, newSrc, diff.Options{Width: 80, Split: true})
			if c := countKind(s.Rows, diff.KindChanged); c != want {
				t.Errorf("split: %d changed rows, want %d", c, want)
			}
		})
	}
}

func countKind(rows []diff.Row, k diff.Kind) int {
	n := 0
	for _, r := range rows {
		if r.Kind == k {
			n++
		}
	}
	return n
}

func TestFewValues(t *testing.T) {
	t.Parallel()
	oldSrc, newSrc := fewValues(25_000)
	a, d := diff.CountLines(oldSrc, newSrc)
	u := diff.Render("x.txt", oldSrc, newSrc, diff.Options{Width: 80})
	if ra, rd := countKind(u.Rows, diff.KindAdded), countKind(u.Rows, diff.KindDeleted); ra != a || rd != d {
		t.Errorf("the diff has %d added and %d deleted rows, but CountLines = +%d -%d", ra, rd, a, d)
	}
}

func TestRenderFile(t *testing.T) {
	t.Parallel()
	src := "package x\n\nfunc f() {\n\treturn\n}\n"
	d := diff.RenderFile("x.go", src, diff.FileOptions{Width: 14})
	if len(screenLines(d)) != 5 || len(d.Rows) != 5 {
		t.Fatalf("RenderFile: %d lines and %d rows, want 5",
			len(screenLines(d)), len(d.Rows))
	}
	if d.Rows[3] != (diff.Row{Kind: diff.KindEqual, Old: 4, New: 4}) {
		t.Errorf("row 3 = %+v, want %+v",
			d.Rows[3], diff.Row{Kind: diff.KindEqual, Old: 4, New: 4})
	}
	if got := ansi.Strip(screenLines(d)[2]); got != " 3  func f() {" {
		t.Errorf("line 2 = %q, want %q", got, " 3  func f() {")
	}
	if got := ansi.Strip(screenLines(d)[0]); got != " 1  package x" {
		t.Errorf("line 0 = %q, want %q", got, " 1  package x")
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()
	long := "x := []string{" + strings.Repeat(`"word", `, 12) + "}"
	oldSrc, newSrc := "a\nb\n", "a\n"+long+"\nb\n"
	const width = 40
	for _, tt := range renderers(oldSrc, newSrc) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cut := tt.render(diff.Options{Width: width})
			d := tt.render(diff.Options{Width: width, Wrap: true})
			if len(screenLines(d)) != len(d.Rows) || len(screenLines(d)) <= len(screenLines(cut)) {
				t.Fatalf("%d lines and %d rows, "+
					"want more than the %d lines without wrap",
					len(screenLines(d)), len(d.Rows), len(screenLines(cut)))
			}
			var text strings.Builder
			for i, l := range screenLines(d) {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("line %d is %d cells wide, want %d or fewer",
						i, w, width)
				}
				r := d.Rows[i]
				if r.New == 2 {
					text.WriteString(ansi.Strip(l))
				}
				prev := d.Rows[max(i-1, 0)]
				sameLines := prev.Old == r.Old && prev.New == r.New
				if r.Continued && !sameLines {
					t.Errorf("continued row %d = %+v, want the lines of %+v",
						i, r, prev)
				}
			}
			noSpace := func(s string) string {
				return strings.Join(strings.Fields(s), "")
			}
			if !strings.Contains(noSpace(text.String()), noSpace(long)) {
				t.Errorf("wrapped lines = \n%s\nwant all of:\n%s",
					text.String(), long)
			}
			got, want := snippet(d.Rows, oldSrc, newSrc), snippet(cut.Rows, oldSrc, newSrc)
			if !slices.Equal(got, want) {
				t.Errorf("snippet with wrap = %q, want %q", got, want)
			}
		})
	}
}

func TestCache(t *testing.T) {
	t.Parallel()
	const src = "def f(): # a comment\n\treturn 1\n"
	tests := []struct {
		path, old, new string
		file           bool
		offset         int
	}{
		{path: "a.go", old: src, new: src + "x\n"},
		{path: "a.go", old: src, new: src + "x\n", offset: 4},
		{path: "a.py", old: src, new: src + "x\n"},
		{path: "a.py", new: src, file: true},
		{path: "a.py", new: src + "y\n", file: true},
		{path: "a.go", new: src + "y\n", file: true},
	}
	var c diff.Cache
	for _, tt := range tests {
		render := func(c *diff.Cache) []string {
			o := diff.Options{Width: 40, ScrollX: tt.offset, Cache: c}
			if tt.file {
				// A busy machine can highlight later than a line needs. A nil
				// Cache gives no way to wait, so use an empty one.
				if c == nil {
					c = &diff.Cache{}
					o.Cache = c
				}
				d := diff.RenderFile(tt.path, tt.new, fileOptions(o))
				waitHighlighted(t, c)
				return screenLines(d)
			}
			return screenLines(diff.Render(tt.path, tt.old, tt.new, o))
		}
		if got, want := render(&c), render(nil); !slices.Equal(got, want) {
			t.Errorf("%+v: lines with the cache = \n%q\nwant:\n%q",
				tt, got, want)
		}
	}
}

func TestNoNewline(t *testing.T) {
	t.Parallel()
	const mark = `\ No newline at end of file`
	d := diff.Render("x.txt", "a\nb", "a\nb\n", diff.Options{Width: 80})
	var marked []string
	for i := range screenLines(d) {
		if l := plainLine(t, d, i); strings.Contains(l, mark) {
			marked = append(marked, l)
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "b") {
		t.Errorf("Render(%q, %q) marks %q, want only the deleted b", "a\nb", "a\nb\n", marked)
	}
	d = diff.Render("x.txt", "a\nb", "c\nb", diff.Options{Width: 80})
	for i := range screenLines(d) {
		if l := plainLine(t, d, i); strings.Contains(l, mark) {
			t.Errorf("Render(%q, %q) line %d = %q, want no mark on the equal b", "a\nb", "c\nb", i, l)
		}
	}
}
