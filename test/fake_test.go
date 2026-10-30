package test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rednafi/rexdiff/internal/testgit"
)

// repoFixture has one commit on main and one on feat, which is checked
// out. It has uncommitted edits to a.go and to the first and last lines of
// long.txt. The directory out/ is ignored.
var repoFixture = testgit.NewFixture(func(tb testing.TB, dir string) {
	tb.Helper()
	testgit.InitRepo(tb, dir)
	testgit.WriteFile(tb, dir, "a.go", "package a\n\nfunc A() int {\n\treturn 1\n}\n")
	testgit.WriteFile(tb, dir, "README.md", "# Title\n\nSome text.\n")
	testgit.WriteFile(tb, dir, "docs/guide.txt", "guide\n")
	testgit.WriteFile(tb, dir, ".gitignore", "out/\n")
	testgit.WriteFile(tb, dir, "long.txt", numbered("line", 1, 40))
	testgit.Git(tb, dir, "add", ".")
	testgit.Git(tb, dir, "commit", "-qm", "first")
	testgit.Git(tb, dir, "checkout", "-qb", "feat")
	testgit.WriteFile(tb, dir, "b.go", "package a\n\nvar B = 2\n")
	testgit.Git(tb, dir, "add", ".")
	testgit.Git(tb, dir, "commit", "-qm", "add b")
	testgit.WriteFile(tb, dir, "a.go", "package a\n\nfunc A() int {\n\tx := 2\n\treturn x\n}\n")
	testgit.WriteFile(tb, dir, "out/build.log", "built\n")
	testgit.WriteFile(tb, dir, "long.txt",
		"first\n"+numbered("line", 2, 39)+"last\n")
})

func numbered(word string, first, last int) string {
	var b strings.Builder
	for n := first; n <= last; n++ {
		fmt.Fprintf(&b, "%s %d\n", word, n)
	}
	return b.String()
}

// newRepo resolves symbolic links so the path matches the root that git
// reports.
func newRepo(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "repo")
	repoFixture.Copy(t, dir)
	return dir
}

// fakeRex is a fake rex for the session S, with rexdiff in block:self. If
// the FIFO gate exists, a paste waits until the test writes to it.
type fakeRex struct {
	dir    string
	blocks []string // JSON
}

func newRex(t *testing.T, repo string) *fakeRex {
	t.Helper()
	r := &fakeRex{dir: t.TempDir()}
	testgit.Script(t, r.dir, "rex", `d="${0%/*}"
printf '%s\n' "$*" >> "$d/calls"
case "$*" in
"api call -s S session.list_blocks {}") cat "$d/blocks" ;;
"session inspect S --json") cat "$d/session" ;;
"block call -s S -b "*" program_status") cat "$d/status.$6" ;;
"block call -s S -b "*" process") cat "$d/process" ;;
"block inspect "*) cat "$d/inspect.$3" ;;
"api call -s S block.event "*) if [ -p "$d/gate" ]; then cat "$d/gate" >/dev/null; fi ;;
"events -s S --json") touch "$d/events"; exec tail -n +1 -f "$d/events" ;;
esac
`)
	r.write(t, "session", recorded(t, "session.json"))
	r.write(t, "process", recorded(t, "process.json", "{{CWD}}", repo))
	r.addBlock(t, "block:self", "rexdiff")
	r.addAgent(t, "block:pi", "agent", "pi")
	return r
}

func recorded(t *testing.T, name string, oldNew ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "rex", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(strings.NewReplacer(oldNew...).Replace(string(b)))
}

func (r *fakeRex) addBlock(t *testing.T, block, label string) {
	t.Helper()
	r.blocks = append(r.blocks, recorded(t, "block.json", "{{ID}}", block, "{{LABEL}}", label))
	r.write(t, "blocks", `{"blocks":[`+strings.Join(r.blocks, ",")+`]}`)
	r.write(t, "inspect."+label, recorded(t, "inspect.json", "{{ID}}", block, "{{LABEL}}", label))
}

func (r *fakeRex) addAgent(t *testing.T, block, label, app string) {
	t.Helper()
	r.addBlock(t, block, label)
	r.status(t, block, app, "idle")
}

func (r *fakeRex) status(t *testing.T, block, app, state string) []string {
	t.Helper()
	values := []string{
		"{{ID}}", block, "{{STATE}}", state, "{{APP}}", app,
		"{{AT}}", time.Now().Format(time.RFC3339Nano),
	}
	r.write(t, "status."+block, recorded(t, "status.json", values...))
	return values
}

func (r *fakeRex) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *fakeRex) add(t *testing.T, name, line string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(r.dir, name), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func (r *fakeRex) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "calls"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func (r *fakeRex) expectCalls(t *testing.T, wants ...string) {
	t.Helper()
	var calls string
	poll(t, func() bool { calls = r.calls(t); return hasAll(calls, wants...) }, func() string {
		return "rex calls = \n" + calls + "\nwant calls with:\n" + strings.Join(wants, "\n")
	})
}

func (r *fakeRex) setState(t *testing.T, state string) {
	t.Helper()
	values := r.status(t, "block:pi", "pi", state)
	r.add(t, "events", recorded(t, "event.json", values...))
}

func tools(t *testing.T, bodies map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range bodies {
		testgit.Script(t, dir, name, body)
	}
	return dir
}

func clipboard(t *testing.T) string {
	t.Helper()
	const body = `cat > "${0%/*}/clip"` + "\n"
	return tools(t, map[string]string{"pbcopy": body, "wl-copy": body})
}

// clipEnv makes rexdiff use the fake wl-copy on Linux too.
var clipEnv = []string{"WAYLAND_DISPLAY=w"}

func lines(s, text string) []string {
	var out []string
	for l := range strings.Lines(s) {
		if strings.Contains(l, text) {
			out = append(out, strings.TrimRight(l, "\n"))
		}
	}
	return out
}
