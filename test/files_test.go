package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// TestFilesTab checks that the Files tab lists all the files, expands and
// collapses an ignored directory, opens a file, and renders markdown.
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

// TestMarkdownSetting checks that markdown_view shows rendered markdown
// at the start. As in herdr-reviewr, a changed block of rendered markdown
// has the bar, and a comment on it is on all the lines of the block. A
// drag copies the rendered text. n goes to a comment that starts in a
// block.
func TestMarkdownSetting(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "README.md", "# Title\n\nSome text.\n\nNew text\non\nthree lines.\n")
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
		return sameLine(s, "▌", "New text on three lines.") && !sameLine(s, "▌", "Some text.")
	}, "mark the changed block")
	tm.keys(":", "6", "enter")
	tm.waitCursor("New text on three lines.")
	tm.keys("c")
	tm.typed("hi")
	tm.keys("enter")
	tm.waitFor("💬 hi")
	tm.keys("y")
	waitFile(t, filepath.Join(bin, "clip"), func(s string) bool {
		return s == "README.md:5-7\n+New text\n+on\n+three lines.\nhi"
	})
	tm.drag("Some", "text.")
	waitFile(t, filepath.Join(bin, "clip"), func(s string) bool { return s == "Some t" })
	tm.keys("m", ":", "6", "enter")
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

// TestSearch checks that / finds files by name and lines by code, shows a
// preview, and opens the pick. The wheel scrolls the preview. A changed
// file opens in the Changes tab, at the line, even in a fold.
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

// TestLiveReload checks that the Changes tab shows an edit, a new file,
// and a removed file without a key.
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

// TestLastTurn checks that the last-turn scope shows what the agent
// changes in its last turn.
func TestLastTurn(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{rex: rex, args: []string{"-log", log}})
	tm.keys("t")
	// rexdiff takes the states of an agent after its first list of them.
	tm.waitFor("waiting for the first turn", "send → 1 agent")
	rex.setState(t, "working")
	// The turn starts with a snapshot of the files.
	waitFile(t, log, func(s string) bool { return strings.Contains(s, "msg=\"turn start\"") })
	testgit.WriteFile(t, repo, "turn.go", "package a\n")
	rex.setState(t, "idle")
	tm.waitFor("Changes (1)", "turn.go", "pi")
}

// TestTurnRaced checks that a turn whose start comes right after a write
// is not known, because its start snapshot can have the write. The scope
// tells so, as herdr-reviewr does.
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

// TestPR checks that the PR tab shows the pull request of the branch from
// gh, with its checks and comments, and the age of each comment. It also
// checks that o opens it, as in herdr-reviewr. Without {url}, the URL goes
// at the end of the opener command. o does not open a URL that is not
// http or https. The keys of the scopes and of the base picker do nothing
// on the PR tab, and r reads the pull request again.
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
	waitFile(t, filepath.Join(bin, "opened"), func(s string) bool {
		return s == "https://github.com/o/r/pull/7\n"
	})
	tm.click("the docs", 1)
	tm.waitFor("opened link in browser")
	waitFile(t, filepath.Join(bin, "opened"), func(s string) bool {
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

// TestNoPR checks that the PR tab tells why it has no pull request. The
// repository has no remote, or the tool of the remote is not installed.
func TestNoPR(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("3")
	tm.waitFor("no git remote")

	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	tm = start(t, repo, opts{})
	tm.keys("3")
	tm.waitFor("gh is not installed")
}
