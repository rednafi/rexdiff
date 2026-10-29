package test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// TestChanges checks that the Changes tab lists the changed files with
// their added and removed line counts and shows the diff of the selected
// file, that f and F move to the next and previous file, and that right
// and left expand and collapse a fold of unchanged lines.
func TestChanges(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.waitFor("Changes (2)", "M a.go", "+2 -1", "M long.txt", "+2 -2",
		"return 1", "x := 2", "not in Rex", "a.go  +2 -1")
	tm.keys("f")
	tm.waitFor("first", "last", "⋯ 32 unmodified lines", "long.txt  +2 -2")
	tm.keys("j", "j", "j", "j", "j", "j")
	tm.waitFor("→ expand fold")
	tm.keys("right")
	tm.waitFor("line 20", "← collapse fold")
	tm.waitGone("unmodified lines")
	tm.keys("left")
	tm.waitFor("⋯ 32 unmodified lines", "→ expand fold")
	tm.waitGone("line 20")
	tm.keys("right")
	tm.waitFor("line 20")
	tm.keys("F")
	tm.waitFor("x := 2")
}

// TestCollapseSelection checks that collapsing a fold that contains the
// selected lines clears the selection.
func TestCollapseSelection(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("f", "j", "j", "j", "j", "j", "j", "right")
	tm.waitFor("line 20", "← collapse fold")
	tm.keys("j", "v", "j", "j")
	tm.waitFor("extend", "clear")
	tm.keys("left")
	tm.waitFor("⋯ 32 unmodified lines", "→ expand fold")
	tm.waitGone("extend")
}

// TestMove checks the keys that move the cursor: ] and [ to the next and
// previous change, page and half-page keys, : to go to a line number, and
// the arrow keys.
func TestMove(t *testing.T) {
	tm := start(t, newRepo(t), opts{height: 16})
	tm.keys("f", "j", "j", "j", "j", "j", "j", "right") // expand the fold to show all of long.txt
	tm.waitFor("line 10")
	tm.keys("]", "]")
	tm.waitCursor("line 40")
	tm.keys("[")
	tm.waitCursor("line 1")
	for _, keys := range [][2]string{{"pgdown", "pgup"}, {"ctrl+d", "ctrl+u"}} {
		tm.keys(keys[0], keys[0], keys[0])
		tm.waitGone(" first")
		tm.keys(keys[1], keys[1], keys[1])
		tm.waitFor(" first")
	}
	tm.keys(":", "3", "3", "enter")
	tm.waitCursor("line 33")
	tm.keys(":", "$", "enter")
	tm.waitCursor("last")
	tm.keys(":", "0", "enter")
	tm.waitCursor("first")
	tm.keys("down")
	tm.waitCursor("line 2")
	tm.keys("up")
	tm.waitCursor("first")
}

// TestScopes checks that the branch, uncommitted and last-turn scopes
// show the right changes, that the commit picker shows a single commit,
// and that the base picker marks the current base and switches to
// another one.
func TestScopes(t *testing.T) {
	repo := newRepo(t)
	// In the base picker, origin/main replaces main.
	testgit.Git(t, repo, "update-ref", "refs/remotes/origin/main", "main")
	tm := start(t, repo, opts{})
	tm.keys("b")
	tm.waitFor("vs main", "Changes (3)", "A b.go")
	tm.keys("u")
	tm.waitFor("Changes (2)")
	tm.waitGone("b.go")
	tm.keys("t")
	tm.waitFor("waiting for the first turn")
	tm.keys("g")
	tm.waitFor("commits · 1 over main", "add b")
	tm.keys("enter")
	tm.waitFor("Changes (1)", "A b.go", "var B = 2")
	tm.keys("G")
	tm.waitFor("commits · 1 over main")
	tm.keys("esc")
	tm.waitGone("commits · 1 over main")
	tm.keys("u", "B")
	if s := tm.waitFor("base branch for the branch scope", "origin/main"); len(lines(s, "default current")) != 1 {
		t.Errorf("the base picker has no current tag on origin/main. It shows:\n%s", s)
	}
	tm.typed("feat")
	tm.keys("enter")
	tm.waitFor("vs feat", "Changes (2)")
}

// TestSlowCommitPicker checks that while the commit list loads, the
// commit picker shows that it is loading, q does not quit, and esc closes
// the picker for good: the end of the cancelled list does not open it
// again.
func TestSlowCommitPicker(t *testing.T) {
	// The fake git lists no commits until the test lets it. The first list
	// notes in the file ready that it can be stopped. It waits until rexdiff
	// stops it and then notes that in the file stopped.
	// The second waits for the FIFO gate. Then it runs the real git, which
	// is next on PATH.
	bin := tools(t, map[string]string{"git": `d="${0%/*}"
case "$*" in
*--first-parent*)
	if mkdir "$d/first" 2>/dev/null; then
		trap 'kill $!; echo stopped > "$d/stopped"; exit 1' TERM
		sleep 60 &
		echo ready > "$d/ready"
		wait
	fi
	cat "$d/gate" >/dev/null ;;
esac
PATH=${PATH#*:} exec git "$@"
`})
	gate := filepath.Join(bin, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, newRepo(t), opts{path: bin, args: []string{"-log", log}})
	tm.keys("G")
	tm.waitFor("loading commits…", "esc cancel")
	waitFile(t, filepath.Join(bin, "ready"), "ready", func(s string) bool { return s == "ready\n" })
	tm.keys("esc")
	tm.waitGone("loading commits…")
	waitFile(t, filepath.Join(bin, "stopped"), "stopped", func(s string) bool { return s == "stopped\n" })
	tm.keys("G")
	tm.waitFor("loading commits…")
	tm.keys("q", "2")
	// Let the list end once rexdiff has read G, esc, G, q and 2.
	waitFile(t, log, "five keys", func(s string) bool { return strings.Count(s, "msg=key") == 5 })
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tm.waitFor("commits · 1 over main", "add b")
	tm.keys("esc")
	tm.waitGone("commits · 1 over main")
	tm.waitFor("1 Changes", "Changes (")
}

// TestFlags checks that -scope, -base, -layout and -wrap set the initial
// state, that -wrap off does not wrap a long line, and that -log writes a
// log file.
func TestFlags(t *testing.T) {
	repo := newRepo(t)
	long := "var long = \"" + strings.Repeat("ab", 100) + "end\"\n"
	testgit.WriteFile(t, repo, "b.go", "package a\n\n"+long)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{width: 220, args: []string{
		"-scope", "branch", "-base", "main", "-layout", "split", "-wrap", "off", "-log", log,
	}})
	// Wait for the whole frame, because the top lines can appear before the
	// rest.
	tm.waitUntil(func(s string) bool {
		return hasAll(s, "vs main", "Changes (3)") && sameLine(s, "return 1", "x := 2")
	}, "show return 1 beside x := 2 vs main")
	tm.keys("f")
	if s := tm.waitFor("var long"); strings.Contains(s, "end\"") {
		t.Errorf("-wrap off wraps the long line. The screen shows:\n%s", s)
	}
	tm.keys("q")
	if err := tm.exited(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(log); err != nil || len(b) == 0 {
		t.Errorf("the log has %d bytes (%v), want a log", len(b), err)
	}
}

// TestSplit checks that | shows the old and new lines side by side on a
// wide screen, and shows a message instead on a narrow screen.
func TestSplit(t *testing.T) {
	tm := start(t, newRepo(t), opts{width: 220})
	tm.keys("|")
	tm.waitUntil(func(s string) bool { return sameLine(s, "return 1", "x := 2") },
		"show return 1 beside x := 2")

	narrow := start(t, newRepo(t), opts{width: 120})
	narrow.keys("|")
	narrow.waitFor("too narrow for split")
}

// TestWrap checks that rexdiff wraps a long line by default, that w turns
// wrapping off, and that the right arrow then scrolls to the end of the
// line.
func TestWrap(t *testing.T) {
	repo := newRepo(t)
	long := "var long = \"" + strings.Repeat("ab", 60) + "end\"\n"
	testgit.WriteFile(t, repo, "b.go", "package a\n\n"+long)
	tm := start(t, repo, opts{width: 120})
	tm.keys("f")
	tm.waitFor("end\"")
	tm.keys("w")
	tm.waitGone("end\"")
	// Each right arrow scrolls 8 columns, so 12 presses reach the end of
	// the line.
	tm.keys(slices.Repeat([]string{"right"}, 12)...)
	tm.waitFor("end\"")
}

// column returns the column of the first occurrence of text in s, or -1
// if s does not contain text.
func column(s, text string) int {
	if x, _, ok := position(s, text); ok {
		return x
	}
	return -1
}

// TestFileList checks that < and > change the width of the file list, p
// moves it to the bottom, z hides it, and tab shows it again.
func TestFileList(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	right := column(tm.waitFor("Changes (2)"), "Changes (2)")
	tm.keys("<")
	tm.waitUntil(func(s string) bool { return column(s, "Changes (2)") < right }, "widen the list")
	tm.keys(">", ">")
	tm.waitUntil(func(s string) bool { return column(s, "Changes (2)") > right }, "narrow the list")
	tm.keys("p") // move the list to the bottom
	tm.waitUntil(func(s string) bool {
		_, y, ok := position(s, "Changes (2)")
		return ok && y > 10
	}, "show the list at the bottom")
	tm.keys("z")
	tm.waitGone("Changes (2)")
	tm.keys("tab") // moving the focus to a hidden list shows it
	tm.waitFor("Changes (2)")
}

// TestKeys checks that ? opens the help with all the keys and esc closes
// it.
func TestKeys(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("?")
	tm.waitFor("search files and code", "pick commits", "move list")
	tm.keys("esc")
	tm.waitGone("pick commits")
}

// TestFind checks that ctrl+f counts the lines that contain the typed
// text, that enter moves the cursor to the first match, and that a pasted
// \r\n becomes a single space.
func TestFind(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("f", "ctrl+f")
	tm.typed("line 3")
	// "line 3" matches line 3 and lines 30 to 39.
	tm.waitUntil(func(s string) bool { return sameLine(s, "find line 3", " 11") },
		"count 11 lines")
	tm.keys("enter")
	tm.waitCursor("line 3")
	tm.keys("esc")
	tm.waitGone("find line 3")
	tm.keys("ctrl+f")
	tm.paste("line\r\n3")
	tm.waitUntil(func(s string) bool { return sameLine(s, "find line 3", "1/11") },
		"count 11 lines for the pasted query")
}

// TestFindInFiles checks that ctrl+f finds text in a file of the Files
// tab, and in a Markdown file shown rendered.
func TestFindInFiles(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("2")
	tm.click("long.txt", 1)
	tm.waitFor("line 37")
	tm.keys("ctrl+f")
	tm.typed("line 3")
	tm.keys("enter")
	tm.waitCursor("line 3")
	tm.keys("esc")
	tm.waitGone("find line 3")
	tm.click("README.md", 1)
	tm.keys("m")
	tm.waitGone("# Title")
	tm.keys("ctrl+f")
	tm.typed("text")
	tm.keys("enter")
	tm.waitCursor("Some text.")
}

// TestFindNothing checks that ctrl+f opens the find bar on a binary file,
// which has no text to find, so that the typed text goes into the bar and
// does not run commands such as t, which switches the scope.
func TestFindNothing(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "bin.dat", "a\x00b\n")
	tm := start(t, repo, opts{})
	tm.keys("f")
	tm.waitFor("bin.dat  binary")
	tm.keys("ctrl+f")
	tm.typed("test")
	tm.waitUntil(func(s string) bool { return sameLine(s, "find test", "nothing to find here") },
		"the query in the find bar with a note")
}

// TestBranchNoCommits checks that the branch scope in a repository with
// no commits says so in plain words, as the commit picker does, and that
// a long error keeps its indent on every line when it wraps.
func TestBranchNoCommits(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	testgit.WriteFile(t, repo, "a.txt", "a\n")
	tm := start(t, repo, opts{args: []string{"-scope", "branch"}})
	tm.waitFor("no commits yet")
	tm.waitGone("merge-base")

	tm = start(t, newRepo(t), opts{width: 50, args: []string{"-scope", "branch", "-base", "nosuchbranch"}})
	s := tm.waitFor("no merge base with")
	_, y, _ := position(s, "no merge base with")
	ls := strings.Split(s, "\n")
	if len(ls) <= y+1 || !strings.HasPrefix(ls[y+1], "  ") || strings.TrimSpace(ls[y+1]) == "" {
		t.Errorf("the error does not wrap with its indent. The screen shows:\n%s", s)
	}
}

// TestSettings checks that -config settings change the layout, the +/-
// signs, the position and width of the file list, the default scope, and
// the key bindings.
func TestSettings(t *testing.T) {
	tm := start(t, newRepo(t), opts{width: 220, args: []string{
		"-config",
		`{"layout": "split", "diff_signs": true, "file_list": "left",` +
			` "file_list_width": 50, "default_scope": "branch",` +
			` "keybindings": {"next-file": ["x"]}}`,
	}})
	s := tm.waitFor("vs main", "4 -     return 1", "4 +     x := 2", "x/F next/prev file")
	if !sameLine(s, "return 1", "x := 2") {
		t.Errorf("the screen does not show the old and the new line side by side. It shows:\n%s", s)
	}
	if c := column(s, "Changes (3)"); c < 0 || c > 5 {
		t.Errorf("the list starts at column %d, want it at the left", c)
	}
	if ls := lines(s, "M a.go"); len(ls) == 0 || column(ls[0], "│") < 48 || column(ls[0], "│") > 52 {
		t.Errorf("the list rows %q, want a width of 50", ls)
	}
	tm.keys("x")
	tm.waitFor("var B = 2")
}

// TestReviewPrefix checks that the review_prefix setting is added before
// the copied review.
func TestReviewPrefix(t *testing.T) {
	bin := clipboard(t)
	tm := start(t, newRepo(t), opts{path: bin, env: clipEnv, args: []string{
		"-config",
		`{"review_prefix": "Please fix:"}`,
	}})
	comment(tm, "hi")
	tm.keys("y")
	waitFile(t, filepath.Join(bin, "clip"), "the review after \"Please fix:\"", func(s string) bool {
		return s == "Please fix:\n\n"+review
	})
}

// TestTinyScreen checks that rexdiff draws its screen and responds to keys
// in a 40 by 8 terminal.
func TestTinyScreen(t *testing.T) {
	tm := start(t, newRepo(t), opts{width: 40, height: 8})
	tm.keys("f", "j", "2", "/")
	tm.waitFor("›")
	tm.keys("esc")
	tm.waitGone("›")
	tm.keys("?")
	tm.waitFor("ctrl+d/u")
	tm.keys("esc")
	tm.waitGone("ctrl+d/u")
	tm.keys("1")
	tm.waitFor("1 Changes")
}

// TestHostileText checks that escape sequences in a file are shown as
// visible symbols and do not affect the terminal.
func TestHostileText(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "evil.txt", "a\x1b[2Jb\x1b]0;title\x07c\n")
	tm := start(t, repo, opts{})
	tm.keys("f")
	tm.waitFor("a␛[2Jb␛]0;title␇c")
}

// TestTinyStackedNavigator checks that a file list placed above or below
// the main pane is hidden when the terminal is too short for it, and that
// no line is wider than the terminal.
func TestTinyStackedNavigator(t *testing.T) {
	for _, pos := range []string{"top", "bottom"} {
		t.Run(pos, func(t *testing.T) {
			const width = 60
			tm := start(t, newRepo(t), opts{width: width, height: 6, args: []string{
				"-config", `{"file_list": "` + pos + `"}`,
			}})
			s := tm.waitFor("a.go", "package a")
			if strings.Contains(s, "Changes (2)") {
				t.Errorf("the list shows with no rows for it:\n%s", s)
			}
			for i, l := range strings.Split(s, "\n") {
				if w := ansi.StringWidth(l); w > width {
					t.Errorf("line %d is %d columns wide, want %d or less: %q", i, w, width, l)
				}
			}
		})
	}
}

// TestSkippedCounts checks that the file list and the title say why a
// binary or too large file has no counts.
func TestSkippedCounts(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "bin.dat", "a\x00b\n")
	testgit.WriteFile(t, repo, "huge.txt", strings.Repeat("x\n", 60_000))
	tm := start(t, repo, opts{})
	tm.waitUntil(func(s string) bool {
		return sameLine(s, "bin.dat", "binary") && sameLine(s, "huge.txt", "too large")
	}, "show binary and too large in the list")
}

// TestEmptyFile checks that a new empty file shows in the list with no
// counts, and that its diff says that the content did not change.
func TestEmptyFile(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "empty.txt", "")
	tm := start(t, repo, opts{})
	tm.waitUntil(func(s string) bool { return sameLine(s, "A empty.txt", "+0 -0") }, "list empty.txt with +0 -0")
	tm.keys("f")
	tm.waitFor("empty.txt  +0 -0", "no change to the content")
}
