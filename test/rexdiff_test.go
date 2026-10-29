// Package test runs the rexdiff program the way a user does. Each test
// starts the binary in a terminal emulator, types keys, clicks, and reads
// the screen. Rex and the other tools are fake scripts on PATH, but git is
// the real git.
package test

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// buildDev builds rexdiff once and shares the result between all tests,
// so it uses context.Background instead of a test's context.
var buildDev = sync.OnceValues(func() (string, error) {
	return build(context.Background(), filepath.Join(buildDir, "dev"), "")
})

// binary returns the path of the development rexdiff binary, building it
// on the first call.
func binary(t *testing.T) string {
	t.Helper()
	bin, err := buildDev()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// build builds rexdiff into dir with the given version and returns the
// binary's path. When the tests run with the race detector, it builds with
// -race too. The build stops when ctx is done. -buildvcs=false stops a
// build with an empty version from picking up a version from git tags.
func build(ctx context.Context, dir, version string) (string, error) {
	bin := filepath.Join(dir, "rexdiff")
	args := []string{"build", "-buildvcs=false", "-o", bin, "-ldflags", "-X main.version=" + version}
	if raceOn {
		args = append(args, "-race")
	}
	cmd := exec.CommandContext(ctx, "go", append(args, "../cmd/rexdiff")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, nil
}

// userEnv lists the user's environment variables that change how rexdiff
// behaves. Each run sets them to empty values.
var userEnv = []string{
	"REX_SESSION", "REX_BLOCK", "REX_BIN_DIR", "REX_CONFIG",
	"REXDIFF_AGENT", "REXDIFF_LOG", "REXDIFF_RESUME", "EDITOR", "VISUAL",
}

// cleanEnv returns the environment for running rexdiff. It drops the
// user's GIT_ variables, empties the variables in userEnv, sets HOME to
// homeDir, and adds the git settings from testgit.Env.
func cleanEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
	for _, k := range userEnv {
		env = append(env, k+"=")
	}
	env = append(env, "HOME="+homeDir, "XDG_CONFIG_HOME=")
	return append(env, testgit.Env()...)
}

// opts holds the options for running rexdiff.
type opts struct {
	args   []string // the flags; start appends the repository path
	bin    string   // the rexdiff binary to run, or "" for the one from binary
	width  uint16   // 0 means 160
	height uint16   // 0 means 40
	rex    *fakeRex // nil runs rexdiff outside Rex
	path   string   // a directory of fake tools to put on PATH, or ""
	env    []string // extra environment variables
}

// term is a rexdiff process running in a terminal emulator.
type term struct {
	t    *testing.T
	emu  *vt.Emulator
	mu   sync.Mutex   // guards emu
	pty  *os.File     // the terminal of rexdiff
	wait func() error // waits for rexdiff to exit; later calls return the same error
	// changed receives a value after each write to the screen. Its buffer
	// holds one value, so a write that happens before a wait is not lost.
	changed chan struct{}
	done    chan struct{} // closed when rexdiff's output ends
}

// waitTime is how long a test waits for the screen or a file to change
// before it fails.
const waitTime = 15 * time.Second

// start runs rexdiff on repo in a new terminal and waits for the first
// screen. rexdiff is stopped when the test ends.
func start(t *testing.T, repo string, o opts) *term {
	t.Helper()
	w, h := cmp.Or(o.width, 160), cmp.Or(o.height, 40)
	bin := o.bin
	if bin == "" {
		bin = binary(t)
	}
	cmd := exec.CommandContext(t.Context(), bin, append(o.args, repo)...)
	cmd.Dir = repo
	// SIGTERM, unlike a kill, lets rexdiff remove its files before it
	// exits.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	var dirs []string
	if o.path != "" {
		dirs = append(dirs, o.path)
	}
	// The PATH from ToolEnv includes git but none of the user's other
	// tools.
	cmd.Env = append(cleanEnv(), "TERM=xterm-256color")
	cmd.Env = append(cmd.Env, testgit.ToolEnv(t, dirs...)...)
	if o.rex != nil {
		cmd.Env = append(cmd.Env, "REX_SESSION=S", "REX_BLOCK=block:self",
			"REX_BIN_DIR="+o.rex.dir)
	}
	cmd.Env = append(cmd.Env, o.env...)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: w, Rows: h})
	if err != nil {
		t.Fatal(err)
	}
	tm := &term{
		t:       t,
		emu:     vt.NewEmulator(int(w), int(h)),
		pty:     f,
		wait:    sync.OnceValue(cmd.Wait),
		changed: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(tm.done)
		buf := make([]byte, 32<<10)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				tm.mu.Lock()
				_, _ = tm.emu.Write(buf[:n])
				tm.mu.Unlock()
				select {
				case tm.changed <- struct{}{}:
				default: // a change is already pending
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// The emulator produces rexdiff's input: the test's key presses and
	// mouse events, and replies to terminal queries, such as colour
	// queries.
	input := make(chan struct{})
	go func() {
		defer close(input)
		_, _ = io.Copy(f, tm.emu)
	}()
	t.Cleanup(func() {
		_ = tm.wait() // t.Context is done by now, so cmd.Cancel stops rexdiff
		_ = f.Close()
		<-tm.done
		// Close the input pipe, not the emulator, to end the copy, because
		// closing the emulator races with its Read.
		_ = tm.emu.InputPipe().(io.Closer).Close()
		<-input
	})
	tm.waitFor("1 Changes")
	return tm
}

// resize changes the size of the terminal, as a user does who resizes
// the window. It clears the screen, so that what shows next is what
// rexdiff draws at the new size.
func (tm *term) resize(w, h int) {
	tm.t.Helper()
	tm.mu.Lock()
	tm.emu.Resize(w, h)
	_, _ = tm.emu.Write([]byte("\x1b[2J"))
	tm.mu.Unlock()
	if err := pty.Setsize(tm.pty, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}); err != nil { //nolint:gosec // the sizes of the tests fit
		tm.t.Fatal(err)
	}
}

// screen returns the text on the screen.
func (tm *term) screen() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.emu.String()
}

// keys types each key in turn. A key is either a name from keyCodes or
// literal text. An esc followed by another key can be read as a single
// key, so esc must come last; wait for the screen before sending more
// keys.
func (tm *term) keys(keys ...string) {
	tm.t.Helper()
	if i := slices.Index(keys, "esc"); i >= 0 && i != len(keys)-1 {
		tm.t.Fatalf("keys %q: esc must be the last key", keys)
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, k := range keys {
		if code, ok := keyCodes[k]; ok {
			tm.emu.SendKey(code)
		} else {
			tm.emu.SendText(k)
		}
	}
}

// paste pastes text as a terminal does, in bracketed paste mode.
func (tm *term) paste(text string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.emu.Paste(text)
}

// typed types text one character at a time.
func (tm *term) typed(text string) {
	tm.t.Helper()
	tm.keys(strings.Split(text, "")...)
}

// keyCodes maps key names to key presses.
var keyCodes = map[string]vt.KeyPressEvent{
	"enter":  {Code: vt.KeyEnter},
	"esc":    {Code: vt.KeyEscape},
	"tab":    {Code: vt.KeyTab},
	"right":  {Code: vt.KeyRight},
	"left":   {Code: vt.KeyLeft},
	"pgdown": {Code: vt.KeyPgDown},
	"pgup":   {Code: vt.KeyPgUp},
	"ctrl+d": {Code: 'd', Mod: vt.ModCtrl},
	"ctrl+u": {Code: 'u', Mod: vt.ModCtrl},
	"ctrl+f": {Code: 'f', Mod: vt.ModCtrl},
	"ctrl+j": {Code: 'j', Mod: vt.ModCtrl},
	"up":     {Code: vt.KeyUp},
	"down":   {Code: vt.KeyDown},
	"bs":     {Code: vt.KeyBackspace},
}

// at waits until the screen shows text and returns its column and line,
// counting from 0.
func (tm *term) at(text string) (x, y int) {
	tm.t.Helper()
	x, y, _ = position(tm.waitFor(text), text)
	return x, y
}

// position returns the column and line of the first occurrence of text in
// s, counting from 0. ok is false if s does not contain text.
func position(s, text string) (x, y int, ok bool) {
	for i, l := range strings.Split(s, "\n") {
		if before, _, found := strings.Cut(l, text); found {
			return utf8.RuneCountInString(before), i, true
		}
	}
	return 0, 0, false
}

// mouse sends the mouse event m to rexdiff, encoded as a terminal would
// encode it.
func (tm *term) mouse(m vt.Mouse) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.emu.SendMouse(m)
}

// click clicks on text on the screen the given number of times in a row.
func (tm *term) click(text string, times int) {
	tm.t.Helper()
	x, y := tm.at(text)
	for range times {
		tm.mouse(vt.MouseClick{X: x, Y: y, Button: vt.MouseLeft})
		tm.mouse(vt.MouseRelease{X: x, Y: y, Button: vt.MouseLeft})
	}
}

// drag drags the mouse from the start of the text from to the start of
// the text to, holding the left button.
func (tm *term) drag(from, to string) {
	tm.t.Helper()
	x0, y0 := tm.at(from)
	x1, y1 := tm.at(to)
	tm.mouse(vt.MouseClick{X: x0, Y: y0, Button: vt.MouseLeft})
	tm.mouse(vt.MouseMotion{X: x1, Y: y1, Button: vt.MouseLeft})
	tm.mouse(vt.MouseRelease{X: x1, Y: y1, Button: vt.MouseLeft})
}

// wheel turns the mouse wheel down n times over text.
func (tm *term) wheel(text string, n int) {
	tm.t.Helper()
	x, y := tm.at(text)
	for range n {
		tm.mouse(vt.MouseWheel{X: x, Y: y, Button: vt.MouseWheelDown})
	}
}

// waitFor waits until the screen shows all of texts, and returns the
// screen.
func (tm *term) waitFor(texts ...string) string {
	tm.t.Helper()
	return tm.waitUntil(func(s string) bool { return hasAll(s, texts...) },
		"show "+strings.Join(texts, ", "))
}

// waitGone waits until the screen shows none of texts.
func (tm *term) waitGone(texts ...string) string {
	tm.t.Helper()
	return tm.waitUntil(func(s string) bool { return !hasAny(s, texts...) },
		"stop showing "+strings.Join(texts, ", "))
}

// hasAll reports whether s contains all of texts.
func hasAll(s string, texts ...string) bool {
	return !slices.ContainsFunc(texts, func(t string) bool { return !strings.Contains(s, t) })
}

// hasAny reports whether s contains at least one of texts.
func hasAny(s string, texts ...string) bool {
	return slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(s, t) })
}

// sameLine reports whether a single line of s contains all of texts.
func sameLine(s string, texts ...string) bool {
	for l := range strings.Lines(s) {
		if hasAll(l, texts...) {
			return true
		}
	}
	return false
}

// waitCursor waits until the line with the cursor marker ▶ also
// shows text.
func (tm *term) waitCursor(text string) {
	tm.t.Helper()
	tm.waitUntil(func(s string) bool { return sameLine(s, "▶", text) },
		"put the cursor on "+text)
}

// waitUntil waits until ok reports true for the screen, and returns the
// screen. It checks again after every write to the screen. what describes
// the condition in the failure message.
func (tm *term) waitUntil(ok func(string) bool, what string) string {
	tm.t.Helper()
	timeout := time.NewTimer(waitTime)
	defer timeout.Stop()
	for {
		s := tm.screen()
		if ok(s) {
			return s
		}
		select {
		case <-tm.changed:
		case <-tm.done:
			// The output has ended, but the last write may have arrived
			// after the check above, so check once more.
			if s = tm.screen(); ok(s) {
				return s
			}
			tm.t.Fatalf("rexdiff stops, and the screen does not %s. It shows:\n%s", what, s)
		case <-timeout.C:
			tm.t.Fatalf("the screen does not %s. It shows:\n%s", what, s)
		}
	}
}

// pollInterval is how often poll checks its condition.
const pollInterval = 20 * time.Millisecond

// poll calls ok until it reports true. Use it for files that the fake
// tools write and for other programs, which send no notification when
// they change. If ok is still false after waitTime, poll fails the test
// with the message from why.
func poll(t *testing.T, ok func() bool, why func() string) {
	t.Helper()
	timeout := time.NewTimer(waitTime)
	defer timeout.Stop()
	tick := time.Tick(pollInterval)
	for !ok() {
		select {
		case <-tick:
		case <-timeout.C:
			t.Fatal(why())
		}
	}
}

// waitFile waits until content reports true for the content of the file
// at path, and returns that content. want describes the expected content
// for the failure message.
func waitFile(t *testing.T, path, want string, content func(string) bool) string {
	t.Helper()
	var s string
	poll(t, func() bool {
		b, _ := os.ReadFile(path)
		s = string(b)
		return content(s)
	}, func() string { return path + " has " + strconv.Quote(s) + ", want " + want })
	return s
}

// exited waits for rexdiff to exit and returns its exit error.
func (tm *term) exited() error {
	tm.t.Helper()
	errc := make(chan error, 1)
	go func() { errc <- tm.wait() }()
	select {
	case err := <-errc:
		return err
	case <-time.After(waitTime):
		tm.t.Fatalf("rexdiff does not exit. The screen shows:\n%s", tm.screen())
		return nil
	}
}
