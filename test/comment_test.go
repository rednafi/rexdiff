package test

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/rednafi/rexdiff/internal/testgit"
)

const review = "a.go:4 (removed)\n-\treturn 1\nhi"

func comment(tm *term, text string) {
	tm.t.Helper()
	tm.keys("]", "c")
	tm.typed(text)
	tm.keys("enter")
	tm.waitFor("💬 " + text)
}

// TestCommentList also checks that saving empty text keeps the old text.
func TestCommentList(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.waitFor("comment added", "●1")
	tm.keys("k")
	tm.waitCursor("func A")
	tm.keys("n") // n stays in the current file
	tm.waitCursor("return 1")
	tm.keys("j", "j")
	tm.waitCursor("return x")
	tm.keys("N")
	tm.waitCursor("return 1")
	tm.keys("c", "z", "enter")
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
	// The flash message hides the draft badge, so wait for the key hints.
	tm.keys("e")
	tm.waitFor("enter save")
	tm.keys(slices.Repeat([]string{"bs"}, len("hi more"))...)
	tm.keys("enter")
	tm.waitGone("enter save")
	tm.waitFor("a.go:4 (removed)  hi more")
	tm.keys("d", "d") // deleting the last comment closes the list
	tm.waitGone("Comments (", "💬")
}

func TestCommentListPages(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	testgit.WriteFile(t, repo, "u.txt", numbered("line", 1, 30))
	testgit.Git(t, repo, "add", ".")
	testgit.Git(t, repo, "commit", "-qm", "first")
	tm := start(t, repo, opts{})
	tm.keys("2")
	tm.waitCursor("line 1")
	for i := 1; i <= 20; i++ {
		tm.keys("c")
		tm.typed(fmt.Sprintf("note %d", i))
		tm.keys("enter", "j")
	}
	tm.waitFor("20 unsent comments")
	tm.keys("l")
	tm.waitFor("Comments (20)")
	for _, step := range []struct{ key, want string }{
		{"pgdown", "u.txt:16  note 16"},
		{"ctrl+u", "u.txt:8  note 8"},
		{"ctrl+d", "u.txt:16  note 16"},
		{"pgup", "u.txt:1  note 1"},
	} {
		tm.keys(step.key)
		tm.waitCursor(step.want)
	}
}

func TestCommentDrag(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.drag(" 1  1 ", " 3  3 ")
	tm.waitFor("✎ a.go:1-3")
	tm.keys("esc")
	tm.waitGone("✎ a.go:1-3")
}

func TestCommentBox(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	// The flash message after adding a comment hides the draft badge.
	tm.keys("]", "j", "c", "x")
	tm.waitFor("✎ a.go:4 ")
	tm.keys("esc")
	tm.waitGone("✎")
	tm.keys("k", "c", "a", "ctrl+j", "b", "enter")
	tm.waitFor("💬 a", "b")
	tm.keys("j", "v", "j", "e", "c")
	if s := tm.waitFor("enter save"); strings.Contains(s, "editor failed") {
		t.Errorf("e with a selection opens the editor. The screen shows:\n%s", s)
	}
	tm.keys("r", "enter", "l")
	tm.waitFor("a.go:4-5  r")
}

func TestCommentBoxAtBottom(t *testing.T) {
	tm := start(t, newRepo(t), opts{height: 16})
	tm.keys("f", ":", "$", "enter")
	tm.waitCursor("last")
	tm.keys("c", "a", "ctrl+j", "b", "ctrl+j", "d", "enter")
	tm.waitFor("💬 a")
	tm.keys("c", "x", "ctrl+j", "y", "ctrl+j", "z")
	tm.waitFor("💬 a", "│ x", "│ y", "│ z", "enter save")
}

// TestSend also checks that a comment edited during the paste is kept.
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

func TestSendWithoutAgent(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.keys("s")
	tm.waitFor("not in Rex")
	tm.waitFor("💬 hi")
}

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

func TestAgentElsewhere(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	rex.write(t, "process", recorded(t, "process.json", "{{CWD}}", t.TempDir()))
	tm := start(t, repo, opts{rex: rex})
	tm.keys("t")
	tm.waitFor("no agent works here")
}

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
		waitFile(t, clip, "the review", func(s string) bool { return s == review })
	}
}

func TestMouse(t *testing.T) {
	// The fake pbcopy takes longer than the gap between the clicks of a
	// triple click. The triple click must still copy the line.
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
	waitFile(t, clip, `"func A() int {"`, func(s string) bool { return s == "func A() int {" })
	tm.drag("package a", "func A()")
	waitFile(t, clip, `"package a\n\nf"`, func(s string) bool { return s == "package a\n\nf" })
	tm.drag("A() int", "func A")
	waitFile(t, clip, `"func A"`, func(s string) bool { return s == "func A" })

	tm.click("M long.txt", 1)
	tm.keys(":", "2", "0", "enter") // going to line 20 expands the fold
	tm.waitCursor("line 20")
	tm.keys(":", "1", "enter")
	tm.waitFor(" first")
	tm.wheel("line 2", 10)
	tm.waitGone(" first")
}

func TestQuit(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	comment(tm, "hi")
	tm.keys("Q", "q")
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

func TestCommentStays(t *testing.T) {
	repo := newRepo(t)
	tm := start(t, repo, opts{})
	tm.keys("]", "j", "c", "h", "i", "enter") // comment on x := 2
	tm.waitFor("💬 hi")
	testgit.WriteFile(t, repo, "a.go",
		"package a\n\nfunc A() int {\n\tx := 2\n\treturn x + 1\n}\n")
	tm.waitFor("return x + 1", "💬 hi")
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

// TestCommentStaleMoved checks that a comment does not follow its code
// when lines are added above it. The review keeps the original code.
func TestCommentStaleMoved(t *testing.T) {
	bin := clipboard(t)
	clip := filepath.Join(bin, "clip")
	repo := newRepo(t)
	tm := start(t, repo, opts{path: bin, env: clipEnv})
	tm.keys("]", "j", "c", "h", "i", "enter") // comment on x := 2, line 4
	tm.waitFor("💬 hi")
	testgit.WriteFile(t, repo, "a.go",
		"package a\n\n// A adds.\n// It is new.\nfunc A() int {\n\tx := 2\n\treturn x\n}\n")
	tm.waitFor("It is new.", "💬 stale hi")
	tm.keys("l")
	tm.waitUntil(func(s string) bool { return sameLine(s, "a.go:4  hi", "stale") },
		"mark the comment stale in the list")
	tm.keys("esc")
	tm.waitGone("Comments (")
	tm.keys("y")
	tm.waitFor("copied 1 comment")
	want := "a.go:4 (stale)\n+\tx := 2\nhi"
	waitFile(t, clip, "the review", func(s string) bool { return s == want })
}

func TestCommentStaleGone(t *testing.T) {
	bin := clipboard(t)
	clip := filepath.Join(bin, "clip")
	repo := newRepo(t)
	tm := start(t, repo, opts{path: bin, env: clipEnv})
	tm.keys("]", "j", "j", "c", "h", "i", "enter") // comment on return x, line 5
	tm.waitFor("💬 hi")
	testgit.WriteFile(t, repo, "a.go", "package a\n\nvar A = 2\n")
	tm.waitGone("💬")
	tm.waitFor("var A = 2")
	tm.keys(":")
	tm.typed("1")
	tm.keys("enter")
	tm.waitCursor("package a")
	tm.keys("l")
	tm.waitUntil(func(s string) bool { return sameLine(s, "a.go:5  hi", "stale") },
		"mark the comment stale in the list")
	tm.keys("enter")
	tm.waitGone("Comments (")
	tm.waitCursor("var A = 2")
	tm.keys("y")
	tm.waitFor("copied 1 comment")
	want := "a.go:5 (stale)\n+\treturn x\nhi"
	waitFile(t, clip, "the review", func(s string) bool { return s == want })
}

// TestCommentStaleWhileOpen checks that a comment keeps the code from when
// the box opened, if the file changes while the box is open.
func TestCommentStaleWhileOpen(t *testing.T) {
	bin := clipboard(t)
	clip := filepath.Join(bin, "clip")
	repo := newRepo(t)
	log := filepath.Join(t.TempDir(), "log")
	tm := start(t, repo, opts{path: bin, env: clipEnv, args: []string{"-log", log}})
	tm.keys("]", "j", "c")
	tm.waitFor("enter save")
	testgit.WriteFile(t, repo, "a.go",
		"package a\n\nfunc A() int {\n\ty := 3\n\treturn y\n}\n")
	// Type once rexdiff has seen the change, while the box is open.
	waitFile(t, log, "a watch message with files=true after the key c", func(s string) bool {
		_, after, _ := strings.Cut(s, "key=c ")
		return sameLine(after, "msg=watch", "files=true")
	})
	tm.typed("hi")
	tm.keys("enter")
	tm.waitFor("y := 3", "💬 stale hi")
	tm.keys("y")
	tm.waitFor("copied 1 comment")
	want := "a.go:4 (stale)\n+\tx := 2\nhi"
	waitFile(t, clip, "the review", func(s string) bool { return s == want })
}

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

func TestWindowEditor(t *testing.T) {
	bin := tools(t, map[string]string{
		"code": `printf '%s\n' "$@" > "${0%/*}/code"` + "\n",
		"mine": `printf '%s\n' "$@" > "${0%/*}/mine"` + "\n",
	})
	tm := start(t, newRepo(t), opts{path: bin, env: []string{"VISUAL=code --wait"}})
	tm.keys("]", "j", "e")
	waitFile(t, filepath.Join(bin, "code"), "arguments that start with --wait -g and end with a.go:4", func(s string) bool {
		return strings.HasPrefix(s, "--wait\n-g\n") && strings.HasSuffix(s, "a.go:4\n")
	})

	tm = start(t, newRepo(t), opts{path: bin, args: []string{
		"-config",
		`{"editor": "mine --line {line} {file}"}`,
	}})
	tm.keys("]", "j", "e")
	waitFile(t, filepath.Join(bin, "mine"), "arguments that start with --line 4 and end with a.go", func(s string) bool {
		return strings.HasPrefix(s, "--line\n4\n") && strings.HasSuffix(s, "a.go\n")
	})
}

func TestNoEditor(t *testing.T) {
	tm := start(t, newRepo(t), opts{})
	tm.keys("]", "j", "e")
	tm.waitFor(`editor failed: no editor: set "editor" in rexdiff.lua, $EDITOR, or git's core.editor`)
}

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

// TestCommentPaste checks that a pasted \r\n or lone \r is one line break.
func TestCommentPaste(t *testing.T) {
	tm := start(t, newRepo(t), opts{width: 100, height: 20})
	tm.keys("]", "c")
	tm.waitFor("enter save")
	tm.paste("first pasted\r\nsecond pasted\rthird pasted")
	s := tm.waitFor("third pasted")
	first, second, third := rowY(s, "first pasted"), rowY(s, "second pasted"), rowY(s, "third pasted")
	if second != first+1 || third != second+1 {
		t.Errorf("the pasted lines are on lines %d, %d and %d, want one after another. The screen shows:\n%s",
			first, second, third, s)
	}
	tm.keys("esc")
	tm.waitGone("enter save")

	var long []string
	for i := 1; i <= 20; i++ {
		long = append(long, fmt.Sprintf("pasted %02d %s", i, strings.Repeat("x", 90)))
	}
	tm.keys("c")
	tm.waitFor("enter save")
	tm.paste(strings.Join(long, "\n"))
	tm.waitFor("pasted 20")
}

func TestCommentStaleInFile(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	testgit.WriteFile(t, repo, "x.txt", "one\ntwo\n")
	testgit.Git(t, repo, "add", ".")
	testgit.Git(t, repo, "commit", "-qm", "first")
	tm := start(t, repo, opts{})
	tm.keys("2")
	tm.waitCursor("one")
	tm.keys("c")
	tm.typed("hi")
	tm.keys("enter")
	tm.waitFor("💬 hi")
	testgit.WriteFile(t, repo, "x.txt", "zero\none\ntwo\n")
	tm.waitFor("zero", "💬 stale hi")
	tm.keys("l")
	tm.waitUntil(func(s string) bool { return sameLine(s, "x.txt:1  hi", "stale") },
		"mark the comment stale in the list")
	tm.keys("esc")
	tm.waitGone("Comments (")
	testgit.WriteFile(t, repo, "x.txt", "one\ntwo\n")
	tm.waitGone("zero", "stale")
	tm.waitFor("💬 hi")
}

// TestCommentStaleInFileFromChanges adds the line while the Changes tab
// shows.
func TestCommentStaleInFileFromChanges(t *testing.T) {
	bin := clipboard(t)
	clip := filepath.Join(bin, "clip")
	repo := filepath.Join(t.TempDir(), "repo")
	testgit.InitRepo(t, repo)
	testgit.WriteFile(t, repo, "u.txt", numbered("line", 1, 9))
	testgit.Git(t, repo, "add", ".")
	testgit.Git(t, repo, "commit", "-qm", "first")
	tm := start(t, repo, opts{path: bin, env: clipEnv})
	tm.keys("2")
	tm.waitCursor("line 1")
	tm.keys(":")
	tm.typed("5")
	tm.keys("enter")
	tm.waitCursor("line 5")
	tm.keys("c")
	tm.typed("hi")
	tm.keys("enter")
	tm.waitFor("💬 hi")
	tm.keys("1")
	tm.waitFor("no changes")
	testgit.WriteFile(t, repo, "u.txt", "line 0\n"+numbered("line", 1, 9))
	tm.waitFor("Changes (1)")
	tm.keys("l")
	tm.waitUntil(func(s string) bool { return sameLine(s, "u.txt:5  hi", "stale") },
		"mark the comment stale in the list")
	tm.keys("esc")
	tm.waitGone("Comments (")
	tm.keys("y")
	tm.waitFor("copied 1 comment")
	waitFile(t, clip, "the review with the comment marked stale", func(s string) bool {
		return strings.HasPrefix(s, "u.txt:5 (stale)\n")
	})
}

// TestSendLong checks that a long review goes in parts. Rex escapes <, &
// and >, so 30,000 bytes become 180,000.
func TestSendLong(t *testing.T) {
	repo := newRepo(t)
	rex := newRex(t, repo)
	tm := start(t, repo, opts{rex: rex})
	tm.waitFor("send → 1 agent")
	long := strings.Repeat("<&>", 10_000)
	tm.keys("]", "c")
	tm.paste(long)
	tm.keys("enter")
	tm.waitFor("comment added")
	tm.keys("s")
	tm.waitFor("sent 1 comment to agent")
	var text strings.Builder
	parts := 0
	for line := range strings.Lines(rex.calls(t)) {
		args, ok := strings.CutPrefix(line, "api call -s S block.event ")
		if !ok {
			continue
		}
		var call struct {
			Event struct {
				Payload struct {
					Text string `json:"text"`
				} `json:"payload"`
			} `json:"event"`
		}
		if err := json.Unmarshal([]byte(args), &call); err != nil {
			t.Fatal(err)
		}
		text.WriteString(call.Event.Payload.Text)
		parts++
	}
	if want := "a.go:4 (removed)\n-\treturn 1\n" + long; text.String() != want {
		t.Errorf("the pasted parts join to %d bytes that differ from the %d of the review",
			text.Len(), len(want))
	}
	if parts < 4 {
		t.Errorf("the review was pasted in %d parts, want at least 4", parts)
	}
}
