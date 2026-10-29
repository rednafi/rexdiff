package diffview_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/diffview"
)

// newHighlighter returns a Highlighter for src at path that escapes the
// text of tokens, as RenderFile does.
func newHighlighter(path, src string) *diffview.Highlighter {
	return diffview.NewHighlighter(path, src, lipgloss.NoColor{}, escapeLines, escapeLines)
}

// escapeLines escapes each line of s.
func escapeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = diffview.Escape(l)
	}
	return strings.Join(lines, "\n")
}

// wait waits until h has highlighted every line, as long as nothing
// calls Stop.
func wait(t *testing.T, h *diffview.Highlighter) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); h.Busy(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the highlighting runs for a minute")
		}
	}
}

// coloured reports whether s contains an escape sequence.
func coloured(s string) bool { return strings.Contains(s, "\x1b[") }

// TestHighlighterLongLine checks that a Highlighter draws a line too long
// to lex plain and escaped, and still highlights the other lines.
func TestHighlighterLongLine(t *testing.T) {
	t.Parallel()
	long := "var s = \"\x07" + strings.Repeat("x", 5000) + "\""
	h := newHighlighter("x.go", "package a\n"+long+"\nfunc f() {}\n")
	wait(t, h)
	if l, _, _ := h.Line(0); !coloured(l) {
		t.Errorf("line 0 = %q, want colours", l)
	}
	if l, _, _ := h.Line(1); l != diffview.Escape(long) {
		t.Errorf("line 1 = %.40q…, want the line plain and escaped", l)
	}
	if l, _, _ := h.Line(2); !coloured(l) {
		t.Errorf("line 2 = %q, want colours", l)
	}
}

// TestHighlighterLargeFile checks that a Highlighter still highlights the
// lines of a file too large to lex as a whole.
func TestHighlighterLargeFile(t *testing.T) {
	t.Parallel()
	if raceOn {
		t.Skip("the race detector makes each line miss its highlighting time limit")
	}
	comment := "// " + strings.Repeat("x", 1400)
	src := "package a\n" + strings.Repeat(comment+"\n", 400) + "func f() {}\n" // about 560 KB
	h := newHighlighter("x.go", src)
	wait(t, h)
	for i, want := range map[int]string{0: "package a", 1: comment, 401: "func f() {}"} {
		if l, _, _ := h.Line(i); !coloured(l) || ansi.Strip(l) != want {
			t.Errorf("line %d = %.40q…, want it highlighted", i, l)
		}
	}
}

// TestHighlighterSlow checks that Line does not wait for a lexer that is
// slow, and returns the line plain instead: one screen of such lines takes
// far less time than the lexing.
func TestHighlighterSlow(t *testing.T) {
	t.Parallel()
	// The Java lexer takes about a second on these lines.
	words := "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor\n"
	src := strings.Repeat(words, 300) + "class A {}\n"
	h := newHighlighter("x.java", src)
	defer h.Stop()
	start := time.Now()
	l, ok, final := h.Line(300)
	if !ok || final || l != "class A {}" {
		t.Errorf("Line(300) = %q, %v, %v, want the line plain and not final", l, ok, final)
	}
	for i := 299; i > 259; i-- {
		if _, _, final := h.Line(i); final {
			t.Errorf("Line(%d) is final before lexing passes line 300", i)
		}
	}
	// The race detector slows the lexer down too much for the time check.
	if d := time.Since(start); d > 250*time.Millisecond && !raceOn {
		t.Errorf("41 lines took %v, want Line to give up waiting sooner", d)
	}
}

// TestHighlighterStop checks that Busy reports true while lexing goes on,
// and that Stop ends the lexing of a source that is slow to lex within a
// second, rather than after its time budget.
func TestHighlighterStop(t *testing.T) {
	t.Parallel()
	h := newHighlighter("x.java", slowJava())
	if !h.Busy() {
		t.Error("Busy = false at the start, want true while lexing")
	}
	h.Stop()
	start := time.Now()
	wait(t, h)
	// The race detector slows the lexer down too much for the time check.
	if d := time.Since(start); d > time.Second && !raceOn {
		t.Errorf("lexing runs %v after Stop, want at most a second", d)
	}
}

// TestHighlighterTypicalFile checks that a typical Go file of 100 KB is
// coloured to the end: no line of it misses a time limit.
func TestHighlighterTypicalFile(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("package a\n\nimport \"fmt\"\n\n")
	for i := 0; b.Len() < 100<<10; i++ {
		fmt.Fprintf(&b, "// F%d prints its argument.\nfunc F%d(x int) string {\n\treturn fmt.Sprintf(\"%%d: %%s\", x, \"text\")\n}\n\n", i, i)
	}
	src := b.String()
	h := newHighlighter("a.go", src)
	defer h.Stop()
	wait(t, h)
	lines := []int{0, 2}
	if !raceOn { // the race detector makes lexing slower than its time limit
		lines = append(lines, strings.Count(src[:strings.LastIndex(src, "func ")], "\n"))
	}
	for _, i := range lines {
		if l, _, final := h.Line(i); !final || !coloured(l) {
			t.Errorf("line %d = %q, final %v, want it highlighted", i, l, final)
		}
	}
}

// TestWrapLine checks that WrapLine breaks a coloured line where WrapParts
// breaks its plain text, even when colours split a grapheme cluster.
func TestWrapLine(t *testing.T) {
	t.Parallel()
	family := "👨\u200d👩\u200d👧" // one cluster of five runes
	plain := strings.Repeat("ab "+family+" cd ", 20)
	red, reset := "\x1b[31m", "\x1b[m"
	// Colour each rune of the cluster on its own, as a lexer can.
	code := strings.ReplaceAll(plain, family, red+"👨"+reset+"\u200d"+red+"👩"+reset+"\u200d👧")
	code = strings.ReplaceAll(code, "ab", red+"ab"+reset)
	for _, width := range []int{1, 3, 7, 10, 25} {
		want := diffview.WrapParts(plain, width)
		got := diffview.WrapLine(plain, code, width)
		if len(got) != len(want) {
			t.Fatalf("width %d: %d parts, want %d", width, len(got), len(want))
		}
		for k := range got {
			if ansi.Strip(got[k]) != want[k] {
				t.Fatalf("width %d: part %d = %q, want the text %q", width, k, got[k], want[k])
			}
		}
		if !coloured(strings.Join(got, "")) {
			t.Errorf("width %d: the parts have no colours", width)
		}
	}
	// A code line that does not match plain gives the plain parts.
	if got := diffview.WrapLine(plain, "other", 10); strings.Join(got, "") != strings.Join(diffview.WrapParts(plain, 10), "") {
		t.Errorf("WrapLine with other text = %q, want the plain parts", got)
	}
}

// slowJava returns about 100 KB of lines of words, on which the Java
// lexer takes about 30 s when it lexes them as a whole.
func slowJava() string {
	return strings.Repeat(strings.Repeat("lorem ipsum dolor sit amet ", 7)+"\n", 500) + "class A {}\n"
}

// TestHighlighterBudget checks that a Highlighter stops lexing a source
// that is slow to lex after its time budget, and draws the rest plain.
func TestHighlighterBudget(t *testing.T) {
	t.Parallel()
	src := slowJava()
	h := newHighlighter("x.java", src)
	start := time.Now()
	wait(t, h)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("lexing took %v, want it to stop after its budget", d)
	}
	if l, ok, final := h.Line(500); !ok || !final || l != "class A {}" {
		t.Errorf("Line(500) = %q, %v, %v, want the line plain and final", l, ok, final)
	}
}
