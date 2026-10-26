package proc_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rednafi/rexdiff/internal/proc"
)

// shEnv is an environment with the PATH of sh.
var shEnv = []string{"PATH=/usr/bin:/bin"}

// TestError checks the text of the error of a run, and that the error
// keeps the error of the process.
func TestError(t *testing.T) {
	t.Parallel()
	exit := proc.Command(t.Context(), shEnv, "sh", "-c", "exit 3").Run()
	tests := []struct {
		name   string
		err    error
		stderr string
		want   string
	}{
		{"/usr/bin/gh", exec.ErrNotFound, "", "gh is not installed"},
		{"git", exit, "\n  fatal: bad\nmore\n", "fatal: bad"},
		{"rex", exit, "Error: no Rex server\n", "no Rex server"},
		{"git", exit, " \n", exit.Error()},
	}
	for _, tt := range tests {
		err := proc.Error(tt.name, tt.err, []byte(tt.stderr))
		if err.Error() != tt.want {
			t.Errorf("Error(%q, %v, %q) = %q, want %q", tt.name, tt.err, tt.stderr, err, tt.want)
		}
		if !errors.Is(err, tt.err) {
			t.Errorf("Error(%q, %v, %q) does not wrap %v", tt.name, tt.err, tt.stderr, tt.err)
		}
	}
}

// TestLastLine checks that LastLine skips the blank lines at the end.
func TestLastLine(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"", ""},
		{"one", "one"},
		{"one\n two \n\n", "two"},
		{"one\r\ntwo\r\n", "two"},
	}
	for _, tt := range tests {
		if got := proc.LastLine(tt.in); got != tt.want {
			t.Errorf("LastLine(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestCommand checks that Command limits the wait for the output, and
// that the program gets only env.
func TestCommand(t *testing.T) {
	t.Parallel()
	env := slices.Concat(shEnv, []string{"A=1"})
	cmd := proc.Command(t.Context(), env, "sh", "-c", "env")
	if cmd.WaitDelay <= 0 {
		t.Errorf("WaitDelay = %v, want more than 0", cmd.WaitDelay)
	}
	if cmd.Args[0] != "sh" {
		t.Errorf("Args[0] = %q, want sh", cmd.Args[0])
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// sh can add variables of its own, for example PWD.
	for _, kv := range env {
		if !strings.Contains(string(out), kv+"\n") {
			t.Errorf("the environment of sh is\n%s\nwant %s", out, kv)
		}
	}
}

// TestCommandNotFound checks that Start returns the error of LookPath if
// the PATH of env has no program name.
func TestCommandNotFound(t *testing.T) {
	t.Parallel()
	for _, env := range [][]string{nil, {"PATH=" + t.TempDir()}} {
		cmd := proc.Command(t.Context(), env, "sh", "-c", "true")
		if cmd.Path != "sh" {
			t.Errorf("env %q: Path = %q, want sh", env, cmd.Path)
		}
		if err := cmd.Run(); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("env %q: Run = %v, want exec.ErrNotFound", env, err)
		}
	}
}

// TestGetenv checks that the last pair of a key wins, and that a key
// does not match the start of another key.
func TestGetenv(t *testing.T) {
	t.Parallel()
	env := []string{"A=1", "AB=2", "A=3", "C=", "D"}
	tests := []struct{ key, want string }{
		{"A", "3"},
		{"AB", "2"},
		{"C", ""},
		{"D", ""},
		{"B", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := proc.Getenv(env, tt.key); got != tt.want {
			t.Errorf("Getenv(%q, %q) = %q, want %q", env, tt.key, got, tt.want)
		}
	}
	if got := proc.Getenv(nil, "A"); got != "" {
		t.Errorf("Getenv(nil, A) = %q, want \"\"", got)
	}
}

// TestLookPath checks that LookPath finds an executable regular file in
// the PATH of env, in the order of PATH.
func TestLookPath(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	write := func(path string, mode os.FileMode) string {
		t.Helper()
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write(filepath.Join(first, "plain"), 0o644)
	plain := write(filepath.Join(second, "plain"), 0o755)
	both := write(filepath.Join(first, "both"), 0o755)
	write(filepath.Join(second, "both"), 0o755)
	if err := os.Mkdir(filepath.Join(first, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + strings.Join([]string{"", first, "", second}, string(os.PathListSeparator))
	env := []string{"PATH=/nowhere", path}
	tests := []struct {
		file, want string // a want of "" is exec.ErrNotFound
	}{
		{"both", both},
		{"plain", plain}, // the file in first cannot run
		{"dir", ""},
		{"none", ""},
		{"", ""},
		{both, both},
		{filepath.Join(first, "plain"), ""},
		{filepath.Join(first, "none"), ""},
	}
	for _, tt := range tests {
		got, err := proc.LookPath(env, tt.file)
		if tt.want == "" {
			ee, ok := errors.AsType[*exec.Error](err)
			if got != "" || !errors.Is(err, exec.ErrNotFound) || !ok || ee.Name != tt.file {
				t.Errorf("LookPath(%q) = %q, %v, want an *exec.Error with ErrNotFound",
					tt.file, got, err)
			}
			continue
		}
		if got != tt.want || err != nil {
			t.Errorf("LookPath(%q) = %q, %v, want %q, nil", tt.file, got, err, tt.want)
		}
	}
	if got, err := proc.LookPath(nil, "sh"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("LookPath(nil, sh) = %q, %v, want exec.ErrNotFound", got, err)
	}
	// A relative entry names a directory of the working directory, which
	// can be the repository under review.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, first)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := proc.LookPath([]string{"PATH=" + rel}, "both"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("LookPath(PATH=%s, both) = %q, %v, want exec.ErrNotFound", rel, got, err)
	}
}
