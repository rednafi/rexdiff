package tree_test

import (
	"fmt"
	"testing"

	"github.com/rednafi/rexdiff/internal/tree"
)

// BenchmarkBuild measures building the rows for a list of 3,000 files.
func BenchmarkBuild(b *testing.B) {
	es := make([]tree.Entry, 3000)
	for i := range es {
		es[i] = tree.Entry{Path: fmt.Sprintf("src/mod%02d/part%d/f%04d.go", i%60, i%5, i), Changed: i%300 == 0}
	}
	for b.Loop() {
		tree.Build(es, nil, true)
	}
}
