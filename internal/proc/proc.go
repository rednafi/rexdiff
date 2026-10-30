// Package proc runs programs and shortens their error messages. It never
// reads the environment of the current process.
package proc

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// waitDelay also limits how long a child process can keep the output open.
const waitDelay = time.Second

// Command returns a command that runs name with args and env. On cancel it
// sends SIGTERM, so that git can remove its lock files.
func Command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	path, err := LookPath(env, name)
	if err != nil {
		return &exec.Cmd{Path: name, Args: append([]string{name}, args...), Err: err}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Args[0] = name
	if env == nil {
		env = []string{} // exec would use the current process's environment for a nil Env
	}
	cmd.Env = env
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = waitDelay
	return cmd
}

// Getenv returns the last value of key in env, as [exec.Cmd.Env] does.
func Getenv(env []string, key string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// LookPath is like [exec.LookPath], but searches the PATH in env.
func LookPath(env []string, file string) (string, error) {
	if strings.ContainsRune(file, filepath.Separator) {
		if isExecutable(file) {
			return file, nil
		}
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}
	for dir := range strings.SplitSeq(Getenv(env, "PATH"), string(os.PathListSeparator)) {
		if !filepath.IsAbs(dir) {
			// A relative entry, such as ".", could point into the repository
			// under review.
			continue
		}
		if path := filepath.Join(dir, file); isExecutable(path) {
			return path, nil
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// NotInstalled returns the error "<base name> is not installed".
func NotInstalled(name string) error {
	return errors.New(filepath.Base(name) + " is not installed")
}

// Error returns a short error that wraps err from the program name.
func Error(ctx context.Context, name string, err error, stderr []byte) error {
	if ctx.Err() != nil {
		cause := context.Cause(ctx)
		return &runError{msg: cause.Error(), errs: []error{err, cause}}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return &runError{msg: NotInstalled(name).Error(), errs: []error{err}}
	}
	msg := strings.TrimPrefix(firstLine(string(stderr)), "Error: ")
	return &runError{msg: cmp.Or(msg, err.Error()), errs: []error{err}}
}

type runError struct {
	msg  string
	errs []error
}

func (e *runError) Error() string   { return e.msg }
func (e *runError) Unwrap() []error { return e.errs }

const maxHead = 4 << 10

// Head is a writer that keeps the first maxHead bytes and drops the rest.
type Head struct{ b []byte }

// Write keeps the part of p that fits. It reports all of p as written.
func (h *Head) Write(p []byte) (int, error) {
	h.b = append(h.b, p[:min(len(p), maxHead-len(h.b))]...)
	return len(p), nil
}

// Bytes returns the bytes kept so far.
func (h *Head) Bytes() []byte { return h.b }

func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// LastLine returns the last non-blank line of s, trimmed.
func LastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndexByte(s, '\n')+1:])
}
