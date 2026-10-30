// Package testgit provides test helpers that run git without the user's
// settings, hooks and templates.
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
	root    string // "" until Main runs
	gitPath string // "" until Main runs, or if git is not on PATH
)

// Main runs the tests in m. Call it from TestMain in every package whose
// tests run git.
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

// Env returns a git environment that ignores the user's config.
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
		// Background maintenance would write to .git while a test reads the
		// repository.
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=maintenance.auto", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=gc.auto", "GIT_CONFIG_VALUE_1=0",
	}
}

// ToolEnv returns [Env] plus a PATH that starts with dirs.
func ToolEnv(tb testing.TB, dirs ...string) []string {
	tb.Helper()
	ready(tb)
	path := slices.Concat(dirs, []string{filepath.Join(root, "bin"), "/usr/bin", "/bin"})
	return append(Env(), "PATH="+strings.Join(path, string(os.PathListSeparator)))
}

// LinkGit creates a symbolic link to git in dir.
func LinkGit(tb testing.TB, dir string) {
	tb.Helper()
	ready(tb)
	if err := os.Symlink(gitPath, filepath.Join(dir, "git")); err != nil {
		tb.Fatal(err)
	}
}

func ready(tb testing.TB) {
	tb.Helper()
	if root == "" {
		tb.Fatal("testgit: TestMain does not call testgit.Main")
	}
	if gitPath == "" {
		tb.Skip("testgit: git is not on PATH")
	}
}

// Git runs git in dir with [ToolEnv] and returns its stdout.
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

// InitRepo creates a repository in dir with the branch main.
func InitRepo(tb testing.TB, dir string) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	Git(tb, dir, "init", "-q", "-b", "main")
}

// WriteFile writes content to the file name in dir.
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

// Script writes an executable sh script to the file name in dir.
func Script(tb testing.TB, dir, name, body string) string {
	tb.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		tb.Fatal(err)
	}
	return p
}

// Fixture is a repository that a package builds once and copies for each
// test.
type Fixture struct {
	build func(tb testing.TB, dir string)
	once  sync.Once
	dir   string // "" if the build failed
}

// NewFixture returns a Fixture that build creates in an empty directory.
func NewFixture(build func(tb testing.TB, dir string)) *Fixture {
	return &Fixture{build: build}
}

// Copy copies the repository of f to dir. It builds it on the first call.
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
