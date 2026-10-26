package test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// TestChanges checks the Changes tab. It checks the file list with its
// counts, the diff of a file, and the next and the previous file. It also
// checks a fold that opens.
func TestChanges(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.waitFor("Changes (2)", "M a.go", "+2 -1", "M long.txt", "+2 -2",
		"return 1", "x := 2", "not in Rex")
	tm.keys("f")
	tm.waitFor("first", "last", "⋯ 32 unmodified lines")
	tm.keys("j", "j", "j", "j", "j", "j")
	tm.waitFor("→ expand fold")
	tm.keys("right")
	tm.waitFor("line 20")
	tm.waitGone("unmodified lines")
	tm.keys("F")
	tm.waitFor("x := 2")
}

// TestMove checks the keys that move the cursor. They go to the next and
// the previous change, a page, half a page, a line, and a line number.
func TestMove(t *testing.T) {
	tm := start(t, newRepo(t), opts{height: 16})
	tm.keys("f", "j", "j", "j", "j", "j", "j", "right") // all of long.txt
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

// TestScopes checks that each scope shows its changes, and the pickers of
// the commits and the base.
func TestScopes(t *testing.T) {
	repo := newRepo(t)
	// origin/main hides main in the base picker.
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

// TestFlags checks that -scope, -base, -layout, and -wrap set the start,
// and that -log writes a log. As in herdr-reviewr, -wrap off cuts a long
// line at the start.
func TestFlags(t *testing.T) {
	repo := newRepo(t)
	long := "var long = \"" + strings.Repeat("ab", 100) + "end\"\n"
	testgit.WriteFile(t, repo, "b.go", "package a\n\n"+long)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{width: 220, args: []string{
		"-scope", "branch", "-base", "main", "-layout", "split", "-wrap", "off", "-log", log,
	}})
	s := tm.waitFor("vs main", "Changes (3)")
	if !sameLine(s, "return 1", "x := 2") {
		t.Errorf("the screen does not show return 1 beside x := 2. It shows:\n%s", s)
	}
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

// TestSplit checks that | shows the old and the new lines side by side
// on a wide screen. On a narrow screen, it tells why it cannot.
func TestSplit(t *testing.T) {
	tm := start(t, newRepo(t), opts{width: 220})
	tm.keys("|")
	tm.waitUntil(func(s string) bool { return sameLine(s, "return 1", "x := 2") },
		"show return 1 beside x := 2")

	narrow := start(t, newRepo(t), opts{width: 120})
	narrow.keys("|")
	narrow.waitFor("too narrow for split")
}

// TestWrap checks that rexdiff wraps a long line, that w cuts it, and
// that → scrolls to its end.
func TestWrap(t *testing.T) {
	repo := newRepo(t)
	long := "var long = \"" + strings.Repeat("ab", 60) + "end\"\n"
	testgit.WriteFile(t, repo, "b.go", "package a\n\n"+long)
	tm := start(t, repo, opts{width: 120})
	tm.keys("f")
	tm.waitFor("end\"")
	tm.keys("w")
	tm.waitGone("end\"")
	// Each → scrolls 8 columns. 12 of them show the end of the line, and
	// keep its start in the view.
	tm.keys(slices.Repeat([]string{"right"}, 12)...)
	tm.waitFor("end\"")
}

// column returns the column of text in the first line of s that has it,
// or -1.
func column(s, text string) int {
	if x, _, ok := position(s, text); ok {
		return x
	}
	return -1
}

// TestFileList checks that < and > change the width of the file list, p
// moves it, z hides it, and tab shows it again.
func TestFileList(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	right := column(tm.waitFor("Changes (2)"), "Changes (2)")
	tm.keys("<")
	tm.waitUntil(func(s string) bool { return column(s, "Changes (2)") < right }, "widen the list")
	tm.keys(">", ">")
	tm.waitUntil(func(s string) bool { return column(s, "Changes (2)") > right }, "narrow the list")
	tm.keys("p") // bottom
	tm.waitUntil(func(s string) bool {
		_, y, ok := position(s, "Changes (2)")
		return ok && y > 10
	}, "show the list at the bottom")
	tm.keys("z")
	tm.waitGone("Changes (2)")
	tm.keys("tab") // the focus shows a hidden list
	tm.waitFor("Changes (2)")
}

// TestKeys checks that ? shows all the keys.
func TestKeys(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("?")
	tm.waitFor("search files and code", "pick commits", "move list")
	tm.keys("esc")
	tm.waitGone("pick commits")
}

// TestFind checks that ctrl+f counts the lines with a text, and that
// enter moves to a hit.
func TestFind(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("f", "ctrl+f")
	tm.typed("line 3")
	// line 3, and line 30 to line 39
	tm.waitUntil(func(s string) bool { return sameLine(s, "find line 3", " 11") },
		"count 11 lines")
	tm.keys("enter")
	tm.waitCursor("line 3")
}

// TestSettings checks that the settings change the layout and the marks
// of the changed lines. They also change the place and the width of the
// file list, the scope, and the keys.
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

// TestReviewPrefix checks that review_prefix comes before the review.
func TestReviewPrefix(t *testing.T) {
	bin := clipboard(t)
	tm := start(t, newRepo(t), opts{path: bin, env: clipEnv, args: []string{
		"-config",
		`{"review_prefix": "Please fix:"}`,
	}})
	comment(tm, "hi")
	tm.keys("y")
	waitFile(t, filepath.Join(bin, "clip"), func(s string) bool {
		return s == "Please fix:\n\n"+review
	})
}

// TestTinyScreen checks that rexdiff draws and takes keys in a small
// terminal.
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

// TestHostileText checks that the escape codes in a file show as text, so
// a file cannot change the terminal.
func TestHostileText(t *testing.T) {
	repo := newRepo(t)
	testgit.WriteFile(t, repo, "evil.txt", "a\x1b[2Jb\x1b]0;title\x07c\n")
	tm := start(t, repo, opts{})
	tm.keys("f")
	tm.waitFor("a␛[2Jb␛]0;title␇c")
}
