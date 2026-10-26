// Package proc runs programs and makes their errors short and clear.
//
// The caller gives the environment as a value, as in [exec.Cmd.Env]. A
// program gets only that environment, and proc finds the program in the
// PATH of that environment. proc does not read the environment of the
// process.
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
	"time"
)

// waitDelay is how long a stopped program can keep its output open. A
// child of the program can keep the output open after the program ends.
const waitDelay = time.Second

// Command returns the command "name args..." with the environment env.
// LookPath finds name in the PATH of env. If LookPath fails, Start returns
// its error. A nil env is an empty environment, not the environment of the
// process. The command stops when ctx is done. Its Wait returns at most
// waitDelay after the program ends.
func Command(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	path, err := LookPath(env, name)
	if err != nil {
		// Start returns Err before it runs anything, so the command needs no
		// context.
		return &exec.Cmd{Path: name, Args: append([]string{name}, args...), Err: err}
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Args[0] = name
	if env == nil {
		env = []string{} // exec gives a nil Env the environment of the process
	}
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	return cmd
}

// Getenv returns the value of the variable key in env, a list of
// "key=value" pairs. The last pair with key wins, as in [exec.Cmd.Env].
// Getenv returns "" if env does not have key.
func Getenv(env []string, key string) string {
	for _, kv := range slices.Backward(env) {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// LookPath returns the path of the program file, as [exec.LookPath] does,
// but it looks in the PATH of env. If file has a separator, LookPath checks
// only file. LookPath skips the empty entries of PATH. The program must be
// an executable regular file. If LookPath finds no program, the error is an
// [*exec.Error] that wraps [exec.ErrNotFound].
func LookPath(env []string, file string) (string, error) {
	if strings.ContainsRune(file, filepath.Separator) {
		if executable(file) {
			return file, nil
		}
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}
	for dir := range strings.SplitSeq(Getenv(env, "PATH"), string(os.PathListSeparator)) {
		if !filepath.IsAbs(dir) {
			// A relative entry, as ".", names a directory of the working
			// tree that rexdiff shows. exec.LookPath refuses it too.
			continue
		}
		if path := filepath.Join(dir, file); executable(path) {
			return path, nil
		}
	}
	return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
}

// executable reports whether path is a regular file that a user can run.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
}

// NotInstalled returns the error of the program name that PATH does not
// have. The text has the base name of the program.
func NotInstalled(name string) error {
	return errors.New(filepath.Base(name) + " is not installed")
}

// Error returns the error err of a run of the program name. If the
// program is not on PATH, the text is "<name> is not installed".
// Else the text is the first line of stderr that is not blank, without a
// leading "Error: ". If stderr has no text, the text is that of err. The
// error wraps err, so a caller can examine the exit code.
func Error(name string, err error, stderr []byte) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &runError{msg: NotInstalled(name).Error(), err: err}
	}
	msg := strings.TrimPrefix(firstLine(string(stderr)), "Error: ")
	return &runError{msg: cmp.Or(msg, err.Error()), err: err}
}

// runError is a failed run of a program. It has a short text and wraps
// the error of the process.
type runError struct {
	msg string
	err error
}

func (e *runError) Error() string { return e.msg }
func (e *runError) Unwrap() error { return e.err }

// firstLine returns the first line of s that is not blank, without the
// leading and trailing space.
func firstLine(s string) string {
	for line := range strings.Lines(s) {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// LastLine returns the last line of s that is not blank, without the
// leading and trailing space.
func LastLine(s string) string {
	s = strings.TrimSpace(s)
	return strings.TrimSpace(s[strings.LastIndexByte(s, '\n')+1:])
}
