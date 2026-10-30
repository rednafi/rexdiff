package vcs_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

// In BenchmarkDiff, only the warm case uses the cache.
func BenchmarkDiff(b *testing.B) {
	dir := b.TempDir()
	testgit.InitRepo(b, dir)
	for i := range 1000 {
		testgit.WriteFile(b, dir, fmt.Sprintf("d%d/f%04d.go", i%20, i), strings.Repeat(fmt.Sprintf("line of file %d\n", i), 500))
	}
	testgit.Git(b, dir, "add", ".")
	testgit.Git(b, dir, "commit", "-qm", "base")
	for i := range 1000 {
		testgit.WriteFile(b, dir, fmt.Sprintf("d%d/f%04d.go", i%20, i), strings.Repeat(fmt.Sprintf("new line of file %d\n", i), 500))
	}
	env := testgit.ToolEnv(b)
	openRepo := func(b *testing.B) (r *vcs.Repo, oldTree, newTree string) {
		b.Helper()
		r, err := vcs.Open(b.Context(), env, dir)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = r.Close() })
		if oldTree, err = r.HeadTree(b.Context()); err != nil {
			b.Fatal(err)
		}
		if newTree, err = r.SnapshotTree(b.Context()); err != nil {
			b.Fatal(err)
		}
		return r, oldTree, newTree
	}
	b.Run("cold", func(b *testing.B) {
		for b.Loop() {
			b.StopTimer()
			r, oldTree, newTree := openRepo(b)
			b.StartTimer()
			if _, err := r.Diff(b.Context(), oldTree, newTree); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm", func(b *testing.B) {
		r, oldTree, newTree := openRepo(b)
		if _, err := r.Diff(b.Context(), oldTree, newTree); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			if _, err := r.Diff(b.Context(), oldTree, newTree); err != nil {
				b.Fatal(err)
			}
		}
	})
}
