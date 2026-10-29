package diff_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/diff"
)

// benchFile returns a Go file of n lines, and a copy in which about one in
// every edit lines is changed. With edit 1, every line changes.
func benchFile(n, edit int) (oldText, newText string) {
	var o, c strings.Builder
	r := rand.New(rand.NewPCG(1, uint64(n))) //nolint:gosec // a fixed seed produces the same files on every run
	for i := range n {
		line := fmt.Sprintf("\tx += compute(%d, \"s%d\") // L%d\n", i, i*7, i)
		if i%20 == 0 {
			line = fmt.Sprintf("func f%d(x int) int { // L%d\n", i, i)
		}
		o.WriteString(line)
		if edit == 1 || r.IntN(edit) == 0 {
			line = strings.Replace(line, "// L", "// M", 1)
		}
		c.WriteString(line)
	}
	return o.String(), c.String()
}

// benchSizes lists the benchmark files, with their line counts and edit
// rates for benchFile.
var benchSizes = []struct {
	name    string
	n, edit int
}{
	{"7k-changed", 7_000, 3},
	{"20k-rewrite", 20_000, 1},
	{"50k-scattered", 50_000, 250},
	{"200k-scattered", 200_000, 1000},
}

// screen reads one screen of lines from d, starting at line top, as a
// view does.
func screen(d *diff.Diff, top int) {
	for i := top; i < min(top+50, len(d.Rows)); i++ {
		d.Line(i)
	}
}

// BenchmarkRender measures rendering a diff and reading its first screen.
// The cold cases use a new Cache each time. The scroll cases render again
// after a horizontal scroll, which reuses the cached diff and rows.
func BenchmarkRender(b *testing.B) {
	for _, s := range benchSizes {
		oldText, newText := benchFile(s.n, s.edit)
		for _, layout := range []string{"unified", "split", "wrap"} {
			o := diff.Options{Width: 130, Split: layout == "split", Wrap: layout == "wrap"}
			b.Run(s.name+"/"+layout+"/cold", func(b *testing.B) {
				for b.Loop() {
					o.Cache = &diff.Cache{}
					screen(diff.Render("x.go", oldText, newText, o), 0)
				}
			})
			b.Run(s.name+"/"+layout+"/scroll", func(b *testing.B) {
				o.Cache = &diff.Cache{}
				diff.Render("x.go", oldText, newText, o)
				for b.Loop() {
					if !o.Wrap {
						o.ScrollX = 8 - o.ScrollX
					}
					screen(diff.Render("x.go", oldText, newText, o), 0)
				}
			})
		}
		b.Run(s.name+"/eager", func(b *testing.B) {
			if s.n > 20_000 {
				b.Skip("an eager render of a large file is slow")
			}
			for b.Loop() {
				diff.Render("x.go", oldText, newText, diff.Options{Width: 130, Cache: &diff.Cache{}})
			}
		})
	}
}

// BenchmarkLines measures reading the next screen of lines from a lazy
// diff, as when paging down.
func BenchmarkLines(b *testing.B) {
	oldText, newText := benchFile(50_000, 3)
	o := diff.Options{Width: 130, Cache: &diff.Cache{}}
	d := diff.Render("x.go", oldText, newText, o)
	top := 0
	for b.Loop() {
		if top+50 >= len(d.Rows) {
			b.StopTimer()
			d, top = diff.Render("x.go", oldText, newText, o), 0
			b.StartTimer()
		}
		screen(d, top)
		top += 50
	}
}

// BenchmarkRenderFile measures what the Files tab does: rendering a file
// and reading its first screen. Each render uses a different file from the
// previous one, so the Cache stops the previous background highlighting.
func BenchmarkRenderFile(b *testing.B) {
	for _, s := range benchSizes {
		srcs := [2]string{}
		srcs[0], srcs[1] = benchFile(s.n, s.edit)
		for _, wrap := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/wrap=%v", s.name, wrap), func(b *testing.B) {
				o := diff.Options{Width: 130, Wrap: wrap, Cache: &diff.Cache{}}
				i := 0
				for b.Loop() {
					screen(diff.RenderFile("x.go", srcs[i%2], fileOptions(o)), 0)
					i++
				}
				diff.RenderFile("x.go", "", fileOptions(o)) // stops the last background highlighting
			})
		}
	}
}

// BenchmarkCountLines measures counting added and deleted lines, as the
// file list does.
func BenchmarkCountLines(b *testing.B) {
	for _, s := range benchSizes {
		oldText, newText := benchFile(s.n, s.edit)
		b.Run(s.name, func(b *testing.B) {
			for b.Loop() {
				diff.CountLines(oldText, newText)
			}
		})
	}
	oldText, newText := fewValues(25_000)
	b.Run("50k-few-values", func(b *testing.B) {
		for b.Loop() {
			diff.CountLines(oldText, newText)
		}
	})
}

// fewValues returns two texts of n lines each, made of random lines from a
// few distinct values. Such texts are the slowest input for a diff.
func fewValues(n int) (oldText, newText string) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed seed produces the same texts on every run
	values := []string{"a\n", "b\n", "}\n", "\n", "return\n"}
	var o, c strings.Builder
	for range n {
		o.WriteString(values[r.IntN(len(values))])
		c.WriteString(values[r.IntN(len(values))])
	}
	return o.String(), c.String()
}
