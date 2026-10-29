// Package proc runs programs and shortens their error messages.
//
// The caller passes the environment as a list, as in [exec.Cmd.Env]. A
// program receives only that environment, and proc looks for the program
// in its PATH. proc never reads the current process's environment.
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

// waitDelay is how long a cancelled program has to exit, and how long a
// child process can keep the output open after the program exits.
const waitDelay = time.Second

// Command returns a command that runs name with args and the environment
// env, where a nil env is empty. [LookPath] finds name in the PATH of env;
// if it fails, Start returns its error. When ctx is done, the command sends
// SIGTERM, so that git can remove its lock files, and kills the program
// after waitDelay.
func Command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	path, err := LookPath(env, name)
	if err != nil {
		// Start returns Err before running anything, so no context is needed.
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

// Getenv returns the value of the variable key in env, a list of
// "key=value" pairs. If key appears more than once, the last value wins,
// as in [exec.Cmd.Env]. Getenv returns "" if key is not set.
func Getenv(env []string, key string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// LookPath is like [exec.LookPath], but searches the PATH in env. If file
// contains a path separator, LookPath checks only file. It skips empty and
// relative PATH entries. The program must be an executable regular file.
// If no program is found, the error is an [*exec.Error] that wraps
// [exec.ErrNotFound].
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

// isExecutable reports whether path is a regular file with an execute bit
// set.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// NotInstalled returns the error "<base name> is not installed" for
// the program called name.
func NotInstalled(name string) error {
	return errors.New(filepath.Base(name) + " is not installed")
}

// Error returns a short error that wraps err, the result of running the
// program name. If ctx is done, the message is that of [context.Cause],
// which the error also wraps. If the program is not on PATH, it is that of
// [NotInstalled]. Otherwise it is the first non-blank line of stderr
// without a leading "Error: ", or the message of err.
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

// runError is the short error for a failed run. It wraps the process
// error, and the context's cause if the context is done.
type runError struct {
	msg  string
	errs []error
}

func (e *runError) Error() string   { return e.msg }
func (e *runError) Unwrap() []error { return e.errs }

// maxHead is the number of bytes that a [Head] keeps.
const maxHead = 4 << 10

// Head is a writer that keeps the first maxHead bytes written to it and
// drops the rest. Use it for the standard error of a program that can run
// for a long time, because [Error] reads only the start.
type Head struct{ b []byte }

// Write keeps the part of p that fits and reports all of p as written.
func (h *Head) Write(p []byte) (int, error) {
	h.b = append(h.b, p[:min(len(p), maxHead-len(h.b))]...)
	return len(p), nil
}

// Bytes returns the bytes kept so far.
func (h *Head) Bytes() []byte { return h.b }

// firstLine returns the first non-blank line of s, with surrounding white
// space trimmed.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// LastLine returns the last non-blank line of s, with surrounding white
// space trimmed.
func LastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndexByte(s, '\n')+1:])
}
