package test

// These tests measure rexdiff on large repos in REXDIFF_PERF. They run only
// if it is set. REXDIFF_PERF_OUT gets "repo<TAB>scenario<TAB>value" lines.
// REXDIFF_PERF_BIN names another rexdiff binary to measure.
//
//	REXDIFF_PERF=/tmp/rexdiff-perf REXDIFF_PERF_OUT=/tmp/perf.tsv \
//	    go test ./test -run TestPerf -count=1 -v -timeout 60m
//
// A time runs from the input to the last screen change before quietDelay
// without changes. RSS is in MB and CPU use in percent.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"

	"github.com/rednafi/rexdiff/internal/testgit"
)

const quietDelay = 1200 * time.Millisecond

const settleTimeout = 5 * time.Minute

var perfOut struct {
	sync.Mutex
	f *os.File
}

func perfRepo(t *testing.T, name string) string {
	t.Helper()
	dir := os.Getenv("REXDIFF_PERF")
	if dir == "" {
		t.Skip("REXDIFF_PERF is not set")
	}
	if raceOn {
		t.Skip("the race detector makes the times wrong")
	}
	repo := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		return repo
	}
	gen, ok := perfRepos[strings.TrimSuffix(name, "-branch")]
	if !ok {
		t.Fatalf("no repository %s", name)
	}
	main := filepath.Join(dir, strings.TrimSuffix(name, "-branch"))
	for _, d := range []string{main, main + "-branch"} {
		if err := os.RemoveAll(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	testgit.InitRepo(t, main)
	gen(t, main)
	return repo
}

var perfRepos = map[string]func(t *testing.T, dir string){
	"go7k":       genGo7k,
	"big50k":     func(t *testing.T, dir string) { t.Helper(); genScattered(t, dir, goLines(50_000, "")) },
	"big200k":    func(t *testing.T, dir string) { t.Helper(); genScattered(t, dir, shortLines(200_000)) },
	"rewrite20k": genRewrite20k,
	"xxl":        genXXL, // also creates xxl-branch
	"md5mb":      func(t *testing.T, dir string) { t.Helper(); genMarkdown(t, dir, 5<<20, 997) },
	"md400k":     func(t *testing.T, dir string) { t.Helper(); genMarkdown(t, dir, 400<<10, 397) },
	"files3k":    genFiles3k,
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	testgit.Git(t, dir, "add", "-A")
	testgit.Git(t, dir, "commit", "-q", "--no-verify", "-m", "base")
}

// goLines ends most lines with the marker L<i>, counting from 1. tag makes
// the function names differ between files.
func goLines(n int, tag string) []string {
	ls := make([]string, 0, n)
	ls = append(ls, "package big", "")
	for i := 3; i <= n; i++ {
		switch i % 20 {
		case 3:
			ls = append(ls, fmt.Sprintf("func %sf%d(x int) int { // L%d", tag, i, i))
		case 19:
			ls = append(ls, fmt.Sprintf("\treturn x // L%d", i))
		case 0:
			ls = append(ls, "}")
		default:
			ls = append(ls, fmt.Sprintf("\tx += compute(%d, \"s%d\") // L%d", i, i*7, i))
		}
	}
	return ls[:n]
}

func shortLines(n int) []string {
	ls := goLines(n, "")
	for i, l := range ls {
		if strings.Contains(l, "compute(") {
			ls[i] = fmt.Sprintf("\tx += %d // L%d", i+1, i+1)
		}
	}
	return ls
}

// mutate changes about frac of the lines. A changed line has the marker
// M<i> instead of L<i>.
func mutate(ls []string, frac float64, r *rand.Rand) []string {
	out := make([]string, 0, len(ls))
	// A run is 4.5 lines long on average.
	p := frac / 4.5 / (1 - frac)
	for i := 0; i < len(ls); i++ {
		if r.Float64() >= p {
			out = append(out, ls[i])
			continue
		}
		run := 1 + r.IntN(8)
		for k := 0; k < run && i < len(ls); k++ {
			l := ls[i]
			switch r.IntN(10) {
			case 0: // delete the line
			case 1:
				out = append(out, fmt.Sprintf("\tlog.Printf(\"inserted %d\") // I%d", i, i), l)
			default:
				out = append(out, strings.ReplaceAll(strings.Replace(l, "compute", "recompute", 1), "// L", "// M"))
			}
			i++
		}
		i--
	}
	return out
}

func join(ls []string) string { return strings.Join(ls, "\n") + "\n" }

func genGo7k(t *testing.T, dir string) {
	t.Helper()
	old := goLines(7000, "")
	testgit.WriteFile(t, dir, "big.go", join(old))
	testgit.WriteFile(t, dir, "small.go", "package big\n\nfunc small() {}\n")
	commitAll(t, dir)
	r := rand.New(rand.NewPCG(1, 7)) //nolint:gosec // a fixed seed makes the files reproducible
	testgit.WriteFile(t, dir, "big.go", join(mutate(old, 0.30, r)))
	testgit.WriteFile(t, dir, "small.go", "package big\n\nfunc small() { println() }\n")
}

func genScattered(t *testing.T, dir string, old []string) {
	t.Helper()
	testgit.WriteFile(t, dir, "huge.go", join(old))
	commitAll(t, dir)
	r := rand.New(rand.NewPCG(2, uint64(len(old)))) //nolint:gosec // a fixed seed makes the files reproducible
	ls := slices.Clone(old)
	for range 200 {
		i := 2 + r.IntN(len(ls)-2)
		ls[i] = strings.ReplaceAll(ls[i], "// L", "// M")
	}
	testgit.WriteFile(t, dir, "huge.go", join(ls))
}

func genRewrite20k(t *testing.T, dir string) {
	t.Helper()
	testgit.WriteFile(t, dir, "rewrite.go", join(goLines(20000, "a")))
	commitAll(t, dir)
	ls := goLines(20000, "b")
	for i := range ls {
		ls[i] = strings.ReplaceAll(strings.ReplaceAll(ls[i], "compute", "other"), "// L", "// N")
	}
	testgit.WriteFile(t, dir, "rewrite.go", join(ls))
}

func genXXL(t *testing.T, dir string) {
	t.Helper()
	r := rand.New(rand.NewPCG(4, 2000)) //nolint:gosec // a fixed seed makes the files reproducible
	type file struct {
		name string
		old  []string
	}
	files := make([]file, 2000)
	for i := range files {
		n := 200 + r.IntN(1801)
		name := fmt.Sprintf("pkg%02d/sub%d/file%04d.go", i%40, i%7, i)
		files[i] = file{name, goLines(n, fmt.Sprintf("p%d", i))}
		testgit.WriteFile(t, dir, name, join(files[i].old))
	}
	commitAll(t, dir)
	for _, f := range files {
		testgit.WriteFile(t, dir, f.name, join(mutate(f.old, 0.23, r)))
	}
	branch := dir + "-branch"
	copyDir(t, dir, branch)
	testgit.Git(t, branch, "checkout", "-q", "-b", "feature")
	commitAll(t, branch)
}

func markdown(size int) []string {
	var ls []string
	n := 0
	for i := 0; n < size; i++ {
		block := []string{
			fmt.Sprintf("## Section %d", i),
			"",
			fmt.Sprintf("Paragraph %d with **bold**, _italic_, `code`, and a [link][ref%d]. %s",
				i, i%50, strings.Repeat("Lorem ipsum dolor sit amet. ", 4)),
			"",
			fmt.Sprintf("- item %d.1", i),
			fmt.Sprintf("- item %d.2", i),
			"",
			"```go",
			fmt.Sprintf("func s%d() int { return %d }", i, i),
			"```",
			"",
			"| a | b |",
			"|---|---|",
			fmt.Sprintf("| %d | %d |", i, i*2),
			"",
		}
		for _, l := range block {
			n += len(l) + 1
		}
		ls = append(ls, block...)
	}
	for i := range 50 {
		ls = append(ls, fmt.Sprintf("[ref%d]: https://example.com/%d", i, i))
	}
	return ls
}

func genMarkdown(t *testing.T, dir string, size, every int) {
	t.Helper()
	old := markdown(size)
	testgit.WriteFile(t, dir, "README.md", join(old))
	commitAll(t, dir)
	ls := slices.Clone(old)
	for i := 0; i < len(ls); i += every {
		ls[i] += " (edited)"
	}
	testgit.WriteFile(t, dir, "README.md", join(ls))
}

func genFiles3k(t *testing.T, dir string) {
	t.Helper()
	r := rand.New(rand.NewPCG(5, 3000)) //nolint:gosec // a fixed seed makes the files reproducible
	name := func(i int) string { return fmt.Sprintf("src/mod%02d/part%d/f%04d.go", i%60, i%5, i) }
	for i := range 3000 {
		testgit.WriteFile(t, dir, name(i), join(goLines(50+r.IntN(300), fmt.Sprintf("g%d", i))))
	}
	commitAll(t, dir)
	for i := 0; i < 3000; i += 300 {
		b, err := os.ReadFile(filepath.Join(dir, name(i)))
		if err != nil {
			t.Fatal(err)
		}
		testgit.WriteFile(t, dir, name(i), string(b)+"// tail edit\n")
	}
}

// recordTime records d in milliseconds.
func recordTime(t *testing.T, repo, scenario string, d time.Duration) {
	t.Helper()
	record(t, repo, scenario, float64(d.Microseconds())/1000)
}

func record(t *testing.T, repo, scenario string, v float64) {
	t.Helper()
	t.Logf("PERF %-12s %-36s %10.1f", repo, scenario, v)
	path := os.Getenv("REXDIFF_PERF_OUT")
	if path == "" {
		return
	}
	perfOut.Lock()
	defer perfOut.Unlock()
	if perfOut.f == nil {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		perfOut.f = f
	}
	fmt.Fprintf(perfOut.f, "%s\t%s\t%.1f\n", repo, scenario, v)
}

func (tm *term) drain() {
	select {
	case <-tm.changed:
	default:
	}
}

func (tm *term) settle(since time.Time) (time.Time, string) {
	tm.t.Helper()
	return tm.settleFrom(since, tm.screen(), quietDelay, false)
}

// settleFrom waits at most first for the first change. ignoreLastLine
// skips the last line, where the find bar blinks.
func (tm *term) settleFrom(since time.Time, prev string, first time.Duration, ignoreLastLine bool) (time.Time, string) {
	tm.t.Helper()
	last := since
	prev = withoutBlink(prev, ignoreLastLine)
	limit := time.NewTimer(settleTimeout)
	defer limit.Stop()
	q := time.NewTimer(first)
	defer q.Stop()
	for {
		select {
		case <-tm.changed:
			// A write that does not change the text, such as a colour query,
			// does not count.
			if s := withoutBlink(tm.screen(), ignoreLastLine); s != prev {
				last, prev = time.Now(), s
				q.Reset(quietDelay)
			}
		case <-q.C:
			return last, tm.screen()
		case <-tm.done:
			tm.t.Fatalf("rexdiff stops. The screen shows:\n%s", tm.screen())
		case <-limit.C:
			tm.t.Fatalf("the screen does not settle. It shows:\n%s", tm.screen())
		}
	}
}

func withoutBlink(s string, ignoreLastLine bool) string {
	if !ignoreLastLine {
		return s
	}
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

const changeTimeout = 15 * time.Second

func (tm *term) waitForText(text string) {
	tm.t.Helper()
	limit := time.NewTimer(settleTimeout)
	defer limit.Stop()
	for !strings.Contains(tm.screen(), text) {
		select {
		case <-tm.changed:
		case <-time.After(50 * time.Millisecond):
		case <-limit.C:
			tm.t.Fatalf("the screen does not show %q", text)
		}
	}
}

func (tm *term) measure(repo, scenario string, act func()) string {
	tm.t.Helper()
	return tm.measureScreen(repo, scenario, false, act)
}

func (tm *term) measureFind(repo, scenario string, act func()) string {
	tm.t.Helper()
	return tm.measureScreen(repo, scenario, true, act)
}

func (tm *term) measureScreen(repo, scenario string, ignoreLastLine bool, act func()) string {
	tm.t.Helper()
	tm.settleFrom(time.Now(), tm.screen(), quietDelay, ignoreLastLine)
	tm.drain()
	t0 := time.Now()
	prev := tm.screen()
	act()
	last, s := tm.settleFrom(t0, prev, changeTimeout, ignoreLastLine)
	if last.Equal(t0) {
		tm.t.Logf("PERF %s %s: the screen does not change", repo, scenario)
	}
	recordTime(tm.t, repo, scenario, last.Sub(t0))
	return s
}

func repeat(k string, n int) []string {
	return slices.Repeat([]string{k}, n)
}

type perfRun struct {
	*term
}

func startPerf(t *testing.T, name, repo string, o opts) *perfRun {
	t.Helper()
	o.width, o.height = 200, 50
	o.bin = os.Getenv("REXDIFF_PERF_BIN")
	if o.bin == "" {
		o.bin = binary(t) // build before the clock starts
	}
	t0 := time.Now()
	tm := start(t, repo, o) // start waits for the first frame
	recordTime(t, name, "first screen", time.Since(t0))
	// The first screen can appear while rexdiff is still busy. Its response
	// to a key shows when it is ready.
	prev := tm.screen()
	tm.drain()
	tm.keys("j")
	last, _ := tm.settleFrom(t0, prev, changeTimeout, false)
	recordTime(t, name, "first key answered (from start)", last.Sub(t0))
	tm.keys("k")
	tm.settle(time.Now())
	return &perfRun{term: tm}
}

func pid(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "pgrep", "-P", strconv.Itoa(os.Getpid()), "rexdiff").Output()
	if err != nil {
		t.Fatalf("pgrep: %v", err)
	}
	return strings.Fields(string(out))[0]
}

// usage returns the RSS of rexdiff in MB and its CPU time in seconds.
func usage(t *testing.T) (rssMB, cpu float64) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "ps", "-o", "rss=,time=", "-p", pid(t)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	f := strings.Fields(string(out))
	kb, _ := strconv.ParseFloat(f[0], 64)
	return kb / 1024, cpuSeconds(f[1])
}

// cpuSeconds converts a ps time such as "M:SS.ss" or "H:MM:SS".
func cpuSeconds(s string) float64 {
	var total float64
	for p := range strings.SplitSeq(s, ":") {
		v, _ := strconv.ParseFloat(p, 64)
		total = total*60 + v
	}
	return total
}

func (r *perfRun) idle(name string, d time.Duration) {
	r.t.Helper()
	r.settle(time.Now())
	rss, c0 := usage(r.t)
	time.Sleep(d)
	_, c1 := usage(r.t)
	record(r.t, name, "RSS MB (value)", rss)
	record(r.t, name, fmt.Sprintf("idle CPU %% over %v (value)", d), (c1-c0)/d.Seconds()*100)
}

func copyRepo(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	copyDir(t, src, dst)
	return dst
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if out, err := exec.CommandContext(t.Context(), "cp", "-R", src, dst).CombinedOutput(); err != nil {
		t.Fatalf("cp: %v\n%s", err, out)
	}
}

func prependLine(t *testing.T, path, line string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(line+"\n"), b...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (tm *term) wheelAt(x, y, n int) {
	for range n {
		tm.mouse(vt.MouseWheel{X: x, Y: y, Button: vt.MouseWheelDown})
	}
}

func (tm *term) clickAt(x, y int) {
	tm.mouse(vt.MouseClick{X: x, Y: y, Button: vt.MouseLeft})
	tm.mouse(vt.MouseRelease{X: x, Y: y, Button: vt.MouseLeft})
}

const noteTooBig = "file too large to show"

func viewScenarios(r *perfRun, name, findQuery string) {
	m := r.measure
	m(name, "j x200", func() { r.keys(repeat("j", 200)...) })
	m(name, "k x200", func() { r.keys(repeat("k", 200)...) })
	m(name, "j x1", func() { r.keys("j") })
	m(name, "] x30", func() { r.keys(repeat("]", 30)...) })
	m(name, "[ x30", func() { r.keys(repeat("[", 30)...) })
	m(name, "pgdown x1", func() { r.keys("pgdown") })
	m(name, "pgdown x20", func() { r.keys(repeat("pgdown", 20)...) })
	m(name, "goto :$", func() { r.keys(":", "$", "enter") })
	m(name, "goto :0", func() { r.keys(":", "0", "enter") })
	m(name, "wheel x30", func() { r.wheelAt(120, 25, 30) })
	m(name, "click after wheel", func() { r.clickAt(120, 25) })
	// Toggling wrap does not change a screen of short lines, so send a j
	// afterwards to see when the toggle is done.
	m(name, "w wrap off (+j)", func() { r.keys("w", "j") })
	m(name, "right (scroll x) x1", func() { r.keys("right") })
	m(name, "left (scroll x) x1", func() { r.keys("left") })
	m(name, "w wrap on (+j)", func() { r.keys("w", "j") })
	m(name, "| split on", func() { r.keys("|") })
	m(name, "j x50 (split)", func() { r.keys(repeat("j", 50)...) })
	m(name, "| split off", func() { r.keys("|") })
	m(name, "ctrl+f open", func() { r.keys("ctrl+f") })
	r.measureFind(name, "find type query", func() { r.typed(findQuery) })
	r.measureFind(name, "find enter x10", func() { r.keys(repeat("enter", 10)...) })
	m(name, "find close", func() { r.keys("esc") })
	m(name, "tab 2 (Files)", func() { r.keys("2") })
	m(name, "tab 1 (Changes)", func() { r.keys("1") })
	m(name, "z hide list", func() { r.keys("z") })
	m(name, "z show list", func() { r.keys("z") })
	m(name, "p x4 move list round", func() { r.keys("p", "p", "p", "p") })
}

func TestPerfGo7k(t *testing.T) {
	name := "go7k"
	r := startPerf(t, name, perfRepo(t, name), opts{})
	r.measure(name, "f next file (small)", func() { r.keys("f") })
	r.measure(name, "F prev file (7k)", func() { r.keys("F") })
	viewScenarios(r, name, "recompute")
	r.idle(name, 10*time.Second)
}

func TestPerfReload(t *testing.T) {
	for _, c := range []struct{ name, file string }{
		{"go7k", "big.go"},
		{"xxl", filepath.Join("pkg00", "sub0", "file0000.go")},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := copyRepo(t, perfRepo(t, c.name))
			r := startPerf(t, c.name+"-copy", repo, opts{})
			for i := range 2 {
				t0 := time.Now()
				prependLine(t, filepath.Join(repo, c.file), fmt.Sprintf("// EXTERNAL EDIT %d", i+1))
				r.waitForText(fmt.Sprintf("EXTERNAL EDIT %d", i+1))
				recordTime(t, c.name, fmt.Sprintf("reload after external edit #%d", i+1), time.Since(t0))
				r.settle(time.Now())
			}
		})
	}
}

func TestPerfBig(t *testing.T) {
	for _, c := range []struct{ name, query string }{
		{"rewrite20k", "other"},
		{"big50k", "M1"},
		{"big200k", "M1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := startPerf(t, c.name, perfRepo(t, c.name), opts{})
			if s := r.screen(); strings.Contains(s, noteTooBig) {
				t.Logf("PERF %s: too large to show", c.name)
			} else {
				viewScenarios(r, c.name, c.query)
			}
			r.measure(c.name, "files: j x200", func() { r.keys("2"); r.keys(repeat("j", 200)...) })
			r.measure(c.name, "files: goto :$", func() { r.keys(":", "$", "enter") })
			r.keys("1")
			r.idle(c.name, 5*time.Second)
		})
	}
}

// TestPerfMarkdown: md5mb is too large to show, so it measures the note.
func TestPerfMarkdown(t *testing.T) {
	for _, name := range []string{"md400k", "md5mb"} {
		t.Run(name, func(t *testing.T) {
			r := startPerf(t, name, perfRepo(t, name), opts{})
			if s := r.screen(); strings.Contains(s, noteTooBig) {
				t.Logf("PERF %s: too large to show", name)
			}
			m := r.measure
			m(name, "j x200", func() { r.keys(repeat("j", 200)...) })
			m(name, "m rendered on", func() { r.keys("m") })
			m(name, "rendered: j x200", func() { r.keys(repeat("j", 200)...) })
			m(name, "rendered: goto :$", func() { r.keys(":", "$", "enter") })
			m(name, "m rendered off", func() { r.keys("m") })
			m(name, "tab 2 (Files)", func() { r.keys("2") })
			m(name, "files: m rendered on", func() { r.keys("m") })
			m(name, "files: m rendered off", func() { r.keys("m") })
			r.idle(name, 5*time.Second)
		})
	}
}

func TestPerfXXL(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"xxl", nil},
		{"xxl-branch", []string{"-scope", "branch", "-base", "main"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			name := c.name
			r := startPerf(t, name, perfRepo(t, name), opts{args: c.args})
			m := r.measure
			m(name, "f next file x1", func() { r.keys("f") })
			m(name, "f next file x20", func() { r.keys(repeat("f", 20)...) })
			m(name, "F prev file x20", func() { r.keys(repeat("F", 20)...) })
			m(name, "j x200", func() { r.keys(repeat("j", 200)...) })
			m(name, "pgdown x20", func() { r.keys(repeat("pgdown", 20)...) })
			m(name, "] x30 (crosses files)", func() { r.keys(repeat("]", 30)...) })
			m(name, "[ x30", func() { r.keys(repeat("[", 30)...) })
			m(name, "tab focus list", func() { r.keys("tab") })
			m(name, "list j x200", func() { r.keys(repeat("j", 200)...) })
			m(name, "list k x200", func() { r.keys(repeat("k", 200)...) })
			m(name, "list wheel x30", func() { r.wheelAt(190, 25, 30) })
			m(name, "list click", func() { r.clickAt(190, 30) })
			m(name, "list wheel x1", func() { r.wheelAt(190, 25, 1) })
			m(name, "tab focus diff", func() { r.keys("tab") })
			m(name, "wheel x30", func() { r.wheelAt(60, 25, 30) })
			m(name, "z hide list", func() { r.keys("z") })
			m(name, "z show list", func() { r.keys("z") })
			m(name, "tab 2 (Files)", func() { r.keys("2") })
			m(name, "tab 1 (Changes)", func() { r.keys("1") })
			m(name, "/ search open", func() { r.keys("/") })
			m(name, "search type query", func() { r.typed("file0999") })
			m(name, "search close", func() { r.keys("esc") })
			r.idle(name, 10*time.Second)
		})
	}
}

func TestPerfFiles3k(t *testing.T) {
	name := "files3k"
	r := startPerf(t, name, perfRepo(t, name), opts{})
	m := r.measure
	m(name, "tab 2 (Files)", func() { r.keys("2") })
	m(name, "tab focus list", func() { r.keys("tab") })
	m(name, "list right (expand dir) x1", func() { r.keys("right") })
	m(name, "list j x200", func() { r.keys(repeat("j", 200)...) })
	m(name, "list k x200", func() { r.keys(repeat("k", 200)...) })
	m(name, "list wheel x30", func() { r.wheelAt(190, 25, 30) })
	m(name, "list click", func() { r.clickAt(190, 30) })
	m(name, "/ search open", func() { r.keys("/") })
	m(name, "search type query", func() { r.typed("f2999") })
	m(name, "search close", func() { r.keys("esc") })
	m(name, "tab 1 (Changes)", func() { r.keys("1") })
	r.idle(name, 10*time.Second)
}

// TestPerfOpenClose shows whether memory grows. It does not fail if it does.
func TestPerfOpenClose(t *testing.T) {
	name := "big50k"
	r := startPerf(t, name, perfRepo(t, name), opts{})
	for i := range 10 {
		r.keys("2", ":", "$", "enter", "1", "w", "|", "w", "|")
		r.settle(time.Now())
		if i == 0 || i == 9 {
			rss, _ := usage(t)
			record(t, name, fmt.Sprintf("RSS MB after %d opens (value)", i+1), rss)
		}
	}
}
