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

	"github.com/rednafi/rexdiff/internal/proc"
)

var (
	// root is the directory of the fixtures. It is "" until Main runs.
	root string
	// gitPath is the path of git, from the PATH of the process. It is ""
	// until Main runs, and also if PATH has no git.
	gitPath string
)

// Main runs the tests of m. Each package that runs git calls it from its
// TestMain. Main does not change the environment of the process.
//
// Main finds git in the PATH of the process, before the tests run. If
// PATH has no git, each test that uses a helper of this package skips.
// The helpers run git with [ToolEnv], so the setup of the user does not
// apply. The tests also need /bin/sh and the tools in /usr/bin and /bin.
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "testgit")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if path, err := exec.LookPath("git"); err == nil {
		if err := os.Mkdir(filepath.Join(dir, "bin"), 0o755); err != nil {
			panic(err)
		}
		if err := os.Symlink(path, filepath.Join(dir, "bin", "git")); err != nil {
			panic(err)
		}
		gitPath = path
	}
	root = dir
	m.Run()
}

// Env returns the git variables of a test, as "key=value" pairs. A test
// that runs a program with an environment of its own adds them.
//
// Git reads no global or system config, so no hooksPath, signing,
// fsmonitor, or diff setting applies. Git reads no system attributes
// file. An empty GIT_TEMPLATE_DIR copies no template, so a new repository
// gets no hooks. The commits have a fixed author.
func Env() []string {
	return []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
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

// ready fails tb if Main does not run. It skips tb if PATH has no git.
func ready(tb testing.TB) {
	tb.Helper()
	if root == "" {
		tb.Fatal("testgit: TestMain does not call testgit.Main")
	}
	if gitPath == "" {
		tb.Skip("testgit: git is not on PATH")
	}
}

// Git runs git in dir with [ToolEnv] and returns its standard output. It
// fails tb if git fails.
func Git(tb testing.TB, dir string, args ...string) string {
	tb.Helper()
	cmd := proc.Command(tb.Context(), ToolEnv(tb), "git", args...)
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
// repository with the build function of f. If the build fails, tb of the
// first call gets the failure, and each later call fails.
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
