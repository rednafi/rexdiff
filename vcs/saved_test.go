package vcs_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

func TestSavedBase(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	if got := r.SavedBase(ctx); got != "" {
		t.Errorf("SavedBase before a save = %q, want \"\"", got)
	}
	if err := r.SaveBase(ctx, "feat"); err != nil {
		t.Fatal(err)
	}
	if got := r.SavedBase(ctx); got != "feat" {
		t.Errorf("SavedBase = %q, want feat", got)
	}
	if err := r.SaveBase(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if got := r.SavedBase(ctx); got != "" {
		t.Errorf("SavedBase after a save of the default = %q, want \"\"", got)
	}
	// A value that is not a valid ref name can be neither saved nor read.
	for _, bad := range []string{"-x", "a b", "a\x7f"} {
		if err := r.SaveBase(ctx, bad); err == nil {
			t.Errorf("SaveBase(%q): no error, want an error", bad)
		}
	}
	blob := hashObject(t, dir, "--upload-pack=x")
	testgit.Git(t, dir, "update-ref", "refs/worktree/rexdiff/base-pick", blob)
	if got := r.SavedBase(ctx); got != "" {
		t.Errorf("SavedBase of an option = %q, want \"\"", got)
	}
}

func hashObject(t *testing.T, dir, s string) string {
	t.Helper()
	tmp := t.TempDir()
	testgit.WriteFile(t, tmp, "blob", s)
	out := testgit.Git(t, dir, "hash-object", "-w", filepath.Join(tmp, "blob"))
	return strings.TrimSpace(out)
}

func TestSavedTurn(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	tree := snapshot(t, r) // no commit refers to it
	if err := r.SaveTurn(ctx, tree); err != nil {
		t.Fatal(err)
	}
	testgit.Git(t, dir, "gc", "-q", "--prune=now")
	if got := r.SavedTurn(ctx); got != tree {
		t.Errorf("SavedTurn = %q, want %q", got, tree)
	}
	if _, err := r.Diff(ctx, tree, head(t, r)); err != nil {
		t.Errorf("the tree after gc: %v", err)
	}
	for range 2 { // deleting twice is not an error
		if err := r.SaveTurn(ctx, ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.SavedTurn(ctx); got != "" {
		t.Errorf("SavedTurn after the delete = %q, want \"\"", got)
	}
}

func TestSavedPerWorktree(t *testing.T) {
	t.Parallel()
	r, dir := openTestRepo(t)
	ctx := t.Context()
	other := filepath.Join(t.TempDir(), "wt")
	testgit.Git(t, dir, "worktree", "add", "-q", other, "main")
	r2 := open(t, other)
	if err := r.SaveBase(ctx, "feat"); err != nil {
		t.Fatal(err)
	}
	if err := r.SaveTurn(ctx, snapshot(t, r)); err != nil {
		t.Fatal(err)
	}
	if got := r2.SavedBase(ctx); got != "" {
		t.Errorf("SavedBase in the other worktree = %q, want \"\"", got)
	}
	if got := r2.SavedTurn(ctx); got != "" {
		t.Errorf("SavedTurn in the other worktree = %q, want \"\"", got)
	}
}
