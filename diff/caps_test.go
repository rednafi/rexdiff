package diff_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// longLine returns n bytes that the Java lexer is slow on.
func longLine(n int) string {
	return strings.Repeat("public static lorem ipsum ", n/26+1)[:n]
}

const keywordColour = "\x1b[35m"

// TestLongLinePlain uses a line that takes about 30 s to highlight.
func TestLongLinePlain(t *testing.T) {
	t.Parallel()
	long := longLine(100_000)
	src := "class A {}\n" + long + "\nclass B {}\n"
	for _, wrap := range []bool{false, true} {
		o := diff.Options{Width: 80, Wrap: wrap}
		renders := map[string]*diff.Diff{
			"Render":     diff.Render("A.java", "", src, o),
			"RenderFile": diff.RenderFile("A.java", src, fileOptions(o)),
		}
		for name, d := range renders {
			var shown []string
			for i := range len(d.Rows) {
				if d.Rows[i].New == 2 {
					shown = append(shown, d.Line(i))
				}
			}
			if len(shown) == 0 {
				t.Fatalf("%s, wrap %v: no rows for the long line", name, wrap)
			}
			if wrap && !strings.Contains(ansi.Strip(strings.Join(shown, "")), long[len(long)-40:]) {
				t.Errorf("%s, wrap %v: the rows do not show the end of the long line", name, wrap)
			}
			for _, l := range shown {
				if strings.Contains(l, keywordColour) {
					t.Fatalf("%s, wrap %v: long line row %.60q… has colours", name, wrap, l)
				}
			}
			if last := d.Line(len(d.Rows) - 1); !strings.Contains(last, keywordColour) {
				t.Errorf("%s, wrap %v: last row %q, want it highlighted", name, wrap, last)
			}
		}
	}
}
