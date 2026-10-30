// Package clipboard writes text to the system clipboard.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rednafi/rexdiff/internal/proc"
)

// ErrNoTool is returned when no clipboard tool is on PATH.
var ErrNoTool = errors.New("no clipboard tool is installed (pbcopy, wl-clipboard, xclip, or xsel)")

var tools = [][]string{
	{"pbcopy"},
	{"wl-copy"},
	{"xclip", "-selection", "clipboard"},
	{"xsel", "--clipboard", "--input"},
}

// Write pipes text into the first clipboard tool on the PATH in env.
func Write(ctx context.Context, env []string, text string) error {
	for _, t := range tools {
		if _, err := proc.LookPath(env, t[0]); err != nil {
			continue
		}
		cmd := proc.Command(ctx, env, t[0], t[1:]...)
		cmd.Stdin = strings.NewReader(text)
		// Do not capture stderr. xclip and wl-copy leave a background process
		// that keeps the pipe open.
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", t[0], proc.Error(ctx, t[0], err, nil))
		}
		return nil
	}
	return ErrNoTool
}
