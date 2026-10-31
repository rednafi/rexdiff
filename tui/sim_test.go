package tui

// TestSim is a deterministic simulation. synctest owns the clock. The
// seed picks the order of input, messages and time.
//
// Replay a seed: SIM_SEED=123 go test -run TestSim ./tui
// More seeds:    SIM_SEEDS=500 SIM_STEPS=2000 go test -run TestSim ./tui

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"hash/fnv"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/dlclark/regexp2/v2"

	"github.com/rednafi/rexdiff/internal/testgit"
	"github.com/rednafi/rexdiff/vcs"
)

func TestMain(m *testing.M) {
	// regexp2 runs one clock goroutine for the whole process. Start it
	// here, so that no synctest bubble owns it.
	re := regexp2.MustCompile("x", regexp2.None)
	re.MatchTimeout = 24 * time.Hour
	_, _ = re.MatchString("x")
	testgit.Main(m)
}

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return n
	}
	return def
}

func TestSim(t *testing.T) {
	t.Parallel()
	steps := envInt("SIM_STEPS", 400)
	if s := os.Getenv("SIM_SEED"); s != "" {
		seed, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		simSeed(t, seed, steps)
		return
	}
	n := envInt("SIM_SEEDS", 8)
	base, err := strconv.ParseUint(os.Getenv("SIM_BASE"), 10, 64)
	if err != nil {
		base = uint64(time.Now().UnixMicro()) % 1e6
	}
	for i := range n {
		seed := base + uint64(i)
		t.Run(strconv.FormatUint(seed, 10), func(t *testing.T) {
			t.Parallel()
			simSeed(t, seed, steps)
		})
	}
}

// TestSimKeys checks that the simulated keys match the key names.
func TestSimKeys(t *testing.T) {
	t.Parallel()
	for _, k := range allKeys() {
		if got := parseKey(k).String(); got != k {
			t.Errorf("parseKey(%q).String() = %q", k, got)
		}
	}
}

func simSeed(t *testing.T, seed uint64, steps int) {
	t.Helper()
	acts := genActions(seed, steps)
	err := simRun(t, acts)
	if err == nil {
		return
	}
	t.Logf("seed %d fails after %d actions: %v", seed, len(acts), err)
	if os.Getenv("SIM_NOSHRINK") == "" {
		acts = shrink(t, acts, err)
	}
	var b strings.Builder
	for i, a := range acts {
		fmt.Fprintf(&b, "  %3d %s\n", i, a)
	}
	t.Fatalf("seed %d: %v\nreplay: SIM_SEED=%d go test -run TestSim ./tui\nsmallest failing actions (%d):\n%s",
		seed, simRun(t, acts), seed, len(acts), b.String())
}

// shrink drops actions while the same kind of error stays.
func shrink(t *testing.T, acts []action, want error) []action {
	t.Helper()
	kind := errKind(want)
	budget := 150
	for chunk := len(acts) / 2; chunk >= 1 && budget > 0; {
		removed := false
		for i := 0; i+chunk <= len(acts) && budget > 0; {
			try := slices.Concat(acts[:i], acts[i+chunk:])
			budget--
			if err := simRun(t, try); err != nil && errKind(err) == kind {
				acts, removed = try, true
				continue
			}
			i += chunk
		}
		if !removed {
			chunk /= 2
		}
	}
	return acts
}

func errKind(err error) string {
	s := err.Error()
	if i := strings.IndexAny(s, ":\n"); i > 0 {
		return s[:i]
	}
	return s
}

// An action is one step. The seed makes all actions first, so shrink can
// drop some.
type action struct {
	kind string // key, text, paste, click, drag, wheel, resize, sleep, deliver, fs
	s    string
	x, y int
	d    time.Duration
	n    int
}

func (a action) String() string {
	switch a.kind {
	case "key", "text", "paste":
		return a.kind + " " + strconv.Quote(a.s)
	case "fs":
		return fmt.Sprintf("fs %q %d", a.s, a.n)
	case "click", "drag", "wheel":
		return fmt.Sprintf("%s %d,%d %s", a.kind, a.x, a.y, a.s)
	case "resize":
		return fmt.Sprintf("resize %dx%d", a.x, a.y)
	case "sleep":
		return "sleep " + a.d.String()
	}
	return fmt.Sprintf("%s %d", a.kind, a.n)
}

// hostile is text that is hard to draw.
var hostile = []string{
	"a", "Z", " ", "\t", "é", "e\u0301", "漢", "字", "🙂", "👩‍👩‍👧", "🇫🇮",
	"\u200b", "\u202e", "\u00ad", "ﷺ", "\x1b[31m", "\x1b]52;c;aGk=\a",
	"\x1b]8;;http://x\x1b\\", "\r", "\x00", "\x7f", "\xff", "\xe2\x82",
	"\\", "$(id)", "%s", "\n", "\r\n",
}

func hostileText(r *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		if r.IntN(3) == 0 {
			b.WriteString(hostile[r.IntN(len(hostile))])
		} else {
			b.WriteByte("abcdefghijklmnopqrstuvwxyz"[r.IntN(26)])
		}
	}
	return b.String()
}

func allKeys() []string {
	set := map[string]bool{}
	for _, a := range keyActions {
		if strings.HasPrefix(a.name, "quit") {
			continue // the run tests quit at its end
		}
		for _, k := range a.keys {
			set[k] = true
		}
	}
	for _, k := range []string{
		"enter", "esc", "tab", "shift+tab", "backspace", "left", "right", "up", "down",
		"home", "end", "pgup", "pgdown", "shift+enter", "space", "ctrl+a", "ctrl+e",
		"0", "1", "5", "9", "G", "g", "n", "N", "/", "?", ":",
	} {
		set[k] = true
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func genActions(seed uint64, steps int) []action {
	r := rand.New(rand.NewPCG(seed, 0x5eed)) //nolint:gosec // a test needs a seed, not security
	keys := allKeys()
	acts := make([]action, 0, steps)
	for range steps {
		var a action
		switch p := r.IntN(100); {
		case p < 40:
			a = action{kind: "key", s: keys[r.IntN(len(keys))]}
		case p < 47:
			a = action{kind: "text", s: hostileText(r, 1+r.IntN(4))}
		case p < 49:
			a = action{kind: "paste", s: hostileText(r, r.IntN(200))}
		case p < 55:
			kinds := []string{"click", "click", "drag", "wheel"}
			a = action{kind: kinds[r.IntN(len(kinds))], x: r.IntN(220), y: r.IntN(70)}
			if r.IntN(2) == 0 {
				a.s = "up"
			}
		case p < 58:
			sizes := [][2]int{{1, 1}, {2, 3}, {10, 5}, {40, 12}, {80, 24}, {120, 40}, {200, 60}, {r.IntN(250), r.IntN(80)}}
			s := sizes[r.IntN(len(sizes))]
			a = action{kind: "resize", x: s[0], y: s[1]}
		case p < 70:
			ds := []time.Duration{time.Millisecond, 50 * time.Millisecond, 300 * time.Millisecond, time.Second, 5 * time.Second, time.Minute}
			a = action{kind: "sleep", d: ds[r.IntN(len(ds))]}
		case p < 92:
			a = action{kind: "deliver", n: r.IntN(1 << 20)}
		default:
			a = action{kind: "fs", s: fsOps[r.IntN(len(fsOps))], n: r.IntN(1 << 20)}
		}
		acts = append(acts, a)
	}
	return acts
}

var fsOps = []string{
	"write", "write", "write", "append", "delete", "add", "commit", "branch", "checkout-main", "stash",
	"rename", "binary", "huge-line", "hostile-name", "conflict", "detach", "symlink", "chmod",
	"to-dir", "many", "unmany", "lock", "unlock", "ignore", "exec-bit", "reset", "amend",
}

// simRepo has commits on a branch, edits, a binary file and odd names.
var simRepo = testgit.NewFixture(func(tb testing.TB, dir string) {
	tb.Helper()
	simGit(tb, dir, "init", "-q", "-b", "main")
	testgit.WriteFile(tb, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	testgit.WriteFile(tb, dir, "README.md", "# Title\n\n* one\n* two\n\n```go\nx := 1\n```\n")
	var long strings.Builder
	for i := range 300 {
		fmt.Fprintf(&long, "line %d\n", i)
	}
	testgit.WriteFile(tb, dir, "long.txt", long.String())
	simGit(tb, dir, "add", ".")
	simGit(tb, dir, "commit", "-qm", "first")
	simGit(tb, dir, "checkout", "-qb", "feat")
	testgit.WriteFile(tb, dir, "b.go", "package a\n\nvar B = \"漢字🙂\"\n")
	testgit.WriteFile(tb, dir, "sp ace/ü\u0301.txt", "x\n")
	simGit(tb, dir, "add", ".")
	simGit(tb, dir, "commit", "-qm", "add b")
	testgit.WriteFile(tb, dir, "a.go", "package a\n\nfunc A() int {\n\tx := 2\n\treturn x\n}\n")
	testgit.WriteFile(tb, dir, "bin.dat", "\x00\x01\x02binary")
	testgit.WriteFile(tb, dir, "long.txt", "first\n"+long.String()[7:]+"last\n")
})

// fixedDates make commit IDs and their ages the same in each run. git
// reads its own clock for "1 second ago", and the fake clock cannot change it.
var fixedDates = []string{"GIT_AUTHOR_DATE=2001-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2001-01-01T00:00:00Z"}

func simGit(tb testing.TB, dir string, args ...string) {
	tb.Helper()
	cmd := exec.CommandContext(tb.Context(), "git", args...)
	cmd.Dir, cmd.Env = dir, append(testgit.ToolEnv(tb), fixedDates...)
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func simRun(t *testing.T, acts []action) error {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "repo")
	simRepo.Copy(t, dir)
	env := append(testgit.ToolEnv(t), "HOME="+tmp, "TERM=xterm-256color")
	env = append(env, fixedDates...)
	var result error
	synctest.Test(t, func(t *testing.T) {
		_, result = runBubble(t, dir, env, acts, false)
	})
	return result
}

type sim struct {
	ctx    context.Context
	m      *model
	dir    string
	env    []string
	mu     sync.Mutex
	queues map[string][]tea.Msg // by the goroutine that sends them, in order
	next   int
	panics []string
	quit   bool
	trace  []string
	seen   map[string]int // the states that the run reaches
	frames []uint64       // a hash of each frame, to find where two runs part
	shots  []string       // each frame, if keep is set
	log    []string       // each delivery, if keep is set
	keep   bool
}

func runBubble(t *testing.T, dir string, env []string, acts []action, keep bool) (s *sim, err error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer func() {
		cancel()
		// Let the ticks and git commands that are in flight finish.
		time.Sleep(time.Hour)
		synctest.Wait()
	}()
	repo, err := vcs.Open(ctx, env, dir)
	if err != nil {
		return s, fmt.Errorf("setup: %w", err)
	}
	cfg, err := parseConfig("")
	if err != nil {
		return s, fmt.Errorf("setup: %w", err)
	}
	m := startModel(ctx, Options{Repo: repo, Env: env}, cfg)
	s = &sim{
		ctx: ctx, m: &m, dir: dir, env: env, queues: map[string][]tea.Msg{}, seen: map[string]int{},
		keep: keep,
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s\nlast actions:\n%s", r, debug.Stack(), s.tail())
		}
	}()
	s.spawn(s.m.Init())
	if err := s.update(tea.WindowSizeMsg{Width: 120, Height: 40}); err != nil {
		return s, err
	}
	for _, a := range acts {
		synctest.Wait()
		if err := s.check(); err != nil {
			return s, err
		}
		if s.quit {
			return s, nil
		}
		s.trace = append(s.trace, a.String())
		if err := s.do(a); err != nil {
			return s, err
		}
	}
	// Close any box and show the uncommitted changes. With no more input,
	// the held keys must go and the list must match git.
	s.readable()
	s.trace = append(s.trace, "3 esc, 1, u")
	for _, k := range []string{"esc", "esc", "esc", "1", "u"} {
		if err := s.update(tea.KeyPressMsg(parseKey(k))); err != nil {
			return s, err
		}
	}
	quiet := func(secs int) error {
		for range secs {
			synctest.Wait()
			for s.deliverAny(0) {
				if s.quit {
					return fmt.Errorf("quit: the run quits early\nlast actions:\n%s", s.tail())
				}
				if err := s.check(); err != nil {
					return err
				}
			}
			time.Sleep(time.Second)
		}
		return nil
	}
	if err := quiet(5); err != nil {
		return s, err
	}
	// Only the watcher can see this change, because no key follows it.
	s.trace = append(s.trace, "write zz.txt, remove a.go")
	_ = os.WriteFile(filepath.Join(s.dir, "zz.txt"), []byte("late\n"), 0o644)
	_ = os.Remove(filepath.Join(s.dir, "a.go"))
	if err := quiet(30); err != nil {
		return s, err
	}
	t.Logf("states: %v", s.seen)
	if len(s.m.held) > 0 {
		return s, fmt.Errorf("stuck: %d input messages are still held after 30s of quiet (tab %v)\nlast actions:\n%s", len(s.m.held), s.m.tab, s.tail())
	}
	if !s.m.changesLoaded {
		return s, fmt.Errorf("stuck: the changes do not load after 30s of quiet\nlast actions:\n%s", s.tail())
	}
	if s.m.scope == ScopeUncommitted && s.m.shownScope == ScopeUncommitted && s.m.changes.tooMany == 0 {
		want, err := s.gitChanged()
		if err != nil {
			return s, fmt.Errorf("setup: %w", err)
		}
		var got []string
		for _, f := range s.m.changes.files {
			got = append(got, f.Path)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			return s, fmt.Errorf("stale: after 30s of quiet, the list shows %q, but git has %q\nlast actions:\n%s", got, want, s.tail())
		}
	}
	// q quits after at most three esc keys. With unsent comments, q asks
	// first, and Q in that prompt quits.
	s.trace = append(s.trace, "3 esc, q")
	for _, k := range []string{"esc", "esc", "esc", "q"} {
		if err := s.update(tea.KeyPressMsg(parseKey(k))); err != nil {
			return s, err
		}
	}
	for range 10 {
		synctest.Wait()
		for !s.quit {
			if !s.deliverAny(0) {
				break
			}
		}
		if s.quit {
			return s, nil
		}
		if s.m.overlay != nil && s.m.overlay.kind == overlayQuit {
			s.trace = append(s.trace, "Q")
			if err := s.update(tea.KeyPressMsg(parseKey("Q"))); err != nil {
				return s, err
			}
			continue
		}
		time.Sleep(time.Second)
	}
	return s, fmt.Errorf("no quit: q does not quit (mode %d, overlay %v)\nlast actions:\n%s", s.m.mode, s.m.overlay != nil, s.tail())
}

func (s *sim) tail() string {
	from := max(0, len(s.trace)-25)
	return "  " + strings.Join(s.trace[from:], "\n  ")
}

// spawn runs cmd in a goroutine, as Bubble Tea does. Only the main
// goroutine calls it, so the IDs are the same in each run.
func (s *sim) spawn(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	id := fmt.Sprintf("%08d", s.next)
	s.next++
	go s.exec(id, id, cmd)
}

var cmdType = reflect.TypeFor[tea.Cmd]()

// exec runs cmd and queues its message under queue. A batch gives each
// command an ID under path, so a race between goroutines cannot change
// the IDs.
func (s *sim) exec(queue, path string, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			s.mu.Lock()
			s.panics = append(s.panics, fmt.Sprintf("%v\n%s", r, debug.Stack()))
			s.mu.Unlock()
		}
	}()
	msg := cmd()
	switch v := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for i, c := range v {
			if c != nil {
				id := fmt.Sprintf("%s.%d", path, i)
				go s.exec(id, id, c)
			}
		}
		return
	}
	// tea.Sequence returns an unexported slice of commands. Its messages
	// stay in order in one queue.
	if rv := reflect.ValueOf(msg); rv.Kind() == reflect.Slice && rv.Type().Elem() == cmdType {
		for i := range rv.Len() {
			s.exec(queue, fmt.Sprintf("%s.s%d", path, i), rv.Index(i).Interface().(tea.Cmd))
		}
		return
	}
	s.mu.Lock()
	s.queues[queue] = append(s.queues[queue], msg)
	s.mu.Unlock()
}

// deliverAny gives the model the next message of one goroutine. n picks
// the goroutine.
func (s *sim) deliverAny(n int) bool {
	// Let the commands of the last message finish, so the set of queues
	// is the same in each run.
	synctest.Wait()
	s.mu.Lock()
	ids := make([]string, 0, len(s.queues))
	for id, q := range s.queues {
		if len(q) > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.mu.Unlock()
		return false
	}
	slices.Sort(ids)
	id := ids[n%len(ids)]
	msg := s.queues[id][0]
	if s.keep {
		s.log = append(s.log, fmt.Sprintf("%d %s %T %v", len(s.trace), id, msg, ids))
	}
	s.queues[id] = s.queues[id][1:]
	if len(s.queues[id]) == 0 {
		delete(s.queues, id)
	}
	s.mu.Unlock()
	if err := s.update(msg); err != nil {
		panic(err)
	}
	return true
}

func (s *sim) update(msg tea.Msg) error {
	if _, ok := msg.(tea.QuitMsg); ok {
		s.quit = true
		return nil
	}
	if t := reflect.TypeOf(msg); t.PkgPath() == "charm.land/bubbletea/v2" && !ast.IsExported(t.Name()) {
		// Only the real program handles these, such as exec.
		return nil
	}
	_, cmd := s.m.Update(msg)
	s.spawn(cmd)
	return nil
}

func (s *sim) do(a action) error {
	switch a.kind {
	case "key":
		return s.update(tea.KeyPressMsg(parseKey(a.s)))
	case "text":
		for _, r := range a.s {
			if r == 'q' || r == 'Q' {
				continue // a held q can quit later, and the run tests quit at its end
			}
			k := tea.Key{Code: r, Text: string(r)}
			if err := s.update(tea.KeyPressMsg(k)); err != nil {
				return err
			}
		}
	case "paste":
		return s.update(tea.PasteMsg{Content: a.s})
	case "click", "drag", "wheel":
		w, h := max(s.m.w, 1), max(s.m.h, 1)
		x, y := a.x%(w+2)-1, a.y%(h+2)-1 // sometimes just off the screen
		mo := tea.Mouse{X: x, Y: y, Button: tea.MouseLeft}
		switch a.kind {
		case "click":
			_ = s.update(tea.MouseClickMsg(mo))
			return s.update(tea.MouseReleaseMsg(mo))
		case "drag":
			_ = s.update(tea.MouseClickMsg(mo))
			mo.Y = (y + 7) % h
			_ = s.update(tea.MouseMotionMsg(mo))
			return s.update(tea.MouseReleaseMsg(mo))
		default:
			mo.Button = tea.MouseWheelDown
			if a.s == "up" {
				mo.Button = tea.MouseWheelUp
			}
			return s.update(tea.MouseWheelMsg(mo))
		}
	case "resize":
		return s.update(tea.WindowSizeMsg{Width: a.x, Height: a.y})
	case "sleep":
		time.Sleep(a.d)
	case "deliver":
		s.deliverAny(a.n)
	case "fs":
		s.mutate(a)
	}
	return nil
}

func parseKey(s string) tea.Key {
	named := map[string]rune{
		"enter": tea.KeyEnter, "esc": tea.KeyEscape, "tab": tea.KeyTab,
		"backspace": tea.KeyBackspace, "left": tea.KeyLeft, "right": tea.KeyRight,
		"up": tea.KeyUp, "down": tea.KeyDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "space": tea.KeySpace,
		"delete": tea.KeyDelete,
	}
	var k tea.Key
	parts := strings.Split(s, "+")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			k.Mod |= tea.ModCtrl
		case "shift":
			k.Mod |= tea.ModShift
		case "alt":
			k.Mod |= tea.ModAlt
		}
	}
	last := parts[len(parts)-1]
	if last == "" { // the key "+"
		last = "+"
	}
	if c, ok := named[last]; ok {
		k.Code = c
		if c == tea.KeySpace && k.Mod == 0 {
			k.Text = " "
		}
		return k
	}
	r, _ := utf8.DecodeRuneInString(last)
	k.Code = r
	switch k.Mod {
	case 0:
		k.Text = last
	case tea.ModShift:
		k.Text = strings.ToUpper(last)
	}
	return k
}

// mutate changes the repository while rexdiff runs. It ignores git
// errors, because a lock can be held.
func (s *sim) mutate(a action) {
	r := rand.New(rand.NewPCG(uint64(a.n), 7)) //nolint:gosec // a test needs a seed, not security
	names := []string{"a.go", "b.go", "README.md", "long.txt", "new.txt", "d/e/f.go", "bin.dat"}
	name := names[r.IntN(len(names))]
	p := filepath.Join(s.dir, filepath.FromSlash(name))
	git := func(args ...string) {
		cmd := exec.CommandContext(s.ctx, "git", args...)
		cmd.Dir, cmd.Env = s.dir, s.env
		_ = cmd.Run()
	}
	switch a.s {
	case "write":
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		var b strings.Builder
		for range 1 + r.IntN(40) {
			b.WriteString(hostileText(r, r.IntN(30)) + "\n")
		}
		_ = os.WriteFile(p, []byte(b.String()), 0o644)
	case "append":
		f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
		if err == nil {
			_, _ = f.WriteString(hostileText(r, 20) + "\n")
			_ = f.Close()
		}
	case "delete":
		_ = os.Remove(p)
	case "add":
		git("add", "-A")
	case "commit":
		git("commit", "-qam", hostileText(r, 10))
	case "branch":
		git("checkout", "-qb", fmt.Sprintf("b%d", a.n))
	case "checkout-main":
		git("checkout", "-q", "main")
	case "stash":
		git("stash", "-q")
	case "rename":
		_ = os.Rename(p, p+".moved")
	case "binary":
		_ = os.WriteFile(p, []byte{0, 1, 2, 0xff, 0}, 0o644)
	case "huge-line":
		_ = os.WriteFile(p, []byte(strings.Repeat("漢x", 20000)+"\n"), 0o644)
	case "conflict":
		// Leave a merge with a conflict in a.go.
		br := fmt.Sprintf("c%d", a.n)
		git("checkout", "-qb", br)
		_ = os.WriteFile(filepath.Join(s.dir, "a.go"), []byte("theirs "+br+"\n"), 0o644)
		git("commit", "-qam", "theirs")
		git("checkout", "-q", "-")
		_ = os.WriteFile(filepath.Join(s.dir, "a.go"), []byte("ours "+br+"\n"), 0o644)
		git("commit", "-qam", "ours")
		git("merge", "-q", br)
	case "detach":
		git("checkout", "-q", "--detach", "HEAD~1")
	case "symlink":
		links := map[string]string{"link.go": "a.go", "dangling": "nowhere", "loop": "loop", "dirlink": "."}
		keys := []string{"link.go", "dangling", "loop", "dirlink"}
		k := keys[r.IntN(len(keys))]
		_ = os.Symlink(links[k], filepath.Join(s.dir, k))
	case "chmod":
		_ = os.Chmod(p, 0)
	case "to-dir":
		_ = os.RemoveAll(p)
		_ = os.MkdirAll(p, 0o755)
		_ = os.WriteFile(filepath.Join(p, "inner.txt"), []byte("x\n"), 0o644)
	case "many":
		d := filepath.Join(s.dir, "many")
		_ = os.MkdirAll(d, 0o755)
		for i := range 3000 {
			_ = os.WriteFile(filepath.Join(d, strconv.Itoa(i)), []byte("x\n"), 0o644)
		}
	case "unmany":
		_ = os.RemoveAll(filepath.Join(s.dir, "many"))
	case "lock":
		_ = os.WriteFile(filepath.Join(s.dir, ".git", "index.lock"), nil, 0o644)
	case "unlock":
		_ = os.Remove(filepath.Join(s.dir, ".git", "index.lock"))
	case "ignore":
		pats := []string{"*.txt\n", "*.go\n", "d/\n", "*\n", "!a.go\n"}
		_ = os.WriteFile(filepath.Join(s.dir, ".gitignore"), []byte(pats[r.IntN(len(pats))]), 0o644)
	case "exec-bit":
		_ = os.Chmod(p, 0o755)
	case "reset":
		git("reset", "-q", "--hard", "HEAD~1")
	case "amend":
		git("commit", "-q", "--amend", "-m", hostileText(r, 8))
	case "hostile-name":
		bad := []string{
			"new\nline.txt", "tab\tname.go", "\x1b[31mred.txt", "-dash.txt", "a b/c d.txt", "🙂.md", "\u202etxt.exe",
			"*.txt", "[ab].go", "?.md", ":(top)x", ":!x", "\\back.txt",
		}
		q := filepath.Join(s.dir, bad[r.IntN(len(bad))])
		_ = os.MkdirAll(filepath.Dir(q), 0o755)
		_ = os.WriteFile(q, []byte("x\n"), 0o644)
	}
}

// allowedEsc matches SGR styles and OSC 8 links.
var allowedEsc = regexp.MustCompile(`\x1b\[[0-9;:]*m|\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)`)

// check tests the invariants after each step.
func (s *sim) check() error {
	s.mu.Lock()
	panics := slices.Clone(s.panics)
	s.mu.Unlock()
	if len(panics) > 0 {
		return fmt.Errorf("panic in a command: %s\nlast actions:\n%s", panics[0], s.tail())
	}
	var v tea.View
	if err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in View: %v\n%s", r, debug.Stack())
			}
		}()
		v = s.m.View()
		h := fnv.New64a()
		_, _ = h.Write([]byte(v.Content))
		s.frames = append(s.frames, h.Sum64())
		if s.keep {
			s.shots = append(s.shots, fmt.Sprintf("after action %d (%s):\n%s", len(s.trace), s.tail(), ansi.Strip(v.Content)))
		}
		return nil
	}(); err != nil {
		return fmt.Errorf("%w\nlast actions:\n%s", err, s.tail())
	}
	s.see()
	if !s.m.sized || s.m.w <= 0 || s.m.h <= 0 {
		return nil
	}
	lines := strings.Split(v.Content, "\n")
	if len(lines) > s.m.h {
		return fmt.Errorf("too tall: the view has %d lines on a %dx%d screen\nlast actions:\n%s", len(lines), s.m.w, s.m.h, s.tail())
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w > s.m.w {
			return fmt.Errorf("too wide: line %d is %d cells on a %dx%d screen: %q\nlast actions:\n%s", i, w, s.m.w, s.m.h, ansi.Strip(l), s.tail())
		}
		rest := allowedEsc.ReplaceAllString(l, "")
		if i := strings.IndexFunc(rest, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }); i >= 0 {
			return fmt.Errorf("control: line %d has a raw control or escape character %q: %q\nlast actions:\n%s", i, rest[i], rest, s.tail())
		}
	}
	return nil
}

func (s *sim) see() {
	m := s.m
	s.seen[fmt.Sprintf("tab%d", m.tab)]++
	s.seen[fmt.Sprintf("mode%d", m.mode)]++
	if m.overlay != nil {
		s.seen[fmt.Sprintf("overlay%d", m.overlay.kind)]++
	}
	if m.changesLoaded {
		s.seen["loaded"]++
	}
	if len(m.held) > 0 {
		s.seen["held"]++
	}
}

// gitChanged lists the uncommitted changes as git sees them. It adds the
// work tree to a copy of the index, so it does not change the repository.
// The copy keeps tracked files that a .gitignore pattern matches.
func (s *sim) gitChanged() ([]string, error) {
	idx := filepath.Join(filepath.Dir(s.dir), "oracle.index")
	_ = os.Remove(idx)
	if b, err := os.ReadFile(filepath.Join(s.dir, ".git", "index")); err == nil {
		if err := os.WriteFile(idx, b, 0o644); err != nil {
			return nil, err
		}
	}
	env := append(slices.Clone(s.env), "GIT_INDEX_FILE="+idx)
	git := func(args ...string) (string, error) {
		cmd := exec.CommandContext(s.ctx, "git", args...)
		cmd.Dir, cmd.Env = s.dir, env
		out, err := cmd.Output()
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			err = fmt.Errorf("git %v: %w: %s", args, err, ee.Stderr)
		}
		return string(out), err
	}
	if _, err := git("add", "-A"); err != nil {
		return nil, err
	}
	tree, err := git("write-tree")
	if err != nil {
		return nil, err
	}
	out, err := git("diff", "--name-only", "-z", "--find-renames", "HEAD", strings.TrimSpace(tree))
	if err != nil {
		return nil, err
	}
	paths := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	paths = slices.DeleteFunc(paths, func(p string) bool { return p == "" })
	slices.Sort(paths)
	return paths, nil
}

// TestSimSame runs one seed twice and reports the first frame that
// differs.
func TestSimSame(t *testing.T) {
	t.Parallel()
	if os.Getenv("SIM_SAME") == "" {
		t.Skip("set SIM_SAME=1")
	}
	acts := genActions(uint64(envInt("SIM_BASE", 1)), envInt("SIM_STEPS", 400)) //nolint:gosec // a seed
	runs := make([][]uint64, 2)
	shots := make([][]string, 2)
	logs := make([][]string, 2)
	traces := make([][]string, 2)
	for i := range runs {
		synctest.Test(t, func(t *testing.T) {
			tmp, _ := filepath.EvalSymlinks(t.TempDir())
			dir := filepath.Join(tmp, "repo")
			simRepo.Copy(t, dir)
			env := append(testgit.ToolEnv(t), "HOME="+tmp, "TERM=xterm-256color")
			env = append(env, fixedDates...)
			s, _ := runBubble(t, dir, env, acts, true)
			runs[i], traces[i], shots[i] = s.frames, s.trace, s.shots
			logs[i] = s.log
		})
	}
	for i := range min(len(logs[0]), len(logs[1])) {
		if logs[0][i] != logs[1][i] {
			from := max(0, i-6)
			t.Logf("delivery %d differs:\nfirst:\n%s\nsecond:\n%s", i,
				strings.Join(logs[0][from:min(i+4, len(logs[0]))], "\n"),
				strings.Join(logs[1][from:min(i+4, len(logs[1]))], "\n"))
			break
		}
	}
	if len(runs[0]) != len(runs[1]) {
		t.Errorf("%d and %d frames", len(runs[0]), len(runs[1]))
	}
	for i := range min(len(runs[0]), len(runs[1])) {
		if runs[0][i] != runs[1][i] {
			a, b := strings.Split(shots[0][i], "\n"), strings.Split(shots[1][i], "\n")
			var d []string
			for j := range min(len(a), len(b)) {
				if a[j] != b[j] {
					d = append(d, "- "+a[j], "+ "+b[j])
				}
			}
			if os.Getenv("SIM_SAME") == "v" {
				t.Logf("first run:\n%s\nsecond run:\n%s", shots[0][i], shots[1][i])
			}
			t.Fatalf("frame %d differs:\n%s", i, strings.Join(d, "\n"))
		}
	}
	t.Logf("same: %d and %d frames", len(runs[0]), len(runs[1]))
}

// readable undoes chmod 0, so that the oracle can read each file. It
// touches each file that it changes, so that the watcher can see it.
func (s *sim) readable() {
	//nolint:gosec // the test owns the directory
	_ = filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return nil //nolint:nilerr // skip what cannot be read
		}
		if info, err := d.Info(); err == nil && info.Mode().Perm()&0o400 == 0 {
			mode := fs.FileMode(0o644)
			if d.IsDir() {
				mode = 0o755
			}
			_ = os.Chmod(p, mode)
			now := time.Now()
			_ = os.Chtimes(p, now, now)
		}
		return nil
	})
}
