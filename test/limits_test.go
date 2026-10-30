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

func fastImport(t *testing.T, repo, stream string) {
	t.Helper()
	cmd := proc.Command(t.Context(), testgit.ToolEnv(t), "git", "fast-import", "--quiet")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(stream)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git fast-import: %v: %s", err, out)
	}
}

// TestTooManyChanges commits the files without writing them to the working
// tree.
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

func TestFindQueryLimit(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("f", "ctrl+f")
	tm.paste(strings.Repeat("x", 300))
	tm.waitFor("find xxxxxxxx", "no matches")
	tm.keys(slices.Repeat([]string{"bs"}, 256)...)
	tm.typed("line 3")
	tm.waitUntil(func(s string) bool { return sameLine(s, "find line 3", " 11") },
		"count 11 lines")
}

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

func cursorLine(s string) string {
	for l := range strings.Lines(s) {
		if strings.Contains(l, "▶") {
			return strings.TrimRight(l, "\n")
		}
	}
	return ""
}

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
