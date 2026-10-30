// Package test runs the rexdiff binary in a terminal emulator, as a user
// does. Rex and the other tools are fake scripts on PATH. git is real.
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

// buildDev is shared between all tests, so it uses context.Background.
var buildDev = sync.OnceValues(func() (string, error) {
	return build(context.Background(), filepath.Join(buildDir, "dev"), "")
})

func binary(t *testing.T) string {
	t.Helper()
	bin, err := buildDev()
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// build uses -race when the tests do. -buildvcs=false stops a build with
// an empty version from taking a version from git tags.
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

var userEnv = []string{
	"REX_SESSION", "REX_BLOCK", "REX_BIN_DIR", "REX_CONFIG",
	"REXDIFF_AGENT", "REXDIFF_LOG", "REXDIFF_RESUME", "EDITOR", "VISUAL",
}

func cleanEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_") })
	for _, k := range userEnv {
		env = append(env, k+"=")
	}
	env = append(env, "HOME="+homeDir, "XDG_CONFIG_HOME=")
	return append(env, testgit.Env()...)
}

type opts struct {
	args   []string // start appends the repo path
	bin    string   // "" means the one from binary
	width  uint16   // 0 means 160
	height uint16   // 0 means 40
	rex    *fakeRex // nil runs rexdiff outside Rex
	path   string
	env    []string
}

type term struct {
	t    *testing.T
	emu  *vt.Emulator
	mu   sync.Mutex // guards emu
	pty  *os.File
	wait func() error // later calls return the same error
	// changed receives a value after each write to the screen. Its buffer
	// holds one value, so a write before a wait is not lost.
	changed chan struct{}
	done    chan struct{}
}

const waitTime = 15 * time.Second

func start(t *testing.T, repo string, o opts) *term {
	t.Helper()
	w, h := cmp.Or(o.width, 160), cmp.Or(o.height, 40)
	bin := o.bin
	if bin == "" {
		bin = binary(t)
	}
	cmd := exec.CommandContext(t.Context(), bin, append(o.args, repo)...)
	cmd.Dir = repo
	// SIGTERM, unlike a kill, lets rexdiff remove its files.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	var dirs []string
	if o.path != "" {
		dirs = append(dirs, o.path)
	}
	// The PATH from ToolEnv has git but none of the user's other tools.
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
	// The emulator also replies to terminal queries, such as colour queries.
	input := make(chan struct{})
	go func() {
		defer close(input)
		_, _ = io.Copy(f, tm.emu)
	}()
	t.Cleanup(func() {
		_ = tm.wait() // t.Context is done by now, so cmd.Cancel stops rexdiff
		_ = f.Close()
		<-tm.done
		// Close the input pipe, not the emulator, to end the copy. Closing
		// the emulator races with its Read.
		_ = tm.emu.InputPipe().(io.Closer).Close()
		<-input
	})
	tm.waitFor("1 Changes")
	return tm
}

// resize clears the screen, so what shows next is what rexdiff draws at
// the new size.
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

func (tm *term) screen() string {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.emu.String()
}

// keys sends names from keyCodes or literal text. An esc and the next key
// can be read as one key, so esc must come last.
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

func (tm *term) paste(text string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.emu.Paste(text)
}

func (tm *term) typed(text string) {
	tm.t.Helper()
	tm.keys(strings.Split(text, "")...)
}

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

func (tm *term) at(text string) (x, y int) {
	tm.t.Helper()
	x, y, _ = position(tm.waitFor(text), text)
	return x, y
}

func position(s, text string) (x, y int, ok bool) {
	for i, l := range strings.Split(s, "\n") {
		if before, _, found := strings.Cut(l, text); found {
			return utf8.RuneCountInString(before), i, true
		}
	}
	return 0, 0, false
}

func (tm *term) mouse(m vt.Mouse) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.emu.SendMouse(m)
}

func (tm *term) click(text string, times int) {
	tm.t.Helper()
	x, y := tm.at(text)
	for range times {
		tm.mouse(vt.MouseClick{X: x, Y: y, Button: vt.MouseLeft})
		tm.mouse(vt.MouseRelease{X: x, Y: y, Button: vt.MouseLeft})
	}
}

func (tm *term) drag(from, to string) {
	tm.t.Helper()
	x0, y0 := tm.at(from)
	x1, y1 := tm.at(to)
	tm.mouse(vt.MouseClick{X: x0, Y: y0, Button: vt.MouseLeft})
	tm.mouse(vt.MouseMotion{X: x1, Y: y1, Button: vt.MouseLeft})
	tm.mouse(vt.MouseRelease{X: x1, Y: y1, Button: vt.MouseLeft})
}

func (tm *term) wheel(text string, n int) {
	tm.t.Helper()
	x, y := tm.at(text)
	for range n {
		tm.mouse(vt.MouseWheel{X: x, Y: y, Button: vt.MouseWheelDown})
	}
}

func (tm *term) waitFor(texts ...string) string {
	tm.t.Helper()
	return tm.waitUntil(func(s string) bool { return hasAll(s, texts...) },
		"show "+strings.Join(texts, ", "))
}

func (tm *term) waitGone(texts ...string) string {
	tm.t.Helper()
	return tm.waitUntil(func(s string) bool { return !hasAny(s, texts...) },
		"stop showing "+strings.Join(texts, ", "))
}

func hasAll(s string, texts ...string) bool {
	return !slices.ContainsFunc(texts, func(t string) bool { return !strings.Contains(s, t) })
}

func hasAny(s string, texts ...string) bool {
	return slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(s, t) })
}

func sameLine(s string, texts ...string) bool {
	for l := range strings.Lines(s) {
		if hasAll(l, texts...) {
			return true
		}
	}
	return false
}

func (tm *term) waitCursor(text string) {
	tm.t.Helper()
	tm.waitUntil(func(s string) bool { return sameLine(s, "▶", text) },
		"put the cursor on "+text)
}

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
			// The last write can arrive after the check above, so check
			// once more.
			if s = tm.screen(); ok(s) {
				return s
			}
			tm.t.Fatalf("rexdiff stops, and the screen does not %s. It shows:\n%s", what, s)
		case <-timeout.C:
			tm.t.Fatalf("the screen does not %s. It shows:\n%s", what, s)
		}
	}
}

const pollInterval = 20 * time.Millisecond

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
