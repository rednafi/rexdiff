// Package clipboard writes text to the system clipboard using the first
// clipboard tool found on PATH. The caller provides the tool's
// environment.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rednafi/rexdiff/internal/proc"
)

// ErrNoTool is returned when no clipboard tool is found on PATH.
var ErrNoTool = errors.New("no clipboard tool is installed (pbcopy, wl-clipboard, xclip, or xsel)")

// tools lists the clipboard tools that read from stdin, in the order Write
// tries them.
var tools = [][]string{
	{"pbcopy"},
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// Write pipes text into the first clipboard tool on the PATH in env, and
// runs the tool with env. If no clipboard tool is found, Write returns
// [ErrNoTool]. Write returns an error if the tool exits with a non-zero
// status. If ctx is done, the error includes the text of [context.Cause].
func Write(ctx context.Context, env []string, text string) error {
	for _, t := range tools {
		if _, err := proc.LookPath(env, t[0]); err != nil {
			continue
		}
		cmd := proc.Command(ctx, env, t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		// Do not capture stderr, because xclip and wl-copy leave a background
		// process that keeps the pipe open.
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", t[0], proc.Error(ctx, t[0], err, nil))
		}
		return nil
	}
	return ErrNoTool
}
