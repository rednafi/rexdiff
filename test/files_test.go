package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
// at the start.
func TestMarkdownSetting(t *testing.T) {
	tm := start(t, newRepo(t), opts{args: []string{"-config", `{"markdown_view": "rendered"}`}})
	tm.keys("2", "tab", "j", "j", "j")
	tm.waitFor("Some text.")
	tm.waitGone("# Title")
}

// TestSearch checks that / finds files by name and lines by code, shows a
// preview, and opens the pick. A changed file opens in the Changes tab, at
// the line, even in a fold.
func TestSearch(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("/")
	tm.typed("long")
	tm.waitFor("files 1", "▶ long.txt", "preview · long.txt", "first")
	tm.keys("enter")
	tm.waitGone("preview ·")
	tm.waitFor("first", "⋯ 32 unmodified lines")

	tm.keys("/", "tab")
	tm.typed("line 7")
	tm.waitFor("code 1", "7: line 7", "▶   7  line 7")
	tm.keys("enter")
	tm.waitCursor("line 7")
}

// TestLiveReload checks that the Changes tab shows an edit and a new file
// without a key, and that r reloads.
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
	tm.keys("r")
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
// gh, with its checks and comments. It also checks that o opens it.
func TestPR(t *testing.T) {
	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	bin := tools(t, map[string]string{
		"gh": `cat <<'EOF'
{"number": 7, "title": "Add b", "state": "OPEN", "isDraft": false,
 "url": "https://github.com/o/r/pull/7", "body": "The body of the PR.",
 "author": {"login": "ann"}, "headRefName": "feat", "baseRefName": "main",
 "comments": [{"author": {"login": "bob"}, "body": "Looks good.",
   "createdAt": "2026-01-02T03:04:05Z"}],
 "statusCheckRollup": [{"name": "test", "conclusion": "SUCCESS"},
   {"name": "lint", "conclusion": "FAILURE"}]}
EOF
`,
		"opener": `printf '%s\n' "$@" > "${0%/*}/opened"` + "\n",
	})
	tm := start(t, repo, opts{path: bin, args: []string{
		"-config",
		`{"url_opener": "opener {url}"}`,
	}})
	tm.keys("3")
	tm.waitFor("Add b #7", "● open", "feat → main", "by ann",
		"checks: ✓ 1  ✗ 1", "✗ lint", "The body of the PR.", "bob",
		"Looks good.")
	tm.keys("o")
	waitFile(t, filepath.Join(bin, "opened"), func(s string) bool {
		return s == "https://github.com/o/r/pull/7\n"
	})
}

// TestNoPR checks that the PR tab tells why it has no pull request. The
// repository has no remote, or the tool of the remote is not installed.
func TestNoPR(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("3")
	tm.waitFor("no git remote")

	repo := newRepo(t)
	testgit.Git(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	// PATH has only git, so it has no gh.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	tm = start(t, repo, opts{env: []string{"PATH=" + bin}})
	tm.keys("3")
	tm.waitFor("gh is not installed")
}
