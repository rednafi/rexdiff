package test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// TestFilesTab checks that the Files tab lists every file, expands and
// collapses an ignored directory, opens a file, and that m switches a
// Markdown file to rendered view.
func TestFilesTab(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("2")
	tm.waitFor("Files (6)", "docs/guide.txt", "▸ out/", "long.txt")
	tm.keys("tab", "j", "right")
	tm.waitFor("▾ out/", "build.log")
	tm.keys("left")
	tm.waitFor("▸ out/")
	tm.waitGone("build.log")
	tm.keys("j", "j")
	tm.waitFor("# Title")
	tm.keys("m")
	tm.waitGone("# Title")
	tm.waitFor("Title", "Some text.")
}

// TestFilesTabDeleted checks that the Files tab leaves out a tracked file
// that was deleted from the working tree, while the Changes tab still
// shows the deletion.
func TestFilesTabDeleted(t *testing.T) {
	repo := newRepo(t)
	if err := os.Remove(filepath.Join(repo, "docs", "guide.txt")); err != nil {
		t.Fatal(err)
	}
	tm := start(t, repo, opts{})
	tm.waitFor("D docs/guide.txt")
	tm.keys("2")
	tm.waitFor("Files (5)", "long.txt")
	tm.waitGone("guide.txt")
}

// TestInvisibleNames checks that the file list shows a zero-width space,
// a byte order mark and a right-to-left override in a file name as
// escapes.
func TestInvisibleNames(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "zero\u200bwidth.txt", "a\n")
	testgit.WriteFile(t, repo, "\ufeffbom.txt", "b\n")
	testgit.WriteFile(t, repo, "evil\u202etxt.exe", "c\n")
	tm := start(t, repo, opts{})
	tm.waitFor(`"zero\u200bwidth.txt"`, `"\ufeffbom.txt"`, `"evil\u202etxt.exe"`)
}

// TestMarkdownSetting checks that the markdown_view setting shows Markdown
// rendered from the start. It also checks, in rendered view, the change
// bars on changed blocks, comments and copying on a block, and moving to
// the next comment with n.
func TestMarkdownSetting(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "README.md", "# Title\n\nGone.\n\nSome text.\n\n<!-- a -->\n")
	testgit.Git(t, repo, "commit", "-qam", "readme")
	testgit.WriteFile(t, repo, "README.md",
		"# Title\n\nSome text.\n\n<!-- b -->\n\nNew text\non\nthree lines.\n")
	bin := clipboard(t)
	tm := start(t, repo, opts{path: bin, env: clipEnv, args: []string{
		"-config", `{"markdown_view": "rendered"}`,
	}})
	tm.keys("2", "tab", "j", "j", "j")
	tm.waitFor("Some text.")
	tm.waitGone("# Title")
	tm.keys("1", "tab")
	tm.waitFor("Some text.")
	tm.waitUntil(func(s string) bool {
		return sameLine(s, "▎", "New text on three lines.") && !sameLine(s, "▎", "Some text.") &&
			sameLine(s, "▎", "− 1 line removed") &&
			sameLine(s, "▎", "⚠ 1 changed line doesn't render · m to see")
	}, "mark the changed blocks, the removed block, and the changed comment")
	tm.keys(":", "8", "enter")
	tm.waitCursor("New text on three lines.")
	tm.keys("c")
	tm.typed("hi")
	tm.keys("enter")
	tm.waitFor("💬 hi")
	tm.keys("y")
	waitFile(t, filepath.Join(bin, "clip"), "the three new lines of README.md with their location", func(s string) bool {
		return s == "README.md:7-9\n+New text\n+on\n+three lines.\nhi"
	})
	tm.drag("Some", "text.")
	waitFile(t, filepath.Join(bin, "clip"), `"Some t"`, func(s string) bool { return s == "Some t" })
	tm.keys("m", ":", "8", "enter")
	tm.waitCursor("on")
	tm.keys("c")
	tm.typed("x")
	tm.keys("enter")
	tm.waitFor("💬 x")
	tm.keys("m", ":", "0", "enter")
	tm.waitCursor("Title")
	tm.keys("n")
	tm.waitCursor("New text on three lines.")
}

// TestMarkdownHunks checks that ] and [ in rendered Markdown visit every
// changed block in turn, including the marker of a removed block, and do
// not get stuck at that marker.
func TestMarkdownHunks(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testgit.InitRepo(t, repo)
	var old, cur strings.Builder
	for n := 1; n <= 30; n++ {
		fmt.Fprintf(&old, "## Section %d\n\nParagraph %d text.\n\n", n, n)
		if n == 25 {
			fmt.Fprintf(&cur, "## Section %d\n\n", n) // paragraph 25 is removed
			continue
		}
		word := "text"
		if n == 5 || n == 20 || n == 27 {
			word = "CHANGED"
		}
		fmt.Fprintf(&cur, "## Section %d\n\nParagraph %d %s.\n\n", n, n, word)
	}
	testgit.WriteFile(t, repo, "doc.md", old.String())
	testgit.Git(t, repo, "add", "doc.md")
	testgit.Git(t, repo, "commit", "-qm", "doc")
	testgit.WriteFile(t, repo, "doc.md", cur.String())
	tm := start(t, repo, opts{width: 120, height: 20, args: []string{
		"-config", `{"markdown_view": "rendered"}`,
	}})
	tm.waitFor("Section 1")
	stops := []string{"Paragraph 5 CHANGED", "Paragraph 20 CHANGED", "1 line removed", "Paragraph 27 CHANGED"}
	for _, s := range stops {
		tm.keys("]")
		tm.waitCursor(s)
	}
	for _, s := range slices.Backward(stops[:3]) {
		tm.keys("[")
		tm.waitCursor(s)
	}
}

// TestSearch checks that / finds files by name and lines by content,
// shows a preview that the mouse wheel scrolls, and opens a changed file
// in the Changes tab at the matching line.
func TestSearch(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("/")
	tm.typed("long")
	tm.waitFor("files 1", "▶ long.txt", "preview · long.txt", "first")
	tm.wheel("line 5", 1)
	tm.waitGone("first")
	tm.keys("enter")
	tm.waitGone("preview ·")
	tm.waitFor("first", "⋯ 32 unmodified lines")

	tm.keys("/", "tab")
	tm.typed("line 7")
	tm.waitFor("code 1", "7: line 7", "▶   7  line 7")
	tm.keys("enter")
	tm.waitCursor("line 7")
}

// TestLiveReload checks that the Changes tab picks up an edit, a new file
// and a removed file without any key press.
func TestLiveReload(t *testing.T) {
	repo := newRepo(t)
	tm := start(t, repo, opts{})
	tm.waitFor("Changes (2)")
	testgit.WriteFile(t, repo, "new/c.go", "package c\n")
	tm.waitFor("Changes (3)", "c.go")
	testgit.WriteFile(t, repo, "a.go", "package a\n\nfunc A() int { return 3 }\n")
	tm.waitFor("return 3 }")
	if err := os.Remove(filepath.Join(repo, "new", "c.go")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor("Changes (2)")
}

// TestRemoveAfterRefresh checks that the Changes tab drops a file that
// was added, shown by r, and removed before the next check of the watcher.
func TestRemoveAfterRefresh(t *testing.T) {
	repo := newRepo(t)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{args: []string{"-log", log}})
	tm.waitFor("Changes (2)")
	// After the first check, a check reports only what changed since the
	// last one.
	waitFile(t, log, "a watcher check", func(s string) bool { return strings.Contains(s, "msg=watch") })
	testgit.WriteFile(t, repo, "fresh.txt", "fresh\n")
	tm.keys("r")
	tm.waitFor("Changes (3)", "fresh.txt")
	if err := os.Remove(filepath.Join(repo, "fresh.txt")); err != nil {
		t.Fatal(err)
	}
	tm.waitFor("Changes (2)")
}

// TestReloadStorm checks that after many quick writes to a file, as an
// agent makes, the Changes tab shows the content of the last write.
func TestReloadStorm(t *testing.T) {
	repo := newRepo(t)
	tm := start(t, repo, opts{})
	tm.waitFor("Changes (2)")
	for i := 1; i <= 200; i++ {
		testgit.WriteFile(t, repo, "a.go", fmt.Sprintf("package a\n\nfunc A() int { return %d }\n", i))
		testgit.WriteFile(t, repo, fmt.Sprintf("storm/%d.go", i%10), fmt.Sprintf("package storm // %d\n", i))
	}
	tm.waitFor("return 200 }", "Changes (12)")
}

// TestLastTurn checks that the last-turn scope shows the changes made
// while the agent was working.
func TestLastTurn(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{rex: rex, args: []string{"-log", log}})
	tm.keys("t")
	// rexdiff follows agent states only after it has listed the agents once.
	tm.waitFor("waiting for the first turn", "send → 1 agent")
	rex.setState(t, "working")
	// Wait until rexdiff has taken the snapshot that starts the turn.
	waitFile(t, log, "a turn start message", func(s string) bool { return strings.Contains(s, "msg=\"turn start\"") })
	testgit.WriteFile(t, repo, "turn.go", "package a\n")
	rex.setState(t, "idle")
	tm.waitFor("Changes (1)", "turn.go", "pi")
}

// TestTurnRaced checks that when a file is written just before a turn
// starts, the last-turn scope says that it could not snapshot the turn,
// because the snapshot may already include that write.
func TestTurnRaced(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	tm := start(t, repo, opts{rex: rex})
	tm.keys("t")
	tm.waitFor("waiting for the first turn", "send → 1 agent")
	testgit.WriteFile(t, repo, "turn.go", "package a\n")
	rex.setState(t, "working")
	tm.waitFor("the last turn started before rexdiff could snapshot it")
}

// TestPR checks the PR tab: the pull request with its checks and comment
// ages, collapsible details blocks, o and links opening in the url_opener
// command, scope keys that leave the tab alone, a link with an unsupported
// scheme, and r to reload.
func TestPR(t *testing.T) {
	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	created := time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	bin := tools(t, map[string]string{
		"gh": `cat <<EOF
{"number": 7, "title": "Add b$(cat "${0%/*}/suffix" 2>/dev/null)", "state": "OPEN", "isDraft": false,
 "url": "${PR_URL:-https://github.com/o/r/pull/7}", "body": "The body of the PR. See [the docs](https://example.com/docs).",
 "author": {"login": "ann"}, "headRefName": "feat", "baseRefName": "main",
 "comments": [{"author": {"login": "bob"}, "body": "Looks good.\n\n<details><summary>The  logs</summary>\n\nA hidden line.\n\n<details><summary>Inner</summary>\n\nDeep.\n\n</details>\n\nAfter inner.\n\n</details>",
   "createdAt": "` + created + `"}],
 "statusCheckRollup": [{"name": "test", "conclusion": "SUCCESS"},
   {"name": "lint", "conclusion": "FAILURE"}]}
EOF
`,
		"opener": `printf '%s\n' "$@" > "${0%/*}/opened"` + "\n",
	})
	config := []string{"-config", `{"url_opener": "opener"}`}
	tm := start(t, repo, opts{path: bin, args: config})
	tm.keys("3")
	s := tm.waitFor("Add b #7", "● open", "feat → main", "by ann",
		"checks: ✓ 1  ✗ 1", "✗ lint", "The body of the PR.", "bob  3h",
		"Looks good.", "▸ The logs")
	if strings.Contains(s, "A hidden line.") || strings.Contains(s, "After inner.") {
		t.Errorf("a closed details block shows its body. The screen shows:\n%s", s)
	}
	tm.keys("right")
	tm.waitFor("▾ The logs", "A hidden line.", "▾ Inner", "Deep.", "After inner.")
	tm.keys("left")
	tm.waitFor("▸ The logs")
	tm.waitGone("A hidden line.")
	tm.keys("u", "b", "t", "g", "B", "o")
	if s := tm.waitFor("opened PR in browser"); !strings.Contains(s, "Add b #7") {
		t.Errorf("a key of a scope leaves the PR tab. The screen shows:\n%s", s)
	}
	waitFile(t, filepath.Join(bin, "opened"), "the pull request URL", func(s string) bool {
		return s == "https://github.com/o/r/pull/7\n"
	})
	tm.click("the docs", 1)
	tm.waitFor("opened link in browser")
	waitFile(t, filepath.Join(bin, "opened"), "the docs URL", func(s string) bool {
		return s == "https://example.com/docs\n"
	})

	repo = newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	tm = start(t, repo, opts{path: bin, args: config, env: []string{"PR_URL=file:///etc/passwd"}})
	tm.keys("3")
	tm.waitFor("Add b #7")
	tm.keys("o")
	tm.waitFor("open failed: unsupported link scheme")
	if err := os.WriteFile(filepath.Join(bin, "suffix"), []byte(" again"), 0o600); err != nil {
		t.Fatal(err)
	}
	tm.keys("r")
	tm.waitFor("Add b again #7")
}

// TestNoPR checks that the PR tab explains why it shows no pull request:
// the repository has no remote, or the forge tool is not installed.
func TestNoPR(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("3")
	tm.waitFor("no git remote")

	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	// The PATH holds only git, because /usr/bin can contain gh.
	bin := t.TempDir()
	testgit.LinkGit(t, bin)
	tm = start(t, repo, opts{env: []string{"PATH=" + bin}})
	tm.keys("3")
	tm.waitFor("gh is not installed")
}
