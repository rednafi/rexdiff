package test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// review is the text of the comment "hi" on the first change of a.go.
const review = "a.go:4 (removed)\n-\treturn 1\nhi"

// comment adds the comment text on the first changed line of a.go.
func comment(tm *term, text string) {
	tm.t.Helper()
	tm.keys("]", "c")
	tm.typed(text)
	tm.keys("enter")
	tm.waitFor("💬 " + text)
}

// TestCommentList checks that a comment shows on its line and in the
// list. n and N move to it. The list edits the comment and deletes it. As
// in herdr-reviewr, an edit keeps the place of the comment in the list. An
// edit with blank text keeps the comment.
func TestCommentList(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.waitFor("comment added", "●1")
	tm.keys("k")
	tm.waitCursor("func A")
	tm.keys("n") // n stays in the file, as in herdr-reviewr
	tm.waitCursor("return 1")
	tm.keys("j", "j")
	tm.waitCursor("return x")
	tm.keys("N")
	tm.waitCursor("return 1")
	tm.keys("c", "z", "enter") // a second comment on the line
	tm.waitFor("💬 z")
	tm.keys("l")
	tm.waitFor("Comments (2)", "a.go:4 (removed)  hi")
	tm.keys("e", " ")
	tm.typed("more")
	tm.keys("enter")
	s := tm.waitFor("a.go:4 (removed)  hi more", "a.go:4 (removed)  z")
	_, edited, _ := position(s, "(removed)  hi more")
	if _, other, _ := position(s, "(removed)  z"); edited > other {
		t.Errorf("the edit moves the comment down. The screen shows:\n%s", s)
	}
	// The flash of the edit hides the badge of the draft, so wait for
	// the keys of the box.
	tm.keys("e")
	tm.waitFor("enter save")
	tm.keys(slices.Repeat([]string{"bs"}, len("hi more"))...)
	tm.keys("enter")
	tm.waitGone("enter save")
	tm.waitFor("a.go:4 (removed)  hi more")
	tm.keys("d", "d") // the list closes with its last comment
	tm.waitGone("Comments (", "💬")
}

// TestCommentDrag checks that a drag on the line numbers comments on a
// range of lines.
func TestCommentDrag(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.drag(" 1  1 ", " 3  3 ")
	tm.waitFor("✎ a.go:1-3")
	tm.keys("esc")
	tm.waitGone("✎ a.go:1-3")
}

// TestCommentBox checks that the box takes more than one line, and that
// esc drops a new comment. It also checks that v comments on a range of
// lines, and that e does not end the range.
func TestCommentBox(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	// The draft comes first, because the flash of an added comment hides
	// the badge of the draft.
	tm.keys("]", "j", "c", "x")
	tm.waitFor("✎ a.go:4 ")
	tm.keys("esc")
	tm.waitGone("✎")
	tm.keys("k", "c", "a", "ctrl+j", "b", "enter")
	tm.waitFor("💬 a", "b")
	// As in herdr-reviewr, e does nothing while a selection is active.
	tm.keys("j", "v", "j", "e", "c")
	if s := tm.waitFor("enter save"); strings.Contains(s, "editor failed") {
		t.Errorf("e with a selection opens the editor. The screen shows:\n%s", s)
	}
	tm.keys("r", "enter", "l")
	tm.waitFor("a.go:4-5  r")
}

// TestSend checks that s pastes the review into the agent and gives it
// the focus. S does the same, and so does s in the question before a quit.
// A comment that the user edits during the send stays.
func TestSend(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	tm := start(t, repo, opts{rex: rex})
	tm.waitFor("send → 1 agent")
	for _, keys := range [][]string{{"s"}, {"S"}, {"q", "s"}} {
		text := "hi " + strings.Join(keys, "")
		comment(tm, text)
		tm.waitGone("sent 1 comment")
		tm.keys(keys...)
		tm.waitFor("sent 1 comment to agent")
		tm.waitGone("💬")
		rex.expectCalls(t, `"payload":{"text":"a.go:4 (removed)\n-\treturn 1\n`+text+`"}`)
	}
	rex.expectCalls(t,
		`block.event {"block_id":"block:pi","event":{"type":"paste",`+
			`"payload":{"text":"a.go:4 (removed)\n-\treturn 1\nhi s"}}}`,
		`session.focus_block {"block_id":"block:pi"}`)
	gate := filepath.Join(rex.dir, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	comment(tm, "hi")
	tm.keys("s")
	rex.expectCalls(t, `"payload":{"text":"a.go:4 (removed)\n-\treturn 1\nhi"}`)
	tm.keys("l", "e", " ")
	tm.typed("edit")
	tm.keys("enter")
	tm.waitFor("a.go:4 (removed)  hi edit")
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tm.keys("esc")
	tm.waitFor("sent 1 comment to agent", "💬 hi edit")
}

// TestSendWithoutAgent checks that s outside Rex tells the user to copy.
func TestSendWithoutAgent(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.keys("s")
	tm.waitFor("not in Rex")
	tm.waitFor("💬 hi")
}

// TestAgentPicker checks that a send with two agents asks which one. As
// in herdr-reviewr, a digit moves the cursor to an agent, and enter sends.
func TestAgentPicker(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	rex.addAgent(t, "block:cc", "cc", "claude-code")
	tm := start(t, repo, opts{rex: rex})
	tm.waitFor("send → 2 agents")
	comment(tm, "hi")
	tm.keys("s")
	tm.waitFor("send to which agent?", "1  agent", "2  cc")
	tm.keys("2")
	tm.waitCursor("2  cc")
	tm.keys("enter")
	tm.waitFor("sent 1 comment to cc")
	rex.expectCalls(t, `block.event {"block_id":"block:cc"`)
}

// TestAgentElsewhere checks that the last-turn scope tells when no agent
// works in the repository.
func TestAgentElsewhere(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	rex.write(t, "process", recorded(t, "process.json", "{{CWD}}", t.TempDir()))
	tm := start(t, repo, opts{rex: rex})
	tm.keys("t")
	tm.waitFor("no agent works here")
}

// TestCopy checks that y and Y copy the review and clear the comments. y
// in the question before a quit does the same.
func TestCopy(t *testing.T) {
	bin := clipboard(t)
	clip := filepath.Join(bin, "clip")
	tm := start(t, newRepo(t), opts{path: bin, env: clipEnv})
	for _, keys := range [][]string{{"y"}, {"Y"}, {"q", "y"}} {
		comment(tm, "hi")
		tm.waitGone("copied 1 comment")
		if err := os.WriteFile(clip, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		tm.keys(keys...)
		tm.waitFor("copied 1 comment")
		tm.waitGone("💬")
		waitFile(t, clip, func(s string) bool { return s == review })
	}
}

// TestMouse checks that a click picks a file, and a triple click copies a
// line. It also checks that a drag on the code copies the lines, and that
// the wheel scrolls. As in herdr-reviewr, the wheel on the file list does
// not pick a file.
func TestMouse(t *testing.T) {
	// The fake pbcopy is slow, as a real tool can be. Its copy takes longer
	// than the time between the clicks of a triple click. The triple click
	// still copies the line.
	slow := `sleep 0.5; cat > "${0%/*}/clip"` + "\n"
	bin := tools(t, map[string]string{"pbcopy": slow, "wl-copy": slow})
	clip := filepath.Join(bin, "clip")
	tm := start(t, newRepo(t), opts{path: bin, env: clipEnv, height: 12})
	tm.click("M long.txt", 1)
	tm.waitFor("⋯ 32 unmodified lines")
	tm.click("M a.go", 1)
	tm.wheel("M a.go", 1)
	tm.click("func A() int {", 3)
	tm.waitFor("copied 14 chars")
	waitFile(t, clip, func(s string) bool { return s == "func A() int {" })
	tm.drag("package a", "func A()")
	waitFile(t, clip, func(s string) bool { return s == "package a\n\nf" })
	tm.drag("A() int", "func A")
	waitFile(t, clip, func(s string) bool { return s == "func A" })

	tm.click("M long.txt", 1)
	tm.keys(":", "2", "0", "enter") // opens the fold
	tm.waitCursor("line 20")
	tm.keys(":", "1", "enter")
	tm.waitFor(" first")
	tm.wheel("line 2", 10)
	tm.waitGone(" first")
}

// TestQuit checks that q with a comment asks first.
func TestQuit(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.keys("Q", "q") // Q does nothing outside the prompt
	tm.waitFor("1 unsent comment will be lost",
		"Q quit (1 pending) · esc cancel · s send 1 · y copy")
	tm.keys("esc")
	tm.waitGone("unsent comment")
	tm.waitFor("💬 hi")
	tm.keys("q", "Q")
	if err := tm.exited(); err != nil {
		t.Errorf("q, Q exits with %v, want no error", err)
	}
}

// TestCommentStays checks that a comment stays when the agent edits the
// file. As in herdr-reviewr, it keeps its line number. When the file
// leaves the changes, the list marks the comment stale.
func TestCommentStays(t *testing.T) {
	repo := newRepo(t)
	tm := start(t, repo, opts{})
	tm.keys("]", "j", "c", "h", "i", "enter") // on x := 2
	tm.waitFor("💬 hi")
	testgit.WriteFile(t, repo, "a.go",
		"package a\n\n// A adds.\n// It is new.\nfunc A() int {\n\tx := 2\n\treturn x\n}\n")
	tm.waitFor("It is new.")
	tm.keys("l")
	tm.waitFor("a.go:4  hi")
	tm.waitGone("stale")
	tm.keys("esc")
	tm.waitGone("Comments (")
	testgit.Git(t, repo, "checkout", "--", "a.go")
	tm.waitFor("Changes (1)")
	tm.keys("l")
	tm.waitUntil(func(s string) bool { return sameLine(s, "a.go:4  hi", "stale") },
		"mark the comment stale")
}

// TestEditorInPane checks that e opens a terminal editor at the line, in
// a new Rex pane.
func TestEditorInPane(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	bin := tools(t, map[string]string{"nvim": ""})
	tm := start(t, repo, opts{rex: rex, path: bin, env: []string{"EDITOR=nvim"}})
	tm.keys("]", "j", "e")
	rex.expectCalls(t, "split -s S -b block:self --cwd "+repo+" -- sh -c nvim +4 '"+
		filepath.Join(repo, "a.go")+"'")
	tm.waitFor("opened a.go")
}

// TestWindowEditor checks that e starts a window editor at the line, and
// that the editor setting picks the editor.
func TestWindowEditor(t *testing.T) {
	bin := tools(t, map[string]string{
		"code": `printf '%s\n' "$@" > "${0%/*}/code"` + "\n",
		"mine": `printf '%s\n' "$@" > "${0%/*}/mine"` + "\n",
	})
	tm := start(t, newRepo(t), opts{path: bin, env: []string{"VISUAL=code --wait"}})
	tm.keys("]", "j", "e")
	waitFile(t, filepath.Join(bin, "code"), func(s string) bool {
		return strings.HasPrefix(s, "--wait\n-g\n") && strings.HasSuffix(s, "a.go:4\n")
	})

	tm = start(t, newRepo(t), opts{path: bin, args: []string{
		"-config",
		`{"editor": "mine --line {line} {file}"}`,
	}})
	tm.keys("]", "j", "e")
	waitFile(t, filepath.Join(bin, "mine"), func(s string) bool {
		return strings.HasPrefix(s, "--line\n4\n") && strings.HasSuffix(s, "a.go\n")
	})
}

// TestNoEditor checks that e tells how to set an editor when none is set.
func TestNoEditor(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("]", "j", "e")
	tm.waitFor(`editor failed: no editor: set "editor" in rexdiff.lua, $EDITOR, or git's core.editor`)
}

// TestCopyHangs checks that a clipboard tool that does not answer fails
// the copy, and rexdiff still takes keys.
func TestCopyHangs(t *testing.T) {
	hang := "exec sleep 60\n"
	bin := tools(t, map[string]string{"pbcopy": hang, "wl-copy": hang})
	tm := start(t, newRepo(t), opts{path: bin, env: clipEnv})
	comment(tm, "hi")
	tm.keys("y")
	tm.waitFor("copy failed")
	tm.waitFor("💬 hi")
	tm.keys("f")
	tm.waitFor("⋯ 32 unmodified lines")
}
