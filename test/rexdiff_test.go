// Package test runs the rexdiff program as a user does. Each test starts
// the binary in a terminal, types keys, clicks, and reads the screen. A
// fake rex stands in for Rex, and the other tools are fakes on PATH. git
// is real. The tests run one at a time, because each runs a program in a
// terminal.
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

// buildDev builds rexdiff once. The build serves all the tests, as
// TestMain does, so it has the context of no test.
var buildDev = sync.OnceValues(func() (string, error) {
	return build(context.Background(), filepath.Join(buildDir, "dev"), "")
})

// binary returns the path of a rexdiff that the package builds once.
func binary(t *testing.T) string {
	t.Helper()
	bin, err := buildDev()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// build builds rexdiff with the version into dir, and returns its path.
// A run with the race detector builds a rexdiff with it. The build stops
// when ctx is done.
func build(ctx context.Context, dir, version string) (string, error) {
	bin := filepath.Join(dir, "rexdiff")
	args := []string{"build", "-o", bin, "-ldflags", "-X main.version=" + version}
	if raceOn {
		args = append(args, "-race")
	}
	cmd := exec.CommandContext(ctx, "go", append(args, "../cmd/rexdiff")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return bin, nil
}

// userEnv are the variables of the user that change what rexdiff does.
// Each run starts with them empty.
var userEnv = []string{
	"REX_SESSION", "REX_BLOCK", "REX_BIN_DIR", "REX_CONFIG",
	"REXDIFF_AGENT", "REXDIFF_LOG", "REXDIFF_RESUME", "EDITOR", "VISUAL",
}

// cleanEnv returns the environment of the process for a run of rexdiff.
// It has no git variable of the user, and the variables of userEnv are
// empty. HOME is homeDir, so no file of the user applies. Git gets the
// variables of testgit.Env.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	for _, k := range userEnv {
		env = append(env, k+"=")
	}
	env = append(env, "HOME="+homeDir, "XDG_CONFIG_HOME=")
	return append(env, testgit.Env()...)
}

// opts are the options of a rexdiff run.
type opts struct {
	args   []string // the flags. start adds the repo.
	bin    string   // the rexdiff to run, or "" for the one of binary
	width  uint16   // 0 means 160
	height uint16   // 0 means 40
	rex    *fakeRex // nil runs rexdiff outside Rex
	path   string   // a directory of fake tools, or ""
	env    []string // more variables
}

// term is a rexdiff that runs in a terminal.
type term struct {
	t    *testing.T
	emu  *vt.Emulator
	mu   sync.Mutex   // guards emu
	wait func() error // waits for rexdiff once, and keeps its error
	// changed gets a value after each write to the screen. It holds one
	// value, so a write that comes before a wait is not lost.
	changed chan struct{}
	done    chan struct{} // closed when the screen gets no more output
}

// waitTime is how long a test waits for the screen or for a file.
const waitTime = 15 * time.Second

// start runs rexdiff on repo in a new terminal. The test stops it at the
// end.
func start(t *testing.T, repo string, o opts) *term {
	t.Helper()
	w, h := cmp.Or(o.width, 160), cmp.Or(o.height, 40)
	bin := o.bin
	if bin == "" {
		bin = binary(t)
	}
	cmd := exec.CommandContext(t.Context(), bin, append(o.args, repo)...)
	cmd.Dir = repo
	// A stop lets rexdiff remove its files before it exits.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	path := "/usr/bin:/bin:/usr/sbin:/sbin"
	if git, err := exec.LookPath("git"); err == nil {
		path = filepath.Dir(git) + string(os.PathListSeparator) + path
	}
	if o.path != "" {
		path = o.path + string(os.PathListSeparator) + path
	}
	cmd.Env = append(cleanEnv(), "PATH="+path, "TERM=xterm-256color")
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
				default: // the wait has a value that it does not take yet
				}
			}
			if err != nil {
				return
			}
		}
	}()
	// The input of rexdiff comes from the emulator, as from a terminal. It
	// has the keys and the mouse of the test. It also has the answers to
	// the queries of rexdiff, for example for the colours.
	input := make(chan struct{})
	go func() {
		defer close(input)
		_, _ = io.Copy(f, tm.emu)
	}()
	t.Cleanup(func() {
		_ = tm.wait() // t.Context is done, so Cancel stops rexdiff
		_ = f.Close()
		<-tm.done
		// Close of the pipe, not of the emulator, ends the copy. The Close
		// of the emulator races with its Read.
		_ = tm.emu.InputPipe().(io.Closer).Close()
		<-input
	})
	tm.waitFor("1 Changes")
	return tm
}

// screen returns the text on the screen.
func (tm *term) screen() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.emu.String()
}

// keys types each key. A key is a name in keyCodes, or else a text. As
// in a terminal, an esc and the key after it can join into one key, so esc
// must be the last key. Wait for the screen after it, as a user does.
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

// typed types each character of text.
func (tm *term) typed(text string) {
	tm.t.Helper()
	tm.keys(strings.Split(text, "")...)
}

// keyCodes are the named keys.
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

// at returns the column and the line of text on the screen, from 0.
func (tm *term) at(text string) (x, y int) {
	tm.t.Helper()
	x, y, _ = position(tm.waitFor(text), text)
	return x, y
}

// position returns the column and the line of the first text in s, from
// 0. ok is false if s does not have text.
func position(s, text string) (x, y int, ok bool) {
	for i, l := range strings.Split(s, "\n") {
		if before, _, found := strings.Cut(l, text); found {
			return utf8.RuneCountInString(before), i, true
		}
	}
	return 0, 0, false
}

// mouse sends the mouse event m, as the terminal encodes it.
func (tm *term) mouse(m vt.Mouse) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.emu.SendMouse(m)
}

// click clicks text on the screen the given number of times in a row.
func (tm *term) click(text string, times int) {
	tm.t.Helper()
	x, y := tm.at(text)
	for range times {
		tm.mouse(vt.MouseClick{X: x, Y: y, Button: vt.MouseLeft})
		tm.mouse(vt.MouseRelease{X: x, Y: y, Button: vt.MouseLeft})
	}
}

// drag drags the mouse from the start of text from to the start of text
// to.
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

// waitFor waits until the screen shows each of texts, and returns the
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
		"not show "+strings.Join(texts, ", "))
}

// hasAll reports whether s has each of texts.
func hasAll(s string, texts ...string) bool {
	return !slices.ContainsFunc(texts, func(t string) bool { return !strings.Contains(s, t) })
}

// hasAny reports whether s has one of texts.
func hasAny(s string, texts ...string) bool {
	return slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(s, t) })
}

// sameLine reports whether a line of s has each of texts.
func sameLine(s string, texts ...string) bool {
	for l := range strings.Lines(s) {
		if hasAll(l, texts...) {
			return true
		}
	}
	return false
}

// waitCursor waits until the line of the cursor shows text.
func (tm *term) waitCursor(text string) {
	tm.t.Helper()
	tm.waitUntil(func(s string) bool { return sameLine(s, "▶", text) },
		"put the cursor on "+text)
}

// waitUntil waits until ok is true for the screen, and returns the screen.
// It checks the screen after each write to it.
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
			// rexdiff writes no more. The last write can come after the
			// check.
			if s = tm.screen(); ok(s) {
				return s
			}
			tm.t.Fatalf("rexdiff stops, and the screen does not %s. It shows:\n%s", what, s)
		case <-timeout.C:
			tm.t.Fatalf("the screen does not %s. It shows:\n%s", what, s)
		}
	}
}

// pollTime is the time between two checks of poll.
const pollTime = 20 * time.Millisecond

// poll checks again and again until ok is true. It is for the files that
// the fake tools write, and for other programs, because they give no
// event. It fails the test with the text of why after waitTime.
func poll(t *testing.T, ok func() bool, why func() string) {
	t.Helper()
	timeout := time.NewTimer(waitTime)
	defer timeout.Stop()
	tick := time.NewTicker(pollTime)
	defer tick.Stop()
	for !ok() {
		select {
		case <-tick.C:
		case <-timeout.C:
			t.Fatal(why())
		}
	}
}

// waitFile waits until the file at path has content, and returns it.
func waitFile(t *testing.T, path string, content func(string) bool) string {
	t.Helper()
	var s string
	poll(t, func() bool {
		b, _ := os.ReadFile(path)
		s = string(b)
		return content(s)
	}, func() string { return path + " has " + strconv.Quote(s) })
	return s
}

// exited waits until rexdiff exits and returns its error.
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
