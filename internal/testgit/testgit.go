// Package testgit has the test helpers that run git. The settings, hooks,
// and templates of the user do not change what git does in a test.
package testgit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

var (
	// root is the directory of the fixtures. It is "" until Main runs.
	root string
	// gitPath is the path of git, from the PATH of the process. It is ""
	// until Main runs.
	gitPath string
)

// Main runs the tests of m in a git environment that ignores the setup of
// the user. Each package that runs git calls it from its TestMain.
//
// Git reads no global or system config, so no hooksPath, signing,
// fsmonitor, or diff setting applies. A new repository gets no hooks from
// a template. The variables that point git to another repository, or that
// add config, are unset. HOME and XDG_CONFIG_HOME are a new directory, so
// no ignore or attributes file of the user applies. The commits have a
// fixed author.
//
// Main finds git in the PATH of the process, before the tests run. A test
// that gives a library an environment of its own gets git from
// [ToolEnv] or [LinkGit].
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "testgit")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path, err := exec.LookPath("git")
	if err != nil {
		panic(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
		panic(err)
	}
	if err := os.Symlink(path, filepath.Join(dir, "bin", "git")); err != nil {
		panic(err)
	}
	gitPath = path
	env := append(Env(), "HOME="+dir, "XDG_CONFIG_HOME="+dir)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	for _, k := range unset {
		if err := os.Unsetenv(k); err != nil {
			panic(err)
		}
	}
	root = dir
	m.Run()
}

// Env returns the variables that Main sets, as "key=value" pairs. A test
// that runs a program with an environment of its own adds them. An empty
// GIT_TEMPLATE_DIR copies no template.
func Env() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TEMPLATE_DIR=",
		"GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t",
	}
}

// ToolEnv returns the environment of a library that runs git in a test.
// It has the variables of [Env] and PATH. PATH has dirs, then a directory
// with only git, then /usr/bin and /bin. A test puts its fake tools in
// dirs. ToolEnv has no HOME, so no file of the user applies.
func ToolEnv(tb testing.TB, dirs ...string) []string {
	tb.Helper()
	ready(tb)
	path := slices.Concat(dirs, []string{filepath.Join(root, "bin"), "/usr/bin", "/bin"})
	return append(Env(), "PATH="+strings.Join(path, string(os.PathListSeparator)))
}

// LinkGit makes a link to git in dir. A test that removes the link takes
// git away from a PATH with dir.
func LinkGit(tb testing.TB, dir string) {
	tb.Helper()
	ready(tb)
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		tb.Fatal(err)
	}
}

// unset are the variables that make git use another repository, more
// config, or another diff program.
var unset = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_CONFIG",
	"GIT_CONFIG_COUNT",
	"GIT_CONFIG_PARAMETERS",
	"GIT_EXTERNAL_DIFF",
	"GIT_DIFF_OPTS",
}

// ready fails tb if Main does not run.
func ready(tb testing.TB) {
	tb.Helper()
	if root == "" {
		tb.Fatal("testgit: TestMain does not call testgit.Main")
	}
}

// Git runs git in dir and returns its standard output. It fails tb if
// git fails.
func Git(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	ready(tb)
	cmd := exec.CommandContext(tb.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			stderr = ee.Stderr
		}
		tb.Fatalf("git %v: %v: %s", args, err, stderr)
	}
	return string(out)
}

// InitRepo makes a repository in dir with the branch main. It makes dir
// if it does not exist.
func InitRepo(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	Git(tb, dir, "init", "-q", "-b", "main")
}

// WriteFile writes content to the file name in dir. It makes the parent
// directories.
func WriteFile(tb testing.TB, dir, name, content string) {
	tb.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		tb.Fatal(err)
	}
}

// Script writes an executable sh script with body to the file name in
// dir, and returns its path. A test puts dir on PATH to fake a tool.
func Script(tb testing.TB, dir, name, body string) string {
	tb.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		tb.Fatal(err)
	}
	return p
}

// Fixture is a repository that a package makes once and copies for each
// test. A copy costs less than the git processes that make it.
type Fixture struct {
	build func(tb testing.TB, dir string)
	once  sync.Once
	dir   string // the repository, or "" if build fails
}

// NewFixture returns a fixture that build makes in an empty directory.
func NewFixture(build func(tb testing.TB, dir string)) *Fixture {
	return &Fixture{build: build}
}

// Copy copies the repository of f to dir. The first call makes the
// repository with the build function of f.
func (f *Fixture) Copy(tb testing.TB, dir string) {
	tb.Helper()
	ready(tb)
	f.once.Do(func() {
		tmp, err := os.MkdirTemp(root, "fixture")
		if err != nil {
			tb.Fatal(err)
		}
		f.build(tb, tmp)
		f.dir = tmp
	})
	if f.dir == "" {
		tb.Fatal("testgit: the fixture fails to build")
	}
	if err := os.CopyFS(dir, os.DirFS(f.dir)); err != nil {
		tb.Fatal(err)
	}
}
