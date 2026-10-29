package test

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/proc"
	"github.com/rednafi/rexdiff/internal/testgit"
)

// fastImport runs git fast-import in repo with stream as its input.
func fastImport(t *testing.T, repo, stream string) {
	t.Helper()
	cmd := proc.Command(t.Context(), testgit.ToolEnv(t), "git", "fast-import", "--quiet")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(stream)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v: %s", err, out)
	}
}

// TestTooManyChanges checks that the Changes tab names the number of
// changed files, without listing them, when there are too many. The
// files are committed but missing from the working tree, so the test
// writes no files.
func TestTooManyChanges(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(testgit.Git(t, repo, "rev-parse", "HEAD"))
	var b strings.Builder
	fmt.Fprintf(&b, "blob\nmark :1\ndata 2\nx\n\ncommit refs/heads/feat\n"+
		"committer a <a@b> 0 +0000\ndata 4\nmany\nfrom %s\n", head)
	for i := range 20_001 {
		fmt.Fprintf(&b, "M 100644 :1 many/%d\n", i)
	}
	fastImport(t, repo, b.String()+"\n")
	testgit.Git(t, repo, "read-tree", "HEAD")
	tm := start(t, repo, opts{})
	// The two changes of the fixture make 20,003.
	tm.waitFor("20003 files changed — too many to show", "Changes (20003)")
}

// TestCommitPickerLimit checks that the commit picker lists at most 1,000
// commits over the base, and says so in its title.
func TestCommitPickerLimit(t *testing.T) {
	repo := newRepo(t)
	head := strings.TrimSpace(testgit.Git(t, repo, "rev-parse", "HEAD"))
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "commit refs/heads/feat\ncommitter a <a@b> %d +0000\ndata 8\nc %05d\n", i, i)
		if i == 0 {
			fmt.Fprintf(&b, "from %s\n", head)
		}
	}
	fastImport(t, repo, b.String())
	tm := start(t, repo, opts{})
	tm.keys("g")
	tm.waitFor("commits · newest 1000 over main", "c 00999")
}

// TestFindQueryLimit checks that the find bar stops taking characters
// once the query holds 256.
func TestFindQueryLimit(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("f", "ctrl+f")
	tm.paste(strings.Repeat("x", 300))
	tm.waitFor("find xxxxxxxx", "no matches")
	// If the query held more than 256 characters, some would remain.
	tm.keys(slices.Repeat([]string{"bs"}, 256)...)
	tm.typed("line 3")
	tm.waitUntil(func(s string) bool { return sameLine(s, "find line 3", " 11") },
		"count 11 lines")
}

// TestFindInLongLine checks that find shows a match far into a long line:
// by scrolling sideways, or when lines wrap and the line is taller than
// the screen, by scrolling down through it.
func TestFindInLongLine(t *testing.T) {
	for _, wrap := range []string{"off", "on"} {
		t.Run("wrap="+wrap, func(t *testing.T) {
			repo := newRepo(t)
			testgit.Git(t, repo, "checkout", "--", "long.txt")
			testgit.WriteFile(t, repo, "a.go",
				"package a\n\n// start "+strings.Repeat("word ", 2000)+"NEEDLE end\nvar x = 1\n")
			tm := start(t, repo, opts{height: 20, args: []string{"-wrap", wrap}})
			tm.waitFor("// start")
			tm.waitGone("NEEDLE")
			tm.keys("ctrl+f")
			tm.typed("needle")
			tm.keys("enter")
			tm.waitFor("NEEDLE end")
		})
	}
}

// TestClickFindBarShortScreen checks that on a screen too short for the
// body, a click on the find bar does not count as a click on the code
// above it, which would move the cursor.
func TestClickFindBarShortScreen(t *testing.T) {
	tm := start(t, newRepo(t), opts{height: 4})
	tm.keys("ctrl+f")
	before := strings.Split(tm.waitFor("find "), "\n")
	tm.click("find ", 1)
	tm.typed("z")
	after := strings.Split(tm.waitFor("find z"), "\n")
	if before[2] != after[2] {
		t.Errorf("the code line changed from %q to %q", before[2], after[2])
	}
}

// cursorLine returns the line of s with the cursor ▶, or "".
func cursorLine(s string) string {
	for l := range strings.Lines(s) {
		if strings.Contains(l, "▶") {
			return strings.TrimRight(l, "\n")
		}
	}
	return ""
}

// TestCursorShortScreen checks that on a screen with room for only one
// row of code, the cursor row stays on screen after each key that moves
// the cursor.
func TestCursorShortScreen(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	testgit.WriteFile(t, repo, "u.txt", numbered("line", 1, 40))
	testgit.Git(t, repo, "add", ".")
	testgit.Git(t, repo, "commit", "-qm", "first")
	tm := start(t, repo, opts{width: 40, height: 4})
	tm.keys("2")
	tm.waitCursor("line 1")
	prev := cursorLine(tm.screen())
	for _, k := range []string{"down", "down", "ctrl+d", "up", "pgdown", "j"} {
		tm.keys(k)
		s := tm.waitUntil(func(s string) bool {
			l := cursorLine(s)
			return l != "" && l != prev
		}, "show the cursor row after "+k)
		prev = cursorLine(s)
	}
}
