package diff_test

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/diff"
)

// checkLazy reads lines from last to first, so no line relies on earlier ones.
func checkLazy(t *testing.T, eager, lazy *diff.Diff) {
	t.Helper()
	want := screenLines(eager)
	if !slices.Equal(eager.Rows, lazy.Rows) {
		t.Fatalf("lazy rows differ:\n eager %v\n lazy  %v", eager.Rows, lazy.Rows)
	}
	if len(lazy.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(lazy.Rows), len(want))
	}
	for i := range slices.Backward(lazy.Rows) {
		if got := lazy.Line(i); got != want[i] {
			t.Fatalf("lazy line %d = %q, want %q", i, got, want[i])
		}
	}
}

// checkLazyFile waits for the highlighter, so that no line shows plain.
func checkLazyFile(t *testing.T, path, src string, o diff.Options) {
	t.Helper()
	o.Cache = &diff.Cache{}
	eager := diff.RenderFile(path, src, fileOptions(o))
	waitHighlighted(t, o.Cache)
	checkLazy(t, eager, diff.RenderFile(path, src, fileOptions(o)))
}

// lazyFile keeps lines under 256 bytes, below the long-line time limit.
func lazyFile(n int, edit bool) string {
	var b strings.Builder
	b.WriteString("package x\n\n")
	for i := range n {
		switch {
		case i%17 == 0:
			fmt.Fprintf(&b, "\tlong%d := %q\n", i, strings.Repeat("word ", 5+i%40))
		case i%13 == 0:
			fmt.Fprintf(&b, "\t\tw%d := \"日本語のテキスト😀 %s\"\n", i, strings.Repeat("中文", i%30))
		case i%11 == 0 && edit:
			fmt.Fprintf(&b, "\tx%d := %d // edited\r\n", i, i)
		default:
			fmt.Fprintf(&b, "\tx%d := %d\n", i, i)
		}
	}
	return b.String()
}

func TestLazy(t *testing.T) {
	t.Parallel()
	oldSrc, newSrc := lazyFile(300, false), lazyFile(310, true)
	for _, split := range []bool{false, true} {
		for _, wrap := range []bool{false, true} {
			for _, width := range []int{0, 1, 10, 30, 80, 141} {
				for _, scrollX := range []int{0, 9} {
					name := fmt.Sprintf("split=%v,wrap=%v,width=%d,scrollX=%d", split, wrap, width, scrollX)
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						expanded := func(f diff.Fold) bool { return f.Old%2 == 0 }
						o := diff.Options{
							Split: split, Wrap: wrap, Width: width, ScrollX: scrollX,
							Signs: scrollX > 0, Expanded: expanded,
							Theme: diff.Theme{Background: color.Black, Foreground: color.White},
						}
						lo := o
						lo.Cache = &diff.Cache{}
						checkLazy(t, diff.Render("x.go", oldSrc, newSrc, o), diff.Render("x.go", oldSrc, newSrc, lo))
						checkLazyFile(t, "x.go", newSrc, o)
						for _, s := range hostile {
							checkLazy(t, diff.Render("x.txt", "a\n"+s, s+"b\n", o), diff.Render("x.txt", "a\n"+s, s+"b\n", lo))
							checkLazyFile(t, "x.txt", s, o)
						}
					})
				}
			}
		}
	}
	// Colour codes between the runes of an emoji can change its width.
	wide := []string{"é 日本\n", "x := 👨‍👩 + 🇬🇧 é\n", "x := 👨‍👩 + 🇬🇧 " + strings.Repeat("日本 ", 20) + "\n"}
	for width := range 40 {
		o := diff.Options{Wrap: true, Width: width, Theme: diff.Theme{Background: color.Black, Foreground: color.White}}
		lo := o
		lo.Cache = &diff.Cache{}
		for _, s := range wide {
			checkLazy(t, diff.Render("x.go", "a\n"+s, s+"b\n", o), diff.Render("x.go", "a\n"+s, s+"b\n", lo))
			checkLazyFile(t, "x.go", s, o)
		}
	}
}

func TestCacheKeepsDiff(t *testing.T) {
	t.Parallel()
	oldSrc, newSrc := lazyFile(120, false), lazyFile(110, true)
	c := &diff.Cache{}
	for range 2 {
		for _, split := range []bool{true, false} {
			for _, wrap := range []bool{false, true} {
				for _, all := range []bool{false, true} {
					for _, width := range []int{90, 60} {
						for _, scrollX := range []int{0, 7} {
							expanded := func(diff.Fold) bool { return all }
							o := diff.Options{Split: split, Wrap: wrap, Width: width, ScrollX: scrollX, Expanded: expanded}
							want := diff.Render("x.go", oldSrc, newSrc, o)
							o.Cache = c
							checkLazy(t, want, diff.Render("x.go", oldSrc, newSrc, o))
						}
					}
				}
			}
		}
	}
}

func waitHighlighted(t *testing.T, c *diff.Cache) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); c.Highlighting(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the highlighting runs for a minute")
		}
	}
}

// TestFileKeepsLines renders another file with the same Cache. That stops
// the first highlighter, but the lazy Diff must still highlight.
func TestFileKeepsLines(t *testing.T) {
	t.Parallel()
	src := lazyFile(3000, false)
	o := diff.Options{Width: 80, Cache: &diff.Cache{}}
	big := diff.RenderFile("x.go", src, fileOptions(o))
	diff.RenderFile("y.go", "package y\n", fileOptions(o))
	wc := &diff.Cache{}
	want := diff.RenderFile("x.go", src, diff.FileOptions{Width: 80, Cache: wc})
	waitHighlighted(t, wc)
	// The line shows plain until the lazy Diff highlights it again.
	last := len(big.Rows) - 1
	deadline := time.Now().Add(time.Minute)
	for got := big.Line(last); got != want.Line(last); got = big.Line(last) {
		if ansi.Strip(got) != ansi.Strip(want.Line(last)) || time.Now().After(deadline) {
			t.Fatalf("last line = %q, want %q", got, want.Line(last))
		}
		time.Sleep(time.Millisecond)
	}
}
